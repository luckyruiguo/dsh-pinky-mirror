package ui

import (
	"slices"
	"sync"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestSettingsQueue applies changes one at a time, in the order they were
// made, and drops a waiting change when its setting changes again.
func TestSettingsQueue(t *testing.T) {
	var q settingsQueue
	var mu sync.Mutex
	var got []string
	add := func(s string) func() {
		return func() {
			mu.Lock()
			got = append(got, s)
			mu.Unlock()
		}
	}
	started, hold := make(chan struct{}), make(chan struct{})
	q.push("a", func() { close(started); <-hold; add("a1")() })
	<-started // a1 is being applied: what follows waits
	q.push("b", add("b1"))
	q.push("a", add("a2"))
	q.push("b", add("b2")) // b1 has not started: dropped
	q.push("c", add("c1"))
	close(hold)
	q.wait()
	if want := []string{"a1", "a2", "b2", "c1"}; !slices.Equal(got, want) {
		t.Errorf("applied %v, want %v", got, want)
	}
	// Idle again, the queue starts over.
	q.push("a", add("a3"))
	q.wait()
	if got[len(got)-1] != "a3" {
		t.Errorf("after idling, applied %v", got)
	}
}

// gatedRecording is a gateway whose first SetRecording waits until the
// test lets it go, as a gateway busy with something else would. It keeps
// what each call set, in the order the calls finished.
type gatedRecording struct {
	*fakeapi.API
	entered chan struct{} // gets a value when the first call starts
	release chan struct{} // closed to let the first call finish

	mu      sync.Mutex
	started int
	set     []bool
}

func (g *gatedRecording) SetRecording(on bool) {
	g.mu.Lock()
	g.started++
	first := g.started == 1
	g.mu.Unlock()
	if first {
		g.entered <- struct{}{}
		<-g.release
	}
	g.API.SetRecording(on)
	g.mu.Lock()
	g.set = append(g.set, on)
	g.mu.Unlock()
}

// TestRecordSwitchKeepsItsOrder turns Record off and quickly on, off and on
// again while the gateway is still busy with the first change. The gateway
// gets the changes in order and ends with the last one; each change used
// to run on a goroutine of its own, so the first could land last.
func TestRecordSwitchKeepsItsOrder(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	g := &gatedRecording{API: ta.api, entered: make(chan struct{}, 1), release: make(chan struct{})}
	ta.app.api = g
	ta.settings.Record = true
	ta.page, ta.set.sec = pageSettings, secPrivacy
	tt := mg.NewTester(ta.view, 1280, 820)
	click := func() {
		t.Helper()
		if err := tt.Click("Record the screen during tasks"); err != nil {
			t.Fatal(err)
		}
	}
	click() // off: the gateway takes it, and is busy with it
	select {
	case <-g.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the change never reached the gateway")
	}
	click() // on
	click() // off
	click() // on
	if !ta.settings.Record {
		t.Fatal("Record is off after four changes")
	}
	close(g.release)
	ta.setq.wait()
	g.mu.Lock()
	set := slices.Clone(g.set)
	g.mu.Unlock()
	// The first change, then the last: the two in between never started.
	if want := []bool{false, true}; !slices.Equal(set, want) {
		t.Errorf("the gateway got %v, want %v", set, want)
	}
	if _, rec := ta.api.Retention(); !rec {
		t.Error("the gateway records nothing after Record was turned back on")
	}
}

// TestReadOnlySwitchKeepsItsOrder: Read-only mode takes the same way.
func TestReadOnlySwitchKeepsItsOrder(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.page, ta.set.sec = pageSettings, secSafety
	tt := mg.NewTester(ta.view, 1280, 820)
	for range 5 {
		if err := tt.Click("Read-only mode"); err != nil {
			t.Fatal(err)
		}
	}
	ta.setq.wait()
	if !ta.settings.ReadOnly || !ta.api.ReadOnly("") {
		t.Errorf("after five changes: the switch is %v, the gateway %v; want both on", ta.settings.ReadOnly, ta.api.ReadOnly(""))
	}
}
