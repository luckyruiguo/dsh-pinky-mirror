//go:build device

package gateway

// Emulator tests for round three: the shared stream and the recording on
// a real Android stream (scrcpy), on our emulators only, never on a phone:
// they block Settings for the run and drive it with adb to make it move.
//
//	go test -c -tags device -o /tmp/pinky-gw/gateway.test ./internal/gateway
//	scripts/devlock.sh emulator-5560 env PINKY_EMULATOR=emulator-5560 /tmp/pinky-gw/gateway.test -test.run TestEmulator -test.v

import (
	"context"
	"errors"
	"image"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

func emulatorID(t *testing.T) string {
	id := os.Getenv("PINKY_EMULATOR")
	if !strings.HasPrefix(id, "emulator-") {
		t.Skip("set PINKY_EMULATOR to an emulator's serial (emulator-5560) and run through scripts/devlock.sh")
	}
	return id
}

func adbShell(t *testing.T, id string, args ...string) string {
	t.Helper()
	out, err := exec.Command("adb", append([]string{"-s", id, "shell"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("adb shell %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// frameTimes lists a recording's frame times with ffprobe.
func frameTimes(t *testing.T, path string) []time.Duration {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "frame=pts_time",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var ts []time.Duration
	for _, l := range strings.Fields(string(out)) {
		f, err := strconv.ParseFloat(strings.TrimSuffix(l, ","), 64)
		if err == nil {
			ts = append(ts, time.Duration(f*float64(time.Second)))
		}
	}
	return ts
}

func TestEmulatorRecording(t *testing.T) {
	id := emulatorID(t)
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("needs ffprobe")
	}
	ctx := context.Background()
	data, err := os.MkdirTemp("/tmp", "pinky-gw-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(data)
	pol := policy.Default()
	// Settings stands in for a banking app: blocked for this run only. Its
	// home page may read as its search component's package.
	for _, id := range []string{"com.android.settings", "com.google.android.settings.intelligence"} {
		if err := pol.SetAppBlocked(screen.App{ID: id}, true); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// Only this emulator: the gateway opens every phone a provider lists.
	prov := &oneDevice{Provider: android.NewProvider(android.Config{}), id: id}
	g, err := New(Options{Providers: []driver.Provider{prov}, DataDir: data,
		TrustNewAgents: true, Policy: pol, Logger: log, NoScreenshots: true, PIN: testPIN})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for deadline := time.Now().Add(60 * time.Second); !deviceOnline(g, id); time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not come online", id)
		}
	}
	adbShell(t, id, "input", "keyevent", "KEYCODE_WAKEUP")
	adbShell(t, id, "input", "keyevent", "KEYCODE_HOME")
	// The main screen: Android 13+ runs tasks on a background display by
	// default, and the blocked app below opens on the main screen.
	if err := g.SetBackground(id, false); err != nil {
		t.Fatal(err)
	}
	unlockForTest(t, g, id)
	g.SetRecording(true)

	// The mirror watches the whole time; the recorder shares its stream.
	mirror, err := g.Stream(ctx, id, driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var mirrored, keys atomic.Int64
	streamStart := time.Now()
	go func() {
		for p := range mirror.Packets() {
			mirrored.Add(1)
			if p.KeyFrame {
				keys.Add(1)
				t.Logf("key frame at %.1f s", time.Since(streamStart).Seconds())
			}
		}
	}()
	s := g.OpenSession(AgentInfo{Name: "Pinky emulator test"})
	defer s.Close()
	res := s.Connect(ctx, ConnectRequest{Call: Call{Device: id}, Task: "Emulator recording test"})
	if res.IsError() || res.Status != StatusSettled {
		t.Fatalf("connect: %+v", res)
	}
	task := taskID(t, g, s)
	act := func(what string, a Action) *Result {
		t.Helper()
		time.Sleep(400 * time.Millisecond)
		res := s.Act(ctx, Call{Device: id}, a)
		t.Logf("%-24s %s%s %s", what, res.Status, res.Error, res.Message)
		if res.IsError() {
			t.Fatalf("%s: %+v", what, res)
		}
		return res
	}
	// Clean part: the agent moves between Clock's tabs, which redraws.
	act("launch Clock", Action{Tool: "launch", App: "Clock"})
	for _, tab := range []string{"Timer", "Stopwatch", "Clock", "Alarm", "Timer"} {
		res := s.Observe(ctx, ObserveRequest{Call: Call{Device: id}})
		if e := findLabel(res, tab); e != nil {
			act("tap "+tab, Action{Tool: "tap", Ref: e.Ref})
		}
	}
	// A blocked app comes up by itself (a payment redirect) and moves.
	blockedFrom := time.Now()
	adbShell(t, id, "am", "start", "-W", "-n", "com.android.settings/.Settings")
	time.Sleep(300 * time.Millisecond)
	for i := 0; i < 10; i++ {
		res = s.Observe(ctx, ObserveRequest{Call: Call{Device: id}})
		if res.Rule != "" {
			break
		}
		time.Sleep(200 * time.Millisecond) // still opening
	}
	if res.Rule == "" {
		t.Fatalf("Settings is not reported blocked: %+v", res.Screen.App)
	}
	for i := 0; i < 6; i++ {
		adbShell(t, id, "input", "swipe", "540", "1800", "540", "600", "300")
		adbShell(t, id, "input", "swipe", "540", "600", "540", "1800", "300")
	}
	blockedTo := time.Now()
	// Home, and the clean part again, long enough for the next key frame.
	act("home", Action{Tool: "key", Key: "home"})
	act("launch Clock", Action{Tool: "launch", App: "Clock"})
	for end := time.Now().Add(14 * time.Second); time.Now().Before(end); {
		for _, tab := range []string{"Stopwatch", "Clock", "Alarm", "Timer"} {
			res := s.Observe(ctx, ObserveRequest{Call: Call{Device: id}})
			if e := findLabel(res, tab); e != nil {
				act("tap "+tab, Action{Tool: "tap", Ref: e.Ref})
			}
		}
	}
	afterFrom := time.Now()
	act("home", Action{Tool: "key", Key: "home"})
	if res := s.Release(ctx, ReleaseRequest{}); res.Status != StatusSettled {
		t.Fatalf("release: %+v", res)
	}
	g.Flush()
	mirror.Close()

	t.Logf("the mirror got %d packets, %d key frames", mirrored.Load(), keys.Load())
	rec, err := g.Recording(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	times := frameTimes(t, rec.Path)
	t.Logf("recording %s: %d frames, %v, %d marks; the mirror got %d packets", rec.Path, len(times), rec.Length, len(rec.Marks), mirrored.Load())
	var before, during, after int
	for _, ft := range times {
		at := rec.Start.Add(ft)
		switch {
		case at.Before(blockedFrom):
			before++
		case at.Before(blockedTo):
			during++
		case at.After(afterFrom.Add(-10 * time.Second)):
			after++
		}
	}
	t.Logf("frames before the blocked app %d, while it was up %d, at the end %d", before, during, after)
	if during != 0 {
		t.Fatalf("%d frames from while Settings (blocked) was in front", during)
	}
	if before == 0 || after == 0 {
		t.Fatalf("frames before %d, after %d: want some on both sides", before, after)
	}
	if mirrored.Load() == 0 {
		t.Fatal("the mirror got nothing while the recorder shared its stream")
	}
}

// oneDevice lists and opens one device of a provider.
type oneDevice struct {
	driver.Provider
	id string
}

func (p *oneDevice) List(ctx context.Context) ([]driver.Info, error) {
	infos, err := p.Provider.List(ctx)
	var out []driver.Info
	for _, info := range infos {
		if info.ID == p.id {
			out = append(out, info)
		}
	}
	return out, err
}

func (p *oneDevice) Open(ctx context.Context, id string) (driver.Device, error) {
	if id != p.id {
		return nil, driver.ErrNotFound
	}
	return p.Provider.Open(ctx, id)
}

func findLabel(res *Result, label string) *screen.Element {
	if res.Screen == nil {
		return nil
	}
	for i := range res.Screen.Elements {
		e := &res.Screen.Elements[i]
		if strings.EqualFold(e.Label, label) && e.Ref > 0 {
			return e
		}
	}
	return nil
}

// The agent works on a background display while the emulator's own
// screen is off; its stream and recording come from that display.
func TestEmulatorBackground(t *testing.T) {
	id := emulatorID(t)
	ctx := context.Background()
	data, err := os.MkdirTemp("/tmp", "pinky-gw-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(data)
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	g, err := New(Options{Providers: []driver.Provider{&oneDevice{Provider: android.NewProvider(android.Config{}), id: id}},
		DataDir: data, TrustNewAgents: true, Logger: log, NoScreenshots: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for deadline := time.Now().Add(60 * time.Second); !deviceOnline(g, id); time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not come online", id)
		}
	}
	adbShell(t, id, "input", "keyevent", "KEYCODE_WAKEUP")
	adbShell(t, id, "input", "keyevent", "KEYCODE_HOME")
	defer func() {
		adbShell(t, id, "input", "keyevent", "KEYCODE_WAKEUP")
		adbShell(t, id, "wm", "dismiss-keyguard")
		adbShell(t, id, "input", "keyevent", "KEYCODE_HOME")
	}()
	if err := g.SetBackground(id, true); err != nil {
		t.Fatal(err)
	}
	g.SetRecording(true)
	s := g.OpenSession(AgentInfo{Name: "Pinky emulator test"})
	defer s.Close()
	start := time.Now()
	res := s.Connect(ctx, ConnectRequest{Call: Call{Device: id}, Task: "Background display test"})
	t.Logf("connect %s%s in %.1f s: %s · %s", res.Status, res.Error, time.Since(start).Seconds(), screenOf(res).App.ID, res.Message)
	if res.IsError() || res.Status != StatusSettled {
		t.Fatalf("connect: %+v", res)
	}
	task := taskID(t, g, s)
	if d := g.Devices()[0]; !strings.Contains(d.StatusLine, "in the background") {
		// The gateway moved the task to the phone's screen and said why;
		// the driver and companion decide whether a display can be read.
		if strings.Contains(res.Message, "background display") && res.Status == StatusSettled {
			t.Skipf("fell back to the phone's screen, as it should when the display cannot be used: %s", res.Message)
		}
		t.Fatalf("not on a background display: %q · %s", d.StatusLine, res.Message)
	}
	agentView, err := g.AgentStream(ctx, id, driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var agentPackets atomic.Int64
	go func() {
		for range agentView.Packets() {
			agentPackets.Add(1)
		}
	}()
	// The phone's own screen goes off (and locks, if it has a lock).
	adbShell(t, id, "input", "keyevent", "KEYCODE_SLEEP")
	time.Sleep(time.Second)
	act := func(what string, a Action) *Result {
		t.Helper()
		time.Sleep(400 * time.Millisecond)
		res := s.Act(ctx, Call{Device: id}, a)
		t.Logf("%-24s %s%s %s · %s", what, res.Status, res.Error, screenOf(res).App.ID, res.Message)
		return res
	}
	res = act("launch Clock", Action{Tool: "launch", App: "Clock"})
	if res.Status != StatusSettled {
		t.Fatalf("launch on the background display with the screen off: %+v", res)
	}
	// An agent thinks between steps. While the phone is locked, Android
	// gives every display group the lock screen's 10 s timeout, and a
	// display whose group timed out sent empty reads: the display must
	// stay readable through pauses longer than that.
	for _, pause := range []time.Duration{8 * time.Second, 14 * time.Second} {
		time.Sleep(pause)
		obs := s.Observe(ctx, ObserveRequest{Call: Call{Device: id}})
		sc := screenOf(obs)
		t.Logf("read after %v idle: %s%s %s, %d elements", pause, obs.Status, obs.Error, sc.App.ID, len(sc.Elements))
		if obs.IsError() || sc.App.ID != "com.google.android.deskclock" || len(sc.Elements) == 0 {
			t.Fatalf("the background display was not readable after %v idle: %+v", pause, obs)
		}
	}
	for _, tab := range []string{"Timer", "Stopwatch", "Clock", "Alarm", "Timer", "Stopwatch"} {
		obs := s.Observe(ctx, ObserveRequest{Call: Call{Device: id}})
		if e := findLabel(obs, tab); e != nil {
			if r := act("tap "+tab, Action{Tool: "tap", Ref: e.Ref}); r.Status != StatusSettled {
				t.Fatalf("tap %s: %+v", tab, r)
			}
		}
	}
	// Screenshots come from the display's video (screencap cannot reach a
	// virtual display) and show it as it is now: with the stopwatch
	// running, two taken apart differ.
	if e := findLabel(s.Observe(ctx, ObserveRequest{Call: Call{Device: id}}), "Start"); e != nil {
		act("tap Start", Action{Tool: "tap", Ref: e.Ref})
	}
	shot := func() image.Image {
		start := time.Now()
		res := s.Observe(ctx, ObserveRequest{Call: Call{Device: id}, Screenshot: true})
		if res.Screenshot == nil {
			t.Fatalf("no screenshot of the background display: %s %s", res.Status, res.Message)
		}
		t.Logf("a screenshot of the background display in %v", time.Since(start).Round(time.Millisecond))
		return res.Screenshot
	}
	a := shot()
	time.Sleep(2 * time.Second)
	if b := shot(); sameImage(a, b) {
		t.Error("two screenshots of a running stopwatch 2 s apart are the same: the picture is stale")
	}
	if e := findLabel(s.Observe(ctx, ObserveRequest{Call: Call{Device: id}}), "Pause"); e != nil {
		act("tap Pause", Action{Tool: "tap", Ref: e.Ref})
	}
	// Display 0's own state: the phone's overall wakefulness stays awake
	// while the background display keeps its display group awake.
	if dev, err := android.NewProvider(android.Config{}).OpenDevice(ctx, id); err == nil {
		on, err := dev.ScreenOn(ctx)
		if err != nil || on {
			t.Errorf("the phone's own screen woke up: on %v, %v", on, err)
		}
		dev.Close()
	} else {
		t.Error(err)
	}
	if res := s.Release(ctx, ReleaseRequest{}); res.Status != StatusSettled {
		t.Fatalf("release: %+v", res)
	}
	g.Flush()
	agentView.Close()
	t.Logf("the agent's display sent %d packets", agentPackets.Load())
	if agentPackets.Load() == 0 {
		t.Error("no video from the background display")
	}
	rec, err := g.Recording(ctx, task)
	if err != nil {
		t.Fatalf("no recording of the background task: %v", err)
	}
	frames := frameTimes(t, rec.Path)
	t.Logf("recording: %d frames, %v", len(frames), rec.Length)
	if len(frames) == 0 {
		t.Error("the recording has no frames")
	}
}

// testPIN hands the gateway the emulator's PIN from PINKY_TEST_PIN, as
// the app hands it one from the keychain.
func testPIN(context.Context, string) (string, error) {
	if pin := os.Getenv("PINKY_TEST_PIN"); pin != "" {
		return pin, nil
	}
	return "", errors.New("PINKY_TEST_PIN is not set")
}

// unlockForTest unlocks the emulator through the gateway (driver.Unlocker)
// when its lock screen shows, or skips the test without a PIN.
func unlockForTest(t *testing.T, g *Gateway, id string) {
	t.Helper()
	if !strings.Contains(adbShell(t, id, "dumpsys", "window"), "mDreamingLockscreen=true") {
		return
	}
	if os.Getenv("PINKY_TEST_PIN") == "" {
		t.Skip("the emulator is locked: set PINKY_TEST_PIN to its PIN")
	}
	start := time.Now()
	if err := g.Unlock(id); err != nil {
		t.Fatalf("unlock through the gateway: %v", err)
	}
	t.Logf("unlocked through the gateway in %.1f s", time.Since(start).Seconds())
	adbShell(t, id, "input", "keyevent", "KEYCODE_HOME")
}

// sameImage reports whether two pictures have the same size and pixels.
func sameImage(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y += 4 {
		for x := r.Min.X; x < r.Max.X; x += 4 {
			if a.At(x, y) != b.At(x, y) {
				return false
			}
		}
	}
	return true
}

func firstLine(s, has string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, has) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// A task that found itself on a background display, which has no home
// screen, connects again with own_screen and gets the phone's own screen
// with its launcher.
func TestEmulatorOwnScreen(t *testing.T) {
	id := emulatorID(t)
	ctx := context.Background()
	data, err := os.MkdirTemp("/tmp", "pinky-gw-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(data)
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	g, err := New(Options{Providers: []driver.Provider{&oneDevice{Provider: android.NewProvider(android.Config{}), id: id}},
		DataDir: data, TrustNewAgents: true, Logger: log, NoScreenshots: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for deadline := time.Now().Add(60 * time.Second); !deviceOnline(g, id); time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not come online", id)
		}
	}
	adbShell(t, id, "input", "keyevent", "KEYCODE_WAKEUP")
	adbShell(t, id, "wm", "dismiss-keyguard")
	adbShell(t, id, "input", "keyevent", "KEYCODE_HOME")
	if err := g.SetBackground(id, true); err != nil {
		t.Fatal(err)
	}
	s := g.OpenSession(AgentInfo{Name: "Pinky emulator test"})
	defer s.Close()
	const task = "Home screen test"
	res := s.Connect(ctx, ConnectRequest{Call: Call{Device: id}, Task: task})
	t.Logf("connect %s%s: %s · %s", res.Status, res.Error, screenOf(res).App.ID, res.Message)
	if res.IsError() || res.Status != StatusSettled {
		t.Fatalf("connect: %+v", res)
	}
	if d := g.Devices()[0]; !strings.Contains(d.StatusLine, "in the background") {
		t.Skipf("not on a background display: %q · %s", d.StatusLine, res.Message)
	}
	if !strings.Contains(res.Message, "own_screen: true") {
		t.Fatalf("the connect message does not point at own_screen: %s", res.Message)
	}
	res = s.Connect(ctx, ConnectRequest{Call: Call{Device: id}, Task: task, OwnScreen: true})
	sc := screenOf(res)
	t.Logf("connect own_screen %s%s: %s · %s", res.Status, res.Error, sc.App.ID, res.Message)
	if res.IsError() || res.Status != StatusSettled {
		t.Fatalf("connect with own_screen: %+v", res)
	}
	if d := g.Devices()[0]; strings.Contains(d.StatusLine, "in the background") {
		t.Fatalf("still on the background display: %q", d.StatusLine)
	}
	if !strings.Contains(sc.App.ID, "launcher") {
		t.Fatalf("the launcher is not in front: %q\n%s", sc.App.ID, sc.Render())
	}
	t.Logf("home screen:\n%s", sc.Render())
}
