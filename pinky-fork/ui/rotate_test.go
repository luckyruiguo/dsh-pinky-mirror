package ui

import (
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestRotate turns a phone's screen from ⋯ under the mirror: the button
// reaches the gateway as the person's input and is marked at once; an
// Android phone has Auto-rotate, an iPad upside down, an iPhone neither,
// and an offline phone has no buttons.
func TestRotate(t *testing.T) {
	ta := newTestApp(t, fakeapi.ThreePhones)
	ta.sync()
	byID := map[string]gateway.DeviceView{}
	for _, d := range ta.devices {
		byID[d.Info.ID] = d
	}
	pixel, iphone, galaxy := byID["pixel-9"], byID["iphone-17"], byID["galaxy-s25"]
	if !ta.canRotate(pixel) || !ta.canRotate(iphone) || ta.canRotate(galaxy) {
		t.Fatalf("can rotate: Pixel %v, iPhone %v, offline Galaxy %v", ta.canRotate(pixel), ta.canRotate(iphone), ta.canRotate(galaxy))
	}
	last := func(d gateway.DeviceView) string {
		rs := rotationsOf(d)
		return rs[len(rs)-1].value
	}
	// An iPhone's screen never turns upside down; an iPad's does.
	ipad := iphone
	ipad.Info.Model = "iPad16,1"
	if last(pixel) != driver.RotationAuto || last(iphone) != driver.RotationReverseLandscape || last(ipad) != driver.RotationUpsideDown {
		t.Fatalf("buttons: Pixel ends with %s, iPhone with %s, iPad with %s", last(pixel), last(iphone), last(ipad))
	}

	n := len(ta.api.Inputs())
	ta.setRotation(pixel, driver.RotationLandscape)
	if got := ta.rotate.got[pixel.Info.ID]; got != driver.RotationLandscape {
		t.Fatalf("marked %q before the gateway answered", got)
	}
	ta.waitFor(t, "the turn to reach the gateway", func() bool { return len(ta.api.Inputs()) > n })
	in := ta.api.Inputs()[n]
	if in.Kind != "switch" || in.Device != pixel.Info.ID || in.Text != "rotation=landscape" {
		t.Fatalf("input %+v", in)
	}
	if v, err := ta.api.DeviceSwitch(t.Context(), pixel.Info.ID, driver.SwitchRotation); err != nil || v != driver.RotationLandscape {
		t.Fatalf("the fake's rotation %q, %v", v, err)
	}
}
