package ios

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	goios "github.com/danielpaulus/go-ios/ios"
)

// mediaSession is a CoreDevice session whose phone refuses the stream
// while inUse is set, as a new iPhone 15 Plus on iOS 27 did.
type mediaSession struct {
	mu     sync.Mutex
	inUse  bool
	fail   error // another failure of the next start
	starts int
	ran    bool
	closed bool
}

// openedDevice is the device a mediaSession opens as.
type openedDevice struct{ driver.Device }

func (f *mediaSession) start(ctx context.Context) error { return f.retryMedia(ctx) }

func (f *mediaSession) retryMedia(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	switch {
	case f.closed:
		return fmt.Errorf("device closed: %w", driver.ErrOffline)
	case f.fail != nil:
		return f.fail
	case f.inUse:
		ce := &CoreDeviceError{Code: errMediaInUse, Domain: "com.apple.dt.CoreDeviceError", Desc: "The device microphone or camera is currently in use."}
		return fmt.Errorf("start display stream (display 1): an app is using the camera or microphone, and iOS does not allow mirroring then: %w", ce)
	}
	return nil
}

func (f *mediaSession) opened() driver.Device {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = true
	return openedDevice{}
}

func (f *mediaSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *mediaSession) set(inUse bool, fail error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inUse, f.fail = inUse, fail
}

func (f *mediaSession) state() (starts int, ran, closed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.ran, f.closed
}

// mediaProvider is a provider with one iPhone on iOS 27 on a cable, whose
// sessions are the ones next hands out.
func mediaProvider(t *testing.T) (p *Provider, phones *[]goios.DeviceEntry, next func(*mediaSession), dials func() int) {
	p = NewProvider()
	p.mediaEvery = 5 * time.Millisecond
	list := []goios.DeviceEntry{entry("phone", 1, "USB")}
	var mu sync.Mutex
	p.listFn = func() (goios.DeviceList, error) {
		mu.Lock()
		defer mu.Unlock()
		return goios.DeviceList{DeviceList: append([]goios.DeviceEntry(nil), list...)}, nil
	}
	p.inspectFn = func(_ context.Context, e goios.DeviceEntry) driver.Info {
		return driver.Info{ID: e.Properties.SerialNumber, Platform: driver.IOS, State: driver.StateOnline, Backend: backendCoreDevice, OSVersion: "27.2"}
	}
	var s *mediaSession
	n := 0
	p.dialFn = func(context.Context, driver.Info) (coreSession, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		return s, nil
	}
	t.Cleanup(func() { p.Close() })
	return p, &list, func(f *mediaSession) {
			mu.Lock()
			s = f
			mu.Unlock()
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return n
		}
}

func listOne(t *testing.T, p *Provider) driver.Info {
	t.Helper()
	got, err := p.List(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %+v, %v; want one phone", got, err)
	}
	return got[0]
}

func TestProviderWaitsForCameraAndMicrophone(t *testing.T) {
	p, _, next, dials := mediaProvider(t)
	f := &mediaSession{inUse: true}
	next(f)
	ctx := context.Background()
	if got := listOne(t, p); got.State != driver.StateOnline {
		t.Fatalf("before the open: %+v", got)
	}

	// The phone refuses the stream: the step, with the cause kept.
	_, err := p.Open(ctx, "phone")
	var se *driver.SetupError
	if !errors.As(err, &se) || se.Setup != driver.SetupMediaInUse || se.Detail != mediaInUseDetail || !mediaInUse(err) {
		t.Fatalf("Open = %v; want the media_in_use step", err)
	}
	if _, _, closed := f.state(); closed {
		t.Fatal("the session closed: it waits for the camera and microphone")
	}

	// Every List says so while the app holds them, and the stream is
	// tried again on the same session, with no new tunnel.
	for range 5 {
		if got := listOne(t, p); got.State != driver.StateNeedsSetup || got.Setup != driver.SetupMediaInUse || got.Detail != mediaInUseDetail {
			t.Fatalf("while in use: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitUntil(t, "the stream tried again", func() bool { n, _, _ := f.state(); return n >= 3 })
	if n := dials(); n != 1 {
		t.Fatalf("%d tunnels while waiting; want 1", n)
	}
	// An Open meanwhile (the gateway does not, while List says so) says
	// the same.
	if _, err := p.Open(ctx, "phone"); !errors.As(err, &se) || se.Setup != driver.SetupMediaInUse {
		t.Fatalf("Open while in use = %v", err)
	}

	// Released: the stream starts, List says online, and Open takes the
	// session over without dialing again.
	f.set(false, nil)
	waitUntil(t, "online again", func() bool { return listOne(t, p).State == driver.StateOnline })
	dev, err := p.Open(ctx, "phone")
	if err != nil || dev == nil {
		t.Fatalf("Open once free = %v, %v", dev, err)
	}
	if _, ran, closed := f.state(); !ran || closed || dials() != 1 {
		t.Fatalf("taken over: ran %v, closed %v, %d tunnels; want the waiting session", ran, closed, dials())
	}
	p.mu.Lock()
	left := len(p.mediaWaits)
	p.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d waits left after the open", left)
	}
	if got := listOne(t, p); got.State != driver.StateOnline {
		t.Fatalf("after the open: %+v", got)
	}
}

func TestMediaWaitEnds(t *testing.T) {
	ctx := context.Background()
	open := func(t *testing.T) (*Provider, *[]goios.DeviceEntry, *mediaSession, func(*mediaSession), func() int) {
		p, phones, next, dials := mediaProvider(t)
		f := &mediaSession{inUse: true}
		next(f)
		listOne(t, p)
		if _, err := p.Open(ctx, "phone"); !mediaInUse(err) {
			t.Fatalf("Open = %v", err)
		}
		return p, phones, f, next, dials
	}
	closes := func(t *testing.T, f *mediaSession) {
		t.Helper()
		waitUntil(t, "the session closed", func() bool { _, _, closed := f.state(); return closed })
	}

	t.Run("unplugged", func(t *testing.T) {
		p, phones, f, _, _ := open(t)
		*phones = nil
		if got, err := p.List(ctx); err != nil || len(got) != 0 {
			t.Fatalf("List = %+v, %v", got, err)
		}
		closes(t, f)
	})
	t.Run("reconnected", func(t *testing.T) {
		p, phones, f, _, _ := open(t)
		*phones = []goios.DeviceEntry{entry("phone", 2, "USB")}
		if got := listOne(t, p); got.State != driver.StateOnline {
			t.Fatalf("over the new attach: %+v", got)
		}
		closes(t, f)
	})
	t.Run("paused", func(t *testing.T) {
		p, _, f, _, _ := open(t)
		p.SetPaused("phone", true)
		closes(t, f)
	})
	t.Run("another connection", func(t *testing.T) {
		p, _, f, _, _ := open(t)
		if err := p.SetConnection("phone", backendWDA); err != nil {
			t.Fatal(err)
		}
		closes(t, f)
	})
	t.Run("provider closed", func(t *testing.T) {
		p, _, f, _, _ := open(t)
		p.Close()
		if _, _, closed := f.state(); !closed {
			t.Fatal("Close left the session open")
		}
	})
	t.Run("stream fails otherwise", func(t *testing.T) {
		// Online again, and the next Open dials afresh, as without a wait.
		p, _, f, next, dials := open(t)
		f.set(false, errors.New("tunnel: the iPhone does not answer"))
		closes(t, f)
		if got := listOne(t, p); got.State != driver.StateOnline {
			t.Fatalf("after the failure: %+v", got)
		}
		g := &mediaSession{}
		next(g)
		if _, err := p.Open(ctx, "phone"); err != nil || dials() != 2 {
			t.Fatalf("Open after the failure = %v with %d tunnels; want a new one", err, dials())
		}
	})
	t.Run("started but not opened", func(t *testing.T) {
		f := &mediaSession{}
		w := newMediaWait("phone", entryKey{deviceID: 1, connection: "USB"}, f, time.Millisecond, nil)
		w.take = 10 * time.Millisecond
		go w.run()
		<-w.done
		if _, _, closed := f.state(); !closed || w.current() != mediaEnded {
			t.Fatalf("closed %v, state %d; want the session closed", closed, w.current())
		}
		if _, ok := w.takeOver(); ok {
			t.Fatal("took over a closed session")
		}
	})
}

// TestMediaInUseIsNoMissingPicture: a phone that refused one display and
// then the stream because an app holds the camera or microphone waits for
// that step; its checkup does not also say it sends no picture.
func TestMediaInUseIsNoMissingPicture(t *testing.T) {
	p := NewProvider()
	refused := errors.New("display 1 refused")
	nv := (*noVideoError)(nil).with(noVideoTry{Display: 1, Err: refused}, refused, nil)
	ce := &CoreDeviceError{Code: errMediaInUse, Domain: "com.apple.dt.CoreDeviceError", Desc: "The device microphone or camera is currently in use."}
	err := nv.add(ce, 2, nil)
	if !mediaInUse(err) {
		t.Fatalf("mediaInUse(%v) = false", err)
	}
	p.noteOpen("phone", err)
	if got := p.OpenChecks("phone"); len(got) != 0 {
		t.Fatalf("OpenChecks while in use = %+v; want none", got)
	}
	other := (*noVideoError)(nil).with(noVideoTry{Display: 1, Err: refused}, refused, nil)
	p.noteOpen("phone", other)
	if got := p.OpenChecks("phone"); len(got) != 1 {
		t.Fatalf("OpenChecks with no picture = %+v; want the screen item", got)
	}
}
