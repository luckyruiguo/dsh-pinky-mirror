package ios

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// mayCapture answers as the gateway does for a person who blocked the bank's
// app and allowed nothing: no locked phone, no app Pinky cannot name, not
// the bank.
func mayCapture(s *screen.Screen) bool {
	return !s.Locked && (s.App.ID != "" || s.App.Name != "") && s.App.ID != "com.bank.app"
}

var (
	bankScreen     = &screen.Screen{App: screen.App{ID: "com.bank.app", Name: "Bank"}}
	settingsScreen = &screen.Screen{App: screen.App{ID: "com.apple.Preferences", Name: "Settings"}}
	homeScreen     = &screen.Screen{App: screen.App{ID: springBoard, Name: "Home Screen"}}
)

func TestCaptureGate(t *testing.T) {
	for _, c := range []struct {
		name    string
		may     func(*screen.Screen) bool
		early   []*screen.Screen // what the read may name; nil: not asked early
		last    *screen.Screen
		started bool
		how     string
		use     bool
	}{
		{"no MayCapture: at once", nil, nil, bankScreen, true, "at once", true},
		{"allowed app: after the app", mayCapture, []*screen.Screen{settingsScreen}, settingsScreen, true, "after the app", true},
		{"blocked app: never", mayCapture, []*screen.Screen{bankScreen}, bankScreen, false, "", false},
		{"locked phone: never", mayCapture, []*screen.Screen{{App: settingsScreen.App, Locked: true}}, &screen.Screen{App: settingsScreen.App, Locked: true}, false, "", false},
		{"home screen over Settings: after the app", mayCapture, []*screen.Screen{homeScreen, settingsScreen}, homeScreen, true, "after the app", true},
		// SpringBoard with the bank beneath: Face ID's prompt over it, or
		// the home screen just after the bank. The read tells.
		{"Face ID over the bank: never", mayCapture, []*screen.Screen{homeScreen, bankScreen}, bankScreen, false, "", false},
		{"home screen after the bank: after the read", mayCapture, []*screen.Screen{homeScreen, bankScreen}, homeScreen, true, "after the read", true},
		{"a process Pinky cannot name: after the read", mayCapture, nil, settingsScreen, true, "after the read", true},
		{"unnamed app: never", mayCapture, nil, &screen.Screen{}, false, "", false},
		// The bank came in front while the read went on: the picture was
		// taken, but its text is not used.
		{"app changed to the bank", mayCapture, []*screen.Screen{settingsScreen}, bankScreen, true, "after the app", false},
		// The bank left: the read takes its picture at the end.
		{"app changed from the bank", mayCapture, []*screen.Screen{bankScreen}, settingsScreen, true, "after the read", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			shots := 0
			g := newCaptureGate(c.may, func() { shots++ })
			if c.early != nil {
				g.early(c.early)
			}
			use := g.late(c.last)
			how, _ := g.stats()
			if (shots == 1) != c.started || shots > 1 || how != c.how || use != c.use {
				t.Fatalf("pictures %d, %q, use %v; want started %v, %q, use %v", shots, how, use, c.started, c.how, c.use)
			}
		})
	}
}

// TestWDAObserveCapturesOnlyWhatMayBe reads the screen through a fake
// WebDriverAgent and counts its screenshots: none of a blocked app the
// person did not allow, nor of a locked phone; one of another app, asked
// for before the page source came.
func TestWDAObserveCapturesOnlyWhatMayBe(t *testing.T) {
	f := &fakeWDA{sourceDelay: 300 * time.Millisecond}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), width: 100, height: 200, scale: 1}
	ctx := context.Background()
	read := func(app string, locked bool, may func(*screen.Screen) bool) (*screen.Screen, int, bool) {
		t.Helper()
		f.mu.Lock()
		f.app, f.locked, f.shots = app, locked, 0
		f.mu.Unlock()
		s, err := d.Observe(ctx, driver.ObserveOptions{OCR: true, MayCapture: may})
		if err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		return s, f.shots, f.shots > 0 && f.shotAt.Before(f.sourceAt)
	}
	bank := `{"bundleId":"com.bank.app","name":"Bank","pid":7}`
	settings := `{"bundleId":"com.apple.Preferences","name":"Settings","pid":8}`

	if s, shots, _ := read(bank, false, mayCapture); shots != 0 || s.App.ID != "com.bank.app" {
		t.Fatalf("the blocked bank: %d screenshots, app %q; want none", shots, s.App.ID)
	}
	if _, shots, _ := read(settings, true, mayCapture); shots != 0 {
		t.Fatalf("a locked phone: %d screenshots; want none", shots)
	}
	if s, shots, early := read(settings, false, mayCapture); shots != 1 || !early || s.App.ID != "com.apple.Preferences" {
		t.Fatalf("Settings: %d screenshots, before the source %v; want one, before it", shots, early)
	}
	if _, shots, early := read(bank, false, nil); shots != 1 || !early {
		t.Fatalf("no MayCapture: %d screenshots, before the source %v; want one, at once", shots, early)
	}
}

// appCandidates lists, before the tree, every app screenApp names once the
// tree is in, over the cases TestScreenApp checks and the lock state; and
// a process the process list does not name waits for the tree.
func TestAppCandidates(t *testing.T) {
	ctx := context.Background()
	sb := driver.App{ID: springBoard, Name: "Home Screen", System: true}
	icon := raxElement{Object: &axpObject{PID: 37, ID: 2}, Type: xcIcon, Visible: true, HasFrame: true, Label: "Clock"}
	faceID := driver.App{ID: "com.apple.CoreAuthUI", Name: "Face ID"}
	note := func(string, ...any) {}
	for _, c := range []struct {
		name string
		d    func() *Device
		els  []raxElement
		ok   bool
	}{
		{"an app", func() *Device {
			return appDevice(map[uint64]driver.App{900: {ID: "com.apple.Preferences", Name: "Settings"}}, nil)
		}, []raxElement{front(900, "Settings")}, true},
		{"a process the list does not name", func() *Device { return appDevice(nil, []uint64{901}) },
			[]raxElement{front(901, "Plasma One")}, false},
		{"SpringBoard over an app", func() *Device { return appDevice(map[uint64]driver.App{37: sb}, nil, "sg.trust") },
			[]raxElement{front(37, " ")}, true},
		{"the home screen while the last app is still in the foreground", func() *Device {
			return appDevice(map[uint64]driver.App{37: sb}, nil, "sg.trust")
		}, []raxElement{front(37, " "), icon}, true},
		{"the home screen", func() *Device { return appDevice(map[uint64]driver.App{37: sb}, nil) },
			[]raxElement{front(37, " ")}, true},
		{"system UI over an app", func() *Device {
			return appDevice(map[uint64]driver.App{902: faceID}, nil, "com.apple.CoreAuthUI", "sg.trust")
		}, []raxElement{front(902, "")}, true},
		{"system UI alone", func() *Device { return appDevice(map[uint64]driver.App{902: faceID}, nil) },
			[]raxElement{front(902, "")}, true},
	} {
		for _, locked := range []bool{false, true} {
			d := c.d()
			apps, ok := d.appCandidates(ctx, c.els[0].Object.PID, locked)
			if ok != c.ok {
				t.Errorf("%s (locked %v): ok %v, want %v", c.name, locked, ok, c.ok)
				continue
			}
			got := d.screenApp(ctx, c.els, locked, note)
			if ok && !slices.Contains(apps, got) {
				t.Errorf("%s (locked %v): screenApp names %+v, not among %+v", c.name, locked, got, apps)
			}
		}
	}
}
