package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/awake"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

func TestAwakeNeeds(t *testing.T) {
	away := gateway.DeviceView{Status: gateway.DeviceIdle, Info: driver.Info{State: driver.StateOnline, Connection: "wifi", Via: driver.ViaTailscale}}
	home := gateway.DeviceView{Status: gateway.DeviceIdle, Info: driver.Info{State: driver.StateOnline, Connection: "wifi"}}
	gone := away
	gone.Status, gone.Info.State = gateway.DeviceOffline, driver.StateOffline
	for _, c := range []struct {
		name        string
		states      []gateway.TaskState
		devs        []gateway.DeviceView
		task, phone bool
	}{
		{"nothing", nil, nil, false, false},
		{"tasks over", []gateway.TaskState{gateway.TaskDone, gateway.TaskStopped, gateway.TaskFailed}, []gateway.DeviceView{home}, false, false},
		{"working", []gateway.TaskState{gateway.TaskDone, gateway.TaskWorking}, nil, true, false},
		{"waiting for the person", []gateway.TaskState{gateway.TaskWaiting}, nil, true, false},
		{"taken over", []gateway.TaskState{gateway.TaskPaused}, nil, true, false},
		{"a phone away", nil, []gateway.DeviceView{home, away}, false, true},
		{"a phone away, offline", nil, []gateway.DeviceView{gone}, false, false},
	} {
		var tasks []gateway.TaskView
		for _, s := range c.states {
			tasks = append(tasks, gateway.TaskView{State: s})
		}
		if task, phone := awakeNeeds(tasks, c.devs); task != c.task || phone != c.phone {
			t.Errorf("%s: task %v, phone %v; want %v, %v", c.name, task, phone, c.task, c.phone)
		}
	}
}

// awakeGateway is a gateway whose tasks and phones the test sets.
type awakeGateway struct {
	mu     sync.Mutex
	tasks  []gateway.TaskView
	devs   []gateway.DeviceView
	events chan gateway.Event
}

func (g *awakeGateway) Subscribe(context.Context) <-chan gateway.Event { return g.events }

func (g *awakeGateway) Tasks() []gateway.TaskView {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.tasks)
}

func (g *awakeGateway) Devices() []gateway.DeviceView {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.devs)
}

func (g *awakeGateway) set(state gateway.TaskState, via string) {
	g.mu.Lock()
	g.tasks, g.devs = nil, nil
	if state != "" {
		g.tasks = []gateway.TaskView{{State: state}}
	}
	if via != "" {
		g.devs = []gateway.DeviceView{{Status: gateway.DeviceIdle, Info: driver.Info{State: driver.StateOnline, Connection: "wifi", Via: via}}}
	}
	g.mu.Unlock()
	g.events <- gateway.Event{Kind: gateway.EventTasks}
}

// awakeRecorder records what is held.
type awakeRecorder struct {
	mu   sync.Mutex
	held map[string]int // holds minus releases, by reason
}

func (r *awakeRecorder) hold(reason string) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.held[reason]++
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.held[reason]--
	}, nil
}

func (r *awakeRecorder) now() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for k, v := range r.held {
		if v != 0 {
			out[k] = v
		}
	}
	return out
}

func (r *awakeRecorder) waitFor(t *testing.T, what string, want map[string]int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := r.now()
		if fmt.Sprint(got) == fmt.Sprint(want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: held %v, want %v", what, got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestKeepDaemonAwake(t *testing.T) {
	for _, away := range []bool{true, false} {
		t.Run(fmt.Sprintf("away %v", away), func(t *testing.T) {
			g := &awakeGateway{events: make(chan gateway.Event, 8)}
			rec := &awakeRecorder{held: map[string]int{}}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				keepDaemonAwake(ctx, g, away, rec.hold, slog.New(slog.DiscardHandler))
			}()
			rec.waitFor(t, "at the start", map[string]int{})
			g.set(gateway.TaskWorking, "")
			rec.waitFor(t, "a task runs", map[string]int{awake.ReasonTask: 1})
			g.set(gateway.TaskWaiting, "")
			rec.waitFor(t, "it waits for the person", map[string]int{awake.ReasonTask: 1})
			g.set(gateway.TaskDone, driver.ViaTailscale)
			phone := map[string]int{}
			if away {
				phone[awake.ReasonAway] = 1
			}
			rec.waitFor(t, "the task is done, a phone is away", phone)
			g.set(gateway.TaskWorking, driver.ViaTailscale)
			both := map[string]int{awake.ReasonTask: 1}
			if away {
				both[awake.ReasonAway] = 1
			}
			rec.waitFor(t, "a task on the phone away", both)
			cancel()
			<-done
			rec.waitFor(t, "the daemon stops", map[string]int{})
		})
	}
}

func TestAwakeHoldsRetry(t *testing.T) {
	var logs bytes.Buffer
	clock := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	tries := 0
	fail := errors.New("Failed to inhibit: Access denied")
	h := &awakeHolds{
		hold: func(string) (func(), error) {
			tries++
			if tries < 3 {
				return nil, fail
			}
			return func() {}, nil
		},
		log:   slog.New(slog.NewTextHandler(&logs, nil)),
		held:  map[string]func(){},
		retry: map[string]time.Time{},
		now:   func() time.Time { return clock },
	}
	h.set(awake.ReasonTask, true)
	h.set(awake.ReasonTask, true) // too soon: not tried
	clock = clock.Add(awakeRetry)
	h.set(awake.ReasonTask, true) // tried again, fails again
	if tries != 2 || len(h.held) != 0 {
		t.Fatalf("after two failures: %d tries, held %v", tries, h.held)
	}
	if n := strings.Count(logs.String(), "cannot keep the computer awake"); n != 1 {
		t.Errorf("logged the failure %d times, want once:\n%s", n, logs.String())
	}
	clock = clock.Add(awakeRetry)
	h.set(awake.ReasonTask, true)
	if tries != 3 || h.held[awake.ReasonTask] == nil || len(h.retry) != 0 {
		t.Fatalf("third try: %d tries, held %v, retry %v", tries, h.held, h.retry)
	}

	// A system with nothing to hold is not tried again.
	tries = 0
	h2 := &awakeHolds{
		hold:  func(string) (func(), error) { tries++; return nil, fmt.Errorf("x: %w", awake.ErrUnsupported) },
		log:   slog.New(slog.DiscardHandler),
		held:  map[string]func(){},
		retry: map[string]time.Time{},
		now:   func() time.Time { return clock },
	}
	h2.set(awake.ReasonAway, true)
	clock = clock.Add(24 * time.Hour)
	h2.set(awake.ReasonAway, true)
	if tries != 1 {
		t.Errorf("unsupported: tried %d times, want once", tries)
	}
}
