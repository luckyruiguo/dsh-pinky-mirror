//go:build device

package android

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/scrcpy"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestDeviceTapsLand taps the dialer's 5 key many times, in batches with
// different disturbances, and counts the digits that arrived: a lost tap
// is a missing digit. It runs on emulators only (it presses keys in the
// phone app hundreds of times, never the call button). PINKY_TAPS sets the
// taps per batch (40).
func TestDeviceTapsLand(t *testing.T) {
	d, _ := openTestDevice(t)
	if !strings.HasPrefix(d.info.ID, "emulator-") {
		t.Skip("emulators only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	n := envInt("PINKY_TAPS", 40)

	pkg := ""
	openDialer := func() screen.Point {
		t.Helper()
		if pkg != "" {
			if _, err := d.client.Shell(ctx, d.info.ID, "am force-stop "+pkg); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := d.client.Shell(ctx, d.info.ID, "am start -a android.intent.action.DIAL >/dev/null 2>&1"); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			time.Sleep(700 * time.Millisecond)
			s, err := d.Observe(ctx, driver.ObserveOptions{})
			if err == nil {
				if e, ok := find(s, func(e screen.Element) bool { return strings.HasPrefix(e.Label, "5,") }); ok {
					pkg = s.App.ID
					return e.Bounds.Center()
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("no dialer key 5 on screen (%v)", err)
			}
		}
	}
	digits := func() int {
		t.Helper()
		time.Sleep(time.Second)
		s, err := d.Observe(ctx, driver.ObserveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		best := 0
		for _, e := range s.Elements {
			if e.Role == "input" { // the dialpad's own labels are "5"
				if c := strings.Count(e.Label, "5"); c > best {
					best = c
				}
			}
		}
		return best
	}

	type disturbance func(stop <-chan struct{}) // runs until stop closes
	every := func(gap time.Duration, f func()) disturbance {
		return func(stop <-chan struct{}) {
			for {
				select {
				case <-stop:
					return
				case <-time.After(gap):
					f()
				}
			}
		}
	}
	restarts := 0
	batches := []struct {
		name    string
		pace    time.Duration
		disturb disturbance
	}{
		{"paced 300 ms", 300 * time.Millisecond, nil},
		{"back to back", 0, nil},
		{"video reset every 400 ms", 300 * time.Millisecond, every(400*time.Millisecond, func() {
			if l, err := d.session(ctx); err == nil {
				_ = l.send(scrcpy.ResetVideo())
			}
		})},
		{"slow stream consumer", 300 * time.Millisecond, func(stop <-chan struct{}) {
			st, err := d.Stream(ctx, driver.StreamOptions{})
			if errors.Is(err, driver.ErrUnsupported) {
				t.Logf("no stream on this phone: %v", err)
				return
			}
			if err != nil {
				t.Errorf("stream: %v", err)
				return
			}
			<-stop // never read: the driver resets the video every 2 s
			st.Close()
		}},
		{"companion turned off and on", 300 * time.Millisecond, every(1500*time.Millisecond, func() {
			if d.companionOn() {
				d.companionOff("tap test")
			} else {
				d.companionClient(ctx)
			}
		})},
		{"session restarted before each tap", -1, nil},
		{"session restarted while tapping", 300 * time.Millisecond, every(350*time.Millisecond, func() {
			// Another caller (the mirror, a recorder) asks for other
			// stream options while an agent taps.
			restarts++
			st, err := d.Stream(ctx, driver.StreamOptions{MaxSize: 720 + 80*(restarts%2)})
			if err == nil {
				st.Close()
			}
		})},
		{"screenshots and reads meanwhile", 300 * time.Millisecond, every(200*time.Millisecond, func() {
			_, _ = d.Screenshot(ctx)
			_, _ = d.Observe(ctx, driver.ObserveOptions{})
		})},
	}
	total, lostAll := 0, 0
	for _, b := range batches {
		key := openDialer()
		var wg sync.WaitGroup
		stop := make(chan struct{})
		if b.disturb != nil {
			wg.Add(1)
			go func() { defer wg.Done(); b.disturb(stop) }()
		}
		start := time.Now()
		taps := 0
		for i := 0; i < n; i++ {
			if b.pace < 0 {
				// A stream with other options restarts the session; the
				// tap then goes to a session that has just started.
				st, err := d.Stream(ctx, driver.StreamOptions{MaxSize: 720 + 80*(i%2)})
				if errors.Is(err, driver.ErrUnsupported) {
					t.Logf("%s: no stream on this phone", b.name)
					break
				}
				if err != nil {
					t.Fatalf("%s: stream: %v", b.name, err)
				}
				st.Close()
			}
			if err := d.Tap(ctx, key, driver.TapOptions{}); err != nil {
				t.Errorf("%s: tap %d: %v", b.name, i+1, err)
			}
			taps++
			if b.pace != 0 {
				time.Sleep(max(b.pace, 300*time.Millisecond))
			}
		}
		close(stop)
		wg.Wait()
		if _, err := d.WaitSettled(ctx, 0); err != nil {
			t.Logf("%s: settle: %v", b.name, err)
		}
		if taps == 0 {
			continue
		}
		got := digits()
		lost := taps - got
		total += taps
		lostAll += max(lost, 0)
		t.Logf("%s: %d taps in %v, %d digits arrived, %d lost", b.name, taps, time.Since(start).Round(time.Millisecond), got, lost)
		if lost != 0 {
			t.Errorf("%s: %d of %d taps lost", b.name, lost, taps)
		}
	}
	if pkg != "" {
		_, _ = d.client.Shell(ctx, d.info.ID, "am force-stop "+pkg)
	}
	t.Logf("%d taps, %d lost", total, lostAll)
}
