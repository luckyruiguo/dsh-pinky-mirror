//go:build device

package ios

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestDeviceConnectionWay opens the phone the way PINKY_WAY names
// ("wda" or "coredevice"), as Settings › Phones would, and reads, taps,
// types and streams on the home screen, Settings (browsing and its search
// field) and Calculator, timing each step. Run it alone:
//
//	PINKY_DEVICE=<udid> PINKY_WAY=wda scripts/devlock.sh <udid> go test -tags device -run TestDeviceConnectionWay -v ./internal/driver/ios/
func TestDeviceConnectionWay(t *testing.T) {
	way := os.Getenv("PINKY_WAY")
	if way == "" {
		t.Skip("PINKY_WAY not set")
	}
	id := os.Getenv("PINKY_DEVICE")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if os.Getenv("PINKY_WDA_LOG") != "" {
		wdaLog = os.Stderr
	}
	p := deviceProvider(t, id)
	if err := p.SetConnection(id, way); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	dev, err := p.Open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	if b := dev.Info().Backend; b != way {
		t.Fatalf("backend %q, want %q", b, way)
	}
	t.Logf("%s: open in %v", way, time.Since(t0).Round(time.Millisecond))

	settle := func() time.Duration {
		s0 := time.Now()
		if st, ok := dev.(driver.Settler); ok {
			st.WaitSettled(ctx, 3*time.Second)
		}
		return time.Since(s0).Round(time.Millisecond)
	}
	observe := func(what string) *screen.Screen {
		t.Helper()
		o0 := time.Now()
		s, err := dev.Observe(ctx, driver.ObserveOptions{})
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		t.Logf("%s: observe %v, %d elements, app %s, keyboard %v", what, time.Since(o0).Round(time.Millisecond), len(s.Elements), s.App.ID, s.Keyboard)
		return s
	}
	find := func(s *screen.Screen, ok func(e screen.Element) bool) (screen.Element, bool) {
		for _, e := range s.Elements {
			if ok(e) && !e.Bounds.Empty() {
				return e, true
			}
		}
		return screen.Element{}, false
	}
	label := func(l string) func(e screen.Element) bool { return func(e screen.Element) bool { return e.Label == l } }
	tap := func(what string, e screen.Element) {
		t.Helper()
		time.Sleep(400 * time.Millisecond)
		a0 := time.Now()
		if err := dev.Tap(ctx, e.Bounds.Center(), driver.TapOptions{}); err != nil {
			t.Fatalf("tap %s: %v", what, err)
		}
		t.Logf("tap %s: %v, settled in %v", what, time.Since(a0).Round(time.Millisecond), settle())
	}

	if err := dev.Key(ctx, driver.KeyHome); err != nil {
		t.Fatal(err)
	}
	settle()
	observe("home screen")
	observe("home screen again")
	if wd, ok := dev.(*WDADevice); ok {
		for _, depth := range []int{30, 60} {
			sid, _ := wd.c.session(ctx)
			wd.c.call(ctx, wd.c.hc, "POST", "/session/"+sid+"/appium/settings", map[string]any{"settings": map[string]any{"snapshotMaxDepth": depth}}, nil)
			observe(fmt.Sprintf("home screen, snapshot depth %d", depth))
		}
	}

	// Settings: General, then back.
	if err := dev.Launch(ctx, "com.apple.Preferences"); err != nil {
		t.Fatal(err)
	}
	settle()
	s := observe("Settings")
	if c, ok := find(s, func(e screen.Element) bool { return e.Label == "Cancel" || e.Label == "close" }); ok {
		tap("leave search", c)
		s = observe("Settings")
	}
	if _, ok := find(s, label("General")); !ok {
		// Account rows can push General below the fold.
		time.Sleep(400 * time.Millisecond)
		w, h := s.Width, s.Height
		if err := dev.Swipe(ctx, screen.Point{X: w / 2, Y: h * 3 / 4}, screen.Point{X: w / 2, Y: h / 3}, 300*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		settle()
		s = observe("Settings, scrolled")
	}
	if gen, ok := find(s, label("General")); ok {
		tap("General", gen)
		s = observe("General")
		if _, ok := find(s, label("About")); !ok {
			t.Errorf("no About on General: %v", labels(s))
		}
		time.Sleep(400 * time.Millisecond)
		b0 := time.Now()
		if err := dev.Key(ctx, driver.KeyBack); err != nil {
			t.Fatal(err)
		}
		t.Logf("back: %v, settled in %v", time.Since(b0).Round(time.Millisecond), settle())
		s = observe("Settings after back")
	} else {
		t.Errorf("no General: %v", labels(s))
	}

	// Settings' search field: type English, then Chinese, then clear.
	if field, ok := find(s, func(e screen.Element) bool { return e.Role == screen.RoleInput }); ok {
		tap("search field", field)
		for _, text := range []string{"clock", "你好"} {
			time.Sleep(400 * time.Millisecond)
			y0 := time.Now()
			if err := dev.Type(ctx, text, driver.TypeOptions{}); err != nil {
				t.Fatalf("type %q: %v", text, err)
			}
			t.Logf("type %q: %v", text, time.Since(y0).Round(time.Millisecond))
			s = observe("after typing")
			// Settings' floating search field can drop out of WDA's tree
			// while the results show.
			if _, ok := find(s, func(e screen.Element) bool { return e.Role == screen.RoleInput && strings.Contains(e.Label, text) }); !ok {
				t.Logf("the field is not listed with %q (%d elements)", text, len(s.Elements))
			}
		}
		time.Sleep(400 * time.Millisecond)
		if err := dev.Type(ctx, "", driver.TypeOptions{}); err != nil {
			t.Errorf("clear: %v", err)
		}
		s = observe("after clearing")
		if c, ok := find(s, func(e screen.Element) bool { return e.Label == "Cancel" || e.Label == "close" }); ok {
			tap("leave search", c)
		}
	} else {
		t.Errorf("no search field: %v", labels(s))
	}

	// Calculator: 7, read back, clear.
	time.Sleep(400 * time.Millisecond)
	if err := dev.Launch(ctx, "com.apple.calculator"); err != nil {
		t.Fatal(err)
	}
	settle()
	s = observe("Calculator")
	if seven, ok := find(s, label("7")); ok {
		tap("7", seven)
		s = observe("Calculator after 7")
		if disp, ok := find(s, func(e screen.Element) bool { return e.ID == "StandardInputView" }); ok {
			t.Logf("display: %q", disp.Label)
		} else {
			t.Logf("display not found: %v", labels(s))
		}
		if clr, ok := find(s, func(e screen.Element) bool {
			return e.Label == "All Clear" || e.Label == "Clear" || e.Label == "Delete"
		}); ok {
			tap(clr.Label, clr)
		}
	} else {
		t.Errorf("no 7 key: %v", labels(s))
	}

	// Picture, screenshot, lock state.
	sh0 := time.Now()
	img, err := dev.Screenshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("screenshot %v in %v", img.Bounds().Size(), time.Since(sh0).Round(time.Millisecond))
	st0 := time.Now()
	stream, err := dev.Stream(ctx, driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	w, h := stream.Size()
	first := time.Since(st0)
	go func() {
		time.Sleep(500 * time.Millisecond)
		if s, err := dev.Observe(ctx, driver.ObserveOptions{}); err == nil {
			if k, ok := find(s, label("8")); ok {
				dev.Tap(ctx, k.Bounds.Center(), driver.TapOptions{})
			}
		}
	}()
	var n, keys, bytes int
	stop := time.After(4 * time.Second)
read:
	for {
		select {
		case pk, ok := <-stream.Packets():
			if !ok {
				break read
			}
			n, bytes = n+1, bytes+len(pk.Data)
			if pk.KeyFrame {
				keys++
			}
		case <-stop:
			break read
		}
	}
	stream.Close()
	t.Logf("stream %s %dx%d: first frame after %v, %d packets (%d key) in 4 s, %.2f Mbit/s", stream.Codec(), w, h, first.Round(time.Millisecond), n, keys, float64(bytes)*8/4e6)
	if lr, ok := dev.(driver.LockReporter); ok {
		l, err := lr.Locked(ctx)
		t.Logf("locked: %v (%v)", l, err)
	}
	s = observe("Calculator at the end")
	if clr, ok := find(s, func(e screen.Element) bool {
		return e.Label == "All Clear" || e.Label == "Clear" || e.Label == "Delete"
	}); ok {
		tap(clr.Label, clr)
	}
	time.Sleep(400 * time.Millisecond)
	dev.Key(ctx, driver.KeyHome)
	time.Sleep(time.Second)
}

func labels(s *screen.Screen) []string {
	var out []string
	for _, e := range s.Elements {
		out = append(out, e.Role+":"+e.Label)
	}
	return out
}

// TestDeviceWDAStart opens the phone through the WebDriverAgent helper and
// shows when the runner waits for automation mode, which iOS asks the
// passcode for now and then. On an unlocked phone it then goes
// to the home screen and reads it once. Run it alone:
//
//	PINKY_DEVICE=<udid> PINKY_WDA_START=1 scripts/devlock.sh <udid> go test -tags device -run TestDeviceWDAStart -v ./internal/driver/ios/
func TestDeviceWDAStart(t *testing.T) {
	if os.Getenv("PINKY_WDA_START") == "" {
		t.Skip("PINKY_WDA_START not set")
	}
	id := os.Getenv("PINKY_DEVICE")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	t0 := time.Now()
	wdaLog = lineFilter(func(line string) {
		if strings.Contains(line, "automation mode") || strings.Contains(line, "synchronous waiter") || strings.Contains(line, "ServerURLHere") {
			t.Logf("%6.2fs runner: %s", time.Since(t0).Seconds(), line)
		}
	})
	defer func() { wdaLog = io.Discard }()
	p := deviceProvider(t, id)
	if err := p.SetConnection(id, backendWDA); err != nil {
		t.Fatal(err)
	}
	dev, err := p.Open(ctx, id)
	if errors.Is(err, errAutomation) {
		// As the gateway would: List until the phone is online again, then
		// Open, which takes the waiting runner over.
		t.Logf("%6.2fs: the phone asks for its passcode; waiting for it", time.Since(t0).Seconds())
		var last string
		for {
			infos, err := p.List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var info driver.Info
			for _, i := range infos {
				if i.ID == id {
					info = i
				}
			}
			if s := string(info.State) + ": " + info.Detail; s != last {
				t.Logf("%6.2fs: List says %s", time.Since(t0).Seconds(), s)
				last = s
			}
			if info.State == driver.StateOnline {
				break
			}
			if time.Since(t0) > 100*time.Second {
				t.Fatal("still not online")
			}
			time.Sleep(time.Second)
		}
		dev, err = p.Open(ctx, id)
	}
	if err != nil {
		t.Fatalf("open after %v: %v", time.Since(t0).Round(time.Millisecond), err)
	}
	defer dev.Close()
	t.Logf("open in %v", time.Since(t0).Round(time.Millisecond))
	locked, err := dev.(driver.LockReporter).Locked(ctx)
	if err != nil || locked {
		t.Logf("locked %v (%v): leaving the phone as it is", locked, err)
		return
	}
	if err := dev.Key(ctx, driver.KeyHome); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	s, err := dev.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("home screen: %d elements, app %s", len(s.Elements), s.App.ID)
}

// lineFilter is a writer that hands each line it is given to f.
type lineFilter func(line string)

func (f lineFilter) Write(b []byte) (int, error) {
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		f(l)
	}
	return len(b), nil
}
