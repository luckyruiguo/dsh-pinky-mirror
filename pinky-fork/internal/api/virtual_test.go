package api

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
)

const testAVD = "Pixel_9_API_36"

// launcher is the demo's Pixel as an emulator: it starts and stops one AVD.
type launcher struct {
	driver.Provider
	mu      sync.Mutex
	running bool
	calls   []string // "start <id>", "stop <id>"
}

func (l *launcher) VirtualPhones(context.Context) (driver.VirtualPhones, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v := driver.VirtualPhone{ID: testAVD, Name: "Pixel 9 API 36", Model: "Pixel 9", OSVersion: "16", Running: l.running}
	if l.running {
		v.DeviceID = "pixel"
	}
	return driver.VirtualPhones{Platform: driver.Android, Phones: []driver.VirtualPhone{v}}, nil
}

func (l *launcher) StartVirtual(_ context.Context, id string) error { return l.set(id, "start", true) }
func (l *launcher) StopVirtual(_ context.Context, id string) error  { return l.set(id, "stop", false) }

func (l *launcher) set(id, op string, running bool) error {
	if id != testAVD {
		return driver.ErrNotFound
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, op+" "+id)
	l.running = running
	return nil
}

// Scripts and e2e tests list, start and stop virtual phones over the
// socket as Add phone does, without the person.
func TestVirtualPhonesOverSocket(t *testing.T) {
	d := shortDirs(t)
	l := &launcher{Provider: demo.NewProvider(driver.Android, demo.NewDevice("pixel", "Pixel 9", driver.Android))}
	startServerWith(t, d, false, l)
	ctl, err := DialControl(d)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	list := func() driver.VirtualPhone {
		t.Helper()
		var vp []driver.VirtualPhones
		if err := ctl.Call("virtual_phones", nil, &vp); err != nil {
			t.Fatal(err)
		}
		if len(vp) != 1 || vp[0].Platform != driver.Android || len(vp[0].Phones) != 1 {
			t.Fatalf("virtual phones: %+v", vp)
		}
		return vp[0].Phones[0]
	}
	if v := list(); v.ID != testAVD || v.Running {
		t.Fatalf("before: %+v", v)
	}
	avd := VirtualParams{Platform: driver.Android, ID: testAVD}
	if err := ctl.Call("start_virtual", avd, nil); err != nil {
		t.Fatal(err)
	}
	if v := list(); !v.Running || v.DeviceID != "pixel" {
		t.Fatalf("started: %+v", v)
	}
	if err := ctl.Call("stop_virtual", avd, nil); err != nil {
		t.Fatal(err)
	}
	if v := list(); v.Running {
		t.Fatalf("stopped: %+v", v)
	}
	l.mu.Lock()
	calls := l.calls
	l.mu.Unlock()
	if want := []string{"start " + testAVD, "stop " + testAVD}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls %q, want %q", calls, want)
	}

	// Errors come back as the client gets them.
	var re *RPCError
	for _, c := range []struct {
		method string
		p      VirtualParams
		code   int
	}{
		{"start_virtual", VirtualParams{Platform: driver.Android}, CodeParams},
		{"start_virtual", VirtualParams{Platform: driver.Android, ID: "Gone_API_30"}, CodeFailed},
		{"stop_virtual", VirtualParams{Platform: driver.IOS, ID: "5F1E7A2C"}, CodeFailed},
	} {
		if err := ctl.Call(c.method, c.p, nil); !errors.As(err, &re) || re.Code != c.code {
			t.Errorf("%s %+v: %v", c.method, c.p, err)
		}
	}
}

// A gateway whose providers start no virtual phones lists none.
func TestNoVirtualPhonesOverSocket(t *testing.T) {
	d := shortDirs(t)
	startServer(t, d, false)
	ctl, err := DialControl(d)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	var vp []driver.VirtualPhones
	if err := ctl.Call("virtual_phones", nil, &vp); err != nil || len(vp) != 0 {
		t.Fatalf("%+v, %v", vp, err)
	}
}
