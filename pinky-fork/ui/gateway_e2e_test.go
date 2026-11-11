package ui

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/video"
	"github.com/DIYgod/pinky/ui/demogw"
)

// The client against the real gateway, on internal/demo's phones, with
// scripted agents calling the gateway as MCP clients do: the prototype's
// flows end to end.

type gwApp struct {
	*app
	w  *demogw.World
	mu sync.Mutex
	q  []func()
}

func newGatewayApp(t *testing.T, trust bool, opts ...func(*demogw.Options)) *gwApp {
	t.Helper()
	demogw.Pace = 20 * time.Millisecond
	o := demogw.Options{DataDir: t.TempDir(), TrustNewAgents: trust, MaxBlock: time.Second}
	for _, f := range opts {
		f(&o)
	}
	w, err := demogw.New(o)
	if err != nil {
		t.Fatal(err)
	}
	ga := &gwApp{w: w}
	ga.app = newApp(w.Gateway, Options{NoTray: true, Shortcut: "-"})
	testHooks(t, ga.app)
	ga.app.videoErr = errors.New("no video in tests")
	ga.app.postHook = func(fn func()) {
		ga.mu.Lock()
		ga.q = append(ga.q, fn)
		ga.mu.Unlock()
	}
	t.Cleanup(func() {
		ga.app.end()
		w.Gateway.Close()
	})
	return ga
}

func (ga *gwApp) sync() {
	ga.mu.Lock()
	q := ga.q
	ga.q = nil
	ga.mu.Unlock()
	for _, fn := range q {
		fn()
	}
	g := ga.w.Gateway
	ga.devices, ga.tasks = g.Devices(), g.Tasks()
	ga.setNeeds(g.Needs())
	ga.setBackgrounds(ga.readBackground(ga.devices))
	first := !ga.loaded
	ga.loaded = true
	ga.afterRefresh(first)
}

func (ga *gwApp) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		ga.sync()
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; needs %+v, tasks %+v", what, ga.needs, ga.tasks)
}

func (ga *gwApp) need(match func(gateway.NeedView) bool) (gateway.NeedView, bool) {
	for _, n := range ga.needs {
		if match(n) {
			return n, true
		}
	}
	return gateway.NeedView{}, false
}

// waitingStep reports whether the task has a step that waits for an
// approval.
func (ga *gwApp) waitingStep(taskID string) bool {
	for _, tk := range ga.tasks {
		if tk.ID != taskID {
			continue
		}
		for _, s := range tk.Steps {
			if s.Status == string(gateway.StatusPending) {
				return true
			}
		}
	}
	return false
}

func (ga *gwApp) device(id string) gateway.DeviceView {
	for _, d := range ga.devices {
		if d.Info.ID == id {
			return d
		}
	}
	return gateway.DeviceView{}
}

// agent starts a scripted agent and returns where its result arrives.
func (ga *gwApp) agent(t *testing.T, name, device string, run func(*demogw.Agent, context.Context) error) <-chan error {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ag := ga.w.NewAgent(name, device)
	ag.Log = t.Logf
	go func() {
		defer ag.Close()
		done <- run(ag, ctx)
	}()
	t.Cleanup(cancel)
	return done
}

func wait(t *testing.T, done <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	return nil
}

const pixel = "fake-pixel-9"

func isPayment(n gateway.NeedView) bool { return n.Kind == gateway.NeedApproval && !newAgentNeed(n) }

// TestGatewayLunch is the main flow: the first-agent approval, the payment
// approval with its screenshot, a takeover by touching the mirror, hand
// back, the payment again, and the finished task in Activity with its
// screenshots.
func TestGatewayLunch(t *testing.T) {
	ga := newGatewayApp(t, false)
	done := ga.agent(t, "Claude Code", pixel, (*demogw.Agent).ReorderLunch)
	tt := mg.NewTester(ga.view, 1280, 820)

	// The first connection asks.
	ga.waitFor(t, "the New agent approval", func() bool { _, ok := ga.need(newAgentNeed); return ok })
	tt.Frame()
	for _, want := range []string{"Let Claude Code use your phones?", "First connection", "Let Claude Code", "All apps except blocked ones", "Approve"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if hasText(tt, "Allowing gives it") {
		t.Errorf("the scope sentence shows next to the scope picker: %q", tt.Texts())
	}
	if err := tt.Click("Approve"); err != nil {
		t.Fatal(err)
	}

	// The payment asks, with the screen. The gateway opens the approval a
	// moment before it adds the task's "Waiting for you" step (it keeps the
	// screenshot in between), so wait for both.
	var pay gateway.NeedView
	ga.waitFor(t, "the payment approval and its step", func() (ok bool) {
		pay, ok = ga.need(isPayment)
		return ok && ga.waitingStep(pay.TaskID)
	})
	if pay.Screenshot == nil || pay.Target.Empty() || !strings.Contains(pay.Title, "$12.80") {
		t.Fatalf("payment approval = %+v", pay)
	}
	tt.Frame()
	if !hasText(tt, pay.Title) || !hasText(tt, "Waiting for you: tap") {
		t.Errorf("no payment card or waiting step in %q", tt.Texts())
	}

	// The person touches the mirror: the agent pauses and waits.
	d := ga.device(pixel)
	m := &mirror{a: ga.app, id: pixel, picW: 1080, picH: 2400, boxW: 270, boxH: 600}
	ga.mirrorInput(m, d, mg.InputEvent{Kind: mg.InputPointerDown, X: 20, Y: 20})
	ga.mirrorInput(m, d, mg.InputEvent{Kind: mg.InputPointerUp, X: 20, Y: 20})
	// The agent then waits with phone_handoff, which the gateway ranks
	// above the person's control.
	ga.waitFor(t, "the agent to wait for the person", func() bool { _, ok := ga.handoffFor(pixel); return ok })
	tt.Frame()
	for _, want := range []string{"You're in control.", "Claude Code is paused.", "Hand back", "You're in control. Click Done to hand the phone back to Claude Code."} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if hasText(tt, "Take over") {
		t.Errorf("Take over shows while the person is in control: %q", tt.Texts())
	}
	if _, ok := ga.need(isPayment); ok {
		t.Error("the payment approval is still open after the takeover")
	}

	// Hand back from the banner: the agent looks again and asks again.
	if err := tt.Click("Hand back"); err != nil {
		t.Fatal(err)
	}
	ga.waitFor(t, "the payment approval again", func() (ok bool) { pay, ok = ga.need(isPayment); return })
	tt.Frame()
	if err := tt.Click("Allow"); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, done, "the agent"); err != nil {
		t.Fatalf("agent: %v", err)
	}
	ga.waitFor(t, "the task to finish", func() bool {
		ts := ga.tasks
		return len(ts) > 0 && ts[len(ts)-1].State == gateway.TaskDone
	})
	if got := ga.device(pixel).Status; got != gateway.DeviceIdle {
		t.Errorf("Pixel is %q after the task", got)
	}

	// Activity lists the task, with screenshots behind its steps.
	ga.page = pageActivity
	tt.Frame()
	ga.waitFor(t, "Activity", func() bool { tt.Frame(); return ga.act.loaded && len(ga.act.tasks) > 0 })
	task := ga.act.tasks[0]
	if task.Title != "Reorder last week's lunch" {
		t.Fatalf("Activity's first task is %q", task.Title)
	}
	ga.act.sel = task.ID
	shots := 0
	for _, s := range task.Steps {
		if ga.hasShot(s) {
			shots++
		}
	}
	if shots == 0 {
		t.Fatalf("no step has a screenshot: %+v", task.Steps)
	}
	tt.Frame()
	ga.waitFor(t, "the thumbnails", func() bool {
		tt.Frame()
		for _, s := range task.Steps {
			if e, _ := ga.stepShot(s); e != nil {
				return true
			}
		}
		return false
	})
	for _, want := range []string{"Reorder last week's lunch", "Watch replay"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in Activity: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Watch replay"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasText(tt, "Step 1 of") {
		t.Errorf("no replay: %q", tt.Texts())
	}
}

// scopeRecorder is the gateway, with the calls that answer a first
// connection recorded in order.
type scopeRecorder struct {
	*gateway.Gateway
	mu    sync.Mutex
	calls []string
}

func (r *scopeRecorder) SetAgentScope(name string, scope gateway.Scope) {
	r.mu.Lock()
	r.calls = append(r.calls, "scope "+name+" "+scope.String())
	r.mu.Unlock()
	r.Gateway.SetAgentScope(name, scope)
}

func (r *scopeRecorder) Decide(id string, d gateway.Decision, note string) error {
	r.mu.Lock()
	r.calls = append(r.calls, "decide "+string(d))
	r.mu.Unlock()
	return r.Gateway.Decide(id, d, note)
}

// TestGatewayAgentScope approves a first connection with a narrower scope
// chosen in the card: the client sets the scope, then allows, so the
// gateway keeps the person's choice as the agent picks up the answer.
func TestGatewayAgentScope(t *testing.T) {
	ga := newGatewayApp(t, false)
	rec := &scopeRecorder{Gateway: ga.w.Gateway}
	ga.app.api = rec
	ga.agent(t, "Codex", pixel, (*demogw.Agent).ReorderLunch)
	var n gateway.NeedView
	ga.waitFor(t, "the New agent approval", func() (ok bool) { n, ok = ga.need(newAgentNeed); return })
	ga.agents.choice = map[string]string{n.ID: "Only the apps I choose"}
	ga.agents.apps = map[string]string{n.ID: "Dinely, Chatter"}
	tt := mg.NewTester(ga.view, 1280, 820)
	if !hasText(tt, "Only the apps I choose") {
		t.Fatalf("no scope picker: %q", tt.Texts())
	}
	if err := tt.Click("Approve"); err != nil {
		t.Fatal(err)
	}
	ga.waitFor(t, "the agent to connect", func() bool {
		ts := ga.tasks
		return len(ts) > 0 && ts[0].Agent == "Codex"
	})
	want := gateway.Scope{Mode: gateway.ScopeApps, Apps: []string{"Dinely", "Chatter"}}
	rec.mu.Lock()
	calls := strings.Join(rec.calls, "; ")
	rec.mu.Unlock()
	if calls != "scope Codex "+want.String()+"; decide allow_once" {
		t.Errorf("calls = %q", calls)
	}

	// Settings › Agents lists it with its scope. Until the gateway keeps a
	// scope set before the answer, the agent picking it up may replace
	// it, so it is set again here.
	ga.w.Gateway.SetAgentScope("Codex", want)
	ga.page, ga.set.sec = pageSettings, secAgents
	ga.waitFor(t, "the agent list", func() bool {
		tt.Frame()
		return ga.agents.loaded && hasText(tt, "Can use Dinely and Chatter")
	})
}

// TestGatewayHandoff answers phone_handoff with Done and a note.
func TestGatewayHandoff(t *testing.T) {
	ga := newGatewayApp(t, true)
	done := ga.agent(t, "Claude Code", "fake-iphone-17", (*demogw.Agent).SignIn)
	ga.waitFor(t, "the handoff", func() bool {
		_, ok := ga.need(func(n gateway.NeedView) bool { return n.Kind == gateway.NeedHandoff })
		return ok
	})
	tt := mg.NewTester(ga.view, 1280, 820)
	for _, want := range []string{"Sign in to Dinely, please", "Do it on the phone and click Done, or take over to do it here.", "Done", "Take over", "Decline", "Add a note"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Add a note"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Type("Signed in as Kim")
	if err := tt.Click("Done"); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, done, "the agent"); err != nil {
		t.Fatalf("agent: %v", err)
	}
	ga.waitFor(t, "the task to finish", func() bool {
		ts := ga.tasks
		return len(ts) > 0 && ts[len(ts)-1].State == gateway.TaskDone
	})
	tk := ga.tasks[len(ga.tasks)-1]
	var handoff, sub string
	for i, s := range tk.Steps {
		if s.Tool == "phone_handoff" {
			handoff, sub, _ = stepDisplay(tk, i)
		}
	}
	if handoff != "You handed it back" || sub != "Asked: “Sign in to Dinely, please”" {
		t.Errorf("handoff step reads %q / %q", handoff, sub)
	}
}

// TestGatewayLockedHandoff shows a handoff on a locked phone as one card.
// Demo phones cannot be unlocked from the computer (no driver.Unlocker),
// so the card has no unlock button and says to unlock on the phone.
func TestGatewayLockedHandoff(t *testing.T) {
	ga := newGatewayApp(t, true)
	ga.w.IPhone.SetLocked(true)
	ga.agent(t, "Claude Code", "fake-iphone-17", func(ag *demogw.Agent, ctx context.Context) error {
		if err := ag.Connect(ctx, "Check Dinely's saved address"); err != nil {
			return err
		}
		return ag.WaitForPerson(ctx, "Unlock the phone, please")
	})
	ga.waitFor(t, "the handoff", func() bool {
		_, ok := ga.need(func(n gateway.NeedView) bool { return n.Kind == gateway.NeedHandoff })
		return ok && ga.unlocks["fake-iphone-17"]
	})
	for _, n := range ga.needs {
		if n.Kind == gateway.NeedUnlock {
			t.Errorf("a separate unlock card shows: %+v", n)
		}
	}
	tt := mg.NewTester(ga.view, 1280, 820)
	// The demo iPhone can be unlocked from the computer (driver.Unlocker),
	// but no PIN is saved: the card offers to save one.
	for _, want := range []string{"Unlock the phone, please",
		"The phone is locked. Save the phone's PIN to unlock it from this computer, or unlock it on the phone, then click Done.",
		"Save PIN…", "Decline", "Done"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if label, _ := unlockWords(); hasText(tt, label) {
		t.Errorf("an unlock button shows for a phone without a saved PIN: %q", tt.Texts())
	}
}

// TestGatewayStopAndStopAll stops one task, then every task.
func TestGatewayStopAndStopAll(t *testing.T) {
	ga := newGatewayApp(t, true)
	lunch := ga.agent(t, "Claude Code", pixel, (*demogw.Agent).ReorderLunch)
	ga.waitFor(t, "the task", func() bool { return len(ga.tasks) > 0 && running(ga.tasks[0].State) })
	ga.selectPhone(pixel)
	tt := mg.NewTester(ga.view, 1280, 820)
	if err := tt.Click("Stop"); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, lunch, "the stopped agent"); !errors.Is(err, demogw.ErrEnded) {
		t.Errorf("stopped agent returned %v", err)
	}
	ga.waitFor(t, "the stopped task", func() bool { return len(ga.tasks) > 0 && ga.tasks[0].State == gateway.TaskStopped })

	signIn := ga.agent(t, "Claude Code", "fake-iphone-17", (*demogw.Agent).SignIn)
	ga.waitFor(t, "the handoff", func() bool {
		_, ok := ga.need(func(n gateway.NeedView) bool { return n.Kind == gateway.NeedHandoff })
		return ok
	})
	tt.Frame()
	if err := tt.Click("Stop all"); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, signIn, "the agent after Stop all"); !errors.Is(err, demogw.ErrEnded) {
		t.Errorf("agent after Stop all returned %v", err)
	}
	ga.waitFor(t, "no needs", func() bool { return len(ga.needs) == 0 })
}

// TestGatewayInControlWithoutTask hands back a phone the person kept after
// the agent left.
func TestGatewayInControlWithoutTask(t *testing.T) {
	ga := newGatewayApp(t, true)
	ga.waitFor(t, "the phones", func() bool { return ga.device(pixel).Status == gateway.DeviceIdle })
	if err := ga.w.Gateway.TakeOver(pixel); err != nil {
		t.Fatal(err)
	}
	ga.waitFor(t, "control", func() bool { return ga.device(pixel).Status == gateway.DeviceInControl })
	ga.selectPhone(pixel)
	tt := mg.NewTester(ga.view, 1280, 820)
	if !hasText(tt, "Hand back to let agents use this phone again.") || hasText(tt, "is paused") {
		t.Errorf("banner reads %q", tt.Texts())
	}
	if err := tt.Click("Hand back"); err != nil {
		t.Fatal(err)
	}
	ga.waitFor(t, "the phone to be idle", func() bool { return ga.device(pixel).Status == gateway.DeviceIdle })
}

// TestGatewayInControlAfterTheAgentLeft: the person took over during a
// task and the agent ended it. The phone stays theirs, "You're in control"
// with no task, and both its page and All phones offer Hand back.
func TestGatewayInControlAfterTheAgentLeft(t *testing.T) {
	ga := newGatewayApp(t, true)
	ga.waitFor(t, "the phones", func() bool { return ga.device(pixel).Status == gateway.DeviceIdle })
	tookOver := make(chan struct{})
	done := ga.agent(t, "Claude Code", pixel, func(ag *demogw.Agent, ctx context.Context) error {
		if err := ag.Connect(ctx, "Look at the home screen"); err != nil {
			return err
		}
		<-tookOver
		ag.Release(ctx, "done", "The person took the phone.")
		return nil
	})
	ga.waitFor(t, "the task", func() bool { return ga.device(pixel).Status == gateway.DeviceWorking })
	ga.takeOver(pixel)
	ga.waitFor(t, "control", func() bool { return ga.device(pixel).Status == gateway.DeviceInControl })
	close(tookOver)
	if err := wait(t, done, "the agent"); err != nil {
		t.Fatal(err)
	}
	ga.waitFor(t, "the task to end", func() bool { tk := ga.taskOf(pixel); return tk != nil && !running(tk.State) })
	if d := ga.device(pixel); d.Status != gateway.DeviceInControl || d.TaskID != "" {
		t.Fatalf("after the agent left, the Pixel is %q with task %q", d.Status, d.TaskID)
	}

	tt := mg.NewTester(ga.view, 1280, 820)
	if !hasText(tt, "You're in control.") || !hasText(tt, "Hand back to let agents use this phone again.") || hasText(tt, "is paused") {
		t.Errorf("banner reads %q", tt.Texts())
	}
	ga.sel = allPhones
	tt.Frame()
	if !hasText(tt, "You're in control") || !hasText(tt, "Hand back") {
		t.Errorf("All phones: %q", tt.Texts())
	}
	ga.selectPhone(pixel)
	tt.Frame()
	if err := tt.Click("Hand back"); err != nil {
		t.Fatal(err)
	}
	ga.waitFor(t, "the phone to be idle", func() bool { return ga.device(pixel).Status == gateway.DeviceIdle })
}

// TestGatewayScreenshots renders the flows above in light and dark into
// $PINKY_SCREENSHOTS, for the notes. Tests have no video, so the mirrors
// show still screenshots of the demo phones.
func TestGatewayScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS to a folder to write the screenshots")
	}
	ga := newGatewayApp(t, false)
	stills := func() {
		for _, dev := range []*demo.Device{ga.w.Pixel, ga.w.IPhone} {
			img, err := dev.Screenshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			b := img.Bounds()
			ga.mirrors[dev.Info().ID] = &mirror{a: ga.app, id: dev.Info().ID, state: "playing",
				bitmap: mg.NewBitmap(img), picW: b.Dx(), picH: b.Dy(), used: ga.buildSeq}
		}
	}
	shoot := func(view func(*mg.Context), w, h int, name string) {
		t.Helper()
		for _, dark := range []bool{false, true} {
			stills()
			tt := mg.NewTester(view, w, h)
			tt.SetScale(2)
			tt.SetDark(dark)
			tt.Frame()
			stills()
			tt.Frame()
			file := name + "-light.png"
			if dark {
				file = name + "-dark.png"
			}
			f, err := os.Create(filepath.Join(dir, file))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
	ga.agent(t, "Claude Code", pixel, (*demogw.Agent).ReorderLunch)
	var n gateway.NeedView
	ga.waitFor(t, "the New agent approval", func() (ok bool) { n, ok = ga.need(newAgentNeed); return })
	ga.selectPhone(pixel)
	shoot(ga.view, 1280, 820, "gateway-new-agent")
	ga.page, ga.set.sec = pageSettings, secAgents
	shoot(ga.view, 1280, 820, "settings-agent-request")
	ga.page = pagePhones
	ga.approveAgent(n, gateway.Scope{Mode: gateway.ScopeEverything})

	ga.waitFor(t, "the payment approval", func() bool { _, ok := ga.need(isPayment); return ok })
	shoot(ga.view, 1280, 820, "gateway-payment")

	ga.agent(t, "Claude Code", "fake-iphone-17", (*demogw.Agent).SignIn)
	ga.waitFor(t, "the handoff", func() bool { _, ok := ga.handoffFor("fake-iphone-17"); return ok })
	ga.selectPhone("fake-iphone-17")
	shoot(ga.view, 1280, 820, "handoff")
	ga.focused = false
	for i, x := range ga.approvals() {
		if x.Kind == gateway.NeedHandoff {
			ga.panelIdx = i
		}
	}
	pw, ph := ga.panelSize()
	shoot(ga.panelView, pw, ph, "handoff-panel")

	pay, _ := ga.need(isPayment)
	ga.decide(pay, gateway.AllowOnce, "")
	ho, _ := ga.handoffFor("fake-iphone-17")
	ga.decide(ho, gateway.AllowOnce, "")
	ga.waitFor(t, "both tasks to finish", func() bool {
		done := 0
		for _, tk := range ga.tasks {
			if tk.State == gateway.TaskDone {
				done++
			}
		}
		return done == 2
	})
	ga.page, ga.set.sec = pageSettings, secAgents
	tt := mg.NewTester(ga.view, 1280, 820)
	ga.waitFor(t, "the agent list", func() bool {
		tt.Frame()
		return ga.agents.loaded && len(ga.agents.list) > 0 && ga.agents.list[0].Approved
	})
	ga.agents.editing = "Claude Code"
	shoot(ga.view, 1280, 820, "settings-agents")
	ga.agents.editing = ""

	ga.page = pageActivity
	ga.waitFor(t, "Activity", func() bool { tt.Frame(); return ga.act.loaded && len(ga.act.tasks) >= 2 })
	var lunch gateway.TaskView
	for _, tk := range ga.act.tasks {
		if tk.Title == "Reorder last week's lunch" {
			lunch = tk
		}
	}
	ga.act.sel = lunch.ID
	for _, s := range lunch.Steps {
		if strings.Contains(s.Text, "Place order") {
			ga.previews[fmt.Sprintf("act/%s/%d", lunch.ID, s.Index)] = true
		}
	}
	ga.waitFor(t, "the screenshots", func() bool {
		tt.Frame()
		for _, s := range lunch.Steps {
			if ga.hasShot(s) {
				if e, _ := ga.stepShot(s); e == nil {
					return false
				}
			}
		}
		return true
	})
	shoot(ga.view, 1280, 820, "activity-shots")
}

// TestMirrorFirstPicture checks that the first picture, which arrives
// while the mirror paints, asks for the view to be built again: painting
// does not build it, and a still phone sends nothing else that would, so
// "Connecting…" stayed over the live picture.
func TestMirrorFirstPicture(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs the ffmpeg command")
	}
	if err := video.Load(); err != nil {
		t.Skip(err)
	}
	w, err := demogw.New(demogw.Options{Video: true, TrustNewAgents: true})
	if err != nil {
		t.Fatal(err)
	}
	ga := &gwApp{w: w}
	ga.app = newApp(w.Gateway, Options{NoTray: true, Shortcut: "-"})
	testHooks(t, ga.app)
	ga.app.postHook = func(fn func()) {
		ga.mu.Lock()
		ga.q = append(ga.q, fn)
		ga.mu.Unlock()
	}
	t.Cleanup(func() {
		ga.closeMirrors()
		ga.app.end()
		w.Gateway.Close()
	})
	ga.waitFor(t, "the phones", func() bool { return ga.device(pixel).Status == gateway.DeviceIdle })
	ga.selectPhone(pixel)
	tt := mg.NewTester(ga.view, 1280, 820)
	ga.waitFor(t, "the stream", func() bool {
		tt.Frame()
		m := ga.mirrors[pixel]
		return m != nil && m.state == "playing"
	})
	if !hasText(tt, "Connecting…") {
		t.Fatalf("no Connecting before the first picture: %q", tt.Texts())
	}
	m := ga.mirrors[pixel]
	for i := 0; m.bitmap == nil; i++ {
		if i > 400 {
			t.Fatalf("no picture: %+v", m.player.Stats())
		}
		time.Sleep(25 * time.Millisecond)
		tt.Frame() // paints, and takes the picture once there is one
	}
	ga.mu.Lock()
	posted := len(ga.q)
	ga.mu.Unlock()
	if posted == 0 {
		t.Fatal("the first picture did not ask for the view to be built again")
	}
	ga.sync()
	tt.Frame()
	if hasText(tt, "Connecting…") {
		t.Errorf("Connecting… over the live picture: %q", tt.Texts())
	}
}

// TestMirrorRetryKeepsError checks that trying a failed stream again keeps
// showing why it failed: a phone without a picture used to flip to the
// Connecting spinner every few seconds.
func TestMirrorRetryKeepsError(t *testing.T) {
	w, err := demogw.New(demogw.Options{TrustNewAgents: true}) // no video: Stream fails
	if err != nil {
		t.Fatal(err)
	}
	ga := &gwApp{w: w}
	ga.app = newApp(w.Gateway, Options{NoTray: true, Shortcut: "-"})
	testHooks(t, ga.app)
	ga.app.postHook = func(fn func()) {
		ga.mu.Lock()
		ga.q = append(ga.q, fn)
		ga.mu.Unlock()
	}
	t.Cleanup(func() {
		ga.closeMirrors()
		ga.app.end()
		w.Gateway.Close()
	})
	ga.waitFor(t, "the phones", func() bool { return ga.device(pixel).Status == gateway.DeviceIdle })
	ga.selectPhone(pixel)
	tt := mg.NewTester(ga.view, 1280, 820)
	ga.waitFor(t, "the failure", func() bool {
		tt.Frame()
		m := ga.mirrors[pixel]
		return m != nil && m.state == "failed" && !m.opening
	})
	if !hasText(tt, "This phone has no live picture.") {
		t.Fatalf("no reason shown: %q", tt.Texts())
	}
	m := ga.mirrors[pixel]
	m.retry = time.Now().Add(-time.Second)
	tt.Frame() // the retry is due: mirrorFor opens the stream again
	if !m.opening {
		t.Fatal("the stream was not tried again")
	}
	if m.state != "failed" || hasText(tt, "Connecting…") {
		t.Errorf("while trying again: state %q, texts %q", m.state, tt.Texts())
	}
	tt.Frame()
	ga.waitFor(t, "the second failure", func() bool { return !m.opening })
	tt.Frame()
	if !hasText(tt, "This phone has no live picture.") || hasText(tt, "Connecting…") {
		t.Errorf("after trying again: %q", tt.Texts())
	}
}

// TestGatewayRound3 runs the round-three controls against the real
// gateway on the demo phones: the controls follow the phones' features,
// Activity offers the task's recording or its replay, Export all writes the
// gateway's report, and pairing over Wi-Fi says why it is unavailable (the
// demo has no Pairer).
func TestGatewayRound3(t *testing.T) {
	ga := newGatewayApp(t, true)
	var revealed []string
	ga.reveal = func(p string) { revealed = append(revealed, p) }
	ga.applySettings() // the stubs take them without complaint
	done := ga.agent(t, "Claude Code", pixel, func(ag *demogw.Agent, ctx context.Context) error {
		if err := ag.Connect(ctx, "Look at the home screen"); err != nil {
			return err
		}
		if err := ag.Observe(ctx); err != nil {
			return err
		}
		ag.Release(ctx, "done", "Looked at the home screen.")
		return nil
	})
	if err := wait(t, done, "the agent"); err != nil {
		t.Fatal(err)
	}
	ga.waitFor(t, "the phones", func() bool { return ga.device(pixel).Status == gateway.DeviceIdle })
	if f := ga.device(pixel).Features; f != (gateway.Features{Unlock: true, Background: true, Files: true, Pinch: true, Rotate: true}) {
		t.Errorf("the demo Pixel has features %+v", f)
	}
	ga.selectPhone(pixel)
	tt := mg.NewTester(ga.view, 1280, 820)
	tt.Frame()
	if hasText(tt, "Pinky Companion") {
		t.Errorf("a companion card for a phone without one: %q", tt.Texts())
	}

	ga.page, ga.set.sec = pageSettings, secPhones
	tt.Frame()
	if !hasText(tt, "Work in the background") || !hasText(tt, "Unlock from this computer") {
		t.Errorf("Settings › Phones lacks controls the phones have: %q", tt.Texts())
	}

	ga.page = pageActivity
	ga.waitFor(t, "the task in Activity", func() bool { tt.Frame(); return ga.act.loaded && len(ga.act.tasks) > 0 })
	ga.act.sel = ga.act.tasks[0].ID
	ga.waitFor(t, "the recording lookup", func() bool {
		tt.Frame()
		e := ga.rec.views[ga.act.sel]
		return e != nil && !e.loading
	})
	tt.Frame()
	if !hasText(tt, "Watch replay") && !hasText(tt, "Watch recording") {
		t.Errorf("a finished task offers neither its recording nor a replay: %q", tt.Texts())
	}
	if err := tt.Click("Export all"); err != nil {
		t.Fatal(err)
	}
	ga.waitFor(t, "the export", func() bool { return len(revealed) == 1 })
	if filepath.Ext(revealed[0]) != ".html" {
		t.Errorf("exported %v", revealed)
	}
	data, err := os.ReadFile(revealed[0])
	if err != nil || !strings.Contains(string(data), "Look at the home screen") {
		t.Errorf("the client's report: %v\n%s", err, data)
	}

	ga.page = pageAddPhone
	ga.add.reset(ga.app)
	ga.add.step = "qr"
	ga.waitFor(t, "the pairing to fail", func() bool { tt.Frame(); return ga.add.pair.state == "failed" })
	tt.Frame()
	if !hasText(tt, "can't pair phones over Wi-Fi") {
		t.Errorf("pairing without a Pairer: %q", tt.Texts())
	}
}
