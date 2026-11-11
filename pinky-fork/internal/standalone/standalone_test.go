package standalone

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/pilot"
	"github.com/DIYgod/pinky/internal/policy"
)

// model is a Messages API whose turns a test scripts: each gets the text
// the request ends with and returns the tool to call; the last repeats.
type model struct {
	mu    sync.Mutex
	n     int
	turns []func(last string, r *http.Request) map[string]any
}

func (m *model) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &body)
	last := ""
	if n := len(body.Messages); n > 0 {
		last = textOf(body.Messages[n-1].Content)
	}
	m.mu.Lock()
	turn := m.turns[min(m.n, len(m.turns)-1)]
	m.n++
	m.mu.Unlock()
	use := turn(last, r)
	if use == nil {
		return // cancelled
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg_1", "type": "message", "role": "assistant",
		"model": "fake", "stop_reason": "tool_use", "content": []any{use},
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 10}})
}

// textOf is a message's text: its last tool result's, or its text.
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(raw, &blocks)
	out := ""
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			for _, c := range b.Content {
				if c.Text != "" {
					out = c.Text
				}
			}
		case "text":
			if out == "" {
				out = b.Text
			}
		}
	}
	return out
}

func call(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

var refRe = regexp.MustCompile(`\[(\d+)\] \w+ "([^"]*)"`)

func refOf(t *testing.T, text, label string) int {
	t.Helper()
	for _, m := range refRe.FindAllStringSubmatch(text, -1) {
		if m[2] == label {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	t.Errorf("%q not in:\n%s", label, text)
	return 0
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type harness struct {
	core *Core
	dev  *demo.Device
	api  *model
	url  string
	sock string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dev := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev.AddApp(policy.StandaloneApp, "Pinky Standalone")
	h := &harness{dev: dev, api: &model{}}
	srv := httptest.NewServer(h.api)
	h.url = srv.URL
	c, err := New(Options{
		Providers: []driver.Provider{demo.NewProvider(driver.Android, dev)},
		Device:    "pixel",
		Version:   "test",
		Preset:    policy.PresetCommits,
		Gateway: gateway.Options{NoScreenshots: true, SettleMax: 200 * time.Millisecond,
			PollInterval: 5 * time.Millisecond, MaxBlock: 50 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond},
		Pilot: pilot.Options{HTTPClient: srv.Client()},
		Log:   quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.core = c
	pol := c.G.Policy()
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	// A short path: a Unix socket's path has a small limit on macOS.
	dir, err := os.MkdirTemp("", "pk")
	if err != nil {
		t.Fatal(err)
	}
	h.sock = filepath.Join(dir, "c")
	ln, err := net.Listen("unix", h.sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		if err := c.Serve(ctx, ln); err != nil {
			t.Error(err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-served
		c.Close()
		srv.Close()
		os.RemoveAll(dir)
	})
	for deadline := time.Now().Add(2 * time.Second); !c.State().Device.Ready; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no demo phone")
		}
	}
	return h
}

// conn is a connection to the control socket, as Pinky's screens open it.
type conn struct {
	t   *testing.T
	c   net.Conn
	in  *bufio.Scanner
	enc *json.Encoder
}

func (h *harness) dial(t *testing.T) *conn {
	t.Helper()
	c, err := net.Dial("unix", h.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	in := bufio.NewScanner(c)
	in.Buffer(make([]byte, 64<<10), 1<<20)
	return &conn{t: t, c: c, in: in, enc: json.NewEncoder(c)}
}

func (c *conn) send(req map[string]any) {
	c.t.Helper()
	if err := c.enc.Encode(req); err != nil {
		c.t.Fatal(err)
	}
}

func (c *conn) read() reply {
	c.t.Helper()
	_ = c.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if !c.in.Scan() {
		c.t.Fatalf("no reply: %v", c.in.Err())
	}
	var r reply
	if err := json.Unmarshal(c.in.Bytes(), &r); err != nil {
		c.t.Fatal(err)
	}
	return r
}

func (c *conn) do(req map[string]any) reply {
	c.t.Helper()
	c.send(req)
	return c.read()
}

// until reads a subscription until a state passes ok.
func (c *conn) until(what string, ok func(State) bool) State {
	c.t.Helper()
	for {
		r := c.read()
		if r.State == nil {
			c.t.Fatalf("waiting for %s: %+v", what, r)
		}
		if ok(*r.State) {
			return *r.State
		}
	}
}

func TestTaskThroughTheControlSocket(t *testing.T) {
	h := newHarness(t)
	h.dev.Show("cart")
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(last string, _ *http.Request) map[string]any {
			return call("t1", "phone_tap", map[string]any{"ref": refOf(t, last, "Place order · $12.80")})
		},
		func(last string, _ *http.Request) map[string]any {
			if !strings.HasPrefix(last, "settled") {
				t.Errorf("after the approval:\n%s", last)
			}
			return call("t2", "phone_launch", map[string]any{"app": "Pinky Standalone"})
		},
		func(last string, _ *http.Request) map[string]any {
			if !strings.HasPrefix(last, "blocked_by_policy") {
				t.Errorf("opening Pinky:\n%s", last)
			}
			return call("t3", "finish", map[string]any{"outcome": "done", "summary": "Ordered."})
		},
	}
	ctl := h.dial(t)
	if st := ctl.do(map[string]any{"cmd": "state"}).State; st == nil || st.Preset != int(policy.PresetCommits) ||
		!st.Device.Ready || st.Agent != nil {
		t.Fatalf("state: %+v", st)
	}
	if r := ctl.do(map[string]any{"cmd": "start", "task": "Order lunch", "base_url": h.url}); r.OK ||
		!strings.Contains(r.Error, "API key") {
		t.Fatalf("start without a key: %+v", r)
	}
	sub := h.dial(t)
	sub.send(map[string]any{"cmd": "subscribe"})
	sub.read()
	if r := ctl.do(map[string]any{"cmd": "start", "task": "Order lunch", "key": "sk-test", "base_url": h.url,
		"model": "fake"}); !r.OK {
		t.Fatalf("start: %+v", r)
	}
	st := sub.until("the approval", func(s State) bool { return len(s.Needs) > 0 })
	n := st.Needs[0]
	if n.Kind != "approval" || n.Rule != "Payments" || n.DeadlineMS == 0 {
		t.Fatalf("need: %+v", n)
	}
	if r := ctl.do(map[string]any{"cmd": "decide", "need": n.ID, "decision": "allow_once"}); !r.OK {
		t.Fatalf("decide: %+v", r)
	}
	st = sub.until("the end", func(s State) bool { return s.Agent != nil && !s.Agent.Running })
	if st.Agent.End != "done" || st.Agent.Summary != "Ordered." || st.Agent.Steps != 2 || len(st.Needs) != 0 {
		t.Fatalf("agent: %+v", st.Agent)
	}
	if st.Task == nil || st.Task.Title != "Order lunch" {
		t.Fatalf("task: %+v", st.Task)
	}
	blocked := false
	for _, s := range st.Task.Steps {
		blocked = blocked || s.Status == "blocked_by_policy"
	}
	if !blocked {
		t.Fatalf("opening Pinky was not blocked: %+v", st.Task.Steps)
	}
	if h.core.key != "" {
		t.Fatal("the key outlived the task")
	}

	// A second task shows only its own steps, and Stop ends it.
	asked := make(chan struct{})
	h.api.mu.Lock()
	h.api.n, h.api.turns = 0, []func(string, *http.Request) map[string]any{
		func(string, *http.Request) map[string]any { return call("t1", "phone_observe", nil) },
		func(_ string, r *http.Request) map[string]any {
			close(asked)
			<-r.Context().Done()
			return nil
		},
	}
	h.api.mu.Unlock()
	if r := ctl.do(map[string]any{"cmd": "start", "task": "Look around", "key": "sk-test", "base_url": h.url,
		"model": "fake"}); !r.OK {
		t.Fatalf("start: %+v", r)
	}
	<-asked
	st = sub.until("the second task", func(s State) bool { return s.Task != nil })
	if st.Task.Title != "Look around" || !st.Agent.Running {
		t.Fatalf("second task: %+v %+v", st.Task, st.Agent)
	}
	if r := ctl.do(map[string]any{"cmd": "stop"}); !r.OK {
		t.Fatalf("stop: %+v", r)
	}
	st = sub.until("stopped", func(s State) bool { return s.Agent != nil && !s.Agent.Running })
	if st.Agent.End != "stopped" || st.Task.State != string(gateway.TaskStopped) {
		t.Fatalf("after Stop: %+v %+v", st.Agent, st.Task)
	}
	if r := ctl.do(map[string]any{"cmd": "dismiss"}); !r.OK {
		t.Fatalf("dismiss: %+v", r)
	}
	if st := ctl.do(map[string]any{"cmd": "state"}).State; st.Agent != nil || st.Task != nil {
		t.Fatalf("after dismiss: %+v", st)
	}
}

func TestPreset(t *testing.T) {
	h := newHarness(t)
	ctl := h.dial(t)
	if r := ctl.do(map[string]any{"cmd": "preset", "preset": 3}); !r.OK {
		t.Fatalf("preset: %+v", r)
	}
	if st := ctl.do(map[string]any{"cmd": "state"}).State; st.Preset != 3 {
		t.Fatalf("preset: %d", st.Preset)
	}
	if r := ctl.do(map[string]any{"cmd": "preset", "preset": 9}); r.OK {
		t.Fatal("an unknown preset was taken")
	}
	if r := ctl.do(map[string]any{"cmd": "nope"}); r.OK || !strings.Contains(r.Error, "unknown command") {
		t.Fatalf("unknown command: %+v", r)
	}
}

func TestFirstRunPreset(t *testing.T) {
	dir := t.TempDir()
	open := func() *Core {
		c, err := New(Options{DataDir: dir, Preset: policy.PresetCommits, Version: "test", Log: quiet})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := open()
	if p := c.G.Policy().Preset(); p != policy.PresetCommits {
		t.Fatalf("first run: preset %d", p)
	}
	if err := c.SetPreset(policy.PresetPayments); err != nil {
		t.Fatal(err)
	}
	c.Close()
	// The person's choice stays.
	c = open()
	defer c.Close()
	if p := c.G.Policy().Preset(); p != policy.PresetPayments {
		t.Fatalf("second run: preset %d", p)
	}
}
