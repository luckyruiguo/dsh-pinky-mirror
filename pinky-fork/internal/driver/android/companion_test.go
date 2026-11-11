package android

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
)

// fakeRunner plays a phone for the companion client: the app may or may
// not be installed, and its socket never answers.
type fakeRunner struct {
	mu        sync.Mutex
	installed bool
	setting   string
	shells    []string
}

func (f *fakeRunner) Shell(_ context.Context, cmd string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shells = append(f.shells, cmd)
	switch {
	case strings.HasPrefix(cmd, "dumpsys package "):
		if f.installed {
			return "Packages:\n  Package [app.pinky.companion]\n    versionName=0.1.0\n", nil
		}
		return "", nil
	case cmd == "settings get secure enabled_accessibility_services":
		return f.setting + "\n", nil
	case strings.HasPrefix(cmd, "settings put secure enabled_accessibility_services "):
		f.setting = strings.Trim(strings.TrimPrefix(cmd, "settings put secure enabled_accessibility_services "), "'")
		return "", nil
	}
	return "", nil
}

func (f *fakeRunner) Forward(context.Context, string) (string, error) {
	return "", errors.New("nothing listens")
}
func (f *fakeRunner) RemoveForward(context.Context, string) error { return nil }
func (f *fakeRunner) Install(context.Context, string) error       { return nil }

func (f *fakeRunner) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.shells {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

func testDevice(r companion.Runner) (*Device, *[]string) {
	var logs []string
	var mu sync.Mutex
	cfg := Config{Logf: func(f string, a ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(f, a...))
		mu.Unlock()
	}}
	cfg.defaults()
	d := newDevice(cfg, driver.Info{ID: "phone"}, deviceProps{})
	d.comp.client = companion.New(r)
	return d, &logs
}

func TestCompanionMissing(t *testing.T) {
	r := &fakeRunner{}
	d, logs := testDevice(r)
	ctx := context.Background()
	if c := d.companionClient(ctx); c != nil {
		t.Fatal("used a companion that is not installed")
	}
	if c := d.companionClient(ctx); c != nil {
		t.Fatal("second call used it")
	}
	// The second call waits for the retry time instead of asking again,
	// and the reason is logged once.
	if n := r.count("dumpsys package"); n != 1 {
		t.Errorf("looked up the package %d times", n)
	}
	if len(*logs) != 1 || !strings.Contains((*logs)[0], "not installed") {
		t.Errorf("logs = %q", *logs)
	}
	if r.count("settings put") != 0 {
		t.Error("changed a setting for a missing app")
	}
}

func TestCompanionNotAnswering(t *testing.T) {
	r := &fakeRunner{installed: true, setting: "com.example/.Service"}
	d, logs := testDevice(r)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if c := d.companionClient(ctx); c != nil {
		t.Fatal("used a companion that does not answer")
	}
	// Enable turned it on, waited, and put the setting back exactly.
	if r.count("settings put") != 2 || r.setting != "com.example/.Service" {
		t.Errorf("setting = %q after %d writes", r.setting, r.count("settings put"))
	}
	if len(*logs) != 1 || !strings.Contains((*logs)[0], "did not start") {
		t.Errorf("logs = %q", *logs)
	}
	if !d.comp.retryAt.After(time.Now()) {
		t.Error("no wait before the next try")
	}
	d.closeCompanion()
	if c := d.companionClient(ctx); c != nil {
		t.Error("used after close")
	}
}

func TestLastChangeByDisplay(t *testing.T) {
	var k companionLink
	k.live = true
	k.lastAt = map[int]time.Time{}
	t0 := time.Unix(1000, 0)
	k.lastAt[0] = t0
	k.lastAt[23] = t0.Add(2 * time.Second)
	if last, live := k.lastChange(0); !live || !last.Equal(t0) {
		t.Errorf("main display = %v, %v", last, live)
	}
	if last, _ := k.lastChange(23); !last.Equal(t0.Add(2 * time.Second)) {
		t.Errorf("display 23 = %v", last)
	}
	// An event whose display is not known counts for every display.
	k.lastAt[-1] = t0.Add(5 * time.Second)
	for _, d := range []int{0, 23, 99} {
		if last, _ := k.lastChange(d); !last.Equal(t0.Add(5 * time.Second)) {
			t.Errorf("display %d after an unknown one = %v", d, last)
		}
	}
}
