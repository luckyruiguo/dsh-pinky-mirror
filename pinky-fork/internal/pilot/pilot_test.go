package pilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/secret"
)

const testKey = "sk-ant-test-0123456789"

// fakeAPI is a model API whose turns a test scripts. Each turn gets the
// text of the last tool result (or of the first message) and returns
// the response's JSON; the last turn repeats.
type fakeAPI struct {
	t     *testing.T
	turns []func(last string, r *http.Request) any

	mu   sync.Mutex
	reqs []request
}

type request struct {
	path   string
	header http.Header
	raw    string
	body   map[string]any
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	var body map[string]any
	json.Unmarshal(data, &body)
	f.mu.Lock()
	n := len(f.reqs)
	f.reqs = append(f.reqs, request{path: r.URL.Path, header: r.Header.Clone(), raw: string(data), body: body})
	f.mu.Unlock()
	turn := f.turns[min(n, len(f.turns)-1)]
	out := turn(lastText(body), r)
	if out == nil {
		return // the request was cancelled
	}
	if e, ok := out.(apiFailure); ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(e.status)
		w.Write([]byte(e.body))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (f *fakeAPI) requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.reqs...)
}

type apiFailure struct {
	status int
	body   string
}

// lastText is the text a request ends with: its last tool result's, or
// its last message's.
func lastText(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	// OpenAI: the last tool message, or the last message.
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m["role"] == "tool" {
			s, _ := m["content"].(string)
			return s
		}
		if m["role"] == "assistant" {
			break
		}
	}
	m, _ := msgs[len(msgs)-1].(map[string]any)
	switch c := m["content"].(type) {
	case string:
		return c
	case []any:
		text := ""
		for _, b := range c {
			b, _ := b.(map[string]any)
			switch b["type"] {
			case "tool_result":
				parts, _ := b["content"].([]any)
				for _, p := range parts {
					p, _ := p.(map[string]any)
					if s, ok := p["text"].(string); ok {
						text = s
					}
				}
			case "text":
				if text == "" {
					text, _ = b["text"].(string)
				}
			}
		}
		return text
	}
	return ""
}

// Responses of the Messages API.
func message(stop string, u int64, blocks ...map[string]any) map[string]any {
	return map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5-5",
		"content": blocks, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": u, "output_tokens": 50, "cache_read_input_tokens": 2000, "cache_creation_input_tokens": 0}}
}

func use(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func say(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

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

type harness struct {
	g    *gateway.Gateway
	dev  *demo.Device
	api  *fakeAPI
	r    *Runner
	keys map[string]string
}

func newHarness(t *testing.T, gopts ...func(*gateway.Options)) *harness {
	t.Helper()
	dev := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	pol := policy.Default()
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	o := gateway.Options{Providers: []driver.Provider{demo.NewProvider(driver.Android, dev)}, Policy: pol,
		NoScreenshots: true, SettleMax: 200 * time.Millisecond, PollInterval: 5 * time.Millisecond,
		MaxBlock: 2 * time.Second, DiscoverEvery: 20 * time.Millisecond}
	for _, f := range gopts {
		f(&o)
	}
	g, err := gateway.New(o)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{g: g, dev: dev, api: &fakeAPI{t: t}, keys: map[string]string{Anthropic: testKey, OpenAI: testKey}}
	srv := httptest.NewServer(h.api)
	old := anthropicURL
	anthropicURL = srv.URL
	h.r = New(Options{Server: mcp.New(g, "test"), Version: "test", HTTPClient: srv.Client(),
		Key: func(ctx context.Context, provider string) (string, error) {
			if k := h.keys[provider]; k != "" {
				return k, nil
			}
			return "", secret.ErrNotFound
		}})
	t.Cleanup(func() {
		h.r.Close()
		srv.Close()
		anthropicURL = old
		g.Close()
	})
	// The demo phone shows up after the first discovery.
	for deadline := time.Now().Add(2 * time.Second); len(g.Devices()) == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no demo phone")
		}
	}
	return h
}

// run starts a task and waits for it to end.
func (h *harness) run(t *testing.T, cfg Config, task string) Status {
	t.Helper()
	if err := h.r.Start(cfg, "pixel", task); err != nil {
		t.Fatal(err)
	}
	return h.wait(t)
}

func (h *harness) wait(t *testing.T) Status {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		st, ok := h.r.Status("pixel")
		if !ok {
			t.Fatal("no status")
		}
		if !st.Running() {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("still running: %+v", st)
		}
	}
}

// task is the gateway's task of the last run, once it ended.
func (h *harness) task(t *testing.T) gateway.TaskView {
	t.Helper()
	h.g.Flush()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		tasks, err := h.g.History(context.Background(), gateway.HistoryQuery{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) == 1 && !tasks[0].Ended.IsZero() {
			return tasks[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no ended task: %+v", tasks)
		}
	}
}

// released is the outcome and summary the task was released with.
func released(t *testing.T, tv gateway.TaskView) (outcome, summary string) {
	t.Helper()
	for _, s := range tv.Steps {
		if s.Tool == "phone_release" {
			var a struct{ Outcome, Summary string }
			json.Unmarshal([]byte(s.Args), &a)
			return a.Outcome, a.Summary
		}
	}
	return "", ""
}

func TestDoneTask(t *testing.T) {
	h := newHarness(t)
	h.api.turns = []func(string, *http.Request) any{
		func(last string, _ *http.Request) any {
			if !strings.Contains(last, "<task>\nAdd my usual lunch to the cart\n</task>") || !strings.Contains(last, "settled · Pixel 9") {
				t.Errorf("first message:\n%s", last)
			}
			return message("tool_use", 1000, say("Opening Dinely."), use("t1", "phone_launch", map[string]any{"app": "Dinely"}))
		},
		func(last string, _ *http.Request) any {
			return message("tool_use", 1000, use("t2", "phone_tap", map[string]any{"ref": refOf(t, last, "Orders")}))
		},
		func(last string, _ *http.Request) any {
			return message("tool_use", 1000, use("t3", "phone_tap", map[string]any{"ref": refOf(t, last, "Reorder · Chicken rice bowl")}))
		},
		func(last string, _ *http.Request) any {
			if !strings.Contains(last, "Added to cart") {
				t.Errorf("diff:\n%s", last)
			}
			return message("tool_use", 1000, use("t4", finishName, map[string]any{"outcome": "done", "summary": "Added the chicken rice bowl to the cart."}))
		},
	}
	st := h.run(t, Config{}, "Add my usual lunch to the cart")
	if st.End != Done || st.Summary != "Added the chicken rice bowl to the cart." || st.Steps != 3 || !st.Priced {
		t.Fatalf("status: %+v", st)
	}
	// 4 requests of 1000 input, 50 output and 2000 cache reads on Opus 5.5.
	if want := 4 * (1000*4 + 50*20 + 2000*0.2) / 1e6; st.Cost < want*0.999 || st.Cost > want*1.001 || st.Tokens != 4*3050 {
		t.Fatalf("cost %v, tokens %d", st.Cost, st.Tokens)
	}
	tv := h.task(t)
	if tv.Agent != mcp.BuiltinName || tv.State != gateway.TaskDone || tv.Title != "Add my usual lunch to the cart" {
		t.Fatalf("task: %+v", tv)
	}
	if o, s := released(t, tv); o != "done" || s != st.Summary {
		t.Fatalf("released %q %q", o, s)
	}

	reqs := h.api.requests()
	if len(reqs) != 4 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[0]
	if r.path != "/v1/messages" || r.header.Get("Authorization") != "Bearer "+testKey || r.header.Get("X-Api-Key") != "" {
		t.Fatalf("request: %s %v", r.path, r.header)
	}
	beta := r.header.Get("Anthropic-Beta")
	if !strings.Contains(beta, "context-management-2025-06-27") || !strings.Contains(beta, "server-side-fallback-2026-07-01") {
		t.Fatalf("betas: %q", beta)
	}
	for _, req := range reqs {
		if strings.Contains(req.raw, testKey) {
			t.Fatal("the key is in a request's body")
		}
	}
	b := r.body
	if b["model"] != "claude-opus-5-5" || b["fallbacks"] != "default" || b["thinking"] != nil || b["tool_choice"] != nil {
		t.Fatalf("body: %s", r.raw)
	}
	if oc, _ := b["output_config"].(map[string]any); oc["effort"] != "medium" {
		t.Fatalf("output_config: %v", b["output_config"])
	}
	if cc, _ := b["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("cache_control: %v", b["cache_control"])
	}
	cm, _ := json.Marshal(b["context_management"])
	if !strings.Contains(string(cm), `"type":"clear_tool_uses_20250919"`) || !strings.Contains(string(cm), `"keep":{"type":"tool_uses","value":5}`) {
		t.Fatalf("context_management: %s", cm)
	}
	sys, _ := b["system"].([]any)
	if len(sys) != 1 || !strings.Contains(fmt.Sprint(sys[0]), "untrusted data") || !strings.Contains(fmt.Sprint(sys[0]), "cache_control") {
		t.Fatalf("system: %v", sys)
	}
	tools, _ := b["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range tools {
		tl, _ := tl.(map[string]any)
		names[tl["name"].(string)] = true
	}
	for _, n := range []string{"phone_tap", "phone_observe", "phone_handoff", "phone_batch", finishName} {
		if !names[n] {
			t.Errorf("no %s in %v", n, names)
		}
	}
	for n := range leftOut {
		if names[n] {
			t.Errorf("%s offered", n)
		}
	}
	if last, _ := tools[len(tools)-1].(map[string]any); last["name"] != finishName || last["cache_control"] == nil {
		t.Fatalf("last tool: %v", last)
	}
	// The history only grows: the second request starts with the first
	// one's message and the model's answer.
	msgs, _ := reqs[1].body["messages"].([]any)
	if len(msgs) != 3 || fmt.Sprint(msgs[0]) != fmt.Sprint(b["messages"].([]any)[0]) || !strings.Contains(fmt.Sprint(msgs[1]), "Opening Dinely.") {
		t.Fatalf("second request's messages: %v", msgs)
	}
}

func TestApprovalWaits(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.MaxBlock = 30 * time.Millisecond })
	h.dev.Show("cart")
	decided := make(chan struct{})
	go func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if needs := h.g.Needs(); len(needs) > 0 {
				// Longer than the task's time limit: the wait does not count.
				time.Sleep(1200 * time.Millisecond)
				h.g.Decide(needs[0].ID, gateway.AllowOnce, "go ahead")
				close(decided)
				return
			}
		}
	}()
	h.api.turns = []func(string, *http.Request) any{
		func(last string, _ *http.Request) any {
			return message("tool_use", 100, use("t1", "phone_tap", map[string]any{"ref": refOf(t, last, "Place order · $12.80")}))
		},
		func(last string, _ *http.Request) any {
			if !strings.HasPrefix(last, "settled") || !strings.Contains(last, `note: "go ahead"`) {
				t.Errorf("after the approval:\n%s", last)
			}
			return message("tool_use", 100, use("t2", finishName, map[string]any{"outcome": "done", "summary": "Ordered."}))
		},
	}
	st := h.run(t, Config{MaxTime: time.Second}, "Order lunch")
	<-decided
	if st.End != Done || st.Steps != 1 {
		t.Fatalf("status: %+v", st)
	}
}

func TestHandoffWaits(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.MaxBlock = 30 * time.Millisecond })
	go func() {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			for _, n := range h.g.Needs() {
				if n.Kind == gateway.NeedHandoff {
					time.Sleep(200 * time.Millisecond)
					h.g.TakeOver("pixel")
					h.g.HandBack("pixel", "signed in for you")
					return
				}
			}
		}
	}()
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t1", "phone_handoff", map[string]any{"reason": "Please sign in to Dinely"}))
		},
		func(last string, _ *http.Request) any {
			if !strings.HasPrefix(last, "settled") || !strings.Contains(last, "signed in for you") {
				t.Errorf("after the handoff:\n%s", last)
			}
			return message("tool_use", 100, use("t2", finishName, map[string]any{"outcome": "done", "summary": "Signed in."}))
		},
	}
	if st := h.run(t, Config{}, "Sign in"); st.End != Done {
		t.Fatalf("status: %+v", st)
	}
}

func TestStop(t *testing.T) {
	h := newHarness(t)
	asked := make(chan struct{})
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t1", "phone_observe", nil))
		},
		func(_ string, r *http.Request) any {
			close(asked)
			<-r.Context().Done()
			return nil
		},
	}
	if err := h.r.Start(Config{}, "pixel", "Look around"); err != nil {
		t.Fatal(err)
	}
	if err := h.r.Start(Config{}, "pixel", "Something else"); err != ErrBusy {
		t.Fatalf("a second task: %v", err)
	}
	<-asked
	// As the app does: the gateway's task first, then the run.
	h.g.Stop("pixel")
	h.r.Stop("pixel")
	if st := h.wait(t); st.End != Stopped {
		t.Fatalf("status: %+v", st)
	}
	if tv := h.task(t); tv.State != gateway.TaskStopped {
		t.Fatalf("task: %+v", tv)
	}
	h.r.Dismiss("pixel")
	if _, ok := h.r.Status("pixel"); ok {
		t.Fatal("not dismissed")
	}
}

func TestStoppedByThePerson(t *testing.T) {
	// The person stops the task in Pinky while the model thinks: the next
	// call comes back stopped and the task ends without another request.
	h := newHarness(t)
	asked := make(chan struct{})
	stopped := make(chan struct{})
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			close(asked)
			<-stopped
			return message("tool_use", 100, use("t1", "phone_observe", nil))
		},
		func(string, *http.Request) any {
			t.Error("asked again after stopped")
			return message("end_turn", 100, say("?"))
		},
	}
	if err := h.r.Start(Config{}, "pixel", "Look around"); err != nil {
		t.Fatal(err)
	}
	<-asked
	h.g.Stop("pixel")
	close(stopped)
	if st := h.wait(t); st.End != Stopped || st.Steps != 1 {
		t.Fatalf("status: %+v", st)
	}
}

func TestLimits(t *testing.T) {
	observe := func(string, *http.Request) any { return message("tool_use", 100, use("t", "phone_observe", nil)) }
	t.Run("steps", func(t *testing.T) {
		h := newHarness(t)
		h.api.turns = []func(string, *http.Request) any{observe}
		st := h.run(t, Config{MaxSteps: 2}, "Look around")
		if st.End != StepLimit || st.Steps != 2 || len(h.api.requests()) != 2 {
			t.Fatalf("status: %+v, %d requests", st, len(h.api.requests()))
		}
		tv := h.task(t)
		if o, s := released(t, tv); tv.State != gateway.TaskFailed || o != "failed" || s != "Stopped: reached the limit of 2 steps." {
			t.Fatalf("task %s, released %q %q", tv.State, o, s)
		}
	})
	t.Run("cost", func(t *testing.T) {
		h := newHarness(t)
		h.api.turns = []func(string, *http.Request) any{func(string, *http.Request) any {
			return message("tool_use", 300_000, use("t", "phone_observe", nil)) // US$1.20 a request
		}}
		st := h.run(t, Config{MaxCost: 2}, "Look around")
		if st.End != CostLimit || st.Steps != 1 || st.Cost < 2 {
			t.Fatalf("status: %+v", st)
		}
		if _, s := released(t, h.task(t)); s != "Stopped: reached the limit of US$2.00." {
			t.Fatalf("released %q", s)
		}
	})
	t.Run("time", func(t *testing.T) {
		h := newHarness(t)
		h.api.turns = []func(string, *http.Request) any{func(string, *http.Request) any {
			time.Sleep(60 * time.Millisecond)
			return message("tool_use", 100, use("t", "phone_observe", nil))
		}}
		if st := h.run(t, Config{MaxTime: 100 * time.Millisecond}, "Look around"); st.End != TimeLimit {
			t.Fatalf("status: %+v", st)
		}
	})
	t.Run("tokens", func(t *testing.T) {
		// A model whose prices Pinky does not know: a budget of tokens.
		h := newHarness(t)
		h.api.turns = []func(string, *http.Request) any{observe}
		st := h.run(t, Config{Model: "glm-5", BaseURL: anthropicURL + "/api/anthropic", MaxCost: 0.003}, "Look around")
		if st.End != CostLimit || st.Priced || st.Cost != 0 || st.Tokens < 3000 {
			t.Fatalf("status: %+v", st)
		}
		if _, s := released(t, h.task(t)); s != "Stopped: reached the limit of 3000 tokens." {
			t.Fatalf("released %q", s)
		}
	})
}

func TestEnds(t *testing.T) {
	t.Run("refused", func(t *testing.T) {
		h := newHarness(t)
		h.api.turns = []func(string, *http.Request) any{func(string, *http.Request) any {
			return message("refusal", 100)
		}}
		st := h.run(t, Config{}, "Something")
		if st.End != Refused {
			t.Fatalf("status: %+v", st)
		}
		if o, s := released(t, h.task(t)); o != "failed" || s != "Couldn't finish: the model declined this task." {
			t.Fatalf("released %q %q", o, s)
		}
	})
	t.Run("bad key", func(t *testing.T) {
		h := newHarness(t)
		h.api.turns = []func(string, *http.Request) any{func(string, *http.Request) any {
			return apiFailure{401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key ` + testKey + `"}}`}
		}}
		st := h.run(t, Config{}, "Something")
		if st.End != BadKey || strings.Contains(st.Summary, testKey) || !strings.Contains(st.Summary, "invalid x-api-key") {
			t.Fatalf("status: %+v", st)
		}
		if tv := h.task(t); tv.State != gateway.TaskFailed {
			t.Fatalf("task: %+v", tv)
		}
	})
	t.Run("server error", func(t *testing.T) {
		h := newHarness(t)
		h.api.turns = []func(string, *http.Request) any{func(string, *http.Request) any {
			return apiFailure{400, `{"type":"error","error":{"type":"invalid_request_error","message":"model: not found"}}`}
		}}
		st := h.run(t, Config{Model: "claude-nonexistent"}, "Something")
		if st.End != Error || !strings.Contains(st.Summary, "400") || !strings.Contains(st.Summary, "model: not found") {
			t.Fatalf("status: %+v", st)
		}
	})
	t.Run("no key", func(t *testing.T) {
		h := newHarness(t)
		h.keys = nil
		if st := h.run(t, Config{}, "Something"); st.End != NoKey {
			t.Fatalf("status: %+v", st)
		}
		if len(h.api.requests()) != 0 || len(h.g.Tasks()) != 0 {
			t.Fatal("ran without a key")
		}
	})
	t.Run("no tools", func(t *testing.T) {
		// It writes instead of calling a tool: once it is asked to go on,
		// then the task ends with what it wrote.
		h := newHarness(t)
		h.api.turns = []func(string, *http.Request) any{
			func(string, *http.Request) any { return message("end_turn", 100, say("Which phone?")) },
			func(last string, _ *http.Request) any {
				if last != nudge {
					t.Errorf("nudge: %q", last)
				}
				return message("end_turn", 100, say("I cannot tell what to do."))
			},
		}
		st := h.run(t, Config{}, "Do the thing")
		if st.End != Failed || st.Summary != "I cannot tell what to do." {
			t.Fatalf("status: %+v", st)
		}
	})
	t.Run("tools it lacks", func(t *testing.T) {
		h := newHarness(t)
		h.api.turns = []func(string, *http.Request) any{
			func(string, *http.Request) any {
				return message("tool_use", 100, use("t1", "phone_connect", map[string]any{"device": "other"}), use("t2", "phone_files", nil))
			},
			func(last string, _ *http.Request) any {
				if !strings.HasPrefix(last, "error: unknown_tool") {
					t.Errorf("result: %q", last)
				}
				return message("tool_use", 100, use("t3", finishName, map[string]any{"outcome": "failed", "summary": "Gave up."}))
			},
		}
		st := h.run(t, Config{}, "Do the thing")
		if st.End != Failed || st.Steps != 0 || st.Summary != "Gave up." {
			t.Fatalf("status: %+v", st)
		}
		if o, _ := released(t, h.task(t)); o != "failed" {
			t.Fatalf("released %q", o)
		}
	})
}

func TestStartErrors(t *testing.T) {
	r := New(Options{})
	defer r.Close()
	if err := r.Start(Config{}, "pixel", "  "); err != ErrNoTask {
		t.Fatalf("no task: %v", err)
	}
	if err := r.Start(Config{Provider: OpenAI}, "pixel", "Do it"); err != ErrNoModel {
		t.Fatalf("no model: %v", err)
	}
	r.Close()
	if err := r.Start(Config{}, "pixel", "Do it"); err != ErrClosed {
		t.Fatalf("closed: %v", err)
	}
}

func TestCustomAnthropicAddress(t *testing.T) {
	// A service that speaks the Messages API gets the plain API, and the key
	// in both headers.
	h := newHarness(t)
	h.api.turns = []func(string, *http.Request) any{func(string, *http.Request) any {
		return message("tool_use", 100, use("t", finishName, map[string]any{"outcome": "done", "summary": "ok"}))
	}}
	st := h.run(t, Config{Model: "kimi-k3", BaseURL: anthropicURL + "/anthropic/"}, "Something")
	if st.End != Done {
		t.Fatalf("status: %+v", st)
	}
	r := h.api.requests()[0]
	if r.path != "/anthropic/v1/messages" || r.header.Get("X-Api-Key") != testKey || r.header.Get("Authorization") != "Bearer "+testKey ||
		r.header.Get("Anthropic-Beta") != "" {
		t.Fatalf("request: %s %v", r.path, r.header)
	}
	for _, k := range []string{"context_management", "fallbacks", "output_config"} {
		if r.body[k] != nil {
			t.Errorf("%s sent: %v", k, r.body[k])
		}
	}
	if sys, _ := r.body["system"].([]any); len(sys) != 1 || !strings.Contains(fmt.Sprint(sys[0]), "cache_control") {
		t.Fatalf("system: %v", sys)
	}
}

func TestFallbacksOnlyWhereSupported(t *testing.T) {
	for model, want := range map[string]bool{"claude-opus-5-5": true, "claude-opus-5": true, "claude-fable-5-2": true,
		"claude-sonnet-5-5": true, "claude-fable-5-1": true, "claude-sonnet-5": false, "claude-haiku-5-5": false, "claude-opus-5-5-20261101": true} {
		c := newClaude(Config{Provider: Anthropic, Model: model}.Normalize(), testKey, http.DefaultClient, "s", nil)
		got := len(c.betas) == 2
		if got != want {
			t.Errorf("%s: fallbacks %v, want %v", model, got, want)
		}
	}
}

// The model's blocks go back as they came, less null fields; before the
// last fallback block, only text and the fallback blocks go back.
func TestEcho(t *testing.T) {
	var in []json.RawMessage
	for _, b := range []string{
		`{"type":"thinking","thinking":"a","signature":"s1"}`,
		`{"type":"text","text":"first","citations":null}`,
		`{"type":"tool_use","id":"t1","name":"phone_observe","input":{}}`,
		`{"type":"fallback","model":"claude-opus-5"}`,
		`{"type":"thinking","thinking":"b","signature":"s2"}`,
		`{"type":"tool_use","id":"t2","name":"phone_tap","input":{"ref":3}}`,
	} {
		in = append(in, json.RawMessage(b))
	}
	var got []string
	for _, b := range echo(in) {
		got = append(got, string(b.(json.RawMessage)))
	}
	want := []string{
		`{"text":"first","type":"text"}`,
		`{"type":"fallback","model":"claude-opus-5"}`,
		`{"type":"thinking","thinking":"b","signature":"s2"}`,
		`{"type":"tool_use","id":"t2","name":"phone_tap","input":{"ref":3}}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("echo:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Without a fallback, everything goes back.
	if n := len(echo(in[:3])); n != 3 {
		t.Errorf("echo without a fallback kept %d of 3 blocks", n)
	}
}

func TestOpenAI(t *testing.T) {
	h := newHarness(t)
	toolCall := func(id, name string, args map[string]any) any {
		a, _ := json.Marshal(args)
		return map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": nil, "reasoning_content": "thinking…",
				"tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(a)}}}},
				"finish_reason": "tool_calls"}},
			"usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 20, "prompt_tokens_details": map[string]any{"cached_tokens": 800}},
		}
	}
	h.api.turns = []func(string, *http.Request) any{
		func(last string, _ *http.Request) any {
			if !strings.Contains(last, "<task>") {
				t.Errorf("first message: %q", last)
			}
			return toolCall("c1", "phone_launch", map[string]any{"app": "Dinely"})
		},
		func(last string, _ *http.Request) any {
			if !strings.HasPrefix(last, "settled") {
				t.Errorf("tool result: %q", last)
			}
			return toolCall("c2", finishName, map[string]any{"outcome": "done", "summary": "Opened Dinely."})
		},
	}
	h.keys[OpenAI] = "sk-openai-test"
	st := h.run(t, Config{Provider: OpenAI, Model: "gpt-6", BaseURL: anthropicURL + "/v1"}, "Open Dinely")
	if st.End != Done || st.Summary != "Opened Dinely." || st.Steps != 1 || st.Priced || st.Tokens != 2*1020 {
		t.Fatalf("status: %+v", st)
	}
	reqs := h.api.requests()
	r := reqs[0]
	if r.path != "/v1/chat/completions" || r.header.Get("Authorization") != "Bearer sk-openai-test" || strings.Contains(r.raw, "sk-openai-test") {
		t.Fatalf("request: %s %v", r.path, r.header)
	}
	if r.body["model"] != "gpt-6" || r.body["tool_choice"] != "auto" {
		t.Fatalf("body: %s", r.raw)
	}
	tools, _ := r.body["tools"].([]any)
	tl, _ := tools[0].(map[string]any)
	fn, _ := tl["function"].(map[string]any)
	if tl["type"] != "function" || fn["name"] == "" || fn["parameters"] == nil || strings.Contains(r.raw, `"$schema"`) {
		t.Fatalf("tool: %v", tl)
	}
	msgs, _ := reqs[1].body["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages: %v", msgs)
	}
	asst, _ := msgs[2].(map[string]any)
	tm, _ := msgs[3].(map[string]any)
	if asst["role"] != "assistant" || asst["reasoning_content"] != "thinking…" || tm["role"] != "tool" || tm["tool_call_id"] != "c1" {
		t.Fatalf("messages: %v", msgs)
	}
}

func TestOpenAIImages(t *testing.T) {
	// A model that takes no images: the request goes again without them,
	// and later ones carry none.
	var posts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		posts = append(posts, string(data))
		if strings.Contains(string(data), "image_url") {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"image input is not supported"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	o := newOpenAI(Config{Provider: OpenAI, Model: "text-only", BaseURL: srv.URL}, "k", srv.Client(), "sys", nil)
	o.history = append(o.history, oaMessage{Role: "assistant", ToolCalls: []oaToolCall{{ID: "c1", Type: "function"}}})
	tr, err := o.send(context.Background(), []result{{id: "c1", text: "settled", images: []image{{data: []byte{1, 2, 3}, mime: "image/jpeg"}}}}, "")
	if err != nil || tr.text != "ok" || !o.noImages || len(posts) != 2 || strings.Contains(posts[1], "image_url") {
		t.Fatalf("turn %+v, err %v, posts %d", tr, err, len(posts))
	}
	o.history = append(o.history, oaMessage{Role: "assistant", ToolCalls: []oaToolCall{{ID: "c2", Type: "function"}}})
	if _, err := o.send(context.Background(), []result{{id: "c2", text: "settled", images: []image{{data: []byte{1}}}}}, ""); err != nil || len(posts) != 3 {
		t.Fatalf("err %v, posts %d", err, len(posts))
	}
}

func TestOpenAIRetries(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	o := newOpenAI(Config{Provider: OpenAI, Model: "m", BaseURL: srv.URL}, "k", srv.Client(), "sys", nil)
	if tr, err := o.send(context.Background(), nil, "hi"); err != nil || tr.text != "ok" || n != 2 {
		t.Fatalf("turn %+v, err %v, %d posts", tr, err, n)
	}
	o = newOpenAI(Config{Provider: OpenAI, Model: "m", BaseURL: srv.URL + "/missing"}, "k", srv.Client(), "sys", nil)
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"no such model"}`))
	})
	if _, err := o.send(context.Background(), nil, "hi"); err == nil || err.Error() != "the API answered 404 Not Found: no such model" {
		t.Fatalf("err %v", err)
	}
}

func TestClearOld(t *testing.T) {
	big := strings.Repeat("x", 40_000)
	c := newClaude(Config{Provider: Anthropic, Model: "glm-5", BaseURL: "http://example.invalid"}, "k", http.DefaultClient, "s", nil)
	if !c.trim {
		t.Fatal("no trimming for a model of another maker")
	}
	for i := range 6 {
		c.history = append(c.history, anthropicUser(result{id: strconv.Itoa(i), text: big}))
	}
	c.clearOld()
	for i, m := range c.history {
		got := m.Content[0].(*anToolResult).Content[0].(anText).Text
		if (i == 0) != (got == cleared) {
			t.Fatalf("result %d: %.20q", i, got)
		}
	}
	if c := newClaude(Config{Provider: Anthropic, Model: "claude-opus-5-5", BaseURL: "http://example.invalid"}, "k", http.DefaultClient, "s", nil); c.trim {
		t.Fatal("a Claude model's history edited")
	}

	o := newOpenAI(Config{Provider: OpenAI, Model: "m"}, "k", http.DefaultClient, "s", nil)
	for i := range 6 {
		o.history = append(o.history, oaMessage{Role: "assistant"}, oaMessage{Role: "tool", ToolCallID: strconv.Itoa(i), Content: big})
	}
	o.clearOld()
	for i, m := range o.history {
		if m.Role != "tool" {
			continue
		}
		if (i == 2) != (m.Content == cleared) {
			t.Fatalf("message %d: %.20q", i, m.Content)
		}
	}
}

func TestPrices(t *testing.T) {
	for model, want := range map[string]float64{"claude-opus-5-5": 4, "claude-opus-5": 5, "claude-sonnet-5-5-20261001": 2, "claude-haiku-5-5": 0.10} {
		// Per million tokens, counted on 10,000.
		p, ok := priceOf(Config{Provider: Anthropic, Model: model})
		if got := p.cost(usage{input: 10_000}) * 100; !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("%s: %v %v", model, got, ok)
		}
	}
	// Haiku 5.5 costs 5 times as much once a prompt is over 100K tokens.
	p, _ := priceOf(Config{Provider: Anthropic, Model: "claude-haiku-5-5"})
	if got := p.cost(usage{input: 50_000, cacheRead: 60_000}); math.Abs(got-(50_000*0.50+60_000*0.05)/1e6) > 1e-9 {
		t.Errorf("Haiku 5.5 above 100K tokens: %v", got)
	}
	if _, ok := priceOf(Config{Provider: OpenAI, Model: "claude-opus-5-5"}); ok {
		t.Error("priced over another API")
	}
	if _, ok := priceOf(Config{Provider: Anthropic, Model: "glm-5"}); ok {
		t.Error("priced an unknown model")
	}
}

func TestNormalize(t *testing.T) {
	c := Config{Provider: " OpenAI ", Model: " m ", BaseURL: "https://x/v1/ "}.Normalize()
	if c.Provider != OpenAI || c.Model != "m" || c.BaseURL != "https://x/v1" || c.MaxSteps != 100 || c.MaxTime != 20*time.Minute || c.MaxCost != 2 {
		t.Fatalf("%+v", c)
	}
	if c := (Config{}).Normalize(); c.Provider != Anthropic || c.Model != DefaultModel {
		t.Fatalf("%+v", c)
	}
}

func anthropicUser(r result) anMessage {
	return anMessage{Role: "user", Content: []any{toolResult(r)}}
}
