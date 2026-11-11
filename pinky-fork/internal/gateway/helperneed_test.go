package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// helperPhone is an iPhone on iOS 27 left on Automatic, over CoreDevice
// with no switches and no second finger, the helper installed (or not).
func helperPhone(t *testing.T, installed bool) (*demo.Device, *Gateway) {
	t.Helper()
	shortSwitchBack(t)
	p, dev := newTwoWayPhone()
	dev.SetVirtual(true) // no Phone settings approval
	p.bareCore, p.noHelper = true, !installed
	g := twoWayGateway(t, p)
	if err := g.Policy().SetPreset(policy.PresetPayments); err != nil {
		t.Fatal(err)
	}
	return dev, g
}

func rotateFeature(g *Gateway) bool {
	ds := g.Devices()
	return len(ds) == 1 && ds[0].Features.Rotate
}

// phone_device moves the task to the helper for a change and goes on; a
// read moves nothing. The task then stays on the helper until it ends, so
// what it changed is put back there.
func TestDeviceMovesToTheHelper(t *testing.T) {
	ctx := context.Background()
	_, g := helperPhone(t, true)
	s, _ := connect(t, g, "Test the landscape layout")
	// A read says so, settled with the phone's battery and storage or
	// unsupported without them.
	if res := s.Device(ctx, DeviceRequest{}); !strings.Contains(res.Message, "a change moves the task to the helper by itself") {
		t.Fatalf("read over CoreDevice: %+v", res)
	}
	if b := backendOf(g); b != wayCoreDevice {
		t.Fatalf("a read moved the phone to %q", b)
	}
	res := s.Device(ctx, DeviceRequest{Rotation: driver.RotationLandscape})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "Pinky moved this task to the WebDriverAgent helper for turning the screen") {
		t.Fatalf("turn: %q", res.Message)
	}
	if b := backendOf(g); b != wayHelper {
		t.Fatalf("backend %q after the turn", b)
	}
	if len(res.Switches) != 1 || res.Switches[0].Value != driver.RotationLandscape || !res.Switches[0].Changed {
		t.Fatalf("switches %+v", res.Switches)
	}
	// Going back before the task ends would leave the turn in place.
	res = s.Connect(ctx, ConnectRequest{Task: "Test the landscape layout", Connection: "coredevice"})
	if !strings.Contains(res.Message, "keeps") || backendOf(g) != wayHelper {
		t.Fatalf("back to CoreDevice with a turn to put back: %q, %q", res.Message, backendOf(g))
	}
	// Put back, it may go.
	mustStatus(t, s.Device(ctx, DeviceRequest{Reset: []string{"rotation"}}), StatusSettled)
	mustStatus(t, s.Connect(ctx, ConnectRequest{Task: "Test the landscape layout", Connection: "coredevice"}), StatusSettled)
	if b := backendOf(g); b != wayCoreDevice {
		t.Fatalf("backend %q after the reset and asking for CoreDevice", b)
	}
}

// The task's turn is put back at its end, through the helper it stayed on;
// then the phone goes back to CoreDevice.
func TestPutBackOnTheHelper(t *testing.T) {
	ctx := context.Background()
	_, g := helperPhone(t, true)
	s, _ := connect(t, g, "Test the landscape layout")
	mustStatus(t, s.Device(ctx, DeviceRequest{Rotation: driver.RotationLandscape}), StatusSettled)
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
	waitUntil(t, "the phone back on CoreDevice", func() bool { return backendOf(g) == wayCoreDevice })
	// Read it through the helper again: portrait, as before the task.
	s2, _ := connect(t, g, "Look")
	res := s2.Device(ctx, DeviceRequest{Rotation: driver.RotationPortrait})
	mustStatus(t, res, StatusSettled)
	if len(res.Switches) != 1 || res.Switches[0].Changed {
		t.Fatalf("after the put-back: %+v", res.Switches)
	}
}

// Without the helper, a change says so and nothing moves.
func TestDeviceWithoutTheHelper(t *testing.T) {
	_, g := helperPhone(t, false)
	s, _ := connect(t, g, "Test the landscape layout")
	res := s.Device(context.Background(), DeviceRequest{Rotation: driver.RotationLandscape})
	if res.Error != ErrUnsupported || !strings.Contains(res.Message, "has no switches") || !strings.Contains(res.Message, "not installed") {
		t.Fatalf("turn without the helper: %+v", res)
	}
	if b := backendOf(g); b != wayCoreDevice {
		t.Fatalf("backend %q", b)
	}
	if rotateFeature(g) {
		t.Fatal("the person's Rotate shows without the helper")
	}
}

// phone_pinch moves the task to the helper and returns stale with the
// screen read there; the next pinch pinches.
func TestPinchMovesToTheHelper(t *testing.T) {
	ctx := context.Background()
	_, g := helperPhone(t, true)
	s, _ := connect(t, g, "Zoom the map")
	res := s.Act(ctx, Call{}, Action{Tool: "pinch", Scale: 2})
	if res.Status != StatusStale || !strings.Contains(res.Message, "for pinching") || !strings.Contains(res.Message, "Nothing was pinched") || res.Screen == nil {
		t.Fatalf("first pinch: %+v", res)
	}
	if b := backendOf(g); b != wayHelper {
		t.Fatalf("backend %q after the pinch", b)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "pinch", Scale: 2}), StatusSettled)
}

// The person's Rotate shows on a phone over CoreDevice with the helper,
// moves it there, turns it, and the phone goes back to CoreDevice once no
// task holds it; the person's choice in Settings › Phones turns that off.
func TestPersonRotatesThroughTheHelper(t *testing.T) {
	ctx := context.Background()
	_, g := helperPhone(t, true)
	waitUntil(t, "Rotate offered", func() bool { return rotateFeature(g) })
	if v, err := g.DeviceSwitch(ctx, "iphone", driver.SwitchRotation); err != nil || v != "" {
		t.Fatalf("read over CoreDevice: %q, %v", v, err)
	}
	if err := g.SetDeviceSwitch(ctx, "iphone", driver.SwitchRotation, driver.RotationLandscape); err != nil {
		t.Fatal(err)
	}
	if v, err := g.DeviceSwitch(ctx, "iphone", driver.SwitchRotation); err != nil || v != driver.RotationLandscape {
		t.Fatalf("read on the helper: %q, %v", v, err)
	}
	waitUntil(t, "the phone back on CoreDevice", func() bool { return backendOf(g) == wayCoreDevice })
	if !rotateFeature(g) {
		t.Fatal("Rotate gone after going back")
	}
	if err := g.SetConnection("iphone", wayCoreDevice); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "Rotate gone with CoreDevice chosen", func() bool { return !rotateFeature(g) })
	if err := g.SetDeviceSwitch(ctx, "iphone", driver.SwitchRotation, driver.RotationPortrait); err == nil {
		t.Fatal("turned with CoreDevice chosen")
	}
}

func pinchFeature(g *Gateway) bool {
	ds := g.Devices()
	return len(ds) == 1 && ds[0].Features.Pinch
}

func touches2(dev *demo.Device) int {
	n := 0
	for _, in := range dev.Inputs() {
		if strings.HasPrefix(in.Kind, "touch2:") {
			n++
		}
	}
	return n
}

// The person's pinch on the mirror over CoreDevice moves the phone to the
// helper in the background, losing that gesture; the next one reaches the
// phone. While the person keeps using the mirror the phone stays on the
// helper, and goes back once they stop.
func TestPersonPinchesThroughTheHelper(t *testing.T) {
	dev, g := helperPhone(t, true)
	waitUntil(t, "pinch offered", func() bool { return pinchFeature(g) })
	p, q := screen.Point{X: 400, Y: 1200}, screen.Point{X: 800, Y: 1200}
	for _, a := range []driver.TouchAction{driver.TouchDown, driver.TouchMove, driver.TouchUp} {
		if err := g.Touch2("iphone", a, p, q); err != nil {
			t.Fatalf("%s over CoreDevice: %v", a, err)
		}
	}
	waitUntil(t, "the phone on the helper", func() bool { return backendOf(g) == wayHelper })
	if n := touches2(dev); n != 0 {
		t.Fatalf("%d touches of the lost gesture reached the phone", n)
	}
	for _, a := range []driver.TouchAction{driver.TouchDown, driver.TouchMove, driver.TouchUp} {
		if err := g.Touch2("iphone", a, p, q); err != nil {
			t.Fatalf("%s on the helper: %v", a, err)
		}
	}
	if n := touches2(dev); n != 3 {
		t.Fatalf("%d touches reached the phone on the helper, want 3", n)
	}
	// Using it for longer than switchBackAfter keeps it there.
	for range 8 {
		if err := g.Touch2("iphone", driver.TouchMove, p, q); err != nil {
			t.Fatal(err)
		}
		time.Sleep(switchBackAfter / 4)
	}
	if b := backendOf(g); b != wayHelper {
		t.Fatalf("went back to %q while the person used it", b)
	}
	waitUntil(t, "the phone back on CoreDevice", func() bool { return backendOf(g) == wayCoreDevice })
}

// Without the helper the person's pinch is refused, as before.
func TestPersonPinchWithoutTheHelper(t *testing.T) {
	_, g := helperPhone(t, false)
	if pinchFeature(g) {
		t.Fatal("pinch offered without the helper")
	}
	if err := g.Touch2("iphone", driver.TouchDown, screen.Point{X: 1, Y: 1}, screen.Point{X: 2, Y: 2}); err == nil {
		t.Fatal("pinched over CoreDevice without the helper")
	}
}

// Touches while the phone moves to the helper are lost quietly: the phone
// is offline meanwhile, which is no error of the person's.
func TestPersonPinchWhileMoving(t *testing.T) {
	shortSwitchBack(t)
	p, dev := newTwoWayPhone()
	p.bareCore, p.slow = true, 300*time.Millisecond
	g := twoWayGateway(t, p)
	waitUntil(t, "pinch offered", func() bool { return pinchFeature(g) })
	a, b := screen.Point{X: 400, Y: 1200}, screen.Point{X: 800, Y: 1200}
	if err := g.Touch2("iphone", driver.TouchDown, a, b); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the move under way", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.devices["iphone"].switching != ""
	})
	for _, act := range []driver.TouchAction{driver.TouchMove, driver.TouchUp, driver.TouchDown, driver.TouchUp} {
		if err := g.Touch2("iphone", act, a, b); err != nil {
			t.Fatalf("%s while moving: %v", act, err)
		}
	}
	waitUntil(t, "the phone on the helper", func() bool { return backendOf(g) == wayHelper })
	if n := touches2(dev); n != 0 {
		t.Fatalf("%d touches reached the phone while it moved", n)
	}
}

// A change a read-only task or read-only mode refuses moves nothing: the
// refusal comes first.
func TestReadOnlyKeepsCoreDevice(t *testing.T) {
	ctx := context.Background()
	_, g := helperPhone(t, true)
	s := g.OpenSession(agent)
	t.Cleanup(s.Close)
	mustStatus(t, s.Connect(ctx, ConnectRequest{Task: "Look around", ReadOnly: true}), StatusSettled)
	res := s.Device(ctx, DeviceRequest{Rotation: driver.RotationLandscape})
	if res.Status != StatusBlocked || strings.Contains(res.Message, "moved") {
		t.Fatalf("read-only task: %+v", res)
	}
	if b := backendOf(g); b != wayCoreDevice {
		t.Fatalf("a read-only task moved the phone to %q", b)
	}
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)

	if err := g.SetReadOnly("iphone", true); err != nil {
		t.Fatal(err)
	}
	s2, _ := connect(t, g, "Test the landscape layout")
	res = s2.Device(ctx, DeviceRequest{Rotation: driver.RotationLandscape})
	if res.Status != StatusBlocked || strings.Contains(res.Message, "moved") {
		t.Fatalf("read-only mode: %+v", res)
	}
	if b := backendOf(g); b != wayCoreDevice {
		t.Fatalf("read-only mode moved the phone to %q", b)
	}
}

// The person's Rotate on a phone whose helper asks for the passcode says
// so in the person's terms, and the phone still goes back to CoreDevice
// by itself once the passcode is in.
func TestPersonRotateWaitsForThePasscode(t *testing.T) {
	ctx := context.Background()
	shortSwitchBack(t)
	p, _ := newTwoWayPhone()
	p.bareCore, p.askOnOpen = true, true
	g := twoWayGateway(t, p)
	waitUntil(t, "Rotate offered", func() bool { return rotateFeature(g) })
	err := g.SetDeviceSwitch(ctx, "iphone", driver.SwitchRotation, driver.RotationLandscape)
	var se *driver.SetupError
	if !errors.As(err, &se) || se.Setup != driver.SetupPasscode || !strings.Contains(se.Detail, "Enter the passcode on") ||
		strings.Contains(err.Error(), "phone_handoff") {
		t.Fatalf("turn with the passcode asked: %v", err)
	}
	// The person enters it: the phone comes up on the helper, then goes
	// back, with no task to keep it there.
	p.mu.Lock()
	p.passcode = false
	p.mu.Unlock()
	waitUntil(t, "the phone back on CoreDevice", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		d := g.devices["iphone"]
		return d.online && d.info.Backend == wayCoreDevice && d.taskWay == ""
	})
}
