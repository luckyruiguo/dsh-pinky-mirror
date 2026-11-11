package gateway

import (
	"context"
	"slices"
	"testing"
	"time"
)

func (p *netProvider) SetPaused(id string, paused bool) {
	p.mu.Lock()
	if paused {
		p.calls = append(p.calls, "pause "+id)
	} else {
		p.calls = append(p.calls, "resume "+id)
	}
	p.mu.Unlock()
}

// stayClosed checks for a while that the gateway does not open the phone.
func stayClosed(t *testing.T, g *Gateway, id string) {
	t.Helper()
	for range 10 {
		time.Sleep(10 * time.Millisecond)
		g.mu.Lock()
		d := g.devices[id]
		open := d != nil && (d.online || d.dev != nil)
		g.mu.Unlock()
		if open {
			t.Fatalf("%s was opened while paused", id)
		}
	}
}

func mustPaused(t *testing.T, g *Gateway, id string) {
	t.Helper()
	v, ok := viewOf(g, id)
	if !ok || !v.Paused || v.Status != DeviceOffline || v.StatusLine != "Paused" || v.Streaming {
		t.Fatalf("%s: %+v, %v; want it listed, paused", id, v, ok)
	}
}

// TestPauseDevice: pausing the Mi, on Wi-Fi, ends its task, closes it and
// keeps it closed, across restarts, while it stays on the list; agents
// hear device_paused. Resuming opens it again.
func TestPauseDevice(t *testing.T) {
	dir := t.TempDir()
	p, _, _ := newTwoPhones()
	g := twoPhones(t, dir, p)
	ctx := context.Background()
	waitOpen(t, g, "mi")
	s := g.OpenSession(agent)
	defer s.Close()
	mustStatus(t, s.Connect(ctx, ConnectRequest{Call: Call{Device: "mi"}, Task: "Look at the menu"}), StatusSettled)

	if err := g.PauseDevice("mi"); err != nil {
		t.Fatal(err)
	}
	mustPaused(t, g, "mi")
	stayClosed(t, g, "mi")
	if got := p.callLog(); !slices.Equal(got, []string{"pause mi"}) {
		t.Errorf("the provider heard %q", got)
	}
	if res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}); res.Error != ErrDevicePaused {
		t.Errorf("the agent's next call: %+v", res)
	}
	if res := s.Connect(ctx, ConnectRequest{Call: Call{Device: "mi"}, Task: "Again"}); res.Error != ErrDevicePaused {
		t.Errorf("connecting to it again: %+v", res)
	}
	var task *TaskView
	for _, tv := range g.Tasks() {
		if tv.DeviceID == "mi" {
			task = &tv
		}
	}
	if task == nil || task.State != TaskStopped {
		t.Fatalf("the Mi's task: %+v", task)
	}
	// Not barred: the session may use the Pixel, and the Mi once resumed.
	mustStatus(t, s.Connect(ctx, ConnectRequest{Call: Call{Device: "pixel"}, Task: "Other phone"}), StatusSettled)
	mustStatus(t, s.Release(ctx, ReleaseRequest{Call: Call{Device: "pixel"}, Outcome: "done"}), StatusSettled)
	g.Flush()

	// After a restart it is still paused, and the provider hears so first.
	g.Close()
	p2, _, _ := newTwoPhones()
	g = twoPhones(t, dir, p2)
	waitIDs(t, g, "mi", "pixel")
	waitOpen(t, g, "pixel")
	mustPaused(t, g, "mi")
	stayClosed(t, g, "mi")
	if got := p2.callLog(); !slices.Equal(got, []string{"pause mi"}) {
		t.Errorf("at start the provider heard %q", got)
	}

	if err := g.ResumeDevice("mi"); err != nil {
		t.Fatal(err)
	}
	waitOpen(t, g, "mi")
	if v, _ := viewOf(g, "mi"); v.Paused {
		t.Errorf("resumed, the view says paused: %+v", v)
	}
	if got := p2.callLog(); !slices.Equal(got, []string{"pause mi", "resume mi"}) {
		t.Errorf("the provider heard %q", got)
	}
	s2 := g.OpenSession(agent)
	defer s2.Close()
	mustStatus(t, s2.Connect(ctx, ConnectRequest{Call: Call{Device: "mi"}, Task: "Back"}), StatusSettled)
	g.Flush()

	g.Close()
	p3, _, _ := newTwoPhones()
	g = twoPhones(t, dir, p3)
	waitOpen(t, g, "mi")
	if got := p3.callLog(); len(got) != 0 {
		t.Errorf("after resuming, a restart told the provider %q", got)
	}
}

// TestPauseOnCable: a phone paused on its cable stays paused while it
// stays plugged in, and resumes when it is plugged in again.
func TestPauseOnCable(t *testing.T) {
	p, _, _ := newTwoPhones()
	g := twoPhones(t, t.TempDir(), p)
	waitOpen(t, g, "pixel")
	if err := g.PauseDevice("pixel"); err != nil {
		t.Fatal(err)
	}
	stayClosed(t, g, "pixel")
	mustPaused(t, g, "pixel")

	p.set(func(p *netProvider) { p.unplugged["pixel"] = true })
	time.Sleep(60 * time.Millisecond)
	mustPaused(t, g, "pixel") // unplugged, it stays listed
	p.set(func(p *netProvider) { delete(p.unplugged, "pixel") })
	waitOpen(t, g, "pixel")
	if v, _ := viewOf(g, "pixel"); v.Paused {
		t.Errorf("plugged in again, still paused: %+v", v)
	}
}

// TestPauseOnWiFiThenCable: a phone paused on Wi-Fi resumes once plugged
// in; one only listed again on Wi-Fi does not.
func TestPauseOnWiFiThenCable(t *testing.T) {
	p, _, _ := newTwoPhones()
	g := twoPhones(t, t.TempDir(), p)
	waitOpen(t, g, "mi")
	if err := g.PauseDevice("mi"); err != nil {
		t.Fatal(err)
	}
	p.set(func(p *netProvider) { p.unplugged["mi"] = true })
	time.Sleep(60 * time.Millisecond)
	p.set(func(p *netProvider) { delete(p.unplugged, "mi") })
	stayClosed(t, g, "mi")
	mustPaused(t, g, "mi")

	p.set(func(p *netProvider) { p.conn["mi"] = "usb" })
	waitOpen(t, g, "mi")
	if g.DevicePaused("mi") {
		t.Error("plugged in, still paused")
	}
}

// TestPauseOnlyPhone: pausing a phone the gateway does not list fails,
// and an agent that names no phone hears that the only phone is paused.
func TestPauseOnlyPhone(t *testing.T) {
	p, _, _ := newTwoPhones()
	p.set(func(p *netProvider) { p.unplugged["mi"] = true })
	g := twoPhones(t, t.TempDir(), p)
	if err := g.PauseDevice("nope"); err != ErrUnknownDevice {
		t.Errorf("pausing an unknown phone: %v", err)
	}
	waitOpen(t, g, "pixel")
	if err := g.PauseDevice("pixel"); err != nil {
		t.Fatal(err)
	}
	s := g.OpenSession(agent)
	defer s.Close()
	if res := s.Connect(context.Background(), ConnectRequest{Task: "Any phone"}); res.Error != ErrDevicePaused {
		t.Errorf("connecting with the only phone paused: %+v", res)
	}
}
