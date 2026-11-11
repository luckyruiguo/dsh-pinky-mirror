package ui

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/secret"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// testApp is the client without windows: the views run in a ui.Tester,
// and what goroutines post runs when the test drains it.
type testApp struct {
	*app
	api *fakeapi.API
	mu  sync.Mutex
	q   []func()
}

func newTestApp(t *testing.T, s fakeapi.Scenario) *testApp {
	return newTestAppTimeout(t, s, time.Minute)
}

func newTestAppTimeout(t *testing.T, s fakeapi.Scenario, timeout time.Duration) *testApp {
	t.Helper()
	return newTestAppWith(t, fakeapi.Options{Scenario: s, Speed: 50, ApprovalTimeout: timeout})
}

func newTestAppWith(t *testing.T, opt fakeapi.Options) *testApp {
	t.Helper()
	api := fakeapi.New(opt)
	ta := &testApp{api: api}
	ta.app = newApp(api, Options{NoTray: true, Shortcut: "-"})
	testHooks(t, ta.app)
	ta.app.videoErr = errors.New("no video in tests") // mirrors show their error instead of streaming
	ta.app.postHook = func(fn func()) {
		ta.mu.Lock()
		ta.q = append(ta.q, fn)
		ta.mu.Unlock()
	}
	t.Cleanup(ta.app.end)
	return ta
}

// testHooks keeps a test's client off the computer: no keychain (PINs go
// to a Memory store), no power assertions, and exports in a temporary
// folder.
func testHooks(t *testing.T, a *app) {
	if a.opt.Secrets == nil {
		a.opt.Secrets = &secret.Memory{}
	}
	a.awake = func(bool) {}
	a.awayAwake = func(bool) {}
	dir := t.TempDir()
	a.exportTo = func() (string, error) { return dir, nil }
	a.reveal = func(string) {}
}

// sync reads the fake's views, as the event loop would, and runs what was
// posted.
func (ta *testApp) sync() {
	ta.mu.Lock()
	q := ta.q
	ta.q = nil
	ta.mu.Unlock()
	for _, fn := range q {
		fn()
	}
	ta.devices, ta.tasks = ta.api.Devices(), ta.api.Tasks()
	ta.setNeeds(ta.api.Needs())
	ta.setBackgrounds(ta.readBackground(ta.devices))
	first := !ta.loaded
	ta.loaded = true
	ta.afterRefresh(first)
}

// waitFor syncs until cond holds.
func (ta *testApp) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 1500; i++ { // long enough for go test -race
		ta.sync()
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (ta *testApp) approval() (gateway.NeedView, bool) {
	for _, n := range ta.needs {
		if n.Kind == gateway.NeedApproval {
			return n, true
		}
	}
	return gateway.NeedView{}, false
}

func hasText(tt *mg.Tester, s string) bool {
	for _, t := range tt.Texts() {
		if strings.Contains(t, s) {
			return true
		}
	}
	return false
}

func TestApproveInRightPanel(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.waitFor(t, "the payment approval", func() bool { _, ok := ta.approval(); return ok })
	tt := mg.NewTester(ta.view, 1280, 820)
	for _, want := range []string{"Pixel 9", "Needs you", "Pay $12.80?", "Reorder last week's lunch", "Waiting for you: pay $12.80", "Stop all"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Allow"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the task to finish", func() bool {
		ts := ta.api.Tasks()
		return len(ts) == 1 && ts[0].State == gateway.TaskDone
	})
	tt.Frame()
	if !hasText(tt, "Done") || !hasText(tt, "You allowed: pay $12.80") {
		t.Errorf("after Allow: %q", tt.Texts())
	}
}

func TestDenyWithNote(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.waitFor(t, "the payment approval", func() bool { _, ok := ta.approval(); return ok })
	n, _ := ta.approval()
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.noting[n.ID] = true // as More options › Deny with a note does
	tt.Frame()
	if !hasText(tt, "Note for Claude Code") {
		t.Fatalf("no note editor: %q", tt.Texts())
	}
	tt.Type("I'll order myself later.")
	if err := tt.Click("Deny"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the task to end", func() bool {
		ts := ta.api.Tasks()
		return len(ts) == 1 && ts[0].State == gateway.TaskDone
	})
	var denied string
	for _, s := range ta.api.Tasks()[0].Steps {
		if strings.HasPrefix(s.Text, "You denied") {
			denied = s.Text
		}
	}
	if !strings.Contains(denied, "I'll order myself later.") {
		t.Errorf("the denial step %q lacks the note", denied)
	}
}

func TestTakeOverAndHandBack(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.waitFor(t, "the payment approval", func() bool { _, ok := ta.approval(); return ok })
	tt := mg.NewTester(ta.view, 1280, 820)
	if err := tt.Click("Take over"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "control", func() bool { return ta.devices[0].Status == gateway.DeviceInControl })
	tt.Frame()
	for _, want := range []string{"You're in control.", "Hand back", "Paused · You're in control"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Hand back"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the agent to ask again", func() bool { _, ok := ta.approval(); return ok })
}

func TestLockedCard(t *testing.T) {
	ta := newTestApp(t, fakeapi.Locked)
	if err := ta.opt.Secrets.Save(secret.PINAccount("pixel-9"), "2468"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the unlock card", func() bool {
		return len(ta.needs) == 1 && ta.needs[0].Kind == gateway.NeedUnlock && ta.pin.ready
	})
	tt := mg.NewTester(ta.view, 1280, 820)
	label, keep := unlockWords()
	for _, want := range []string{"Pixel 9 is locked", "Claude Code is waiting to reply to Sam.", label, "or unlock it on the phone", keep, "Locked"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the phone to unlock", func() bool { return ta.devices[0].Status != gateway.DeviceLocked })
}

func TestThreePhonesAndAllPhones(t *testing.T) {
	ta := newTestApp(t, fakeapi.ThreePhones)
	ta.sync()
	tt := mg.NewTester(ta.view, 1280, 820)
	for _, want := range []string{"All phones", "Pixel 9", "iPhone 17", "Galaxy S25", "Offline"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in the sidebar: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("All phones"); err != nil {
		t.Fatal(err)
	}
	if !hasText(tt, "2 connected · 1 offline") {
		t.Errorf("All phones header: %q", tt.Texts())
	}
	if err := tt.Click("Show Galaxy S25"); err != nil {
		t.Fatal(err)
	}
	if !hasText(tt, "Galaxy S25 is offline") || !hasText(tt, "Last seen 2 days ago.") {
		t.Errorf("offline phone: %q", tt.Texts())
	}
	// Back to one phone: All phones goes away.
	ta.api.Play(fakeapi.OnePhone)
	ta.sync()
	tt.Frame()
	if hasText(tt, "All phones") || ta.sel != "pixel-9" {
		t.Errorf("one phone: selection %q, texts %q", ta.sel, tt.Texts())
	}
}

func TestPagesRender(t *testing.T) {
	// The phone plugged in below waits at "USB debugging is off" until the
	// test turns it on, so a slow frame cannot skip that state.
	ta := newTestAppWith(t, fakeapi.Options{Scenario: fakeapi.ThreePhones, Speed: 50, ApprovalTimeout: time.Minute, HoldSetup: true})
	ta.sync()
	tt := mg.NewTester(ta.view, 1280, 820)
	if err := tt.Click("Activity"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "Activity to load", func() bool { return ta.act.loaded })
	tt.Frame()
	for _, want := range []string{"Activity", "Today", "Reorder last week's lunch", "Cancel Thursday spin class", "Denied", "Stopped", "Steps"} {
		if !hasText(tt, want) {
			t.Errorf("Activity lacks %q: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"When should agents ask you?", "Blocked apps", "Northbank", "Read-only mode"} {
		if !hasText(tt, want) {
			t.Errorf("Safety lacks %q", want)
		}
	}
	if err := tt.Click("Advanced"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Developer mode"); err != nil {
		t.Fatal(err)
	}
	if !ta.settings.DeveloperMode {
		t.Fatal("Developer mode did not turn on")
	}
	if err := tt.Click("Add phone"); err != nil {
		t.Fatal(err)
	}
	if !hasText(tt, "Which phone?") {
		t.Errorf("Add phone: %q", tt.Texts())
	}
	if err := tt.Click("Android"); err != nil {
		t.Fatal(err)
	}
	ta.api.ConnectPhone()
	ta.waitFor(t, "the new phone", func() bool { return len(ta.devices) == 4 })
	tt.Frame()
	if !hasText(tt, "Found Pixel 8a. Unlock it and tap Allow on the phone.") {
		t.Errorf("Add phone after plugging in: %q", tt.Texts())
	}
	ta.api.FinishSetup()
	ta.waitFor(t, "the new phone online", func() bool { tt.Frame(); return ta.devices[3].Info.State == driver.StateOnline })
	ta.waitFor(t, "its checkup", func() bool {
		tt.Frame()
		r := ta.checkupOf(ta.devices[3].Info.ID)
		return r != nil && r.have && !r.running
	})
	tt.Frame()
	if !hasText(tt, "Everything works.") || hasText(tt, "Unlock it and tap Allow") {
		t.Errorf("Add phone after the phone came online: %q", tt.Texts())
	}
}

func TestDetailsInDeveloperMode(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.settings.DeveloperMode = true
	ta.waitFor(t, "the payment approval", func() bool { _, ok := ta.approval(); return ok })
	tt := mg.NewTester(ta.view, 1280, 820)
	if !hasText(tt, "Developer mode is on") {
		t.Fatalf("no Details switch: %q", tt.Texts())
	}
	if err := tt.Click("Show technical details"); err != nil {
		t.Fatal(err)
	}
	if !hasText(tt, `phone_launch {"app":"Dinely"}`) {
		t.Errorf("no tool calls: %q", tt.Texts())
	}
}

func TestApprovalPanelKeys(t *testing.T) {
	ta := newTestApp(t, fakeapi.ThreePhones)
	ta.waitFor(t, "two approvals", func() bool { return len(ta.approvals()) == 2 })
	w, h := ta.panelSize()
	tt := mg.NewTester(ta.panelView, w, h)
	for _, want := range []string{"1 of 2", "Pixel 9 · Dinely", "Pay $12.80?", "Denies in", "Allow", "More options"} {
		if !hasText(tt, want) {
			t.Errorf("panel lacks %q: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Next approval"); err != nil {
		t.Fatal(err)
	}
	if !hasText(tt, "Delete a group?") {
		t.Errorf("second approval: %q", tt.Texts())
	}
	tt.Key(0, mg.KeyEscape) // Esc denies the one showing
	ta.waitFor(t, "the denial", func() bool {
		for _, x := range ta.api.Tasks() {
			if x.DeviceID == "iphone-17" && x.State == gateway.TaskDone {
				return true
			}
		}
		return false
	})
	tt.Frame()
	if !hasText(tt, "Denied") {
		t.Errorf("no confirmation: %q", tt.Texts())
	}
}

func TestFlyout(t *testing.T) {
	ta := newTestApp(t, fakeapi.ThreePhones)
	ta.waitFor(t, "an approval", func() bool { _, ok := ta.approval(); return ok })
	tt := mg.NewTester(ta.flyoutView, 320, ta.flyoutHeight())
	for _, want := range []string{"Needs you", "Pay $12.80", "Review", "Phones", "Galaxy S25", "Open Pinky", "Stop all"} {
		if !hasText(tt, want) {
			t.Errorf("flyout lacks %q: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Stop all"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "Stop all", func() bool { return len(ta.api.Needs()) == 0 })
}

// TestMirrorInput sends pointer, wheel, key and text events to a mirror
// and checks what reaches the gateway, in the device's pixels.
func TestMirrorInput(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.sync()
	d := ta.devices[0]
	m := &mirror{a: ta.app, id: d.Info.ID, picW: 1080, picH: 2400, boxW: 270, boxH: 600}
	in := func(ev mg.InputEvent) {
		if !ta.mirrorInput(m, d, ev) {
			t.Errorf("event %v not taken", ev.Kind)
		}
	}
	in(mg.InputEvent{Kind: mg.InputPointerDown, X: 135, Y: 300})
	in(mg.InputEvent{Kind: mg.InputPointerMove, X: 135, Y: 200, Button: 0})
	in(mg.InputEvent{Kind: mg.InputPointerUp, X: 135, Y: 150})
	in(mg.InputEvent{Kind: mg.InputPointerDown, Button: 1, X: 10, Y: 10}) // right click: Back
	in(mg.InputEvent{Kind: mg.InputKeyDown, Key: mg.KeyEnter})
	in(mg.InputEvent{Kind: mg.InputText, Text: "你好"})
	var got []fakeapi.Input
	ta.waitFor(t, "the input", func() bool { got = ta.api.Inputs(); return len(got) >= 6 })
	want := []fakeapi.Input{
		{Device: "pixel-9", Kind: "touch", Action: driver.TouchDown, Point: screen.Point{X: 540, Y: 1200}},
		{Device: "pixel-9", Kind: "touch", Action: driver.TouchMove, Point: screen.Point{X: 540, Y: 800}},
		{Device: "pixel-9", Kind: "touch", Action: driver.TouchUp, Point: screen.Point{X: 540, Y: 600}},
		{Device: "pixel-9", Kind: "key", Key: driver.KeyBack},
		{Device: "pixel-9", Kind: "key", Key: driver.KeyEnter},
		{Device: "pixel-9", Kind: "text", Text: "你好"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("input %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The person's input paused the agent.
	ta.waitFor(t, "takeover", func() bool { return ta.devices[0].Status == gateway.DeviceInControl })

	// A touchpad's scroll becomes a finger dragging up from the pointer
	// (a mouse wheel's: TestMirrorWheel).
	n := len(ta.api.Inputs())
	in(mg.InputEvent{Kind: mg.InputScroll, X: 135, Y: 400, DY: 40, Precise: true})
	ta.waitFor(t, "the swipe", func() bool { return len(ta.api.Inputs()) >= n+3 })
	sw := ta.api.Inputs()[n:]
	if sw[0].Action != driver.TouchDown || sw[1].Action != driver.TouchMove || sw[2].Action != driver.TouchUp ||
		sw[1].Point.Y >= sw[0].Point.Y || sw[0].Point != (screen.Point{X: 540, Y: 1600}) {
		t.Errorf("wheel swipe = %+v", sw)
	}
}
