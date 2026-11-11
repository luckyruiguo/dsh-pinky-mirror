package standalone

import (
	"time"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/pilot"
)

// State is what Pinky's screens show: the phone, the agent's task with its
// steps, and what waits for the person.
type State struct {
	Device DeviceState `json:"device"`
	Preset int         `json:"preset"` // when agents ask: policy.Preset
	Agent  *AgentState `json:"agent,omitempty"`
	Task   *TaskState  `json:"task,omitempty"`
	Needs  []Need      `json:"needs"`
}

// DeviceState is the phone as the gateway sees it.
type DeviceState struct {
	Ready  bool   `json:"ready"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	Line   string `json:"line,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// AgentState is Ask Pinky's task: running, or the last one that ended.
type AgentState struct {
	Task    string  `json:"task"`
	Model   string  `json:"model"`
	Running bool    `json:"running"`
	End     string  `json:"end,omitempty"`
	Summary string  `json:"summary,omitempty"`
	Steps   int     `json:"steps"`
	Cost    float64 `json:"cost"`
	Tokens  int64   `json:"tokens"`
	Seconds int64   `json:"seconds"`
}

// TaskState is the gateway's task: its steps in plain words.
type TaskState struct {
	Title string `json:"title"`
	State string `json:"state"`
	Steps []Step `json:"steps"`
}

// Step is one step of the task.
type Step struct {
	Text   string `json:"text"`
	Status string `json:"status"`
	MS     int64  `json:"ms"`
}

// Need is an approval or a handoff the agent waits for.
type Need struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Detail     string `json:"detail,omitempty"`
	App        string `json:"app,omitempty"`
	Rule       string `json:"rule,omitempty"`
	DeadlineMS int64  `json:"deadline_ms,omitempty"` // Unix milliseconds; no answer by then means deny
	AllowTask  bool   `json:"allow_task,omitempty"`
}

// State reads the current state.
func (c *Core) State() State {
	st := State{Preset: int(c.G.Policy().Preset())}
	d, ok := c.device()
	if ok {
		st.Device = DeviceState{Ready: d.Info.State == "online", Name: d.Info.Name, Status: string(d.Status),
			Line: d.StatusLine, Detail: d.Info.Detail}
	}
	// Only the agent starts tasks here: its task is the newest one of the
	// phone that started with it, never an earlier one or a dismissed one.
	var task *gateway.TaskView
	if ok {
		if s, has := c.pilot.Status(d.Info.ID); has {
			st.Agent = agentState(s)
			since := s.Started.Add(-time.Second)
			for _, t := range c.G.Tasks() {
				if t.DeviceID == d.Info.ID && !t.Started.Before(since) {
					task = &t
					break
				}
			}
		}
	}
	c.mu.Lock()
	if task != nil {
		c.last = task
	} else if st.Agent != nil {
		task = c.last // ended more than ten minutes ago: keep its steps until dismissed
	}
	c.mu.Unlock()
	if task != nil {
		ts := &TaskState{Title: task.Title, State: string(task.State)}
		for _, s := range task.Steps {
			ts.Steps = append(ts.Steps, Step{Text: s.Text, Status: s.Status, MS: s.Elapsed.Milliseconds()})
		}
		st.Task = ts
	}
	st.Needs = []Need{}
	for _, n := range c.G.Needs() {
		if ok && n.DeviceID != d.Info.ID {
			continue
		}
		need := Need{ID: n.ID, Kind: string(n.Kind), Title: n.Title, Detail: n.Detail, App: n.App, Rule: n.Rule,
			AllowTask: n.AllowTask}
		if !n.Deadline.IsZero() {
			need.DeadlineMS = n.Deadline.UnixMilli()
		}
		st.Needs = append(st.Needs, need)
	}
	return st
}

func agentState(s pilot.Status) *AgentState {
	end := s.Ended
	if end.IsZero() {
		end = time.Now()
	}
	a := &AgentState{Task: s.Task, Model: s.Config.Model, Running: s.Running(), End: string(s.End),
		Summary: s.Summary, Steps: s.Steps, Cost: s.Cost, Tokens: s.Tokens,
		Seconds: int64(end.Sub(s.Started).Seconds())}
	if !s.Running() && a.Summary == "" {
		a.Summary = pilot.Describe(s)
	}
	return a
}
