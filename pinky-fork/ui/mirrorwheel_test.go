package ui

import (
	"math"
	"runtime"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestWheelGlide checks the timing of a wheel's notches: one on its own
// glides evenly over wheelGlideAlone and rests (no fling), the notches of
// a spin keep the finger moving without a pause until the last one's
// glide ends, which lifts it moving, and slow notches rest between.
func TestWheelGlide(t *testing.T) {
	t0 := time.Unix(1000, 0)
	ms := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Millisecond) }
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

	// A notch on its own.
	var g wheelGlide
	g.add(t0, 0, -300)
	if off, moving := g.at(t0); off != [2]float64{} || !moving {
		t.Fatalf("at the notch: %v %v, want no move yet", off, moving)
	}
	if off, moving := g.at(ms(45)); !near(off[1], -150) || !moving {
		t.Fatalf("halfway: %v %v, want -150 and moving", off, moving)
	}
	if off, moving := g.at(ms(89)); off[1] <= -300 || !moving {
		t.Fatalf("just before the end: %v %v", off, moving)
	}
	if off, moving := g.at(ms(90)); !near(off[1], -300) || moving {
		t.Fatalf("at the end: %v %v, want -300 and still", off, moving)
	}
	if g.spinning() {
		t.Error("a notch on its own flings")
	}

	// A spin: a notch every 60 ms.
	g = wheelGlide{}
	for i := range 4 {
		g.add(ms(60*i), 0, -300)
	}
	if !g.spinning() {
		t.Fatal("a spin does not fling")
	}
	end := 180 + int(60*wheelOverlap) // the last notch's glide
	prev := 0.0
	for n := 5; n < end; n += 5 {
		off, moving := g.at(ms(n))
		if !moving || off[1] >= prev {
			t.Fatalf("at %d ms: %v %v after %v, want moving on", n, off, moving, prev)
		}
		// The speed never drops below a notch over its glide.
		if step := prev - off[1]; n > 5 && step < 300/float64(wheelGlideAlone/time.Millisecond)*5-1e-6 {
			t.Fatalf("at %d ms the finger slows to %.1f px in 5 ms", n, step)
		}
		prev = off[1]
	}
	if off, moving := g.at(ms(end)); !near(off[1], -1200) || moving {
		t.Fatalf("after the spin: %v %v, want -1200 and still", off, moving)
	}

	// Slow notches rest between and do not fling.
	g = wheelGlide{}
	g.add(t0, 0, -300)
	if _, moving := g.at(ms(200)); moving {
		t.Error("moving between slow notches")
	}
	g.add(ms(400), 0, -300)
	if g.spinning() {
		t.Error("slow notches fling")
	}
	if off, _ := g.at(ms(400 + 90)); !near(off[1], -600) {
		t.Errorf("after two slow notches: %v, want -600", off)
	}

	// A very fast spin still glides each notch over wheelGlideMin.
	g = wheelGlide{}
	g.add(t0, 0, -300)
	g.add(ms(5), 0, -300)
	if g.segs[1].dur != wheelGlideMin {
		t.Errorf("a notch 5 ms after another glides over %v, want %v", g.segs[1].dur, wheelGlideMin)
	}
}

// TestWheelNotches checks that a notch is one on every platform.
func TestWheelNotches(t *testing.T) {
	for goos, d := range map[string]float32{"darwin": 40, "windows": 99, "linux": 50} {
		if n := wheelNotches(d, goos); math.Abs(n-1) > 1e-6 {
			t.Errorf("%s: %v DIPs are %v notches, want 1", goos, d, n)
		}
	}
}

// wheelNotchDIPs is what this platform's wheel sends a notch as.
func wheelNotchDIPs() float32 {
	switch runtime.GOOS {
	case "windows":
		return 99
	case "linux":
		return 50
	}
	return 40
}

// TestMirrorWheel scrolls the mirror with a mouse wheel: a notch puts one
// finger down and glides it in many small moves by its share of the
// screen, then rests before lifting it; the notches of a spin keep the
// same finger moving the whole way.
func TestMirrorWheel(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.sync()
	d := ta.devices[0]
	m := &mirror{a: ta.app, id: d.Info.ID, picW: 1080, picH: 2400, boxW: 270, boxH: 600}
	notch := wheelNotchDIPs()
	// run runs the client's posted work for a while, as its window would.
	run := func(d time.Duration) {
		for end := time.Now().Add(d); ; time.Sleep(time.Millisecond) {
			ta.mu.Lock()
			q := ta.q
			ta.q = nil
			ta.mu.Unlock()
			for _, fn := range q {
				fn()
			}
			if !time.Now().Before(end) {
				return
			}
		}
	}
	// pump runs it until the finger lifts, noting when each input arrived.
	pump := func(from int) ([]fakeapi.Input, []time.Time) {
		var seen []time.Time
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			run(0)
			ins := ta.api.Inputs()[from:]
			for len(seen) < len(ins) {
				seen = append(seen, time.Now())
			}
			if !m.scrolling && len(ins) > 0 && ins[len(ins)-1].Action == driver.TouchUp {
				return ins, seen
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("the finger never lifted")
		return nil, nil
	}
	check := func(ins []fakeapi.Input, notches int) {
		t.Helper()
		if ins[0].Action != driver.TouchDown || ins[0].Point.X != 540 || ins[0].Point.Y != 1600 {
			t.Fatalf("first input %+v, want down at the pointer, 540,1600", ins[0])
		}
		moves := 0
		y := ins[0].Point.Y
		for i, in := range ins[1 : len(ins)-1] {
			if in.Kind != "touch" || in.Action != driver.TouchMove {
				t.Fatalf("input %d: %+v, want a move", i+1, in)
			}
			if in.Point.Y > y || in.Point.X != 540 {
				t.Fatalf("input %d moves back or aside: %+v after y %d", i+1, in, y)
			}
			y = in.Point.Y
			moves++
		}
		if moves < 3*notches {
			t.Errorf("%d notches in %d moves, want them spread over more", notches, moves)
		}
		want := 1600 - notches*int(wheelNotch*2400)
		if y < want-2 || y > want+1 || ins[len(ins)-1].Point.Y != y {
			t.Errorf("the finger ends at y %d and lifts at %v, want %d", y, ins[len(ins)-1].Point, want)
		}
	}

	// A notch on its own glides, then rests before the finger lifts.
	ta.mirrorInput(m, d, mg.InputEvent{Kind: mg.InputScroll, X: 135, Y: 400, DY: notch})
	ins, at := pump(0)
	check(ins, 1)
	if rest := at[len(at)-1].Sub(at[len(at)-2]); rest < wheelHold-20*time.Millisecond {
		t.Errorf("the finger lifted %v after its last move, want it to rest %v (no fling)", rest, wheelHold)
	}

	// A spin: four notches 40 ms apart are one finger moving the whole way.
	n := len(ta.api.Inputs())
	for i := range 4 {
		if i > 0 {
			run(40 * time.Millisecond)
		}
		ta.mirrorInput(m, d, mg.InputEvent{Kind: mg.InputScroll, X: 135, Y: 400, DY: notch})
	}
	ins, _ = pump(n)
	check(ins, 4)

	// A touchpad's scroll is not glided: one move each.
	n = len(ta.api.Inputs())
	ta.mirrorInput(m, d, mg.InputEvent{Kind: mg.InputScroll, X: 135, Y: 400, DY: 10, Precise: true})
	ins, _ = pump(n)
	if len(ins) != 3 || ins[1].Point.Y != 1600-40 {
		t.Errorf("a touchpad's scroll sends %+v, want down, one move of 40 px and up", ins)
	}
}
