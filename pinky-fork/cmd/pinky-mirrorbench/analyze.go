package main

import (
	"math"
	"sort"
	"time"
)

// pic is one decoded picture of the stream.
type pic struct {
	at      time.Time // when its packet arrived
	ready   time.Time // when it was decoded and its planes copied, as the mirror has it
	bytes   int       // the packet's size
	changed bool      // differs from the picture before it
	repeat  bool      // the same as the picture before it, sample for sample
	diff    float64   // the share of samples that changed
	peak    int       // the largest change of a sample, in levels
	moved   int       // samples that moved past the threshold, changed or not
	top     float64   // the highest and lowest of them, as shares of the height
	bottom  float64
}

// trial is one drag and the pictures that came while it ran.
type trial struct {
	move  time.Time // the first move past the touch slop
	end   time.Time // the last move
	pics  []pic     // every picture from the drag's start to a while after its end
	app   int       // frames the app rendered meanwhile, when known (Android's gfxinfo); -1 otherwise
	appAt time.Duration
}

// trialResult sums up one drag.
type trialResult struct {
	// Latency runs from the first move past the slop to the first picture
	// that changed, by its packet's arrival; Ready to when the mirror has
	// the picture decoded.
	Latency, Ready time.Duration
	// FPS counts the pictures that changed per second while the content
	// moved; Sent counts every picture in the same span, changed or not.
	FPS, Sent float64
	// Gaps between pictures that changed, while the content moved.
	GapMedian, GapP95, GapMax time.Duration
	// Mbps is the stream's bit rate in the same span.
	Mbps float64
	// Drop60 and Drop120 are the shares of changed pictures a mirror
	// painting 60 or 120 times a second, at an even pace, would never
	// show, averaged over every phase of its paints against the pictures.
	Drop60, Drop120 float64
	// AppFPS is the frames a second the app itself drew, when known.
	AppFPS float64
	Moving int // pictures that changed in the span
}

// analyze measures one drag. The span the content moved in starts at the
// first picture that changed after the first move and ends at the last
// move plus the latency, when the last moved picture should come.
func analyze(t trial) (trialResult, bool) {
	var r trialResult
	first := -1
	for i, p := range t.pics {
		if p.changed && p.at.After(t.move) {
			first = i
			break
		}
	}
	if first < 0 {
		return r, false
	}
	r.Latency = t.pics[first].at.Sub(t.move)
	r.Ready = t.pics[first].ready.Sub(t.move)
	until := t.end.Add(r.Latency)
	var moving []pic
	for _, p := range t.pics[first:] {
		if p.at.After(until) {
			break
		}
		if p.changed {
			moving = append(moving, p)
		}
	}
	r.Moving = len(moving)
	if len(moving) < 2 {
		return r, true
	}
	a, b := moving[0].at, moving[len(moving)-1].at
	span := b.Sub(a)
	if span <= 0 {
		return r, true
	}
	// Every picture sent from the first that changed to the last, and
	// the bytes after the first.
	sent, bytes := 0, 0
	for _, p := range t.pics[first:] {
		if p.at.After(b) {
			break
		}
		sent++
		if p.at.After(a) {
			bytes += p.bytes
		}
	}
	r.FPS = float64(len(moving)-1) / span.Seconds()
	r.Sent = float64(sent-1) / span.Seconds()
	r.Mbps = float64(bytes*8) / span.Seconds() / 1e6
	gaps := make([]time.Duration, 0, len(moving)-1)
	for i := 1; i < len(moving); i++ {
		gaps = append(gaps, moving[i].at.Sub(moving[i-1].at))
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	r.GapMedian = gaps[len(gaps)/2]
	r.GapP95 = gaps[min(len(gaps)-1, int(math.Ceil(float64(len(gaps))*0.95))-1)]
	r.GapMax = gaps[len(gaps)-1]
	ready := make([]time.Time, len(moving))
	for i, p := range moving {
		ready[i] = p.ready
	}
	r.Drop60 = paintDrops(ready, time.Second/60)
	r.Drop120 = paintDrops(ready, time.Second/120)
	if t.app >= 0 && t.appAt > 0 {
		r.AppFPS = float64(t.app) / t.appAt.Seconds()
	}
	return r, true
}

// paintDrops is the share of pictures, ready at the given times, that a
// window painting every period would never show: a picture replaced by a
// newer one before the next paint took it. It averages over 32 phases of
// the paints against the pictures, since nothing ties the two clocks.
func paintDrops(ready []time.Time, period time.Duration) float64 {
	if len(ready) < 2 {
		return 0
	}
	const phases = 32
	t0 := ready[0]
	dropped := 0
	for ph := 0; ph < phases; ph++ {
		offset := period * time.Duration(ph) / phases
		// The paint that takes picture i is the first tick at or after
		// its ready time; two pictures taken by the same tick lose the
		// older.
		last := int64(math.MinInt64)
		for _, r := range ready {
			d := r.Sub(t0) - offset
			tick := int64(math.Ceil(float64(d) / float64(period)))
			if tick == last {
				dropped++
			}
			last = tick
		}
	}
	return float64(dropped) / float64(phases*len(ready))
}

// lumaDiff compares two luma samplings of the same size and returns the
// share of samples that moved by more than a threshold: video noise and an
// encoder refining a still picture stay under it, moved content does not.
func lumaDiff(a, b []byte) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 1
	}
	n := 0
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d > lumaThreshold || d < -lumaThreshold {
			n++
		}
	}
	return float64(n) / float64(len(a))
}

const (
	// lumaThreshold is the change in a luma sample that counts.
	lumaThreshold = 16
	// changedShare is the share of samples that must change for a
	// picture to count as changed: a few hundred of a phone's samples.
	changedShare = 0.002
)

// peakDiff returns the largest change of a sample between two luma
// samplings of the same size.
func peakDiff(a, b []byte) int {
	if len(a) != len(b) {
		return 255
	}
	peak := 0
	for i := range a {
		peak = max(peak, int(a[i])-int(b[i]), int(b[i])-int(a[i]))
	}
	return peak
}

// paintFor is how long the mirror paints after a picture it took
// (ui.mirrorPaintFor).
const paintFor = 250 * time.Millisecond

// painting returns how long, between from and to, the mirror would paint
// for these pictures: from each one it is handed until paintFor later. It
// is handed no repeats when it skips them, as the app's mirror does.
func painting(pics []pic, from, to time.Time, skipRepeats bool) time.Duration {
	var total time.Duration
	var end time.Time // the end of the span counted so far
	for _, p := range pics {
		if skipRepeats && p.repeat {
			continue
		}
		a, b := p.ready, p.ready.Add(paintFor)
		if a.Before(end) {
			a = end
		}
		if a.Before(from) {
			a = from
		}
		if b.After(to) {
			b = to
		}
		if b.After(a) {
			total += b.Sub(a)
			end = b
		}
	}
	return total
}

// movedRows finds the samples, in rows of cols, that moved by more than
// the threshold: how many, and the first and last row that holds one (-1
// for none). It tells where a picture that hardly changed changed.
func movedRows(a, b []byte, cols int) (n, top, bottom int) {
	top, bottom = -1, -1
	if len(a) != len(b) || cols <= 0 {
		return 0, top, bottom
	}
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d > lumaThreshold || d < -lumaThreshold {
			n++
			r := i / cols
			if top < 0 {
				top = r
			}
			bottom = r
		}
	}
	return n, top, bottom
}

// sample reads every step-th luma sample of each step-th row into dst.
func sample(dst []byte, plane []byte, stride, w, h, step int) []byte {
	dst = dst[:0]
	for y := 0; y < h; y += step {
		row := plane[y*stride:]
		for x := 0; x < w; x += step {
			dst = append(dst, row[x])
		}
	}
	return dst
}

// median returns the middle of the values; zero for none.
func median[T int | float64 | time.Duration](v []T) T {
	if len(v) == 0 {
		var zero T
		return zero
	}
	s := append([]T(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}
