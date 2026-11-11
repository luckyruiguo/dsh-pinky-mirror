package gateway

import (
	"fmt"

	"github.com/DIYgod/pinky/internal/screen"
)

// A gesture by ref, or across the screen, must start and stay where the
// app takes touches. An element's bounds may reach under the on-screen
// keyboard or a system bar, which take the touch instead: a swipe on a
// search page's results list started on the keyboard and typed a
// character (GitHub issue #71). The gateway keeps such gestures to the
// part of the element that shows.

// A cover is a part of the screen where touches go to the keyboard or a
// system window instead of the app.
type cover struct {
	r        screen.Rect
	keyboard bool
}

// covers returns the covers of s. One that takes most of the screen is
// not counted: it is a panel the agent acts on, such as a keyboard's
// full-screen sheet, not something lying over the app.
func covers(s *screen.Screen) []cover {
	var out []cover
	add := func(r screen.Rect, kb bool) {
		if r.Empty() || (s.Width > 0 && s.Height > 0 && 4*r.W*r.H >= 3*s.Width*s.Height) {
			return
		}
		out = append(out, cover{r, kb})
	}
	if s.Keyboard {
		add(s.KeyboardFrame, true)
	}
	for _, r := range s.Covered {
		add(r, false)
	}
	return out
}

// clearPart returns the largest part of r that no cover lies on, and the
// covers it gave way to. A system bar that holds all of r is what r
// belongs to, such as the clock in the status bar, and leaves it as it
// is. Whatever lies wholly under the keyboard is the app's: a screen that
// lists the keyboard's own keys (Android's read of a keyboard panel over
// an empty app) has no KeyboardFrame.
func clearPart(s *screen.Screen, r screen.Rect) (part screen.Rect, cut []cover) {
	for _, c := range covers(s) {
		if (!c.keyboard && encloses(c.r, r)) || !overlaps(c.r, r) {
			continue
		}
		r = largestOutside(r, c.r)
		cut = append(cut, c)
		if r.Empty() {
			break
		}
	}
	return r, cut
}

// largestOutside returns the largest part of a that lies above, below,
// left or right of o.
func largestOutside(a, o screen.Rect) screen.Rect {
	var best screen.Rect
	for _, c := range []screen.Rect{
		{X: a.X, Y: a.Y, W: a.W, H: o.Y - a.Y},                     // above
		{X: a.X, Y: o.Y + o.H, W: a.W, H: a.Y + a.H - (o.Y + o.H)}, // below
		{X: a.X, Y: a.Y, W: o.X - a.X, H: a.H},                     // left
		{X: o.X + o.W, Y: a.Y, W: a.X + a.W - (o.X + o.W), H: a.H}, // right
	} {
		if c = intersect(c, a); c.W*c.H > best.W*best.H {
			best = c
		}
	}
	return best
}

// coverName names what lies over an element in a message: "the keyboard"
// when it is one of cut, else "a system bar".
func coverName(cut []cover) string {
	for _, c := range cut {
		if c.keyboard {
			return "the keyboard"
		}
	}
	return "a system bar"
}

// hideKeyboard is what a refusal asks the agent to do about cover.
func hideKeyboard(cover string) string {
	if cover == "the keyboard" {
		return "Hide the keyboard first (phone_key back on Android; on an iPhone tap the keyboard's Done or Search key, or outside the field), then try again"
	}
	return "Act on another element, or at x,y that show"
}

// minSwipeRun is the least room, as a part of the screen's height (or
// width, for a sideways swipe), that a swipe by ref needs: the finger
// moves across 60% of it, and less hardly scrolls at all.
const minSwipeRun = 10

// swipeArea returns where a swipe by ref on e, or across the screen when
// e is nil, moves in direction dir: the part of it clear of the keyboard
// and the system bars. It refuses when too little of it is clear.
func swipeArea(s *screen.Screen, e *screen.Element, dir string) (screen.Rect, *Result) {
	area := screen.Rect{W: s.Width, H: s.Height}
	what := "the screen"
	if e != nil {
		area, what = e.Bounds, fmt.Sprintf("[%d]", e.Ref)
	}
	part, cut := clearPart(s, area)
	if len(cut) == 0 {
		return area, nil
	}
	run, need := part.H, s.Height/minSwipeRun
	if dir == "left" || dir == "right" {
		run, need = part.W, s.Width/minSwipeRun
	}
	if part.Empty() || run < need {
		cover := coverName(cut)
		if part.Empty() {
			return part, errResult(ErrInvalidArgument, "%s lies under %s, which would take the swipe. Nothing was done. %s.", what, cover, hideKeyboard(cover))
		}
		return part, errResult(ErrInvalidArgument, "Too little of %s shows beside %s to swipe in (%d px). Nothing was done. %s.", what, cover, run, hideKeyboard(cover))
	}
	return part, nil
}

// touchable returns e with its bounds cut to the part clear of the
// keyboard and the system bars, for a tap or a drag by ref to land on.
// It refuses when no part of e is clear.
func touchable(s *screen.Screen, e *screen.Element) (screen.Element, *Result) {
	t := *e
	part, cut := clearPart(s, e.Bounds)
	if len(cut) == 0 {
		return t, nil
	}
	if part.Empty() {
		cover := coverName(cut)
		return t, errResult(ErrInvalidArgument, "[%d] lies under %s, which would take the touch. Nothing was done. %s.", e.Ref, cover, hideKeyboard(cover))
	}
	t.Bounds = part
	return t, nil
}
