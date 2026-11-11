package gateway

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

func TestBackgroundDisplay(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	if err := g.SetBackground("pixel", true); err != nil {
		t.Fatal(err)
	}
	s, _ := connect(t, g, "Lunch")
	bgs := x.backgrounds()
	if len(bgs) != 1 {
		t.Fatalf("%d background displays opened", len(bgs))
	}
	bg := bgs[0]
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	if bg.Current() != "dinely" || x.Current() != "home" {
		t.Fatalf("the launch reached the phone's screen: background %q, phone %q", bg.Current(), x.Current())
	}
	if d := g.Devices()[0]; !strings.Contains(d.StatusLine, "in the background") || !d.Features.Background {
		t.Fatalf("device %+v", d)
	}

	// The phone's own lock does not stop the agent, and asks nobody.
	x.SetLocked(true)
	time.Sleep(60 * time.Millisecond) // the lock poll
	res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Chatter"})
	mustStatus(t, res, StatusSettled)
	for _, n := range g.Needs() {
		if n.Kind == NeedUnlock {
			t.Fatalf("an unlock card for a task on a background display: %+v", n)
		}
	}
	// The person using the phone (or the mirror of its screen) does not
	// pause it either; Take over does.
	x.PersonTouch()
	if err := g.Touch("pixel", driver.TouchUp, screen.Point{X: 10, Y: 10}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "back"}), StatusSettled)
	if err := g.TakeOver("pixel"); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "back"}), StatusTookOver)
	if err := g.HandBack("pixel", ""); err != nil {
		t.Fatal(err)
	}

	// The display closes with the task.
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
	deadline := time.Now().Add(2 * time.Second)
	for !bg.isClosed() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !bg.isClosed() {
		t.Fatal("the background display stayed open after the task")
	}
	if _, err := g.AgentStream(ctx, "pixel", driver.StreamOptions{}); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("agent stream without a task: %v", err)
	}
}

// An app that will not run on a background display moves the task to the
// phone's own screen, and the agent hears why.
func TestBackgroundRefusedFallsBack(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	x.refuse[demo.Chatter] = true
	g := newExtraGateway(t, x)
	g.SetBackground("pixel", true)
	s, _ := connect(t, g, "Chat")
	res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Chatter"})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "does not run on a background display") {
		t.Fatalf("message %q", res.Message)
	}
	if x.Current() != "chats" {
		t.Fatalf("Chatter is not on the phone's screen: %q", x.Current())
	}
	if !x.backgrounds()[0].isClosed() {
		t.Fatal("the background display stayed open")
	}
	// The rest of the task stays on the phone's screen.
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusSettled)
	if x.Current() != "home" || len(x.backgrounds()) != 1 {
		t.Fatalf("phone %q, %d displays", x.Current(), len(x.backgrounds()))
	}

	// When the phone's screen is locked, the launch does not happen there.
	x2 := newExtraDev("pixel", "Pixel 9")
	x2.refuse[demo.Chatter] = true
	g2 := newExtraGateway(t, x2)
	g2.SetBackground("pixel", true)
	s2, _ := connect(t, g2, "Chat")
	x2.SetLocked(true)
	res = s2.Act(ctx, Call{}, Action{Tool: "launch", App: "Chatter"})
	mustStatus(t, res, StatusLocked)
	if x2.Current() == "chats" {
		t.Fatal("launched on a locked phone")
	}
}

func TestBackgroundCannotOpen(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	x.bgErr = errors.New("this phone has no virtual displays")
	g := newExtraGateway(t, x)
	g.SetBackground("pixel", true)
	s := g.OpenSession(agent)
	defer s.Close()
	res := s.Connect(ctx, ConnectRequest{Task: "Lunch"})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "could not open a background display") {
		t.Fatalf("message %q", res.Message)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	if x.Current() != "dinely" {
		t.Fatalf("phone %q", x.Current())
	}
}

func TestBackgroundSetting(t *testing.T) {
	g := newIPhoneGateway(t)
	if err := g.SetBackground("iphone", true); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("a phone without background displays: %v", err)
	}
	if err := g.SetBackground("nope", true); !errors.Is(err, ErrUnknownDevice) {
		t.Fatalf("unknown phone: %v", err)
	}
	// Kept across restarts.
	x := newExtraDev("pixel", "Pixel 9")
	dir := t.TempDir()
	g1 := newExtraGateway(t, x, func(o *Options) { o.DataDir = dir })
	if err := g1.SetBackground("pixel", true); err != nil {
		t.Fatal(err)
	}
	g1.Close()
	g2 := newExtraGateway(t, x, func(o *Options) { o.DataDir = dir })
	if !g2.Background("pixel") {
		t.Fatal("background off after a restart")
	}
}

func TestWakeBeforeTheAgentActs(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	x.setScreen(false)
	s, _ := connect(t, g, "Lunch")
	if x.wakes != 1 {
		t.Fatalf("woke %d times for the first screen", x.wakes)
	}
	// Woken moments ago: not again (the screen stays on for its timeout).
	x.setScreen(false)
	mustStatus(t, s.Observe(ctx, ObserveRequest{}), StatusSettled)
	if x.wakes != 1 {
		t.Fatalf("woke %d times", x.wakes)
	}
	g.mu.Lock()
	g.devices["pixel"].wokeAt = time.Time{}
	g.mu.Unlock()
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	if x.wakes != 2 {
		t.Fatalf("woke %d times, want once more before the action", x.wakes)
	}
	// A task on a background display leaves the phone's screen off.
	x2 := newExtraDev("pixel", "Pixel 9")
	g2 := newExtraGateway(t, x2)
	g2.SetBackground("pixel", true)
	x2.setScreen(false)
	s2, _ := connect(t, g2, "Lunch")
	mustStatus(t, s2.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	if x2.wakes != 0 {
		t.Fatalf("woke the phone %d times for a background task", x2.wakes)
	}
}

// unreadable is a background display that cannot be read, like a virtual
// display Android keeps from accessibility services.
type unreadable struct{ *bgDisplay }

func (u unreadable) Observe(context.Context, driver.ObserveOptions) (*screen.Screen, error) {
	return nil, fmt.Errorf("display 358 is not open to accessibility services: %w", driver.ErrUnsupported)
}

type unreadableDev struct{ *extraDev }

func (x unreadableDev) OpenBackground(ctx context.Context, opt driver.BackgroundOptions) (driver.Device, error) {
	d, err := x.extraDev.OpenBackground(ctx, opt)
	if err != nil {
		return nil, err
	}
	return unreadable{d.(*bgDisplay)}, nil
}

func TestBackgroundUnreadableFallsBack(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x, func(o *Options) {
		o.Providers = []driver.Provider{extraProvider2{demo.NewProvider(driver.Android, x.Device), unreadableDev{x}}}
	})
	g.SetBackground("pixel", true)
	s := g.OpenSession(agent)
	defer s.Close()
	res := s.Connect(ctx, ConnectRequest{Task: "Lunch"})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "cannot read the background display") {
		t.Fatalf("message %q", res.Message)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	if x.Current() != "dinely" || !x.backgrounds()[0].isClosed() {
		t.Fatalf("phone %q", x.Current())
	}
}

type extraProvider2 struct {
	*demo.Provider
	d driver.Device
}

func (p extraProvider2) Open(context.Context, string) (driver.Device, error) { return p.d, nil }

// The lock screen can show notifications from blocked apps: no screenshot
// of it for the agent either.
func TestNoScreenshotOfALockedPhone(t *testing.T) {
	g, dev := newTestGateway(t)
	s, _ := connect(t, g, "Lunch")
	dev.SetLocked(true)
	res := s.Observe(context.Background(), ObserveRequest{Screenshot: true})
	if res.Screenshot != nil || !strings.Contains(res.Message, "No screenshot: the phone is locked") {
		t.Fatalf("screenshot of a locked phone: %v, %q", res.Screenshot != nil, res.Message)
	}
	dev.SetLocked(false)
	if res := s.Observe(context.Background(), ObserveRequest{Screenshot: true}); res.Screenshot == nil {
		t.Fatalf("no screenshot when unlocked: %q", res.Message)
	}
}

// Turning background on applies from the next task; a watch-only agent
// watches the phone's own screen.
func TestBackgroundFromTheNextTask(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Lunch")
	if err := g.SetBackground("pixel", true); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	if len(x.backgrounds()) != 0 || x.Current() != "dinely" {
		t.Fatalf("the running task moved: %d displays, phone %q", len(x.backgrounds()), x.Current())
	}
	s.Release(ctx, ReleaseRequest{})

	g.SetAgentScope("Watcher", Scope{Mode: ScopeWatchOnly})
	w := g.OpenSession(AgentInfo{Name: "Watcher"})
	defer w.Close()
	mustStatus(t, w.Connect(ctx, ConnectRequest{Task: "Watch"}), StatusSettled)
	mustStatus(t, w.Observe(ctx, ObserveRequest{}), StatusSettled)
	if len(x.backgrounds()) != 0 {
		t.Fatal("a watch-only agent got a background display")
	}
}

// When an app refuses the background display while the person is using
// the phone's own screen, the agent waits for them instead of taking it.
func TestBackgroundFallbackWaitsForThePerson(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	x.refuse[demo.Chatter] = true
	g := newExtraGateway(t, x)
	g.SetBackground("pixel", true)
	s, _ := connect(t, g, "Chat")
	x.Show("settings") // the person is in Settings
	if err := g.PersonTouched("pixel"); err != nil {
		t.Fatal(err)
	}
	res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Chatter"})
	mustStatus(t, res, StatusTookOver)
	if x.Current() != "settings" {
		t.Fatalf("the agent took the person's screen: %q", x.Current())
	}
	if d := g.Devices()[0]; d.Status != DeviceInControl {
		t.Fatalf("device %+v", d)
	}
}

// Unlocking a phone that is already unlocked enters no PIN: it would go
// into the app in front.
func TestUnlockOfAnUnlockedPhone(t *testing.T) {
	x := newExtraDev("pixel", "Pixel 9")
	x.pin = "4711"
	g := newExtraGateway(t, x, func(o *Options) {
		o.PIN = func(context.Context, string) (string, error) { return "4711", nil }
	})
	connect(t, g, "Lunch")
	if err := g.Unlock("pixel"); err != nil {
		t.Fatal(err)
	}
	if len(x.entered) != 0 {
		t.Fatalf("entered a PIN on an unlocked phone: %d times", len(x.entered))
	}
}

// slowStreamDev opens its stream only when told.
type slowStreamDev struct {
	*demo.Device
	release chan struct{}
}

func (s *slowStreamDev) Stream(ctx context.Context, _ driver.StreamOptions) (driver.Stream, error) {
	select {
	case <-s.release:
		return nil, errors.New("no video")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// A viewer that gives up before the stream opens leaves nothing running.
func TestStreamGivenUpLeavesNothing(t *testing.T) {
	dev := &slowStreamDev{Device: demo.NewDevice("pixel", "Pixel 9", driver.Android), release: make(chan struct{})}
	g, err := New(Options{Providers: []driver.Provider{extraProvider2{demo.NewProvider(driver.Android, dev.Device), dev}},
		TrustNewAgents: true, NoScreenshots: true, DiscoverEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		if _, err := g.Stream(ctx, "pixel", driver.StreamOptions{}); err == nil {
			t.Fatal("a stream that never opened")
		}
		cancel()
	}
	close(dev.release)
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+2 {
		t.Fatalf("%d goroutines left behind (%d before)", n-before, before)
	}
}

// defaultBgDev is a phone whose driver wants background work unless the
// person chooses otherwise, as Android 13 and later do.
type defaultBgDev struct{ *extraDev }

func (defaultBgDev) BackgroundByDefault() bool { return true }

type defaultBgProvider struct{ extraProvider }

func (p defaultBgProvider) Open(context.Context, string) (driver.Device, error) {
	return defaultBgDev{p.x}, nil
}

// TestBackgroundByDefault uses a background display on a phone whose
// driver says so, until the person turns it off, which is kept.
func TestBackgroundByDefault(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	dir := t.TempDir()
	opts := func(o *Options) {
		o.DataDir = dir
		o.Providers = []driver.Provider{defaultBgProvider{extraProvider{demo.NewProvider(driver.Android, x.Device), x}}}
	}
	g1 := newExtraGateway(t, x, opts)
	if !g1.Background("pixel") {
		t.Fatal("off on a phone whose driver wants it by default")
	}
	s, _ := connect(t, g1, "Lunch")
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	x.mu.Lock()
	opened := len(x.bgs)
	x.mu.Unlock()
	if opened == 0 {
		t.Fatal("the task did not use a background display")
	}
	s.Close()
	if err := g1.SetBackground("pixel", false); err != nil {
		t.Fatal(err)
	}
	if g1.Background("pixel") {
		t.Fatal("the person's choice did not win")
	}
	g1.Close()
	g2 := newExtraGateway(t, x, opts)
	if g2.Background("pixel") {
		t.Fatal("the person's choice was lost in a restart")
	}
}

// oldAndroidDev is a phone where a background display cannot be read, as on
// Android 10.
type oldAndroidDev struct{ *extraDev }

func (oldAndroidDev) BackgroundSupported() bool { return false }

type oldAndroidProvider struct{ extraProvider }

func (p oldAndroidProvider) Open(context.Context, string) (driver.Device, error) {
	return oldAndroidDev{p.x}, nil
}

// TestBackgroundUnsupported offers no background work on a phone whose
// driver says it cannot be used, and refuses it.
func TestBackgroundUnsupported(t *testing.T) {
	x := newExtraDev("pixel", "Pixel")
	g := newExtraGateway(t, x, func(o *Options) {
		o.Providers = []driver.Provider{oldAndroidProvider{extraProvider{demo.NewProvider(driver.Android, x.Device), x}}}
	})
	if f := g.Devices()[0].Features; f.Background {
		t.Errorf("features %+v offer background work", f)
	}
	if err := g.SetBackground("pixel", true); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("SetBackground on Android 10: %v", err)
	}
	if g.Background("pixel") {
		t.Error("background on")
	}
}

// A background display has no home screen, so an agent that works on the
// home screen itself asks for the phone's own screen (phone_connect
// own_screen); the person sees it in the task's steps.
func TestBackgroundOwnScreen(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	g.SetBackground("pixel", true)
	s := g.OpenSession(agent)
	defer s.Close()
	res := s.Connect(ctx, ConnectRequest{Task: "Tidy the home screen", OwnScreen: true})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "works on the phone's own screen") || strings.Contains(res.Message, "own_screen: true") {
		t.Fatalf("message %q", res.Message)
	}
	if n := len(x.backgrounds()); n != 0 {
		t.Fatalf("%d background displays opened", n)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	if x.Current() != "dinely" {
		t.Fatalf("phone %q", x.Current())
	}
	if d := g.Devices()[0]; strings.Contains(d.StatusLine, "in the background") {
		t.Fatalf("status line %q", d.StatusLine)
	}
	s.Release(ctx, ReleaseRequest{})
	g.Flush()
	tasks, err := g.History(ctx, HistoryQuery{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("history: %v %v", tasks, err)
	}
	if st := tasks[0].Steps[0]; st.Text != "Connected to Pixel 9 for “Tidy the home screen”, on the phone's own screen" ||
		!strings.Contains(string(st.Args), `"own_screen":true`) {
		t.Fatalf("connect step %q %s", st.Text, st.Args)
	}

	// Without background work, own_screen changes nothing.
	x2 := newExtraDev("pixel", "Pixel 9")
	g2 := newExtraGateway(t, x2)
	s2 := g2.OpenSession(agent)
	defer s2.Close()
	res = s2.Connect(ctx, ConnectRequest{Task: "Tidy the home screen", OwnScreen: true})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, "own screen") {
		t.Fatalf("message %q", res.Message)
	}
}

// An agent that found itself on a background display connects again for
// the same task with own_screen: the display closes and the task goes on
// on the phone's own screen. A person who used that screen in the last
// minute keeps it until they hand it over.
func TestBackgroundOwnScreenAgain(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	g.SetBackground("pixel", true)
	s, res := connect(t, g, "Tidy the home screen")
	if !strings.Contains(res.Message, "has no home screen") || !strings.Contains(res.Message, "own_screen: true") {
		t.Fatalf("connect message %q", res.Message)
	}
	bgs := x.backgrounds()
	if len(bgs) != 1 {
		t.Fatalf("%d background displays opened", len(bgs))
	}
	res = s.Connect(ctx, ConnectRequest{Task: "Tidy the home screen", OwnScreen: true})
	mustStatus(t, res, StatusSettled)
	deadline := time.Now().Add(2 * time.Second)
	for !bgs[0].isClosed() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !bgs[0].isClosed() {
		t.Fatal("the background display stayed open")
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	if x.Current() != "dinely" || bgs[0].Current() == "dinely" {
		t.Fatalf("phone %q, display %q", x.Current(), bgs[0].Current())
	}
	// Asking again changes nothing more.
	res = s.Connect(ctx, ConnectRequest{Task: "Tidy the home screen", OwnScreen: true})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, "As asked") || len(x.backgrounds()) != 1 {
		t.Fatalf("message %q, %d displays", res.Message, len(x.backgrounds()))
	}

	// The person is using the phone's own screen.
	x2 := newExtraDev("pixel", "Pixel 9")
	g2 := newExtraGateway(t, x2)
	g2.SetBackground("pixel", true)
	s2, _ := connect(t, g2, "Tidy the home screen")
	x2.Show("settings")
	if err := g2.PersonTouched("pixel"); err != nil {
		t.Fatal(err)
	}
	res = s2.Connect(ctx, ConnectRequest{Task: "Tidy the home screen", OwnScreen: true})
	mustStatus(t, res, StatusPaused)
	if !strings.Contains(res.Message, "phone_handoff") {
		t.Fatalf("message %q", res.Message)
	}
	if d := g2.Devices()[0]; d.Status != DeviceInControl {
		t.Fatalf("device %+v", d)
	}
	mustStatus(t, s2.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusPaused)
	if x2.Current() != "settings" {
		t.Fatalf("the agent took the person's screen: %q", x2.Current())
	}

	// The phone's own screen is locked.
	x3 := newExtraDev("pixel", "Pixel 9")
	g3 := newExtraGateway(t, x3)
	g3.SetBackground("pixel", true)
	x3.SetLocked(true)
	s3 := g3.OpenSession(agent)
	defer s3.Close()
	mustStatus(t, s3.Connect(ctx, ConnectRequest{Task: "Tidy the home screen", OwnScreen: true}), StatusLocked)
}
