package gateway

import (
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// Limits of phone_drag.
const (
	MaxDragVia     = 8    // points between from and to
	MaxDragRestMS  = 5000 // hold_ms, pause_ms and each waypoint's pause_ms
	MinDragMoveMS  = 100  // duration_ms: each move from one point to the next
	MaxDragMoveMS  = 3000
	MaxDragTotalMS = 30000 // how long the finger may stay down in all

	defaultDragMove  = 500 * time.Millisecond
	defaultDragPause = 300 * time.Millisecond
)

// Waypoint is a point a drag passes through on its way to To, resting
// there for PauseMS: a rest near the screen's edge turns a home screen to
// its next page while the finger holds an app.
type Waypoint struct {
	screen.Point
	PauseMS int
}

// checkDrag validates phone_drag's arguments.
func (a Action) checkDrag() *Result {
	bad := func(format string, args ...any) *Result { return errResult(ErrInvalidArgument, format, args...) }
	rest := func(name string, ms int) *Result {
		if ms < 0 || ms > MaxDragRestMS {
			return bad("%s must be between 0 and %d.", name, MaxDragRestMS)
		}
		return nil
	}
	switch {
	case (a.Ref > 0) == (a.From != nil):
		return bad("phone_drag takes either ref or from for where the finger presses.")
	case a.To == nil:
		return bad("phone_drag needs to: where the finger lets go.")
	case len(a.Via) > MaxDragVia:
		return bad("via takes at most %d points.", MaxDragVia)
	case a.DurationMS != 0 && (a.DurationMS < MinDragMoveMS || a.DurationMS > MaxDragMoveMS):
		return bad("duration_ms must be between %d and %d for phone_drag.", MinDragMoveMS, MaxDragMoveMS)
	}
	if res := rest("hold_ms", a.HoldMS); res != nil {
		return res
	}
	if a.PauseMS != nil {
		if res := rest("pause_ms", *a.PauseMS); res != nil {
			return res
		}
	}
	for i, w := range a.Via {
		if res := rest(fmt.Sprintf("via[%d].pause_ms", i), w.PauseMS); res != nil {
			return res
		}
	}
	if g := a.dragGesture(screen.Point{}); g.Duration() > MaxDragTotalMS*time.Millisecond {
		return bad("This drag keeps the finger down for %.1f s; the limit is %d s. Use fewer points, shorter moves or shorter rests.", g.Duration().Seconds(), MaxDragTotalMS/1000)
	}
	return nil
}

// dragGesture is the drag a.Via and a.To shape from the point from.
func (a Action) dragGesture(from screen.Point) driver.Drag {
	move := defaultDragMove
	if a.DurationMS > 0 {
		move = time.Duration(a.DurationMS) * time.Millisecond
	}
	pause := defaultDragPause
	if a.PauseMS != nil {
		pause = time.Duration(*a.PauseMS) * time.Millisecond
	}
	g := driver.Drag{From: from, Hold: time.Duration(a.HoldMS) * time.Millisecond}
	for _, w := range a.Via {
		g.Stops = append(g.Stops, driver.DragStop{Point: w.Point, Move: move, Pause: time.Duration(w.PauseMS) * time.Millisecond})
	}
	if a.To != nil {
		g.Stops = append(g.Stops, driver.DragStop{Point: *a.To, Move: move, Pause: pause})
	}
	return g
}

// prepareDrag plans a drag on cur: the finger presses the middle of the
// element a.Ref, or a.From, and every point it passes through must lie on
// the screen. Its target is the element it presses. The rules judge it as
// a swipe, against the elements under every one of its points (fingers).
func (p *prep) prepareDrag(a Action, cur *screen.Screen, platform driver.Platform, byRef func(int) (*screen.Element, *Result), inBounds func(screen.Point) bool) *Result {
	var from screen.Point
	if a.Ref > 0 {
		e, res := byRef(a.Ref)
		if res != nil {
			return res
		}
		t, res := touchable(cur, e)
		if res != nil {
			return res
		}
		p.target, from = e, tapPoint(cur, t, platform)
	} else {
		from = *a.From
		p.target = hitTest(cur, from)
	}
	g := a.dragGesture(from)
	for _, pt := range g.Points() {
		if !inBounds(pt) {
			return errResult(ErrInvalidArgument, "%d,%d is outside the %dx%d screen: every point of a drag must lie on it. Nothing was done.", pt.X, pt.Y, cur.Width, cur.Height)
		}
	}
	p.drag = &g
	// point is where the finger presses, without hasPt: the step that
	// waits for the person shows it, and an approval is stale when the
	// element under it moved.
	p.from, p.to, p.point = from, g.To(), from
	p.dur = g.Duration()
	return nil
}

// describeDrag says a drag for history: "Dragged “Maps”", or "Dragged
// from 100,200 to 300,400" when nothing labelled is under the finger.
// label is the target's, in quotes, or empty.
func describeDrag(a Action, p *prep, label string) string {
	if label != "" {
		return "Dragged " + label
	}
	if p != nil && p.drag != nil {
		return fmt.Sprintf("Dragged from %d,%d to %d,%d", p.from.X, p.from.Y, p.to.X, p.to.Y)
	}
	if a.From != nil && a.To != nil {
		return fmt.Sprintf("Dragged from %d,%d to %d,%d", a.From.X, a.From.Y, a.To.X, a.To.Y)
	}
	return "Dragged"
}
