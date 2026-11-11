package ui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/pilot"
	"github.com/DIYgod/pinky/internal/secret"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// fakePilot is Ask Pinky's runner without a model: Start records the task
// and leaves it running until Stop.
type fakePilot struct {
	mu      sync.Mutex
	started []fakeStart
	stopped []string
	status  map[string]pilot.Status
}

type fakeStart struct {
	cfg          pilot.Config
	device, task string
}

func newFakePilot() *fakePilot { return &fakePilot{status: map[string]pilot.Status{}} }

func (f *fakePilot) Start(cfg pilot.Config, device, task string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.TrimSpace(task) == "" {
		return pilot.ErrNoTask
	}
	f.started = append(f.started, fakeStart{cfg, device, task})
	f.status[device] = pilot.Status{Device: device, Task: task, Config: cfg, Started: time.Now(), Steps: 3,
		Cost: 0.12, Priced: true}
	return nil
}

func (f *fakePilot) Stop(device string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, device)
	if s, ok := f.status[device]; ok && s.Running() {
		s.Ended, s.End = time.Now(), pilot.Stopped
		f.status[device] = s
	}
}

func (f *fakePilot) StopAll() {
	f.mu.Lock()
	ids := make([]string, 0, len(f.status))
	for id := range f.status {
		ids = append(ids, id)
	}
	f.mu.Unlock()
	for _, id := range ids {
		f.Stop(id)
	}
}

func (f *fakePilot) Status(device string) (pilot.Status, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.status[device]
	return s, ok
}

func (f *fakePilot) Dismiss(device string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.status[device]; ok && !s.Running() {
		delete(f.status, device)
	}
}

func (f *fakePilot) stops() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stopped...)
}

// newAskApp is the demo with one phone and Ask Pinky; idle stops the demo
// agent's task first, so that the phone is free, and keyed saves an API key
// for Anthropic before the first frame asks the keychain.
func newAskApp(t *testing.T, idle, keyed bool) (*testApp, *fakePilot, *mg.Tester) {
	t.Helper()
	ta := newTestApp(t, fakeapi.OnePhone)
	fp := newFakePilot()
	ta.app.opt.Pilot = fp
	ta.waitFor(t, "the phone", func() bool { return len(ta.devices) == 1 })
	if idle {
		id := ta.devices[0].Info.ID
		if err := ta.api.Stop(id); err != nil {
			t.Fatal(err)
		}
		ta.waitFor(t, "the demo task to stop", func() bool {
			tk := ta.taskOf(id)
			return tk == nil || !running(tk.State)
		})
	}
	if keyed {
		saveTestKey(t, ta, pilot.Anthropic)
	}
	return ta, fp, mg.NewTester(ta.view, 1280, 820)
}

// frameUntil draws frames, running what goroutines post, until the view
// shows s.
func frameUntil(t *testing.T, ta *testApp, tt *mg.Tester, s string) {
	t.Helper()
	for i := 0; i < 500; i++ {
		ta.sync()
		tt.Frame()
		if hasText(tt, s) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %q in %q", s, tt.Texts())
}

func saveTestKey(t *testing.T, ta *testApp, provider string) {
	t.Helper()
	if err := ta.secrets().Save(secret.ModelKeyAccount(provider), "sk-test-"+provider); err != nil {
		t.Fatal(err)
	}
}

func TestAskPinkySetupAndKey(t *testing.T) {
	ta, _, tt := newAskApp(t, true, false)
	frameUntil(t, ta, tt, "Add an API key in Settings › Agents")
	if hasText(tt, "Start") {
		t.Errorf("Start shows without a key: %q", tt.Texts())
	}
	if err := tt.Click("Add an API key in Settings › Agents"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ta.page != pageSettings || ta.set.sec != secAgents {
		t.Fatalf("the setup link opened page %v, section %v", ta.page, ta.set.sec)
	}
	ta.waitFor(t, "Add an API key", func() bool {
		tt.Frame()
		_, ok := tt.Access("Add an API key")
		return ok
	})
	if err := tt.Click("Add an API key"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Type("sk-ant-secret")
	tt.Frame()
	if hasText(tt, "sk-ant-secret") {
		t.Errorf("the key shows as typed: %q", tt.Texts())
	}
	tt.Key(0, mg.KeyEnter)
	frameUntil(t, ta, tt, "Saved")
	key, err := secret.Get(context.Background(), ta.secrets(), secret.ModelKeyAccount(pilot.Anthropic))
	if err != nil || key != "sk-ant-secret" {
		t.Fatalf("keychain has %q, %v", key, err)
	}
	if ta.ask.value != "" {
		t.Errorf("the key stayed in the client's state")
	}

	// Back on the phone, the box offers to start.
	ta.page = pagePhones
	frameUntil(t, ta, tt, "Start")
	for _, want := range []string{"Claude Opus 5.5", "Up to US$2.00 a task"} {
		if !hasText(tt, want) {
			t.Errorf("no %q: %q", want, tt.Texts())
		}
	}

	// An OpenAI-compatible service needs a model too.
	saveTestKey(t, ta, pilot.OpenAI)
	ta.settings.PilotProvider = pilot.OpenAI
	frameUntil(t, ta, tt, "Choose a model in Settings › Agents")
}

func TestAskPinkyStartAndStop(t *testing.T) {
	ta, fp, tt := newAskApp(t, true, true)
	ta.settings.PilotMaxSteps = 50
	id := ta.devices[0].Info.ID
	frameUntil(t, ta, tt, "Start")
	if err := tt.Click("Task for Pinky"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Check the battery level")
	tt.Frame()
	if err := tt.Click("Start"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(fp.started) != 1 {
		t.Fatalf("started %v", fp.started)
	}
	st := fp.started[0]
	if st.device != id || st.task != "Check the battery level" || st.cfg.Provider != pilot.Anthropic ||
		st.cfg.Model != pilot.DefaultModel || st.cfg.MaxSteps != 50 || st.cfg.MaxTime != pilot.DefaultMaxTime {
		t.Errorf("started %+v", st)
	}
	if ta.ask.draft != "" {
		t.Errorf("the field kept %q", ta.ask.draft)
	}
	for _, want := range []string{"Pinky is working on it", "3 steps · US$0.12"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Stop Pinky's task"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the pilot to stop", func() bool { return len(fp.stops()) == 1 })
	frameUntil(t, ta, tt, "Stopped")
	if err := tt.Click("Dismiss"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := fp.Status(id); ok {
		t.Errorf("Dismiss left the status")
	}
}

func TestAskPinkyStopAllStopsIt(t *testing.T) {
	ta, fp, tt := newAskApp(t, true, true)
	id := ta.devices[0].Info.ID
	if err := fp.Start(ta.settings.PilotConfig(), id, "Open Clock"); err != nil {
		t.Fatal(err)
	}
	frameUntil(t, ta, tt, "Pinky is working on it")
	ta.stopAll()
	ta.waitFor(t, "Stop all to reach the pilot", func() bool { return len(fp.stops()) == 1 })
}

func TestAskPinkyWaitsForAnotherAgent(t *testing.T) {
	ta, _, tt := newAskApp(t, false, true)
	ta.ask.draft = "Open Clock"
	frameUntil(t, ta, tt, "is using Pixel 9 now")
	b, ok := tt.Access("Start")
	if !ok || !b.Disabled {
		t.Errorf("Start while another agent works: %+v, %v", b, ok)
	}
}

func TestAskPinkyOff(t *testing.T) {
	ta, _, tt := newAskApp(t, true, true)
	frameUntil(t, ta, tt, "Start")
	ta.settings.AskPinky = false
	tt.Frame()
	if hasText(tt, "Task for Pinky") || hasText(tt, "Ask Pinky") {
		t.Errorf("the box shows while off: %q", tt.Texts())
	}
}

func TestDescribePilot(t *testing.T) {
	cfg := pilot.Config{MaxSteps: 100, MaxTime: 20 * time.Minute, MaxCost: 2}
	for _, c := range []struct {
		s    pilot.Status
		want string
	}{
		{pilot.Status{End: pilot.StepLimit, Config: cfg}, "Stopped. Reached the limit of 100 steps."},
		{pilot.Status{End: pilot.TimeLimit, Config: cfg}, "Stopped. Reached the limit of 20 minutes."},
		{pilot.Status{End: pilot.CostLimit, Config: cfg, Priced: true}, "Stopped. Reached the limit of US$2.00."},
		{pilot.Status{End: pilot.CostLimit, Config: cfg}, "Stopped. Reached the limit of 2,000,000 tokens."},
		{pilot.Status{End: pilot.Refused, Config: cfg}, "Couldn't finish. The model declined this task."},
		{pilot.Status{End: pilot.BadKey, Config: cfg, Summary: "invalid x-api-key"}, "Couldn't start. The service didn't accept the API key. Check it in Settings › Agents."},
		{pilot.Status{End: pilot.Error, Config: cfg, Summary: "overloaded"}, "Couldn't finish. overloaded"},
		{pilot.Status{End: pilot.Stopped, Config: cfg}, "Stopped"},
	} {
		if got := DescribePilot(c.s); got != c.want {
			t.Errorf("%s: %q, want %q", c.s.End, got, c.want)
		}
	}
}

// The box's phone on All phones is the one chosen in it.
func TestAskPinkyAllPhones(t *testing.T) {
	ta := newTestApp(t, fakeapi.ThreePhones)
	fp := newFakePilot()
	ta.app.opt.Pilot = fp
	saveTestKey(t, ta, pilot.Anthropic)
	ta.waitFor(t, "the phones", func() bool { return len(ta.devices) == 3 })
	if err := ta.api.StopAll(); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the demo tasks to stop", func() bool {
		for _, tk := range ta.tasks {
			if running(tk.State) {
				return false
			}
		}
		return true
	})
	ta.selectPhone(allPhones)
	tt := mg.NewTester(ta.view, 1280, 820)
	frameUntil(t, ta, tt, "Start")
	if _, ok := tt.Access("Phone"); !ok {
		t.Fatalf("no phone menu on All phones: %q", tt.Texts())
	}
	var second string
	for _, d := range ta.devices[1:] {
		if d.Status != gateway.DeviceOffline {
			second = d.Info.ID
		}
	}
	ta.ask.device = second
	ta.ask.draft = "Open Clock"
	tt.Frame()
	if err := tt.Click("Start"); err != nil {
		t.Fatal(err)
	}
	if len(fp.started) != 1 || fp.started[0].device != second {
		t.Errorf("started %+v, want on %s", fp.started, second)
	}
}
