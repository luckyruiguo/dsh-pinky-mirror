package ui

import (
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
)

// The person pinches on the mirror in two ways. A touchpad's pinch puts two
// fingers down around the pointer and moves them apart or together as the
// pinch goes on. Dragging with Option held, as in Xcode's Simulator, moves
// one finger with the pointer and the other opposite it across the
// screen's centre, which also turns things with two fingers. Both reach
// the phone as two-finger touches (Touch2), which pause the agent like any
// of the person's input.

// multiToucher is the gateway's two-finger touch (gateway.Gateway.Touch2).
type multiToucher interface {
	Touch2(deviceID string, a driver.TouchAction, p, q screen.Point) error
}

const (
	pinchLift = 150 * time.Millisecond // a touchpad's pinch lifts its fingers once it has been still this long
	// A touchpad's pinch keeps its fingers inside the screen less these
	// margins (the edges' system gestures), as the agent's pinches do.
	pinchMarginX, pinchMarginY = 0.07, 0.08
)

// mirrorPinch is a two-finger gesture under way on the mirror. Points are
// in the upright picture's pixels.
type mirrorPinch struct {
	drag bool         // an Option-drag, rather than a touchpad's pinch
	a, b screen.Point // where the fingers are
	// A touchpad's pinch: the fingers sit on either side of c, along x
	// (axis 0) or y (axis 1), span apart, which stays within lo and hi.
	c      [2]float64
	axis   int
	span   float64
	lo, hi float64
	seq    int
}

// canPinch reports whether the person's pinch reaches the phone.
func (a *app) canPinch(d gateway.DeviceView) bool {
	_, ok := a.api.(multiToucher)
	return ok && d.Features.Pinch
}

// touch2 sends the fingers of the person's pinch to the phone.
func (a *app) touch2(m *mirror, id string, act driver.TouchAction, p, q screen.Point) {
	mt, ok := a.api.(multiToucher)
	if !ok {
		return
	}
	p, q = m.toPicture(p), m.toPicture(q)
	a.call(id, locales.MirrorCallPinch(), func() error { return mt.Touch2(id, act, p, q) })
}

// endPinch lifts the fingers of a pinch under way.
func (a *app) endPinch(m *mirror, id string) {
	if p := m.pinch; p != nil {
		m.pinch = nil
		a.touch2(m, id, driver.TouchUp, p.a, p.b)
	}
}

// mirrorDrag moves the fingers of an Option-drag: one at the pointer, the
// other opposite it across the screen's centre.
func (a *app) mirrorDrag(m *mirror, id string, act driver.TouchAction, pt screen.Point) {
	q := screen.Point{X: m.picW - 1 - pt.X, Y: m.picH - 1 - pt.Y}
	switch act {
	case driver.TouchDown:
		m.pinch = &mirrorPinch{drag: true}
	case driver.TouchUp:
		m.pinch = nil
		a.touch2(m, id, act, pt, q)
		return
	}
	m.pinch.a, m.pinch.b = pt, q
	a.touch2(m, id, act, pt, q)
}

// mirrorZoom turns a touchpad's pinch into two fingers around the pointer:
// down when the pinch starts, apart or together by its scale, and up once
// it stops. Where they come as far apart or as close as they go, they lift
// and start again, so a long pinch keeps zooming.
func (a *app) mirrorZoom(m *mirror, id string, ev mg.InputEvent) {
	p := m.pinch
	if p == nil {
		p = m.zoomAt(m.toDevice(ev.X, ev.Y))
		p.span = p.lo
		if ev.Scale < 1 {
			p.span = p.hi
		}
		p.place()
		m.pinch = p
		a.touch2(m, id, driver.TouchDown, p.a, p.b)
	}
	span := p.span * float64(ev.Scale)
	if span > p.hi || span < p.lo {
		p.span = min(max(span, p.lo), p.hi)
		p.place()
		a.touch2(m, id, driver.TouchMove, p.a, p.b)
		a.touch2(m, id, driver.TouchUp, p.a, p.b)
		// Start again from the other end, as far past it as the pinch went.
		if span > p.hi {
			span = p.lo * span / p.hi
		} else {
			span = p.hi * span / p.lo
		}
		p.span = min(max(span, p.lo), p.hi)
		p.place()
		a.touch2(m, id, driver.TouchDown, p.a, p.b)
	} else {
		p.span = span
		p.place()
		a.touch2(m, id, driver.TouchMove, p.a, p.b)
	}
	p.seq++
	seq := p.seq
	time.AfterFunc(pinchLift, func() {
		a.post(func() {
			if m.pinch == p && p.seq == seq {
				a.endPinch(m, id)
			}
		})
	})
}

// zoomAt plans a touchpad's pinch at pt: its fingers move along the axis
// with more room, as far apart as the margins allow at most and a quarter
// of that at least. A point too near an edge moves in a little.
func (m *mirror) zoomAt(pt screen.Point) *mirrorPinch {
	w, h := float64(m.picW), float64(m.picH)
	lo := [2]float64{w * pinchMarginX, h * pinchMarginY}
	hi := [2]float64{w * (1 - pinchMarginX), h * (1 - pinchMarginY)}
	c := [2]float64{min(max(float64(pt.X), lo[0]), hi[0]), min(max(float64(pt.Y), lo[1]), hi[1])}
	room := func(i int) float64 { return min(c[i]-lo[i], hi[i]-c[i]) }
	axis := 0
	if room(1) > room(0) {
		axis = 1
	}
	// At least a sixth of the screen's shorter side on either side.
	least := min(w, h) / 6
	c[axis] = min(max(c[axis], lo[axis]+least), hi[axis]-least)
	most := 2 * room(axis)
	return &mirrorPinch{c: c, axis: axis, lo: most / 4, hi: most}
}

// place puts a touchpad pinch's fingers span apart around its centre.
func (p *mirrorPinch) place() {
	at := func(d float64) screen.Point {
		x, y := p.c[0], p.c[1]
		if p.axis == 0 {
			x += d
		} else {
			y += d
		}
		return screen.Point{X: int(x + 0.5), Y: int(y + 0.5)}
	}
	p.a, p.b = at(-p.span/2), at(p.span/2)
}

// drawFingers shows where the fingers of the person's pinch are, as
// Simulator does.
func drawFingers(pt *mg.Painter, r mg.Rect, m *mirror) {
	p := m.pinch
	if p == nil || m.picW == 0 || m.picH == 0 {
		return
	}
	sx, sy := r.W/float32(m.picW), r.H/float32(m.picH)
	for _, f := range []screen.Point{p.a, p.b} {
		x, y := r.X+float32(f.X)*sx, r.Y+float32(f.Y)*sy
		var dot mg.Path
		dot.Circle(x, y, 13)
		pt.FillPath(&dot, mg.RGBA(255, 255, 255, 0.35))
		pt.StrokePath(&dot, 1.5, mg.RGBA(255, 255, 255, 0.85))
	}
}
