//go:build device

package android

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

// GitHub issue #30: an app launched cold on a background display read as
// an empty screen (no app, no elements) until the first key on the display.
// This launches apps cold on a display the way a task does (the display
// opened empty, then Launch) and reads it every 250 ms for 7 s, logging the
// companion's windows for the display and the activity's state.
//
//	go test -c -tags device -o /tmp/pinky-and/android.test ./internal/driver/android
//	scripts/devlock.sh emulator-5560 env PINKY_DEVICE=emulator-5560 PINKY_TEST_PIN=1234 /tmp/pinky-and/android.test -test.run TestEmulatorBackgroundColdLaunch -test.v
func TestEmulatorBackgroundColdLaunch(t *testing.T) {
	d, ctx, _ := openEmulator(t)
	locked := os.Getenv("PINKY_LOCKED") != ""
	if locked {
		lockWithPower(t, ctx, d)
	}
	// What the person does on the phone's own screen meanwhile.
	if main := os.Getenv("PINKY_MAIN_APP"); main != "" && !locked {
		if _, err := d.client.Shell(ctx, d.info.ID, "am start -W -n "+main); err != nil {
			t.Fatal(err)
		}
	}
	pkgs := []string{"com.google.android.deskclock", "com.android.settings"}
	if p := os.Getenv("PINKY_APPS"); p != "" {
		pkgs = strings.Split(p, ",")
	}
	idle, _ := time.ParseDuration(os.Getenv("PINKY_IDLE"))
	for _, pkg := range pkgs {
		t.Run(pkg, func(t *testing.T) {
			if _, err := d.client.Shell(ctx, d.info.ID, "am force-stop "+pkg); err != nil {
				t.Fatal(err)
			}
			b, err := d.OpenDisplay(ctx, DisplayOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if idle > 0 {
				// The agent reads the empty display and thinks a while.
				if _, _, err := b.observeTree(ctx); err != nil {
					t.Fatal(err)
				}
				time.Sleep(idle)
			}
			start := time.Now()
			if err := b.Launch(ctx, pkg); err != nil {
				t.Fatal(err)
			}
			t.Logf("launched in %v on display %d", time.Since(start).Round(time.Millisecond), b.display)
			readable := time.Duration(-1)
			for i := 0; i < 28; i++ {
				s, tr, err := b.observeTree(ctx)
				if err != nil {
					t.Logf("%5v read: %v", time.Since(start).Round(10*time.Millisecond), err)
					if i == 3 {
						t.Logf("state:\n%s", displayState(ctx, d, b.display, pkg))
					}
					time.Sleep(250 * time.Millisecond)
					continue
				}
				var wins []string
				if tr != nil {
					for _, w := range tr.Windows {
						wins = append(wins, fmt.Sprintf("%s:%s root=%v nodes=%d focused=%v active=%v", w.Type, w.Package, w.Root != nil, w.Nodes, w.Focused, w.Active))
					}
				}
				t.Logf("%5v app=%q elements=%d windows=[%s]", time.Since(start).Round(10*time.Millisecond), s.App.ID, len(s.Elements), strings.Join(wins, "; "))
				if s.App.ID != "" && len(s.Elements) > 0 && readable < 0 {
					readable = time.Since(start)
					break
				}
				if i == 3 {
					t.Logf("state:\n%s", displayState(ctx, d, b.display, pkg))
				}
				time.Sleep(250 * time.Millisecond)
			}
			if readable < 0 {
				t.Errorf("%s never became readable on the display", pkg)
			} else {
				t.Logf("readable %v after the launch", readable.Round(10*time.Millisecond))
			}
		})
	}
}

// displayState sums up what Android says of a display and an app on it:
// the activity's state, the focused display and window, and the power
// groups.
func displayState(ctx context.Context, d *Device, display int, pkg string) string {
	cmds := []string{
		"dumpsys activity activities | grep -E 'Display #|" + pkg + "|mResumedActivity|mFocusedApp|isSleeping|mLastPausedActivity|ResumedActivity' | head -30",
		"dumpsys window | grep -E 'mCurrentFocus|mFocusedApp|mFocusedDisplayId|mTopFocusedDisplayId|mPerDisplayFocusEnabled' | head -10",
		"dumpsys power | grep -E 'groupId|wakefulness|PowerGroup' | head -10",
		fmt.Sprintf("dumpsys display | grep -E 'mDisplayId=%d|mState=' | head -6", display),
	}
	var b strings.Builder
	for _, c := range cmds {
		out, _ := d.client.Shell(ctx, d.info.ID, c)
		b.Write(out)
	}
	return b.String()
}

// The nudges a read sends when an app just started shows no window: the
// key that does nothing, then a second start, which leaves the app where it
// was. Run on an emulator with the same command as above.
func TestEmulatorBackgroundNudges(t *testing.T) {
	d, ctx, _ := openEmulator(t)
	b, err := d.OpenDisplay(ctx, DisplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.Launch(ctx, "com.android.settings"); err != nil {
		t.Fatal(err)
	}
	for step, ago := range []time.Duration{2 * time.Second, 5 * time.Second} {
		b.mu.Lock()
		b.launchedAt = time.Now().Add(-ago)
		b.mu.Unlock()
		b.nudge(ctx, nil)
		b.mu.Lock()
		n := b.nudges
		b.mu.Unlock()
		if n != step+1 {
			t.Fatalf("nudge %d not sent (nudges %d)", step+1, n)
		}
	}
	b.nudge(ctx, nil) // no third
	var s *screen.Screen
	for i := 0; i < 20; i++ {
		time.Sleep(250 * time.Millisecond)
		if s, _, err = b.observeTree(ctx); err == nil && s.App.ID == "com.android.settings" && len(s.Elements) > 0 {
			break
		}
	}
	if s == nil || s.App.ID != "com.android.settings" || b.nudges != 2 {
		t.Fatalf("after the nudges: %+v, nudges %d", s, b.nudges)
	}
}
