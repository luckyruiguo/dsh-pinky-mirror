//go:build device

package android

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/screen"
)

// Background displays while the phone's own screen is off. On a phone
// where the display has a group of its own (Android 13 to 16) it runs by
// itself; where it shares the main display's power (Android 17) Pinky
// wakes the phone with its panel off (dark.go). Let the screen time out
// first: the tests skip while it is on, since someone may be using the
// phone.
//
//	go test -c -tags device -o /tmp/pinky-android.test ./internal/driver/android
//	scripts/devlock.sh 3e8be48e env PINKY_DEVICE=3e8be48e /tmp/pinky-android.test -test.run TestDeviceBackgroundScreenOff -test.v
//
// PINKY_BG_DUMP=1 logs the power and display state along the way.

// openOff opens the phone for a test that needs its screen off.
func openOff(t *testing.T, ctx context.Context) *Device {
	t.Helper()
	id := os.Getenv("PINKY_DEVICE")
	if id == "" {
		t.Skip("PINKY_DEVICE not set")
	}
	d, err := NewProvider(Config{Logf: t.Logf}).OpenDevice(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	p := phonePowerOf(t, ctx, d)
	if p.interactive {
		t.Skip("the phone's screen is on: someone may be using it; let it time out first")
	}
	if !p.locked {
		t.Skip("the phone is not locked: Pinky turns its screen on, dark, only on a locked phone")
	}
	return d
}

func phonePowerOf(t *testing.T, ctx context.Context, d *Device) phonePower {
	t.Helper()
	out, err := d.client.Shell(ctx, d.info.ID, powerCmd)
	if err != nil {
		t.Fatal(err)
	}
	return parsePhonePower(string(out))
}

// countFrames counts the stream's frames until it closes.
func countFrames(st driver.Stream) *atomic.Int64 {
	var n atomic.Int64
	go func() {
		for p := range st.Packets() {
			if !p.Config {
				n.Add(1)
			}
		}
	}()
	return &n
}

// scroll swipes Settings up and down and waits for its frames.
func scroll(ctx context.Context, b *Background, times int) error {
	for i := range times {
		from, to := screen.Point{X: 540, Y: 1700}, screen.Point{X: 540, Y: 700}
		if i%2 == 1 {
			from, to = to, from
		}
		if err := b.Swipe(ctx, from, to, 300*time.Millisecond); err != nil {
			return err
		}
		time.Sleep(700 * time.Millisecond)
	}
	return nil
}

var (
	sfPowerRE = regexp.MustCompile(`^\s*(\d+\.\d+) .*SurfaceFlinger: (Finished s|S)etting power mode (\d) on physical display`)
	unblockRE = regexp.MustCompile(`^\s*(\d+\.\d+) .*DisplayPowerController\[0\]: Unblocked screen on`)
)

// litWindows reads `logcat -v monotonic` for each time the panel was
// powered on: how long until Pinky's panel-off call began, and how much of
// that came after the lock screen was ready and after the power-on had
// finished, when the brightness is set. Only builds that log
// SurfaceFlinger's power modes have them.
func litWindows(log string) []string {
	var out []string
	on, ready, done := -1.0, -1.0, -1.0
	for _, line := range strings.Split(log, "\n") {
		if m := unblockRE.FindStringSubmatch(line); m != nil && on >= 0 {
			ready, _ = strconv.ParseFloat(m[1], 64)
		}
		m := sfPowerRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		at, _ := strconv.ParseFloat(m[1], 64)
		finished := m[2] == "Finished s"
		switch {
		case m[3] == "2" && !finished && on < 0:
			on, ready, done = at, -1, -1
		case m[3] == "2" && finished && on >= 0:
			done = at
		case m[3] != "2" && !finished && on >= 0:
			w := fmt.Sprintf("panel powered on %.0f ms before the panel-off call began", (at-on)*1000)
			if ready >= 0 {
				w += fmt.Sprintf(", %.0f ms after the lock screen was ready", (at-ready)*1000)
			}
			if done >= 0 {
				w += fmt.Sprintf(", %.0f ms after the power-on finished", (at-done)*1000)
			}
			out = append(out, w)
			on = -1
		}
	}
	return out
}

func TestDeviceBackgroundScreenOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	d := openOff(t, ctx)
	sh := func(cmd string) string {
		out, _ := d.client.Shell(ctx, d.info.ID, cmd)
		return strings.TrimSpace(string(out))
	}
	dump := func(what string) {
		if os.Getenv("PINKY_BG_DUMP") != "" {
			t.Logf("%s:\n%s", what, sh(`dumpsys power | grep -E '^  mWakefulness=|^  mLastSleepReason='; `+powerCmd))
		}
	}
	since := sh("date +%s")
	dump("before")
	start := time.Now()
	b, err := d.OpenDisplay(ctx, DisplayOptions{App: "com.android.settings"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	shared := b.shared
	t.Logf("display %d opened in %v; it shares the main display's power: %v", b.DisplayID(), time.Since(start).Round(time.Millisecond), shared)
	dump("open")
	p := phonePowerOf(t, ctx, d)
	if shared && (!p.interactive || p.panel != "Off" || !p.locked) {
		t.Errorf("after opening: %+v, want the phone awake and locked, with its panel off", p)
	}
	if !shared && p.interactive {
		t.Errorf("after opening: %+v, want the phone's own screen off", p)
	}
	on, err := d.ScreenOn(ctx)
	if err != nil || on {
		t.Errorf("ScreenOn: %v, %v; want off", on, err)
	}
	st, err := b.Stream(ctx, driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	frames := countFrames(st)
	check := func(what string) {
		t.Helper()
		before := frames.Load()
		if err := scroll(ctx, b, 4); err != nil {
			t.Errorf("%s: swipe: %v", what, err)
		}
		s, err := b.Observe(ctx, driver.ObserveOptions{})
		n := 0
		if s != nil {
			n = len(s.Elements)
		}
		got := frames.Load() - before
		t.Logf("%s: %d frames for 4 swipes, %d elements read (%v)", what, got, n, err)
		if got < 10 || err != nil || n == 0 {
			t.Errorf("%s: the display did not run", what)
		}
	}
	check("at once")
	// Longer than the lock screen's 10 s timeout, with no action.
	time.Sleep(25 * time.Second)
	dump("after 25 s idle")
	check("after 25 s idle")
	if p := phonePowerOf(t, ctx, d); shared && p.panel != "Off" {
		t.Errorf("the panel is %s, want Off", p.panel)
	}
	st.Close()
	start = time.Now()
	b.Close()
	t.Logf("closed in %v", time.Since(start).Round(100*time.Millisecond))
	// The phone times out by itself: the lock screen's 10 s since the last
	// keep-awake key.
	var after phonePower
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if after = phonePowerOf(t, ctx, d); !after.interactive {
			break
		}
	}
	t.Logf("asleep %v after closing", time.Since(start).Round(100*time.Millisecond))
	time.Sleep(time.Second)
	dump("closed")
	if p := phonePowerOf(t, ctx, d); p.interactive || (p.panel != "Off" && !strings.HasPrefix(p.panel, "Doze")) {
		t.Errorf("after closing: %+v, want the phone asleep as before", p)
	}
	d.Close() // ends the panel control's session, which must change nothing
	time.Sleep(2 * time.Second)
	if p := phonePowerOf(t, ctx, d); p.interactive || p.panel == "On" {
		t.Errorf("after the device closed: %+v, want the phone asleep as before", p)
	}
	log := sh("logcat -d -v monotonic -T " + since + ".000")
	for _, w := range litWindows(log) {
		t.Log(w)
	}
	if strings.Contains(log, "Restoring display power") {
		t.Error("the scrcpy server turned the panel back on when it exited")
	}
}

// TestDeviceBackgroundPerson acts as the person on a shared phone: a touch
// on the dark phone gives them the screen, and after they press the power
// key Pinky waits for them. It presses the power key and touches the
// screen (through the emulator console), so it runs on emulators only.
func TestDeviceBackgroundPerson(t *testing.T) {
	if !strings.HasPrefix(os.Getenv("PINKY_DEVICE"), "emulator-") {
		t.Skip("set PINKY_DEVICE to an emulator: the test presses the power key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	d := openOff(t, ctx)
	adbPath, err := adb.FindADB()
	if err != nil {
		t.Fatal(err)
	}
	console := func(events ...string) {
		t.Helper()
		args := append([]string{"-s", d.info.ID, "emu", "event", "send"}, events...)
		out, err := exec.CommandContext(ctx, adbPath, args...).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "OK") {
			t.Fatalf("emulator console: %v: %s", err, out)
		}
	}
	// The console's keys do not reach the emulator's gpio-keys device; an
	// injected power key takes the same path in the window manager.
	power := func() {
		t.Helper()
		if _, err := d.client.Shell(ctx, d.info.ID, "input keyevent KEYCODE_POWER"); err != nil {
			t.Fatal(err)
		}
	}
	touch := func() {
		console("EV_ABS:ABS_MT_SLOT:0", "EV_ABS:ABS_MT_TRACKING_ID:9", "EV_ABS:ABS_MT_TOOL_TYPE:0",
			"EV_ABS:ABS_MT_POSITION_X:16000", "EV_ABS:ABS_MT_POSITION_Y:4000", "EV_ABS:ABS_MT_PRESSURE:200", "EV_SYN:0:0")
		console("EV_ABS:ABS_MT_TRACKING_ID:-1", "EV_SYN:0:0")
	}
	wait := func(what string, max time.Duration, ok func(phonePower) bool) phonePower {
		t.Helper()
		deadline := time.Now().Add(max)
		for {
			p := phonePowerOf(t, ctx, d)
			if ok(p) {
				return p
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: still %+v after %v", what, p, max)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	d.PersonInput() // watch the touchscreen, as the gateway does
	time.Sleep(2 * time.Second)
	b, err := d.OpenDisplay(ctx, DisplayOptions{App: "com.android.settings"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !b.shared {
		t.Skip("the display has a group of its own on this phone")
	}
	dk := d.darkOf()
	if !dk.on.Load() {
		t.Fatal("the phone is not dark after opening")
	}

	// A touch on the dark phone turns its panel on.
	touch()
	p := wait("after a touch", 5*time.Second, func(p phonePower) bool { return p.panel == "On" })
	t.Logf("a touch gave the person the screen: %+v", p)
	if dk.on.Load() {
		t.Error("still dark after the touch")
	}
	// The person's screen times out (the lock screen's 10 s); Pinky wakes
	// it dark again, since that was a timeout.
	p = wait("after the timeout", 40*time.Second, func(p phonePower) bool { return p.interactive && p.panel == "Off" && dk.on.Load() })
	t.Logf("dark again after the timeout: %+v", p)
	time.Sleep(2 * time.Second) // as a person would, not while it wakes up

	// The power key puts the dark phone to sleep, and Pinky waits for the
	// person.
	power()
	wait("after the power key", 5*time.Second, func(p phonePower) bool { return !p.interactive })
	time.Sleep(2 * darkPoll)
	if p := phonePowerOf(t, ctx, d); p.interactive {
		t.Errorf("Pinky woke the phone after the person turned it off: %+v", p)
	}
	_, err = b.Observe(ctx, driver.ObserveOptions{})
	if !errors.Is(err, driver.ErrLocked) {
		t.Errorf("Observe after the power key: %v, want driver.ErrLocked", err)
	} else {
		t.Logf("Observe after the power key: %v", err)
	}
	// They turn it on again: the display runs, and Pinky leaves the panel
	// to them.
	power()
	wait("after the second power key", 5*time.Second, func(p phonePower) bool { return p.interactive && p.panel == "On" })
	if err := scroll(ctx, b, 2); err != nil {
		t.Errorf("swipe while the person's screen is on: %v", err)
	}
	if s, err := b.Observe(ctx, driver.ObserveOptions{}); err != nil || len(s.Elements) == 0 {
		t.Errorf("Observe while the person's screen is on: %v", err)
	}
	if dk.on.Load() {
		t.Error("dark while the person's screen is on")
	}
	// After the last display closes, the dark phone times out by itself.
	wait("dark again", 40*time.Second, func(p phonePower) bool { return p.interactive && p.panel == "Off" && dk.on.Load() })
	b.Close()
	start := time.Now()
	wait("after closing", 20*time.Second, func(p phonePower) bool { return !p.interactive })
	t.Logf("asleep %v after closing", time.Since(start).Round(100*time.Millisecond))
}
