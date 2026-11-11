package gateway

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
)

// stepProvider lists one phone as the test sets it: waiting for a step,
// offline, online (opened as a demo phone), or not at all (unplugged).
type stepProvider struct {
	dev  *demo.Device
	open driver.Device // opened in place of the demo phone, when set
	mu   sync.Mutex
	info *driver.Info // nil: not listed
}

func (p *stepProvider) Platform() driver.Platform { return driver.Android }

func (p *stepProvider) List(context.Context) ([]driver.Info, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.info == nil {
		return nil, nil
	}
	return []driver.Info{*p.info}, nil
}

func (p *stepProvider) Open(context.Context, string) (driver.Device, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.info == nil || p.info.State != driver.StateOnline {
		return nil, driver.ErrOffline
	}
	if p.open != nil {
		return p.open, nil
	}
	return demo.Android{Device: p.dev}, nil
}

// list lists the phone as info from the next discovery on; nil unplugs it.
func (p *stepProvider) list(info *driver.Info) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if info == nil {
		p.info = nil
		return
	}
	cp := *info
	p.info = &cp
}

// viewUntil waits until the gateway's view of the phone passes ok.
func viewUntil(t *testing.T, g *Gateway, id, what string, ok func(DeviceView) bool) DeviceView {
	t.Helper()
	var last DeviceView
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, v := range g.Devices() {
			if v.Info.ID == id {
				if last = v; ok(v) {
					return v
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s: the phone is %q %q, %+v", what, last.Status, last.StatusLine, last.Info)
	return last
}

// TestListedPhoneNeedsSetup: a phone its driver lists as waiting for a
// step (allow USB debugging, the computer's USB permission) is connected,
// not offline: it needs setup, and keeps the step and the driver's words
// for the client. Unplugged, it is offline and no longer claims the step,
// and the client hears of it; plugged in again it needs setup again. Once
// allowed the gateway opens it, and a phone that needs a step after it
// was open needs setup too.
func TestListedPhoneNeedsSetup(t *testing.T) {
	p := &stepProvider{dev: demo.NewDevice("px", "Pixel 8a", driver.Android)}
	allow := driver.Info{ID: "px", Platform: driver.Android, Name: "Pixel 8a", Model: "Pixel 8a", Connection: "usb", Backend: "scrcpy",
		State: driver.StateUnauthorized, Detail: "Unlock the phone and allow USB debugging for this computer."}
	p.list(&allow)
	g := checkupGateway(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := g.Subscribe(ctx)
	needsSetup := func(v DeviceView) bool { return v.Status == DeviceNeedsSetup }

	v := viewUntil(t, g, "px", "waiting to be allowed", needsSetup)
	if v.StatusLine != "Needs setup" || v.Info.State != driver.StateUnauthorized || v.Info.Detail != allow.Detail || v.Features != (Features{}) {
		t.Errorf("waiting to be allowed: %+v", v)
	}

	usb := allow
	usb.State, usb.Setup, usb.Detail = driver.StateNeedsSetup, driver.SetupUSBPermission, "This computer has no permission to use the phone over USB."
	p.list(&usb)
	v = viewUntil(t, g, "px", "another step", func(v DeviceView) bool { return v.Info.Setup == driver.SetupUSBPermission })
	if v.Status != DeviceNeedsSetup || v.StatusLine != "Needs setup" || v.Info.State != driver.StateNeedsSetup || v.Info.Detail != usb.Detail {
		t.Errorf("another step: %+v", v)
	}
	cu, err := g.Checkup(ctx, "px")
	if err != nil {
		t.Fatal(err)
	}
	if conn, _ := checkByID(cu, driver.CheckConnection); conn.Status != driver.CheckPass {
		t.Errorf("the connection of a phone that needs setup: %+v", conn)
	}
	if auth, _ := checkByID(cu, driver.CheckAuthorized); auth.Status != driver.CheckFail || auth.Fix != driver.SetupUSBPermission {
		t.Errorf("the step of a phone that needs setup: %+v", auth)
	}

	// Unplugged while it waits: offline, without the step or the words
	// that went with it, and an event says so.
	for len(events) > 0 {
		<-events
	}
	p.list(nil)
	v = viewUntil(t, g, "px", "unplugged", func(v DeviceView) bool { return v.Status == DeviceOffline })
	if v.StatusLine != "Offline" || v.Info.State != driver.StateOffline || v.Info.Setup != "" || v.Info.Detail != "" {
		t.Errorf("unplugged: %+v", v)
	}
	heard := false
	for deadline := time.After(2 * time.Second); !heard; {
		select {
		case e := <-events:
			heard = e.Kind == EventDevices && e.DeviceID == "px"
		case <-deadline:
			t.Fatal("no event for the phone that went away")
		}
	}
	cu, err = g.Checkup(ctx, "px")
	if err != nil {
		t.Fatal(err)
	}
	if conn, _ := checkByID(cu, driver.CheckConnection); conn.Status != driver.CheckFail || conn.Fix != driver.FixReconnect {
		t.Errorf("the connection of an unplugged phone: %+v", conn)
	}
	if auth, _ := checkByID(cu, driver.CheckAuthorized); auth.Status != driver.CheckSkip {
		t.Errorf("an unplugged phone still waits for a step: %+v", auth)
	}

	// Plugged in again, and listed offline with a reason before it went
	// away again: the reason goes with it.
	p.list(&allow)
	viewUntil(t, g, "px", "plugged in again", needsSetup)
	off := allow
	off.State, off.Detail = driver.StateOffline, "adb reports the phone as offline."
	p.list(&off)
	v = viewUntil(t, g, "px", "listed offline", func(v DeviceView) bool { return v.Status == DeviceOffline })
	if v.StatusLine != "Offline · "+off.Detail || v.Info.Detail != off.Detail {
		t.Errorf("listed offline: %+v", v)
	}
	p.list(nil)
	viewUntil(t, g, "px", "unplugged when offline", func(v DeviceView) bool { return v.StatusLine == "Offline" && v.Info.Detail == "" })

	// Allowed: online, and opened.
	online := allow
	online.State, online.Detail = driver.StateOnline, ""
	p.list(&online)
	waitOnline(t, g, "px")
	if v = viewUntil(t, g, "px", "online", func(v DeviceView) bool { return v.Status == DeviceIdle }); v.StatusLine != "Idle" {
		t.Errorf("online: %+v", v)
	}

	// It needs a step again: closed, and needs setup.
	again := allow
	again.State, again.Detail = driver.StateNeedsSetup, "Turn USB debugging on again."
	p.list(&again)
	v = viewUntil(t, g, "px", "a step after it was open", needsSetup)
	if v.StatusLine != "Needs setup" || v.Info.Detail != again.Detail || v.Features != (Features{}) {
		t.Errorf("a step after it was open: %+v", v)
	}
}

// needsUntil waits until the gateway has a need that passes ok.
func needsUntil(t *testing.T, g *Gateway, what string, ok func(NeedView) bool) NeedView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range g.Needs() {
			if ok(n) {
				return n
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no need for %s: %+v", what, g.Needs())
	return NeedView{}
}

// stepGateway is a gateway on p with short waits, where a lease survives
// a dropped phone for 50 ms.
func stepGateway(t *testing.T, p driver.Provider) *Gateway {
	t.Helper()
	g, err := New(Options{Providers: []driver.Provider{p}, Policy: testPolicy(t), TrustNewAgents: true,
		MaxBlock: 5 * time.Second, ReconnectGrace: 50 * time.Millisecond, SettleMax: 200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond, NoScreenshots: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

// TestAgentPhoneNeedsSetup: an agent that connects to a phone waiting for
// a step is told so, with the step, not that the phone is offline. Its
// lease waits with the phone, past the reconnect grace; other calls say
// the same and do nothing; a handoff asks the person and ends by itself
// once the phone is ready, with its screen.
func TestAgentPhoneNeedsSetup(t *testing.T) {
	p := &stepProvider{dev: demo.NewDevice("px", "Pixel 8a", driver.Android)}
	allow := driver.Info{ID: "px", Platform: driver.Android, Name: "Pixel 8a", Model: "Pixel 8a", Connection: "usb", Backend: "scrcpy",
		State: driver.StateUnauthorized, Detail: "Unlock the phone and allow USB debugging for this computer."}
	p.list(&allow)
	g := stepGateway(t, p)
	viewUntil(t, g, "px", "listed", func(v DeviceView) bool { return v.Status == DeviceNeedsSetup })
	ctx := context.Background()
	s := g.OpenSession(AgentInfo{Name: "Claude Code"})
	defer s.Close()

	res := s.Connect(ctx, ConnectRequest{Call: Call{Device: "nope"}, Task: "Lunch"})
	if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, `Pixel 8a (device "px", connected, needs setup)`) {
		t.Errorf("the phones an agent is offered: %+v", res)
	}
	// With none online, the one that waits is the phone.
	res = s.Connect(ctx, ConnectRequest{Task: "Lunch"})
	if res.Status != StatusNeedsSetup || res.Setup != driver.FixAuthorize || res.Device != "px" || res.Task == "" || res.Screen != nil ||
		!strings.Contains(res.Message, allow.Detail) || !strings.Contains(res.Message, "phone_handoff") {
		t.Fatalf("connecting: %+v", res)
	}

	// The task waits with the phone, past the 50 ms grace of a dropped one.
	time.Sleep(200 * time.Millisecond)
	if v := viewUntil(t, g, "px", "the task", func(v DeviceView) bool { return v.Status == DeviceNeedsSetup }); v.TaskID != res.Task {
		t.Fatalf("the task did not wait: %+v", v)
	}
	if r := s.Observe(ctx, ObserveRequest{}); r.Status != StatusNeedsSetup || r.Screen != nil {
		t.Errorf("phone_observe: %+v", r)
	}
	if r := s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}); r.Status != StatusNeedsSetup || !strings.Contains(r.Message, "Nothing was done") {
		t.Errorf("phone_key: %+v", r)
	}
	if ins := p.dev.Inputs(); len(ins) != 0 {
		t.Errorf("input reached the phone: %+v", ins)
	}

	done := make(chan *Result, 1)
	go func() { done <- s.Handoff(ctx, HandoffRequest{Reason: "Allow USB debugging on the phone"}) }()
	needsUntil(t, g, "the handoff", func(n NeedView) bool { return n.Kind == NeedHandoff && n.DeviceID == "px" })
	online := allow
	online.State, online.Detail = driver.StateOnline, ""
	p.list(&online)
	select {
	case r := <-done:
		if r.Status != StatusSettled || r.Note != "The phone is ready." || r.Screen == nil || !r.Full {
			t.Fatalf("the handoff once the phone was ready: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handoff did not end once the phone was ready")
	}
	if r := s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}); r.IsError() || r.Status != StatusSettled {
		t.Errorf("an action once the phone is ready: %+v", r)
	}
}

// TestAgentHandBackBeforeTheStep: the person hands back while the phone
// still waits for its step; the agent hears that it still waits, with the
// person's note. Unplugged then, the lease has the grace of a dropped
// phone.
func TestAgentHandBackBeforeTheStep(t *testing.T) {
	p := &stepProvider{dev: demo.NewDevice("ip", "iPhone 16", driver.IOS)}
	trust := driver.Info{ID: "ip", Platform: driver.IOS, Name: "iPhone 16", Connection: "usb", Backend: "coredevice",
		State: driver.StateUnauthorized, Setup: driver.SetupTrust,
		Detail: "Unlock the iPhone and tap Trust on the \"Trust This Computer?\" prompt, then enter its passcode."}
	p.list(&trust)
	g := stepGateway(t, p)
	viewUntil(t, g, "ip", "listed", func(v DeviceView) bool { return v.Status == DeviceNeedsSetup })
	ctx := context.Background()
	s := g.OpenSession(AgentInfo{Name: "Codex"})
	defer s.Close()
	if res := s.Connect(ctx, ConnectRequest{Call: Call{Device: "iPhone 16"}, Task: "Photos"}); res.Status != StatusNeedsSetup || res.Setup != driver.SetupTrust {
		t.Fatalf("connecting: %+v", res)
	}
	done := make(chan *Result, 1)
	go func() { done <- s.Handoff(ctx, HandoffRequest{Reason: "Trust this computer on the iPhone"}) }()
	needsUntil(t, g, "the handoff", func(n NeedView) bool { return n.Kind == NeedHandoff && n.DeviceID == "ip" })
	if err := g.HandBack("ip", "I tapped Trust"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.Status != StatusNeedsSetup || r.Note != "I tapped Trust" || !strings.Contains(r.Message, "still waits") || r.Setup != driver.SetupTrust {
			t.Fatalf("handed back while it waits: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handoff did not end on Hand back")
	}

	// Unplugged: offline, and the lease ends after the grace.
	p.list(nil)
	viewUntil(t, g, "ip", "the task ends", func(v DeviceView) bool { return v.Status == DeviceOffline && v.TaskID == "" })
	if r := s.Observe(ctx, ObserveRequest{}); r.Status != StatusStopped && r.Error != ErrInvalidArgument {
		t.Errorf("a call after the lease ended: %+v", r)
	}
}

// TestUnopenedPhoneForgotten: a phone the gateway never opened leaves the
// list a while after it is no longer listed (unopenedGrace), and the client
// hears of it; one with a task waiting on it stays until the task ends.
// A phone that was open stays, offline.
func TestUnopenedPhoneForgotten(t *testing.T) {
	saved := unopenedGrace
	unopenedGrace = 100 * time.Millisecond
	t.Cleanup(func() { unopenedGrace = saved })
	p := &stepProvider{dev: demo.NewDevice("px", "Pixel 8a", driver.Android)}
	allow := driver.Info{ID: "px", Platform: driver.Android, Name: "Pixel 8a", Connection: "usb", Backend: "scrcpy",
		State: driver.StateUnauthorized, Detail: "Unlock the phone and allow USB debugging for this computer."}
	p.list(&allow)
	g := stepGateway(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := g.Subscribe(ctx)
	viewUntil(t, g, "px", "listed", func(v DeviceView) bool { return v.Status == DeviceNeedsSetup })

	gone := func(what string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if len(g.Devices()) == 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("%s: still listed: %+v", what, g.Devices())
	}
	for len(events) > 0 {
		<-events
	}
	p.list(nil)
	viewUntil(t, g, "px", "offline at first", func(v DeviceView) bool { return v.Status == DeviceOffline })
	gone("never opened, unplugged")
	heard := false
	for deadline := time.After(time.Second); !heard; {
		select {
		case e := <-events:
			heard = e.Kind == EventDevices && e.DeviceID == "px" && len(g.Devices()) == 0
		case <-deadline:
			t.Fatal("no event for the phone that was forgotten")
		}
	}

	// Back, with an agent's task waiting on it: it stays while the task
	// does (50 ms of grace once unplugged), then goes.
	p.list(&allow)
	viewUntil(t, g, "px", "back", func(v DeviceView) bool { return v.Status == DeviceNeedsSetup })
	s := g.OpenSession(AgentInfo{Name: "Claude Code"})
	defer s.Close()
	if res := s.Connect(ctx, ConnectRequest{Call: Call{Device: "px"}, Task: "Lunch"}); res.Status != StatusNeedsSetup {
		t.Fatalf("connecting: %+v", res)
	}
	p.list(nil)
	viewUntil(t, g, "px", "kept for its task", func(v DeviceView) bool { return v.Status == DeviceOffline && v.TaskID != "" })
	gone("once its task ended")

	// Opened once: it stays, offline.
	online := allow
	online.State, online.Detail = driver.StateOnline, ""
	p.list(&online)
	waitOnline(t, g, "px")
	p.list(nil)
	viewUntil(t, g, "px", "unplugged after it was open", func(v DeviceView) bool { return v.Status == DeviceOffline })
	time.Sleep(300 * time.Millisecond)
	if len(g.Devices()) != 1 {
		t.Errorf("a phone that was open left the list: %+v", g.Devices())
	}
}

// TestAgentWorkingAfterSetup: an agent connected while the phone waited for
// a step. Once the gateway opens the phone, its driver hears that an agent
// works on it (driver.WorkWatcher), so Android's Pinky Companion comes on
// for the task.
func TestAgentWorkingAfterSetup(t *testing.T) {
	w := &workDevice{Device: demo.NewDevice("px", "Pixel 8a", driver.Android)}
	p := &stepProvider{dev: w.Device, open: w}
	allow := driver.Info{ID: "px", Platform: driver.Android, Name: "Pixel 8a", Connection: "usb", Backend: "scrcpy",
		State: driver.StateUnauthorized, Detail: "Unlock the phone and allow USB debugging for this computer."}
	p.list(&allow)
	g := stepGateway(t, p)
	viewUntil(t, g, "px", "listed", func(v DeviceView) bool { return v.Status == DeviceNeedsSetup })
	s := g.OpenSession(AgentInfo{Name: "Claude Code"})
	defer s.Close()
	if res := s.Connect(context.Background(), ConnectRequest{Task: "Lunch"}); res.Status != StatusNeedsSetup {
		t.Fatalf("connecting: %+v", res)
	}
	online := allow
	online.State, online.Detail = driver.StateOnline, ""
	p.list(&online)
	waitOnline(t, g, "px")
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Equal(w.heard(), []bool{true}) {
		if time.Now().After(deadline) {
			t.Fatalf("the driver heard %v once the phone opened with a task waiting on it", w.heard())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
