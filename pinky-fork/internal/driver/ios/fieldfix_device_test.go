//go:build device

package ios

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// observeAfter launches app (or presses Home for ""), settles as the
// gateway does, and reads the screen, logging how long the read took.
func observeAfter(t *testing.T, ctx context.Context, d *Device, app string) (*screen.Screen, ObserveStats) {
	t.Helper()
	if app == "" {
		d.Key(ctx, driver.KeyHome)
	} else if err := d.Launch(ctx, app); err != nil {
		t.Fatal(err)
	}
	d.WaitSettled(ctx, 3*time.Second)
	s, st, err := d.ObserveWithStats(ctx, driver.ObserveOptions{})
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	t.Logf("%s: %s (%s) in %v from %s, %d elements, %d events; trace %v", app, s.App.Name, s.App.ID, st.Total.Round(time.Millisecond), st.Source, len(s.Elements), len(s.Events), st.Trace)
	return s, st
}

// sameRefs reports the elements of b whose ref differs from the element
// with the same label and place in a.
func sameRefs(t *testing.T, a, b *screen.Screen) {
	t.Helper()
	type k struct {
		label string
		r     screen.Rect
	}
	refs := map[k]int{}
	for _, e := range a.Elements {
		refs[k{e.Label, e.Bounds}] = e.Ref
	}
	changed := 0
	for _, e := range b.Elements {
		if r, ok := refs[k{e.Label, e.Bounds}]; ok && r != e.Ref {
			changed++
			t.Errorf("%s %q: ref %d, then %d", e.Role, e.Label, r, e.Ref)
		}
	}
	t.Logf("%d of %d elements kept their refs", len(b.Elements)-changed, len(b.Elements))
}

// TestDeviceFieldFixes checks the fixes from the iPhone field test on the
// phone: app ids on every read, refs that survive a read from a push and
// one from a walk, Clock's tab bar and picker wheels, and the video stream
// stopping while nobody watches. It opens Clock's Add Alarm sheet and
// cancels it, and leaves Clock on the tab it found.
func TestDeviceFieldFixes(t *testing.T) {
	d := testDevice(t)
	ctx := ctxFor(t, 3*time.Minute)
	refs := screen.NewRefs()

	home, _ := observeAfter(t, ctx, d, "")
	if home.App.ID != springBoard {
		t.Errorf("home screen read as %+v", home.App)
	}
	pause()
	set1, _ := observeAfter(t, ctx, d, "com.apple.Preferences")
	if set1.App.ID != "com.apple.Preferences" {
		t.Errorf("Settings read as %+v", set1.App)
	}
	refs.Assign(set1)
	// Past raxPushTrust the next read walks.
	time.Sleep(raxPushTrust)
	set2, st, err := d.ObserveWithStats(ctx, driver.ObserveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if st.Source != "remoteax" {
		t.Logf("second read from %s", st.Source)
	}
	refs.Assign(set2)
	sameRefs(t, set1, set2)

	pause()
	clock, _ := observeAfter(t, ctx, d, "com.apple.mobiletimer")
	if clock.App.ID != "com.apple.mobiletimer" {
		t.Errorf("Clock read as %+v", clock.App)
	}
	var tabs []string
	var startTab, alarms screen.Element
	for _, e := range clock.Elements {
		if e.Role == screen.RoleTab {
			tabs = append(tabs, e.Label)
			if e.Has(screen.StateSelected) {
				startTab = e
			}
			if e.Label == "Alarms" {
				alarms = e
			}
		}
	}
	t.Logf("tabs %v, on %q", tabs, startTab.Label)
	if alarms.Label == "" {
		t.Fatalf("no Alarms tab in %v", tabs)
	}
	pause()
	d.Tap(ctx, alarms.Bounds.Center(), driver.TapOptions{})
	d.WaitSettled(ctx, 3*time.Second)
	list, _, err := d.ObserveWithStats(ctx, driver.ObserveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var add screen.Element
	for _, e := range list.Elements {
		if e.Role == screen.RoleButton && e.Label == "Add" {
			add = e
		}
	}
	if add.Label == "" {
		t.Fatal("no Add button")
	}
	pause()
	d.Tap(ctx, add.Bounds.Center(), driver.TapOptions{})
	d.WaitSettled(ctx, 3*time.Second)
	sheet, _, err := d.ObserveWithStats(ctx, driver.ObserveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var wheels []string
	var cancel screen.Element
	for _, e := range sheet.Elements {
		if e.Has(stateAdjustable) {
			wheels = append(wheels, e.Label)
		}
		if e.Label == "Cancel" {
			cancel = e
		}
	}
	t.Logf("picker wheels %q", wheels)
	if len(wheels) != 3 || strings.HasPrefix(wheels[0], "50") {
		t.Errorf("picker wheels read %q", wheels)
	}
	if cancel.Label == "" {
		t.Fatal("no Cancel on the sheet")
	}
	pause()
	d.Tap(ctx, cancel.Bounds.Center(), driver.TapOptions{})
	d.WaitSettled(ctx, 3*time.Second)
	if startTab.Label != "" && startTab.Label != "Alarms" {
		back, _, err := d.ObserveWithStats(ctx, driver.ObserveOptions{})
		if err == nil {
			for _, e := range back.Elements {
				if e.Role == screen.RoleTab && e.Label == startTab.Label {
					pause()
					d.Tap(ctx, e.Bounds.Center(), driver.TapOptions{})
					d.WaitSettled(ctx, 3*time.Second)
				}
			}
		}
	}
	home2, _ := observeAfter(t, ctx, d, "")
	if home2.App.ID != springBoard {
		t.Errorf("home screen read as %+v", home2.App)
	}

	// The stream stops once nobody watches and nothing is sent.
	st0, ok := d.StreamStats()
	t.Logf("stream running %v since %v", ok, st0.Since.Format("15:04:05"))
	time.Sleep(streamIdle + 2*idleCheck)
	if _, ok := d.StreamStats(); ok {
		t.Error("the stream still runs with nobody watching")
	}
	start := time.Now()
	if err := d.Key(ctx, driver.KeyHome); err != nil {
		t.Fatalf("Home after the idle stop: %v", err)
	}
	t.Logf("input after the idle stop started the stream again in %v", time.Since(start).Round(time.Millisecond))
	if _, ok := d.StreamStats(); !ok {
		t.Error("no stream after input")
	}
}

// TestDeviceNotInScreenshot reads screens that show everything, with OCR
// as the gateway does after an action, and expects no element marked as
// missing from the screenshot.
func TestDeviceNotInScreenshot(t *testing.T) {
	d := testDevice(t)
	ctx := ctxFor(t, 3*time.Minute)
	for _, app := range []string{"", "com.apple.Preferences", "com.apple.mobiletimer", "com.apple.calculator", ""} {
		if app == "" {
			d.Key(ctx, driver.KeyHome)
		} else if err := d.Launch(ctx, app); err != nil {
			t.Fatal(err)
		}
		d.WaitSettled(ctx, 3*time.Second)
		s, st, err := d.ObserveWithStats(ctx, driver.ObserveOptions{OCR: true})
		if err != nil {
			t.Fatal(err)
		}
		marked := 0
		for _, e := range s.Elements {
			if e.Has(stateNotInScreenshot) {
				marked++
				t.Errorf("%s: %s %q marked not in the screenshot", s.App.ID, e.Role, e.Label)
			}
		}
		t.Logf("%s: %d elements, %d OCR lines, %d marked, %v (shot %v, OCR %v)", s.App.ID, len(s.Elements), st.OCRLines, marked, st.Total.Round(time.Millisecond), st.Shot.Round(time.Millisecond), st.OCR.Round(time.Millisecond))
		pause()
	}
}

// TestDeviceForegroundApps looks at whether the inspector's app states
// follow launches, and after turning app monitoring off and on again.
func TestDeviceForegroundApps(t *testing.T) {
	d := testDevice(t)
	ctx := ctxFor(t, time.Minute)
	ax, err := d.axReady(ctx)
	if err != nil {
		t.Fatal(err)
	}
	show := func(when string) {
		var ids []string
		for _, a := range ax.foregroundApps() {
			ids = append(ids, a.id)
		}
		t.Logf("%s: foreground %v", when, ids)
	}
	t.Logf("first app states in time: %v", ax.waitApps(ctx, appListWait))
	show("connected")
	d.Launch(ctx, "com.apple.Preferences")
	time.Sleep(2 * time.Second)
	show("after launching Settings")
	d.Key(ctx, driver.KeyHome)
	time.Sleep(2 * time.Second)
	show("after Home")
}
