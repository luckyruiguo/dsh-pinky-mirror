//go:build device

package android

// Reads the logs and crash reports of Crash Test (testdata/crashtest), a
// tiny app made for this test: it crashes and hangs on a button. Use an
// emulator; install the app first:
//
//	internal/driver/android/testdata/crashtest/build.sh true /tmp/crash-debug.apk
//	adb -s <id> install -r /tmp/crash-debug.apk
//	scripts/devlock.sh <id> env PINKY_DEVICE=<id> go test -tags device -run TestDeviceAppLogs -v ./internal/driver/android/
//
// With a release build (build.sh false) installed, set
// PINKY_CRASHTEST_RELEASE=1: it is then not a development build.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

const crashTestPackage = "dev.pinky.crashtest"

func crashTestApp(t *testing.T, ctx context.Context, d *Device) driver.App {
	t.Helper()
	apps, err := d.Apps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range apps {
		if a.ID == crashTestPackage {
			return a
		}
	}
	t.Skip("Crash Test is not installed: see testdata/crashtest/build.sh")
	return driver.App{}
}

// crashTestButton finds one of Crash Test's buttons on the screen, waiting
// a while for the app to show.
func crashTestButton(t *testing.T, ctx context.Context, d *Device, label string) screen.Element {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		s := observe(t, ctx, d)
		e, ok := find(s, func(e screen.Element) bool {
			return e.Role == screen.RoleButton && strings.EqualFold(e.Label, label)
		})
		if ok {
			return e
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %q button:\n%s", label, s.Render())
		}
		time.Sleep(time.Second)
	}
}

func startCrashTest(t *testing.T, ctx context.Context, d *Device) {
	t.Helper()
	if _, err := d.client.Shell(ctx, d.info.ID, "am force-stop "+crashTestPackage); err != nil {
		t.Fatal(err)
	}
	act(t, ctx, d, "launch Crash Test", func() error { return d.Launch(ctx, crashTestPackage) })
}

// waitCrash reads the app's crash reports until one of the kind is there.
func waitCrash(t *testing.T, ctx context.Context, d *Device, app driver.App, since time.Time, kind string) driver.CrashReport {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for {
		reports, err := d.AppCrashes(ctx, driver.LogQuery{App: app, Since: since, Max: 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range reports {
			if r.Kind == kind {
				return r
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s report since %s; reports: %+v", kind, since.Format(time.TimeOnly), reports)
		}
		time.Sleep(time.Second)
	}
}

func TestDeviceAppLogs(t *testing.T) {
	d, _ := openTestDevice(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	app := crashTestApp(t, ctx, d)
	t.Cleanup(func() { d.client.Shell(context.Background(), d.info.ID, "am force-stop "+crashTestPackage) })

	dev, err := d.DevBuild(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	if want := os.Getenv("PINKY_CRASHTEST_RELEASE") == ""; dev != want {
		t.Fatalf("development build: %v, want %v", dev, want)
	}

	// Its own lines, since a time, with a filter and a level.
	since := time.Now().Add(-time.Second)
	startCrashTest(t, ctx, d)
	logButton := crashTestButton(t, ctx, d, "Log")
	act(t, ctx, d, "tap Log", func() error { return d.Tap(ctx, logButton.Bounds.Center(), driver.TapOptions{}) })
	res, err := d.AppLogs(ctx, driver.LogQuery{App: app, Since: since, Max: 500})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d lines, %d dropped, since %s", len(res.Lines), res.Dropped, since.Format(time.TimeOnly))
	var created, tapped bool
	pid := 0
	for _, l := range res.Lines {
		if l.Time.Before(since) {
			t.Errorf("line from before since: %+v", l)
		}
		if l.Tag == "CrashTest" {
			created = created || l.Text == "Main created"
			tapped = tapped || l.Text == "Log button tapped" && l.Level == driver.LevelWarn
			pid = l.PID
		}
	}
	if !created || !tapped {
		t.Fatalf("Crash Test's lines missing (created %v, tapped %v): %+v", created, tapped, res.Lines)
	}
	for _, l := range res.Lines {
		if l.PID != pid {
			t.Errorf("a line of another process (%d, the app is %d): %s: %s", l.PID, pid, l.Tag, l.Text)
		}
	}
	res, err = d.AppLogs(ctx, driver.LogQuery{App: app, Since: since, Level: driver.LevelWarn, Filter: "button", Max: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 1 || res.Lines[0].Text != "Log button tapped" {
		t.Fatalf("warnings with \"button\": %+v", res.Lines)
	}

	// A crash: its lines and its report.
	crashButton := crashTestButton(t, ctx, d, "Crash")
	act(t, ctx, d, "tap Crash", func() error { return d.Tap(ctx, crashButton.Bounds.Center(), driver.TapOptions{}) })
	crash := waitCrash(t, ctx, d, app, since, "crash")
	t.Logf("crash: %s pid %d %s: %s (%d bytes)", crash.Time.Format(time.TimeOnly), crash.PID, crash.Process, crash.Summary, len(crash.Text))
	if crash.PID != pid || crash.Process != crashTestPackage || crash.Summary != "java.lang.IllegalStateException: Crash button tapped" ||
		!strings.Contains(crash.Text, "dev.pinky.crashtest.Main") {
		t.Fatalf("crash report %+v", crash)
	}
	res, err = d.AppLogs(ctx, driver.LogQuery{App: app, Since: since, Filter: "Crash button tapped", Max: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) == 0 || res.Lines[0].Tag != "AndroidRuntime" {
		t.Fatalf("the crash's lines: %+v", res.Lines)
	}

	// An ANR: the main thread sleeps while a key waits. (A tap would not
	// do on Android 10, which lets touches queue up behind a busy window.)
	since = time.Now().Add(-time.Second)
	startCrashTest(t, ctx, d)
	hang := crashTestButton(t, ctx, d, "Hang")
	if err := d.Tap(ctx, hang.Bounds.Center(), driver.TapOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := d.Key(ctx, driver.KeyBack); err != nil {
		t.Fatal(err)
	}
	anr := waitCrash(t, ctx, d, app, since, "ANR")
	t.Logf("ANR: %s pid %d %s: %s (%d bytes)", anr.Time.Format(time.TimeOnly), anr.PID, anr.Process, anr.Summary, len(anr.Text))
	if anr.Process != crashTestPackage || !strings.Contains(anr.Text, "Main.lambda") || !strings.Contains(anr.Text, `"main"`) {
		t.Fatalf("ANR report %+v", anr)
	}
	for _, other := range []string{"system_server", "surfaceflinger"} {
		if strings.Contains(anr.Text, other) {
			t.Errorf("the ANR report has %q", other)
		}
	}

	// Pinky's own app is never read.
	if _, err := d.AppLogs(ctx, driver.LogQuery{App: driver.App{ID: "app.pinky.companion"}}); err == nil {
		t.Fatal("read Pinky Companion's logs")
	}
}
