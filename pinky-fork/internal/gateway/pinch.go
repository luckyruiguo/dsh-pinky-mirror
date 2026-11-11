package gateway

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// Limits of phone_pinch.
const (
	MinPinchScale    = 0.25
	MaxPinchScale    = 4
	MaxPinchRotation = 180 // degrees, either way
	minPinchMS       = 200
	maxPinchMS       = 2000
	defaultPinch     = 500 * time.Millisecond
)

// The fingers of a pinch stay this far inside the screen's edges, as
// fractions of its width and height: clear of the status bar, the home
// indicator or navigation bar, and the edges where Android's back gesture
// starts.
const (
	pinchMarginX = 0.07
	pinchMarginY = 0.08
	// pinchFill is how much of the room around the centre the fingers use
	// at their widest.
	pinchFill = 0.9
	// minPinchSpan is the least room, in screen pixels, for the fingers at
	// their widest; less is no pinch at all.
	minPinchSpan = 160
)

// pinchArea is the part of a w × h screen the fingers of a pinch keep to.
func pinchArea(w, h int) screen.Rect {
	mx, my := int(float64(w)*pinchMarginX), int(float64(h)*pinchMarginY)
	return screen.Rect{X: mx, Y: my, W: w - 2*mx, H: h - 2*my}
}

// intersect returns the part of a that b covers too.
func intersect(a, b screen.Rect) screen.Rect {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	if x1 <= x0 || y1 <= y0 {
		return screen.Rect{}
	}
	return screen.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// pinchScale is the action's scale, 1 when it only turns.
func (a Action) pinchScale() float64 {
	if a.Scale == 0 {
		return 1
	}
	return a.Scale
}

// pinchGesture plans a pinch around c that keeps both fingers inside
// area: they lie on the line through c along which area leaves them the
// most room (over the whole turn, when they turn), as far apart at their
// widest as that room allows, and scale apart or together. ok is false
// when area leaves too little room around c.
func pinchGesture(area screen.Rect, c screen.Point, scale, rotate float64, dur time.Duration) (g driver.Pinch, ok bool) {
	if area.Empty() || !area.Contains(c) {
		return driver.Pinch{}, false
	}
	best, angle := -1.0, 0.0
	for deg := 0; deg < 180; deg += 5 {
		if r := pinchRoom(area, c, float64(deg), rotate); r > best+0.5 {
			best, angle = r, float64(deg)
		}
	}
	wide := 2 * best * pinchFill
	if wide < minPinchSpan {
		return driver.Pinch{}, false
	}
	narrow := wide / max(scale, 1/scale)
	g = driver.Pinch{Center: c, Angle: angle, Rotate: rotate, Duration: dur}
	if scale >= 1 {
		g.From, g.To = int(math.Round(narrow)), int(math.Round(wide))
	} else {
		g.From, g.To = int(math.Round(wide)), int(math.Round(narrow))
	}
	return g, true
}

// pinchRoom is how far from c both fingers may go, in opposite directions
// along the line at angle degrees, while staying inside area as the line
// turns by rotate degrees.
func pinchRoom(area screen.Rect, c screen.Point, angle, rotate float64) float64 {
	n := max(1, int(math.Ceil(math.Abs(rotate)/5)))
	room := math.Inf(1)
	for i := 0; i <= n; i++ {
		room = min(room, pinchReach(area, c, angle+rotate*float64(i)/float64(n)))
	}
	return room
}

// pinchReach is how far from c a point may go along the line at angle
// degrees, either way, and stay inside area.
func pinchReach(area screen.Rect, c screen.Point, angle float64) float64 {
	rad := angle * math.Pi / 180
	ux, uy := math.Abs(math.Cos(rad)), math.Abs(math.Sin(rad))
	dx := float64(min(c.X-area.X, area.X+area.W-1-c.X))
	dy := float64(min(c.Y-area.Y, area.Y+area.H-1-c.Y))
	r := math.Inf(1)
	if ux > 1e-9 {
		r = dx / ux
	}
	if uy > 1e-9 {
		r = min(r, dy/uy)
	}
	return r
}

// checkPinch validates phone_pinch's arguments.
func (a Action) checkPinch() *Result {
	bad := func(format string, args ...any) *Result { return errResult(ErrInvalidArgument, format, args...) }
	switch {
	case a.Ref > 0 && a.Point != nil:
		return bad("phone_pinch takes ref or x and y for its centre, not both.")
	case math.IsNaN(a.Scale) || a.Scale < 0 || (a.Scale != 0 && (a.Scale < MinPinchScale || a.Scale > MaxPinchScale)):
		return bad("scale must be between %g and %g: 2 zooms in to twice the size, 0.5 zooms out to half.", MinPinchScale, float64(MaxPinchScale))
	case math.IsNaN(a.Rotation) || math.Abs(a.Rotation) > MaxPinchRotation:
		return bad("rotation must be between -%d and %d degrees.", MaxPinchRotation, MaxPinchRotation)
	case a.pinchScale() == 1 && a.Rotation == 0:
		return bad("phone_pinch needs a scale other than 1 (above 1 zooms in, below 1 zooms out) or a rotation.")
	case a.DurationMS != 0 && (a.DurationMS < minPinchMS || a.DurationMS > maxPinchMS):
		return bad("duration_ms must be between %d and %d for phone_pinch.", minPinchMS, maxPinchMS)
	}
	return nil
}

// preparePinch plans a pinch on cur: around the centre of the element
// a.Ref, a.Point, or the middle of the screen, with the fingers inside the
// element and clear of the screen's edges. The rules judge it as a swipe.
func (p *prep) preparePinch(a Action, cur *screen.Screen, byRef func(int) (*screen.Element, *Result)) *Result {
	// The fingers keep clear of the keyboard and the system bars too. raw
	// is the room without them, to tell what leaves too little of it.
	raw := pinchArea(cur.Width, cur.Height)
	area, cut := clearPart(cur, raw)
	c, rawC := area.Center(), raw.Center()
	where := "the middle of the screen"
	switch {
	case a.Ref > 0:
		e, res := byRef(a.Ref)
		if res != nil {
			return res
		}
		p.target = e
		raw = intersect(raw, e.Bounds)
		area, cut = clearPart(cur, raw)
		c, rawC = area.Center(), raw.Center()
		where = fmt.Sprintf("[%d]", a.Ref)
		if area.Empty() && !raw.Empty() {
			cover := coverName(cut)
			return errResult(ErrInvalidArgument, "[%d] lies under %s, which would take the fingers. Nothing was done. %s.", a.Ref, cover, hideKeyboard(cover))
		}
	case a.Point != nil:
		if a.Point.X < 0 || a.Point.Y < 0 || (cur.Width > 0 && a.Point.X >= cur.Width) || (cur.Height > 0 && a.Point.Y >= cur.Height) {
			return errResult(ErrInvalidArgument, "%d,%d is outside the %dx%d screen.", a.Point.X, a.Point.Y, cur.Width, cur.Height)
		}
		c, rawC = *a.Point, *a.Point
		p.target = hitTest(cur, c)
		where = fmt.Sprintf("%d,%d", c.X, c.Y)
	default:
		p.target = hitTest(cur, c)
	}
	dur := defaultPinch
	if a.DurationMS > 0 {
		dur = time.Duration(a.DurationMS) * time.Millisecond
	}
	g, ok := pinchGesture(area, c, a.pinchScale(), a.Rotation, dur)
	if !ok && len(cut) > 0 {
		// The keyboard or a bar is to blame only when there would be room
		// without it; else the place is too small or too near the edges.
		if _, fits := pinchGesture(raw, rawC, a.pinchScale(), a.Rotation, dur); fits {
			cover := coverName(cut)
			return errResult(ErrInvalidArgument, "There is no room for two fingers around %s clear of %s. Nothing was done. %s.", where, cover, hideKeyboard(cover))
		}
	}
	if !ok {
		return errResult(ErrInvalidArgument, "There is no room for two fingers around %s, which is too small or too close to the screen's edges. Nothing was done. Pinch around a larger element or nearer the middle of the screen.", where)
	}
	p.pinch = &g
	p.point = c
	p.from, _ = g.At(0)
	p.to, _ = g.At(1)
	p.dur = dur
	return nil
}

// describePinch says a pinch for history: "Zoomed in on “Map”", "Zoomed
// out", "Turned “Photo” with two fingers". label is the target's, in
// quotes, or empty.
func describePinch(a Action, label string) string {
	switch s := a.pinchScale(); {
	case s > 1 && label != "":
		return "Zoomed in on " + label
	case s > 1:
		return "Zoomed in"
	case s < 1 && label != "":
		return "Zoomed out on " + label
	case s < 1:
		return "Zoomed out"
	case label != "":
		return "Turned " + label + " with two fingers"
	}
	return "Turned with two fingers"
}

// Touch2 is Touch for two fingers at once, for the person's pinch on the
// mirror: TouchDown puts them down at p and q, TouchMove moves them there,
// TouchUp lifts both. The person's input pauses the agent. A phone that
// cannot take two fingers returns driver.ErrUnsupported.
func (g *Gateway) Touch2(deviceID string, a driver.TouchAction, p, q screen.Point) error {
	if d, err := g.device(deviceID); err == nil {
		g.mu.Lock()
		switching := d.switching != ""
		g.mu.Unlock()
		if switching {
			return nil // lost while the phone moves to the helper (helperneed.go)
		}
	}
	d, dev, err := g.personInput(deviceID)
	if err != nil {
		return err
	}
	m, ok := dev.(driver.MultiToucher)
	if !ok {
		// An iPhone or iPad over CoreDevice moves to the helper, which
		// has a second finger; this gesture is lost (helperneed.go).
		if g.personPinchHelper(d, a) {
			return nil
		}
		return fmt.Errorf("pinching: %w", driver.ErrUnsupported)
	}
	ctx, cancel := context.WithTimeout(g.ctx, 10*time.Second)
	defer cancel()
	return m.Touch2(ctx, a, p, q)
}
