package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
)

const mediaDetail = "An app on the iPhone is using the camera or microphone, and iOS does not allow mirroring the screen then. " +
	"Pinky connects by itself once the camera and microphone are free."

// mediaProvider lists an iPhone as the iOS driver does while an app on it
// uses the camera or microphone: online until an open finds the stream
// refused, then needing the media_in_use step at every listing while its
// session tries the stream again, and online once the camera and
// microphone are free, when the next open takes that session over.
type mediaProvider struct {
	dev     *demo.Device
	mu      sync.Mutex
	inUse   bool // an app holds the camera or microphone
	waiting bool // an open found it so, and its session waits
	opens   int
}

func (p *mediaProvider) Platform() driver.Platform { return driver.IOS }

func (p *mediaProvider) List(context.Context) ([]driver.Info, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	info := driver.Info{ID: "ip", Platform: driver.IOS, Name: "iPhone 15 Plus", Model: "iPhone 15 Plus", OSVersion: "27.2",
		Connection: "usb", Backend: "coredevice", State: driver.StateOnline}
	if p.waiting && p.inUse {
		info.State, info.Setup, info.Detail = driver.StateNeedsSetup, driver.SetupMediaInUse, mediaDetail
	}
	return []driver.Info{info}, nil
}

func (p *mediaProvider) Open(context.Context, string) (driver.Device, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opens++
	if p.inUse {
		p.waiting = true
		return nil, &driver.SetupError{Setup: driver.SetupMediaInUse, Detail: mediaDetail,
			Err: errors.New("start display stream (display 1): The device microphone or camera is currently in use. (com.apple.dt.CoreDeviceError 9022)")}
	}
	p.waiting = false
	return p.dev, nil
}

func (p *mediaProvider) free() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inUse = false
}

func (p *mediaProvider) openCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opens
}

// TestMediaInUseStep: an iPhone whose stream iOS refuses while an app uses
// the camera or microphone needs the media_in_use step from the first open
// on, and keeps needing it, round after round of discovery, without being
// opened again or going online in between. An agent hears the step and its
// words; once the camera and microphone are free, the phone opens.
func TestMediaInUseStep(t *testing.T) {
	p := &mediaProvider{dev: demo.NewDevice("ip", "iPhone 15 Plus", driver.IOS), inUse: true}
	g := stepGateway(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := g.Subscribe(ctx)

	v := viewUntil(t, g, "ip", "the step", func(v DeviceView) bool { return v.Info.Setup == driver.SetupMediaInUse })
	if v.Status != DeviceNeedsSetup || v.StatusLine != "Camera or microphone in use" || v.Info.State != driver.StateNeedsSetup || v.Info.Detail != mediaDetail {
		t.Fatalf("the step: %+v", v)
	}

	// Many discoveries (every 20 ms) later: the same step all along, the
	// phone opened once, and no event about it.
	for len(events) > 0 {
		<-events
	}
	for range 30 {
		for _, v := range g.Devices() {
			if v.Info.ID == "ip" && (v.Status != DeviceNeedsSetup || v.Info.Setup != driver.SetupMediaInUse) {
				t.Fatalf("the step flickered: %q, %+v", v.Status, v.Info)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := p.openCount(); n != 1 {
		t.Errorf("opened %d times while it waited; want once", n)
	}
	for len(events) > 0 {
		if e := <-events; e.DeviceID == "ip" && (e.Kind == EventDevices || e.Kind == EventNeeds) {
			t.Errorf("an event while nothing changed: %+v", e)
		}
	}
	cu, err := g.Checkup(ctx, "ip")
	if err != nil {
		t.Fatal(err)
	}
	if auth, _ := checkByID(cu, driver.CheckAuthorized); auth.Status != driver.CheckFail || auth.Fix != driver.SetupMediaInUse || auth.Detail != mediaDetail {
		t.Errorf("the checkup's step: %+v", auth)
	}

	// An agent hears the step, what to do, and how to ask the person.
	s := g.OpenSession(AgentInfo{Name: "Claude Code"})
	defer s.Close()
	res := s.Connect(ctx, ConnectRequest{Task: "Photos"})
	if res.Status != StatusNeedsSetup || res.Setup != driver.SetupMediaInUse || !strings.Contains(res.Message, mediaDetail) ||
		!strings.Contains(res.Message, "phone_handoff") || !strings.Contains(res.Message, "call") {
		t.Fatalf("connecting: %+v", res)
	}

	// Free: online, and opened.
	p.free()
	waitOnline(t, g, "ip")
	if v := viewUntil(t, g, "ip", "online", func(v DeviceView) bool { return v.Status != DeviceNeedsSetup }); v.Info.Setup != "" {
		t.Errorf("online: %+v", v)
	}
	if r := s.Observe(ctx, ObserveRequest{}); r.IsError() || r.Screen == nil {
		t.Errorf("phone_observe once free: %+v", r)
	}
}
