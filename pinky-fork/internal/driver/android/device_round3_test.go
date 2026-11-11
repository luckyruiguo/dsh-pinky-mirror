//go:build device

package android

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/screen"
)

// Tests for waking, unlocking, the Pinky folder, the companion installer
// and background displays on a locked phone. They lock the phone with the
// power key, set settings and install apps, so they run on emulators only,
// which are Pinky's own; PINKY_TEST_PIN is the emulator's PIN.

func openEmulator(t *testing.T) (*Device, context.Context, string) {
	t.Helper()
	serial := os.Getenv("PINKY_DEVICE")
	if !strings.HasPrefix(serial, "emulator-") {
		t.Skip("emulators only")
	}
	pin := os.Getenv("PINKY_TEST_PIN")
	if pin == "" {
		t.Skip("PINKY_TEST_PIN not set")
	}
	d, ctx := openTestDevice(t)
	return d, ctx, pin
}

// lockWithPower presses the power key, which Pinky itself never does.
func lockWithPower(t *testing.T, ctx context.Context, d *Device) {
	t.Helper()
	if _, err := d.client.Shell(ctx, d.info.ID, "input keyevent KEYCODE_POWER"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		time.Sleep(300 * time.Millisecond)
		on, err := d.ScreenOn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		locked, err := d.Locked(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !on && locked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the power key: screen on %v, locked %v", on, locked)
		}
	}
}

func stayOnSettings(t *testing.T, ctx context.Context, d *Device) string {
	t.Helper()
	out, err := d.client.Shell(ctx, d.info.ID, "settings get global stay_on_while_plugged_in; settings get system screen_off_timeout")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestDeviceWakeAndUnlock(t *testing.T) {
	d, ctx, pin := openEmulator(t)
	before := stayOnSettings(t, ctx, d)
	lockWithPower(t, ctx, d)
	wrong := "0000"
	if pin == wrong {
		wrong = "1111"
	}
	start := time.Now()
	err := d.Unlock(ctx, wrong)
	if !errors.Is(err, driver.ErrLocked) {
		t.Fatalf("a wrong PIN: %v, want ErrLocked", err)
	}
	t.Logf("wrong PIN: %v in %v", err, time.Since(start).Round(time.Millisecond))
	if locked, _ := d.Locked(ctx); !locked {
		t.Fatal("unlocked with a wrong PIN")
	}
	start = time.Now()
	if err := d.Unlock(ctx, pin); err != nil {
		t.Fatalf("the right PIN: %v", err)
	}
	t.Logf("unlocked in %v", time.Since(start).Round(time.Millisecond))
	if locked, err := d.Locked(ctx); err != nil || locked {
		t.Fatalf("after Unlock: locked %v, %v", locked, err)
	}
	if on, err := d.ScreenOn(ctx); err != nil || !on {
		t.Errorf("after Unlock: screen on %v, %v", on, err)
	}
	// Unlocking an unlocked phone does nothing.
	if err := d.Unlock(ctx, wrong); err != nil {
		t.Errorf("Unlock of an unlocked phone: %v", err)
	}
	// Wake turns the screen on and never unlocks.
	lockWithPower(t, ctx, d)
	start = time.Now()
	if err := d.Wake(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("woke in %v", time.Since(start).Round(time.Millisecond))
	if on, _ := d.ScreenOn(ctx); !on {
		t.Error("Wake left the screen off")
	}
	if locked, _ := d.Locked(ctx); !locked {
		t.Error("Wake unlocked the phone")
	}
	if err := d.Wake(ctx); err != nil {
		t.Errorf("Wake with the screen on: %v", err)
	}
	if err := d.Unlock(ctx, pin); err != nil {
		t.Fatal(err)
	}
	// Nothing Pinky did keeps the screen awake.
	if after := stayOnSettings(t, ctx, d); after != before {
		t.Errorf("stay-awake settings changed: %q -> %q", before, after)
	}
}

func TestDeviceFiles(t *testing.T) {
	d, ctx, _ := openEmulator(t)
	var b [4]byte
	rand.Read(b[:])
	dir := "pinky-test-" + hex.EncodeToString(b[:])
	defer d.client.Shell(context.Background(), d.info.ID, "rm -r "+remotePath(dir))
	data := bytes.Repeat([]byte("pinky\n"), 50_000)
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	name := dir + "/sub folder/\u7167\u7247.txt"
	if err := d.Push(ctx, name, bytes.NewReader(data), int64(len(data)), mtime); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *driver.FileInfo
	dirs := 0
	for i, f := range list {
		if f.Path == name {
			found = &list[i]
		}
		if strings.HasPrefix(f.Path, dir) && f.Dir {
			dirs++
		}
	}
	if found == nil || found.Size != int64(len(data)) || !found.ModTime.Equal(mtime) || found.Dir {
		t.Fatalf("listed %+v, want %d bytes from %v", found, len(data), mtime)
	}
	if dirs != 2 {
		t.Errorf("%d folders listed under %s, want 2", dirs, dir)
	}
	var got bytes.Buffer
	if err := d.Pull(ctx, name, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), data) {
		t.Errorf("pulled %d bytes, pushed %d", got.Len(), len(data))
	}
	for _, p := range []string{"../escape.txt", "/sdcard/Download/x", dir + "/../../x", dir, "missing.txt"} {
		if err := d.Pull(ctx, p, &got); !errors.Is(err, driver.ErrNotFound) {
			t.Errorf("Pull(%q) = %v, want ErrNotFound", p, err)
		}
	}
	if err := d.Push(ctx, "../escape.txt", strings.NewReader("x"), 1, time.Now()); !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("Push outside the folder = %v, want ErrNotFound", err)
	}
	if err := d.Push(ctx, dir+"/sub folder", strings.NewReader("x"), 1, time.Now()); !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("Push over a folder = %v, want ErrNotFound", err)
	}
}

func TestDeviceCompanionInstall(t *testing.T) {
	d, ctx, _ := openEmulator(t)
	if os.Getenv("PINKY_COMPANION_APK") == "" {
		t.Skip("PINKY_COMPANION_APK not set")
	}
	read := func() string {
		out, err := d.client.Shell(ctx, d.info.ID, "settings get secure enabled_accessibility_services")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	original := read()
	defer func() {
		v := "settings put secure enabled_accessibility_services " + shellArg(original)
		if original == "null" {
			v = "settings delete secure enabled_accessibility_services"
		}
		d.client.Shell(context.Background(), d.info.ID, v)
	}()
	// Another service the person has on: an entry for a component that
	// is not installed stays in the setting as written.
	other := "com.example.pinkytest/.OtherService"
	if _, err := d.client.Shell(ctx, d.info.ID, "settings put secure enabled_accessibility_services "+other); err != nil {
		t.Fatal(err)
	}
	// Android may drop a component that is not installed; whatever it
	// keeps must stay.
	time.Sleep(time.Second)
	kept := read()
	t.Logf("setting before install: %q", kept)
	st, err := d.CompanionState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("state before: %s", st)
	start := time.Now()
	res, err := d.SetUpCompanion(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("set up in %v: %+v", time.Since(start).Round(time.Millisecond), res)
	if st, err := d.CompanionState(ctx); err != nil || st != driver.CompanionOn {
		t.Errorf("state after: %s, %v", st, err)
	}
	got := read()
	if !strings.Contains(got, "app.pinky.companion/") {
		t.Errorf("setting after install: %q", got)
	}
	for _, e := range strings.Split(kept, ":") {
		if e != "" && e != "null" && !strings.Contains(got, e) {
			t.Errorf("the install dropped %q: %q", e, got)
		}
	}
	// The driver's reads leave a service the person turned on alone.
	observe(t, ctx, d)
	d.closeCompanion()
	if got := read(); !strings.Contains(got, "app.pinky.companion/") {
		t.Errorf("the driver turned the service off: %q", got)
	}
}

func shellArg(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// TestDeviceBackgroundLocked checks the question behind the owner's
// keep-awake worry: whether an app on a background display keeps working
// while the phone's own screen is off and locked.
func TestDeviceBackgroundLocked(t *testing.T) {
	d, ctx, pin := openEmulator(t)
	if d.props.SDK < 33 {
		t.Skip("background displays ignore the lock from Android 13")
	}
	defer d.Unlock(context.Background(), pin)
	// Start Settings afresh: an earlier search page would come back.
	if _, err := d.client.Shell(ctx, d.info.ID, "am force-stop com.android.settings; am force-stop com.google.android.settings.intelligence"); err != nil {
		t.Fatal(err)
	}
	act(t, ctx, d, "home", func() error { return d.Key(ctx, driver.KeyHome) })
	lockWithPower(t, ctx, d)
	start := time.Now()
	bg, err := d.OpenBackground(ctx, driver.BackgroundOptions{App: "com.android.settings"})
	if err != nil {
		t.Fatal(err)
	}
	defer bg.Close()
	t.Logf("background display open in %v", time.Since(start).Round(time.Millisecond))
	b := bg.(*Background)
	if locked, err := b.Locked(ctx); err != nil || locked {
		t.Errorf("background display locked: %v, %v", locked, err)
	}
	var s *screen.Screen
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if s, err = b.Observe(ctx, driver.ObserveOptions{}); err == nil && s.App.ID == "com.android.settings" {
			break
		}
	}
	if err != nil || s == nil || s.App.ID != "com.android.settings" || s.Locked {
		t.Fatalf("background observe: %v, %v", s, err)
	}
	search, ok := find(s, func(e screen.Element) bool { return strings.Contains(strings.ToLower(e.Label), "search") })
	if !ok {
		t.Fatalf("no search on the background display:\n%s", s.Render())
	}
	if err := b.Tap(ctx, search.Bounds.Center(), driver.TapOptions{}); err != nil {
		t.Fatal(err)
	}
	b.WaitSettled(ctx, 0)
	time.Sleep(300 * time.Millisecond)
	if err := b.Type(ctx, "pinky", driver.TypeOptions{}); err != nil {
		t.Fatal(err)
	}
	b.WaitSettled(ctx, 0)
	s, err = b.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := find(s, func(e screen.Element) bool { return e.Role == "input" && e.Label == "pinky" }); !ok {
		t.Errorf("the text did not arrive on the background display:\n%s", s.Render())
	}
	vs, err := b.VideoStats(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs.Packets) == 0 {
		t.Error("no video from the background display")
	}
	on, _ := d.ScreenOn(ctx)
	locked, _ := d.Locked(ctx)
	t.Logf("phone: screen on %v, locked %v; background display: %d packets", on, locked, len(vs.Packets))
	if on || !locked {
		t.Errorf("the phone's own screen: on %v, locked %v; want off and locked", on, locked)
	}
}

// TestDevicePersonTouchOnEmulator sends a touch through the emulator's
// virtual touchscreen, as a finger would arrive, and expects a signal;
// Pinky's own taps meanwhile must give none.
func TestDevicePersonTouchOnEmulator(t *testing.T) {
	d, ctx, _ := openEmulator(t)
	adbPath, err := adb.FindADB()
	if err != nil {
		t.Skip(err)
	}
	signals := d.PersonInput()
	time.Sleep(2 * time.Second) // let getevent start
	drain := func(wait time.Duration) int {
		n := 0
		deadline := time.After(wait)
		for {
			select {
			case _, ok := <-signals:
				if !ok {
					return n
				}
				n++
			case <-deadline:
				return n
			}
		}
	}
	mid := screen.Point{X: 540, Y: 900}
	for i := 0; i < 3; i++ {
		if err := d.Tap(ctx, mid, driver.TapOptions{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if n := drain(time.Second); n != 0 {
		t.Errorf("%d signals for Pinky's own taps", n)
	}
	// A finger, not `event mouse`: that one arrives as a stylus, and Gboard
	// then covers the next text field with its stylus tutorial.
	for _, events := range [][]string{
		{"EV_ABS:ABS_MT_SLOT:0", "EV_ABS:ABS_MT_TRACKING_ID:7", "EV_ABS:ABS_MT_TOOL_TYPE:0",
			"EV_ABS:ABS_MT_POSITION_X:16000", "EV_ABS:ABS_MT_POSITION_Y:4000", "EV_ABS:ABS_MT_PRESSURE:200", "EV_SYN:0:0"},
		{"EV_ABS:ABS_MT_TRACKING_ID:-1", "EV_SYN:0:0"},
	} {
		args := append([]string{"-s", d.info.ID, "emu", "event", "send"}, events...)
		out, err := exec.CommandContext(ctx, adbPath, args...).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "OK") {
			t.Fatalf("emulator console: %v: %s", err, out)
		}
	}
	if n := drain(3 * time.Second); n < 1 {
		t.Error("no signal for a touch on the emulator's touchscreen")
	} else {
		t.Logf("%d signal(s) for one touch", n)
	}
}
