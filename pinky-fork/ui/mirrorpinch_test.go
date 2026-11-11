package ui

import (
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestMirrorPinch pinches on the mirror: a touchpad's pinch puts two
// fingers down around the pointer, moves them apart, and lifts them once
// it stops; an Option-drag moves one finger with the pointer and the other
// across the screen's centre from it; a phone that cannot pinch gets
// neither.
func TestMirrorPinch(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.sync()
	d := ta.devices[0]
	if !d.Features.Pinch {
		t.Fatal("the fake phone does not pinch")
	}
	m := &mirror{a: ta.app, id: d.Info.ID, picW: 1080, picH: 2400, boxW: 270, boxH: 600}
	in := func(d gateway.DeviceView, ev mg.InputEvent) bool { return ta.mirrorInput(m, d, ev) }
	pinches := func(from int) []fakeapi.Input {
		var out []fakeapi.Input
		for _, in := range ta.api.Inputs()[from:] {
			if in.Kind != "touch2" {
				t.Fatalf("input %+v", in)
			}
			out = append(out, in)
		}
		return out
	}
	span := func(in fakeapi.Input) int { return in.Point2.Y - in.Point.Y }

	// A touchpad's pinch at the middle: the fingers go down along the
	// screen's length, around the pointer, and apart by its scale.
	for range 3 {
		if !in(d, mg.InputEvent{Kind: mg.InputZoom, X: 135, Y: 300, Scale: 1.25}) {
			t.Fatal("zoom not taken")
		}
	}
	if m.pinch == nil {
		t.Fatal("no fingers down")
	}
	in(d, mg.InputEvent{Kind: mg.InputScroll, X: 135, Y: 300, DY: 40, Precise: true}) // no scrolling while pinching
	var got []fakeapi.Input
	ta.waitFor(t, "the fingers to lift", func() bool { got = ta.api.Inputs(); return len(got) >= 5 })
	got = pinches(0)
	acts := []driver.TouchAction{driver.TouchDown, driver.TouchMove, driver.TouchMove, driver.TouchMove, driver.TouchUp}
	for i, a := range acts {
		if got[i].Action != a {
			t.Fatalf("input %d is %s, want %s: %+v", i, got[i].Action, a, got)
		}
	}
	down, last := got[0], got[3]
	if down.Point.X != 540 || down.Point2.X != 540 || down.Point.Y+down.Point2.Y < 2398 || down.Point.Y+down.Point2.Y > 2402 {
		t.Errorf("fingers down at %v and %v, want either side of 540,1200", down.Point, down.Point2)
	}
	if r := float64(span(last)) / float64(span(down)); r < 1.9 || r > 2.0 {
		t.Errorf("spans %d to %d: %.2f times, want 1.95", span(down), span(last), r)
	}
	if m.pinch != nil || m.scrolling {
		t.Error("still down after the pinch")
	}

	// A long pinch out lifts the fingers where they meet and starts again.
	n := len(ta.api.Inputs())
	for range 12 {
		in(d, mg.InputEvent{Kind: mg.InputZoom, X: 135, Y: 300, Scale: 0.8})
	}
	ta.waitFor(t, "the fingers to lift", func() bool {
		ins := ta.api.Inputs()
		return m.pinch == nil && len(ins) > n+12 && ins[len(ins)-1].Action == driver.TouchUp
	})
	got = pinches(n)
	downs, ups := 0, 0
	for _, in := range got {
		switch in.Action {
		case driver.TouchDown:
			downs++
		case driver.TouchUp:
			ups++
		}
		if in.Point.Y < 192 || in.Point2.Y > 2208 {
			t.Errorf("a finger left the screen's margins: %+v", in)
		}
	}
	if downs < 2 || downs != ups || got[0].Action != driver.TouchDown || got[len(got)-1].Action != driver.TouchUp {
		t.Errorf("%d downs and %d ups: %+v", downs, ups, got)
	}

	// Option-drag: the second finger mirrors the pointer across the centre.
	n = len(ta.api.Inputs())
	in(d, mg.InputEvent{Kind: mg.InputPointerDown, X: 67.5, Y: 150, Mods: mg.Alt})
	in(d, mg.InputEvent{Kind: mg.InputPointerMove, X: 30, Y: 60})
	in(d, mg.InputEvent{Kind: mg.InputPointerUp, X: 30, Y: 60})
	ta.waitFor(t, "the drag", func() bool { return len(ta.api.Inputs()) >= n+3 })
	want := []fakeapi.Input{
		{Device: "pixel-9", Kind: "touch2", Action: driver.TouchDown, Point: screen.Point{X: 270, Y: 600}, Point2: screen.Point{X: 809, Y: 1799}},
		{Device: "pixel-9", Kind: "touch2", Action: driver.TouchMove, Point: screen.Point{X: 120, Y: 240}, Point2: screen.Point{X: 959, Y: 2159}},
		{Device: "pixel-9", Kind: "touch2", Action: driver.TouchUp, Point: screen.Point{X: 120, Y: 240}, Point2: screen.Point{X: 959, Y: 2159}},
	}
	for i, in := range pinches(n) {
		if in != want[i] {
			t.Errorf("drag input %d = %+v, want %+v", i, in, want[i])
		}
	}

	// A phone that cannot pinch: the touchpad's pinch is not taken, and
	// Option-drag is a plain drag.
	d.Features.Pinch = false
	if in(d, mg.InputEvent{Kind: mg.InputZoom, X: 135, Y: 300, Scale: 1.25}) {
		t.Error("zoom taken by a phone that cannot pinch")
	}
	n = len(ta.api.Inputs())
	in(d, mg.InputEvent{Kind: mg.InputPointerDown, X: 67.5, Y: 150, Mods: mg.Alt})
	in(d, mg.InputEvent{Kind: mg.InputPointerUp, X: 67.5, Y: 150})
	ta.waitFor(t, "the touch", func() bool { return len(ta.api.Inputs()) >= n+2 })
	if got := ta.api.Inputs()[n]; got.Kind != "touch" || got.Point != (screen.Point{X: 270, Y: 600}) {
		t.Errorf("Option-click on a phone that cannot pinch: %+v", got)
	}
}

// TestPinchMarks: an approval's screenshot of a pinch zooms on both
// fingers' paths.
func TestPinchMarks(t *testing.T) {
	n := gateway.NeedView{
		Point: screen.Point{X: 540, Y: 1000}, To: screen.Point{X: 540, Y: 600},
		Point2: screen.Point{X: 540, Y: 1400}, To2: screen.Point{X: 540, Y: 1800},
	}
	m := marksOf(n)
	if !m.swipe() || !m.pinch() {
		t.Fatalf("marks %+v", m)
	}
	r, ok := m.focus(1080, 2400)
	if !ok || r.Y != 600 || r.Y+r.H != 1800 {
		t.Errorf("focus %v, want 600 to 1800", r)
	}
}
