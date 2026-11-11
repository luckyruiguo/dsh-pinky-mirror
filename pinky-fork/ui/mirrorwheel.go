package ui

import (
	"math"
	"time"
)

// A mouse wheel scrolls in notches, each one event with a fixed distance,
// while a phone scrolls by a finger that moves. Moving the finger a whole
// notch at once made the content jump, and lifting it only once it had
// stood still left the phone no speed to fling with (issue 75). So a
// notch glides the finger over its distance (wheelGlide), the notches of a
// spin keep one finger down and moving, and the finger lifts while still
// moving when the wheel was spinning, so the phone flings as after a swipe.

const (
	// wheelNotch is how far a notch drags the content: this share of the
	// mirror's height, whatever its size, as the phone's content scales
	// with it.
	wheelNotch = 0.15
	// wheelTick is how often a gliding finger moves, about as often as
	// the phone samples a real one.
	wheelTick = 8 * time.Millisecond
	// A notch on its own glides over wheelGlideAlone. A notch that comes
	// within wheelSpinGap of the one before is part of a spin: it glides
	// over its gap and a bit more (wheelOverlap), so the finger keeps
	// moving until the next notch at the same pace, never shorter than
	// wheelGlideMin.
	wheelGlideAlone = 90 * time.Millisecond
	wheelGlideMin   = 50 * time.Millisecond
	wheelSpinGap    = 160 * time.Millisecond
	wheelOverlap    = 1.3
	// wheelHold is how long the finger rests after a notch on its own
	// before it lifts: long enough for the phone to see it stopped (no
	// fling), and for a slow next notch to go on with the same drag.
	wheelHold = 120 * time.Millisecond
)

// wheelNotches turns a wheel's scroll in DIPs into notches, by what each
// platform sends a notch as: on macOS a line of 40 DIPs (a notch is a
// line, more when the system speeds a fast spin up), on Windows 3 lines
// of 33 (its "lines to scroll" setting, 3 unless changed), and on Linux a
// step of 50 (MyGo's internal/darwin, internal/windows, internal/linux).
func wheelNotches(d float32, goos string) float64 {
	switch goos {
	case "windows":
		return float64(d) / 99
	case "linux":
		return float64(d) / 50
	}
	return float64(d) / 40
}

// wheelGlide is the finger of one wheel scroll: where the notches so far
// put it, from where it went down, as time passes.
type wheelGlide struct {
	segs []glideSeg
	done [2]float64    // the moves of the notches that finished
	last time.Time     // the latest notch
	gap  time.Duration // between the latest two notches; 0 for the first
}

// glideSeg is one notch's move: d spread evenly over dur from start.
type glideSeg struct {
	start time.Time
	dur   time.Duration
	d     [2]float64
}

// add starts a notch's move of dx, dy at now.
func (g *wheelGlide) add(now time.Time, dx, dy float64) {
	g.gap = 0
	if !g.last.IsZero() {
		g.gap = max(now.Sub(g.last), 0)
	}
	g.last = now
	dur := wheelGlideAlone
	if g.spinning() {
		dur = max(time.Duration(float64(g.gap)*wheelOverlap), wheelGlideMin)
	}
	g.segs = append(g.segs, glideSeg{start: now, dur: dur, d: [2]float64{dx, dy}})
}

// spinning reports whether the latest notch came soon after the one
// before: the wheel is spinning, and the finger lifts moving to fling.
func (g *wheelGlide) spinning() bool {
	return g.gap > 0 && g.gap <= wheelSpinGap
}

// at returns how far the finger has moved by now from where it went down,
// and whether it is still moving.
func (g *wheelGlide) at(now time.Time) (off [2]float64, moving bool) {
	keep := g.segs[:0]
	for _, s := range g.segs {
		f := float64(now.Sub(s.start)) / float64(s.dur)
		if f >= 1 {
			g.done[0] += s.d[0]
			g.done[1] += s.d[1]
			continue
		}
		keep = append(keep, s)
		f = math.Max(f, 0)
		off[0] += s.d[0] * f
		off[1] += s.d[1] * f
	}
	g.segs = keep
	off[0] += g.done[0]
	off[1] += g.done[1]
	return off, len(keep) > 0
}
