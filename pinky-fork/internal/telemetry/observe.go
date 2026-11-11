package telemetry

import (
	"context"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Observe counts what the gateway's views show, until ctx ends: the phones
// seen by platform, connection and system version, agent runs and how they
// ended, and what was asked of the person. It reads the views when they
// change, at most twice a second. Phone ids, task names, app names and
// screens are never kept: only counts with fixed names.
func (c *Client) Observe(ctx context.Context, api gateway.API) {
	if c == nil || api == nil {
		return
	}
	o := newObserver(c)
	events := api.Subscribe(ctx)
	o.read(api)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				return
			}
		}
		// Events come in bursts: read once for each half second of them.
		wait := time.NewTimer(observeEvery)
	drain:
		for {
			select {
			case _, ok := <-events:
				if !ok {
					break drain
				}
			case <-wait.C:
				break drain
			case <-ctx.Done():
				wait.Stop()
				return
			}
		}
		wait.Stop()
		o.read(api)
	}
}

// observeEvery is how long Observe gathers events before it reads the
// views; tests shorten it.
var observeEvery = 500 * time.Millisecond

func newObserver(c *Client) *observer {
	return &observer{c: c, tasks: map[string]gateway.TaskState{}, bg: map[string]bool{}, needs: map[string]string{}}
}

type observer struct {
	c     *Client
	tasks map[string]gateway.TaskState // the state each task had when last read
	bg    map[string]bool              // tasks counted as running in the background
	needs map[string]string            // open needs, by id, with their kind
}

func (o *observer) read(api gateway.API) {
	c := o.c
	devs := api.Devices()
	lines := map[string]string{}
	online := 0
	for _, d := range devs {
		lines[d.Info.ID] = d.StatusLine
		info := d.Info
		platform := platformName(info)
		if d.Status == gateway.DeviceOffline || info.State == driver.StateOffline {
			continue
		}
		if info.State != driver.StateOnline {
			// Waiting for the person: not trusted yet, or Developer Mode off.
			c.SeePhone(Key(platform, "state", string(info.State)), info.ID)
			continue
		}
		online++
		c.SeePhone(Key(platform, connectionName(info.Connection)), info.ID)
		if v := majorVersion(info.OSVersion); v != "" {
			c.SeePhone(Key(platform, "os", v), info.ID)
		}
		if b := backendName(info.Backend); b != "" {
			c.SeePhone(Key(platform, "backend", b), info.ID)
		}
	}
	c.Max("phones.max_online", int64(online))

	live := map[string]bool{}
	for _, t := range api.Tasks() {
		live[t.ID] = true
		prev, seen := o.tasks[t.ID]
		if !seen {
			c.Count("runs.started")
			c.Count(Key("runs", "agent", agentName(t.Agent)))
			if strings.HasSuffix(t.Agent, remoteSuffix) {
				c.Count("runs.remote")
			}
		}
		if t.State != prev {
			switch t.State {
			case gateway.TaskDone:
				c.Count("runs.done")
			case gateway.TaskStopped:
				c.Count("runs.stopped")
			case gateway.TaskFailed:
				c.Count("runs.failed")
			case gateway.TaskPaused:
				c.Count("runs.paused")
			}
		}
		if t.Ended.IsZero() && !o.bg[t.ID] && strings.HasSuffix(lines[t.DeviceID], "in the background") {
			o.bg[t.ID] = true
			c.Count("runs.background")
		}
		o.tasks[t.ID] = t.State
	}
	for id := range o.tasks {
		if !live[id] {
			delete(o.tasks, id)
			delete(o.bg, id)
		}
	}

	open := map[string]bool{}
	for _, n := range api.Needs() {
		open[n.ID] = true
		if _, seen := o.needs[n.ID]; seen {
			continue
		}
		kind := needKind(n)
		o.needs[n.ID] = kind
		c.Count(Key("needs", kind, "asked"))
		if kind == "approval" {
			c.Count(Key("needs", "approval", "rule", ruleName(n.Rule)))
		}
	}
	for id, kind := range o.needs {
		if open[id] {
			continue
		}
		delete(o.needs, id)
		// Answered in Pinky (Gateway.Decide counts how), or it went away
		// without an answer: timed out, or the agent left.
		if !c.takeDecided(id) {
			c.Count(Key("needs", kind, "unanswered"))
		}
	}
}

// needKind names what a need asks: "approval", "new_agent", "handoff" or
// "unlock".
func needKind(n gateway.NeedView) string {
	if n.Kind == gateway.NeedApproval && n.Rule == "New agent" {
		return "new_agent"
	}
	switch n.Kind {
	case gateway.NeedApproval, gateway.NeedHandoff, gateway.NeedUnlock:
		return string(n.Kind)
	}
	return "other"
}

// platformName is a phone's platform in counter names. Simulators and
// emulators count apart from phones: "ios_simulator", "android_emulator".
func platformName(info driver.Info) string {
	switch {
	case info.Platform != driver.Android && info.Platform != driver.IOS:
		return "other"
	case info.Virtual && info.Platform == driver.IOS:
		return "ios_simulator"
	case info.Virtual:
		return "android_emulator"
	}
	return string(info.Platform)
}

func connectionName(c string) string {
	switch c {
	case "usb", "wifi":
		return c
	}
	return "other"
}

// backendName keeps the backends the drivers name.
func backendName(b string) string {
	switch b = strings.ToLower(strings.TrimSpace(b)); b {
	case "scrcpy", "companion", "adb", "coredevice", "wda", "mirroring", "fake", "demo":
		return b
	case "":
		return ""
	}
	if strings.HasPrefix(b, "scrcpy") {
		return "scrcpy"
	}
	return "other"
}

// majorVersion is a system version's first number: "17" of "17.0.1".
func majorVersion(v string) string {
	v = strings.TrimSpace(v)
	end := 0
	for end < len(v) && end < 3 && v[end] >= '0' && v[end] <= '9' {
		end++
	}
	return v[:end]
}

// remoteSuffix ends the names of agents that connect over HTTP
// (internal/mcp.RemoteSuffix).
const remoteSuffix = " (remote)"

// knownAgents are the agent names internal/mcp gives the MCP clients it
// knows; usage statistics name only these.
var knownAgents = map[string]bool{
	"Claude Code": true, "Claude": true, "Codex": true, "Cursor": true, "Windsurf": true, "VS Code": true,
	"Gemini CLI": true, "Goose": true, "Cline": true, "Zed": true, "opencode": true, "Continue": true,
	"MCP Inspector": true, "Agent": true,
}

func agentName(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), remoteSuffix)
	if knownAgents[name] {
		return name
	}
	return "other"
}

// knownRules are the rule names Pinky ships with; the person's own rules
// count as "custom".
var knownRules = map[string]bool{
	"Payments": true, "Messages": true, "Commits": true, "Installs and links": true, "Calls": true,
	"Settings changes": true, "Every action": true, "Agent request": true, "Handoff": true, "Read-only": true,
	"Blocked by you": true, "credentials": true, "Finance apps": true, "Password managers": true,
	"Task apps": true, "Agent scope": true, "New agent": true, "Files": true, "Recordings": true, "App logs": true,
	"Installing from the computer": true, "Uninstalling apps": true, "Pinky's own apps": true, "System apps": true,
	"Phone settings": true,
}

func ruleName(rule string) string {
	if rule == "" {
		return "none"
	}
	if knownRules[rule] {
		return rule
	}
	return "custom"
}
