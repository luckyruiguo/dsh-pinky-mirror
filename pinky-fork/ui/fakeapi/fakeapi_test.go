package fakeapi

import (
	"context"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
)

// waitFor polls the fake until cond holds, for up to ten seconds of real
// time; the scripts run at 50 times their pace.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func approval(a *API) (gateway.NeedView, bool) {
	for _, n := range a.Needs() {
		if n.Kind == gateway.NeedApproval {
			return n, true
		}
	}
	return gateway.NeedView{}, false
}

func TestLunchAllowed(t *testing.T) {
	a := New(Options{Scenario: OnePhone, Speed: 50})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := a.Subscribe(ctx)

	var n gateway.NeedView
	waitFor(t, "the payment approval", func() (ok bool) { n, ok = approval(a); return })
	if n.Title != "Pay $12.80?" || n.Rule != "Payments" || n.Screenshot == nil || n.Target.Empty() {
		t.Fatalf("approval = %+v", n)
	}
	if d := a.Devices()[0]; d.Status != gateway.DeviceNeedsYou {
		t.Errorf("device status %q while asking, want needs_you", d.Status)
	}
	if err := a.Decide(n.ID, gateway.AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the task to finish", func() bool {
		ts := a.Tasks()
		return len(ts) == 1 && ts[0].State == gateway.TaskDone
	})
	steps := a.Tasks()[0].Steps
	if got := steps[len(steps)-1].Text; got != "Delivery in 25–35 min" {
		t.Errorf("last step %q", got)
	}
	if d := a.Devices()[0]; d.Status != gateway.DeviceIdle {
		t.Errorf("device status %q after the task, want idle", d.Status)
	}
	hist, _ := a.History(ctx, gateway.HistoryQuery{})
	if len(hist) != 6 || hist[0].Title != "Reorder last week's lunch" {
		t.Errorf("history has %d tasks, newest %q", len(hist), hist[0].Title)
	}
	select {
	case <-events:
	default:
		t.Error("no events")
	}
}

func TestTouchTakesOver(t *testing.T) {
	a := New(Options{Scenario: OnePhone, Speed: 50})
	waitFor(t, "the payment approval", func() bool { _, ok := approval(a); return ok })
	if err := a.Touch("pixel-9", driver.TouchDown, screen.Point{X: 500, Y: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, ok := approval(a); ok {
		t.Error("the approval stayed open after the person took over")
	}
	d := a.Devices()[0]
	if d.Status != gateway.DeviceInControl {
		t.Fatalf("device status %q, want in_control", d.Status)
	}
	if st := a.Tasks()[0].State; st != gateway.TaskPaused {
		t.Errorf("task state %q, want paused", st)
	}
	if err := a.HandBack("pixel-9", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the agent to ask again", func() bool { _, ok := approval(a); return ok })
	if err := a.StopAll(); err != nil {
		t.Fatal(err)
	}
	if st := a.Tasks()[0].State; st != gateway.TaskStopped {
		t.Errorf("task state %q after Stop all, want stopped", st)
	}
	if len(a.Needs()) != 0 {
		t.Error("needs left after Stop all")
	}
}

func TestLockedUnlock(t *testing.T) {
	a := New(Options{Scenario: Locked, Speed: 50})
	waitFor(t, "the unlock card", func() bool {
		ns := a.Needs()
		return len(ns) == 1 && ns[0].Kind == gateway.NeedUnlock
	})
	if d := a.Devices()[0]; d.Status != gateway.DeviceLocked {
		t.Errorf("device status %q, want locked", d.Status)
	}
	if err := a.Unlock("pixel-9"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the send approval", func() bool {
		n, ok := approval(a)
		return ok && n.Title == "Send a message to Sam?"
	})
}

func TestThreePhones(t *testing.T) {
	a := New(Options{Scenario: ThreePhones, Speed: 50})
	ds := a.Devices()
	if len(ds) != 3 || ds[2].Status != gateway.DeviceOffline {
		t.Fatalf("devices %+v", ds)
	}
	waitFor(t, "two approvals", func() bool { return len(a.Needs()) == 2 })
	a.Play(OnePhone)
	if len(a.Devices()) != 1 || len(a.Needs()) != 0 {
		t.Errorf("Play did not reset: %d devices, %d needs", len(a.Devices()), len(a.Needs()))
	}
}

func needOf(a *API, kind gateway.NeedKind) (gateway.NeedView, bool) {
	for _, n := range a.Needs() {
		if n.Kind == kind {
			return n, true
		}
	}
	return gateway.NeedView{}, false
}

func TestHandoffDone(t *testing.T) {
	a := New(Options{Scenario: Handoff, Speed: 50})
	var n gateway.NeedView
	waitFor(t, "the handoff", func() (ok bool) { n, ok = needOf(a, gateway.NeedHandoff); return })
	if n.Title != "Sign in to Dinely, please" || n.DeviceID != "pixel-9" {
		t.Fatalf("handoff = %+v", n)
	}
	if d := a.Devices()[0]; d.Status != gateway.DeviceNeedsYou {
		t.Errorf("device status %q during the handoff", d.Status)
	}
	if err := a.Decide(n.ID, gateway.AllowOnce, "Signed in as Kim"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the task to finish", func() bool {
		ts := a.Tasks()
		return len(ts) == 1 && ts[0].State == gateway.TaskDone
	})
	var found bool
	for _, s := range a.Tasks()[0].Steps {
		if s.Tool == "phone_handoff" {
			found = true
			if s.Status != "settled" || s.Text != "Asked you to take over: Sign in to Dinely, please\nYour note: “Signed in as Kim”" {
				t.Errorf("handoff step %q (%s)", s.Text, s.Status)
			}
		}
	}
	if !found {
		t.Error("no handoff step")
	}
}

func TestHandoffTakeOver(t *testing.T) {
	a := New(Options{Scenario: Handoff, Speed: 50})
	var n gateway.NeedView
	waitFor(t, "the handoff", func() (ok bool) { n, ok = needOf(a, gateway.NeedHandoff); return })
	if err := a.Decide(n.ID, gateway.TakeOver, ""); err != nil {
		t.Fatal(err)
	}
	if d := a.Devices()[0]; d.Status != gateway.DeviceInControl {
		t.Fatalf("device status %q after Take over", d.Status)
	}
	if _, open := needOf(a, gateway.NeedHandoff); open {
		t.Error("the handoff is still open")
	}
	if err := a.HandBack("pixel-9", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the task to finish", func() bool {
		ts := a.Tasks()
		return len(ts) == 1 && ts[0].State == gateway.TaskDone
	})
	for _, s := range a.Tasks()[0].Steps {
		if s.Tool == "phone_handoff" && s.Status != "settled" {
			t.Errorf("handoff step status %q after hand back", s.Status)
		}
	}
}

func TestNewAgent(t *testing.T) {
	a := New(Options{Scenario: NewAgent, Speed: 50})
	ctx := context.Background()
	n, ok := approval(a)
	if !ok || n.Rule != "New agent" || n.Agent != "Codex" || n.DeviceID != "" {
		t.Fatalf("approval = %+v", n)
	}
	agents, _ := a.Agents(ctx)
	if len(agents) != 2 || agents[1].Name != "Codex" || agents[1].Approved {
		t.Fatalf("agents = %+v", agents)
	}
	scope := gateway.Scope{Mode: gateway.ScopeApps, Apps: []string{"Weather"}}
	a.SetAgentScope("Codex", scope)
	if err := a.Decide(n.ID, gateway.AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Codex's task to finish", func() bool {
		ts := a.Tasks()
		return len(ts) == 1 && ts[0].Agent == "Codex" && ts[0].State == gateway.TaskDone
	})
	agents, _ = a.Agents(ctx)
	if !agents[1].Approved || agents[1].Scope.Mode != gateway.ScopeApps || len(agents[1].Scope.Apps) != 1 {
		t.Errorf("Codex after approval = %+v", agents[1])
	}
}

func TestMediaAndWipe(t *testing.T) {
	a := New(Options{Scenario: OnePhone, Speed: 50, FFmpeg: "no-such-ffmpeg"})
	ctx := context.Background()
	hist, _ := a.History(ctx, gateway.HistoryQuery{})
	var id string
	for _, s := range hist[0].Steps {
		if s.After != "" {
			id = s.After
			break
		}
	}
	if id == "" {
		t.Fatal("no step screenshot in history")
	}
	path, err := a.MediaPath(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if path2, _ := a.MediaPath(ctx, id); path2 != path {
		t.Errorf("second MediaPath %q, want %q", path2, path)
	}
	if _, err := a.MediaPath(ctx, "nope"); err == nil {
		t.Error("MediaPath of an unknown id worked")
	}
	a.SetReadOnly("", true)
	if !a.ReadOnly("") {
		t.Error("read-only not set")
	}
	if err := a.WipeHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if hist, _ := a.History(ctx, gateway.HistoryQuery{}); len(hist) != 0 {
		t.Errorf("%d tasks after WipeHistory", len(hist))
	}
}
