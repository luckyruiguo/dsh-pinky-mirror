package ios

import (
	"context"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// appDevice is a Device with an app catalog, known processes and a list of
// foreground apps, and nothing to dial.
func appDevice(pids map[uint64]driver.App, unknown []uint64, fg ...string) *Device {
	apps := []driver.App{
		{ID: "sg.trust", Name: "Trust"},
		{ID: "to.plasma.one.application", Name: "Plasma One"},
		{ID: "com.apple.Preferences", Name: "Settings", System: true},
		{ID: "com.example.twin.a", Name: "Twin"},
		{ID: "com.example.twin.b", Name: "Twin"},
	}
	d := &Device{apps: &appCatalog{apps: apps, at: time.Now()}, pidApps: pids, pidMiss: map[uint64]time.Time{}}
	for _, p := range unknown {
		d.pidMiss[p] = time.Now()
	}
	d.foreground = func(context.Context) []axApp {
		var out []axApp
		for i, id := range fg {
			out = append(out, axApp{id: id, state: appForeground, seq: len(fg) - i, at: time.Now().Add(-time.Duration(i) * time.Second)})
		}
		return out
	}
	return d
}

func front(pid int, label string) raxElement {
	return raxElement{Object: &axpObject{PID: pid, ID: 1, IsApp: true}, Label: label}
}

func TestScreenApp(t *testing.T) {
	ctx := context.Background()
	sb := driver.App{ID: springBoard, Name: "Home Screen", System: true}
	icon := raxElement{Object: &axpObject{PID: 37, ID: 2}, Type: xcIcon, Visible: true, HasFrame: true, Label: "Clock"}
	note := func(string, ...any) {}
	for _, c := range []struct {
		name string
		d    *Device
		els  []raxElement
		want screen.App
	}{
		{"an app found by its process",
			appDevice(map[uint64]driver.App{900: {ID: "com.apple.Preferences", Name: "Settings"}}, nil),
			[]raxElement{front(900, "Settings")}, screen.App{Name: "Settings", ID: "com.apple.Preferences"}},
		{"the first read of a session, when the process lookup failed (item 27)",
			appDevice(nil, []uint64{901}),
			[]raxElement{front(901, "Plasma One")}, screen.App{Name: "Plasma One", ID: "to.plasma.one.application"}},
		{"Face ID in front of an app: SpringBoard is frontmost (item 14)",
			appDevice(map[uint64]driver.App{37: sb}, nil, "sg.trust"),
			[]raxElement{front(37, " ")}, screen.App{Name: "Trust", ID: "sg.trust"}},
		{"Face ID in front of an app: a process with no name",
			appDevice(nil, []uint64{902}, "com.apple.CoreAuthUI", "sg.trust"),
			[]raxElement{front(902, "")}, screen.App{Name: "Trust", ID: "sg.trust"}},
		{"the home screen while the last app is still listed in the foreground",
			appDevice(map[uint64]driver.App{37: sb}, nil, "sg.trust"),
			[]raxElement{front(37, " "), icon}, screen.App{Name: "Home Screen", ID: springBoard}},
		{"the home screen",
			appDevice(map[uint64]driver.App{37: sb}, nil),
			[]raxElement{front(37, " ")}, screen.App{Name: "Home Screen", ID: springBoard}},
		{"two apps share the name: no id is guessed",
			appDevice(nil, []uint64{903}),
			[]raxElement{front(903, "Twin")}, screen.App{Name: "Twin"}},
	} {
		if got := c.d.screenApp(ctx, c.els, false, note); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// What a read found in front stands for the app beneath when the inspector
// connected after it came there, and not once the daemon says it left.
func TestScreenAppFromLastRead(t *testing.T) {
	ctx := context.Background()
	note := func(string, ...any) {}
	sb := driver.App{ID: springBoard, Name: "Home Screen", System: true}
	trust := driver.App{ID: "sg.trust", Name: "Trust"}
	d := appDevice(map[uint64]driver.App{37: sb, 950: trust}, nil)
	if got := d.screenApp(ctx, []raxElement{front(950, "Trust")}, false, note); got.ID != "sg.trust" {
		t.Fatalf("Trust read as %+v", got)
	}
	faceID := []raxElement{front(37, " ")}
	if got := d.screenApp(ctx, faceID, false, note); got.ID != "sg.trust" {
		t.Errorf("Face ID over Trust read as %+v", got)
	}
	if got := d.screenApp(ctx, faceID, true, note); got.ID != springBoard {
		t.Errorf("a locked phone read as %+v", got)
	}
	// The daemon saw Trust leave the foreground after that read.
	d.foreground = func(context.Context) []axApp {
		return []axApp{{id: "sg.trust", state: 4, at: time.Now().Add(time.Second)}}
	}
	if got := d.screenApp(ctx, faceID, false, note); got.ID != springBoard {
		t.Errorf("after Trust left: %+v", got)
	}
	// The home screen with its icons forgets the app.
	d = appDevice(map[uint64]driver.App{37: sb, 950: trust}, nil)
	d.screenApp(ctx, []raxElement{front(950, "Trust")}, false, note)
	icon := raxElement{Object: &axpObject{PID: 37, ID: 2}, Type: xcIcon, Visible: true, HasFrame: true}
	d.screenApp(ctx, []raxElement{front(37, " "), icon}, false, note)
	if got := d.screenApp(ctx, faceID, false, note); got.ID != springBoard {
		t.Errorf("after the home screen: %+v", got)
	}
}

// iOS reports apps it wakes in the background in the foreground too (China
// Merchants Bank, seen on the iPhone 13 mini while Settings was in front).
func TestScreenAppIgnoresBackgroundWakes(t *testing.T) {
	ctx := context.Background()
	note := func(string, ...any) {}
	sb := driver.App{ID: springBoard, Name: "Home Screen", System: true}
	settings := driver.App{ID: "com.apple.Preferences", Name: "Settings"}
	d := appDevice(map[uint64]driver.App{37: sb, 960: settings}, nil)
	woke := time.Now()
	d.foreground = func(context.Context) []axApp {
		return []axApp{{id: "sg.trust", state: appForeground, at: woke}}
	}
	time.Sleep(time.Millisecond)
	d.screenApp(ctx, []raxElement{front(960, "Settings")}, false, note) // a read after the wake
	if got := d.screenApp(ctx, []raxElement{front(37, " ")}, false, note); got.ID != "com.apple.Preferences" {
		t.Errorf("an overlay over Settings read as %+v; want the app the last read found", got)
	}
	// An app that comes to the foreground after the last read is the one.
	d.foreground = func(context.Context) []axApp {
		return []axApp{{id: "sg.trust", state: appForeground, at: time.Now().Add(time.Second)}}
	}
	if got := d.screenApp(ctx, []raxElement{front(37, " ")}, false, note); got.ID != "sg.trust" {
		t.Errorf("Face ID over an app opened since: %+v", got)
	}
}

func TestExecutablePath(t *testing.T) {
	for in, want := range map[string]string{
		"file:///private/var/containers/Bundle/Application/D4/Plasma%20One.app/Plasma%20One": "/private/var/containers/Bundle/Application/D4/Plasma One.app/Plasma One",
		"file:///System/Library/CoreServices/SpringBoard.app/SpringBoard":                    "/System/Library/CoreServices/SpringBoard.app/SpringBoard",
		"/usr/libexec/lockdownd": "/usr/libexec/lockdownd",
	} {
		if got := executablePath(in); got != want {
			t.Errorf("executablePath(%q) = %q, want %q", in, got, want)
		}
	}
}
