package driver

import (
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

// TestDragSteps checks the samples drivers play: down at From, a rest for
// the hold, moves that ease from stop to stop and end on it, a rest for
// each pause, and waits that add up to the drag's whole length.
func TestDragSteps(t *testing.T) {
	g := Drag{
		From: screen.Point{X: 100, Y: 1000},
		Hold: time.Second,
		Stops: []DragStop{
			{Point: screen.Point{X: 1050, Y: 1000}, Move: 400 * time.Millisecond, Pause: 1200 * time.Millisecond},
			{Point: screen.Point{X: 500, Y: 600}, Move: 300 * time.Millisecond},
		},
	}
	if g.To() != (screen.Point{X: 500, Y: 600}) {
		t.Errorf("To %v", g.To())
	}
	if pts := g.Points(); len(pts) != 3 || pts[0] != g.From || pts[1] != g.Stops[0].Point || pts[2] != g.To() {
		t.Errorf("Points %v", pts)
	}
	if g.Duration() != 2900*time.Millisecond {
		t.Errorf("Duration %v", g.Duration())
	}

	steps := g.Steps(10 * time.Millisecond)
	if steps[0] != (DragStep{P: g.From}) {
		t.Fatalf("first step %+v", steps[0])
	}
	if steps[1] != (DragStep{Wait: time.Second, P: g.From}) {
		t.Fatalf("hold %+v", steps[1])
	}
	// 1 down + 1 hold + 40 moves + 1 pause + 30 moves.
	if len(steps) != 73 {
		t.Fatalf("%d steps", len(steps))
	}
	var total time.Duration
	for i, s := range steps {
		total += s.Wait
		if s.Wait < 0 {
			t.Fatalf("step %d waits %v", i, s.Wait)
		}
	}
	if total != g.Duration() {
		t.Errorf("steps take %v, want %v", total, g.Duration())
	}
	if s := steps[41]; s.P != g.Stops[0].Point {
		t.Errorf("first leg ends at %v", s.P)
	}
	if s := steps[42]; s != (DragStep{Wait: 1200 * time.Millisecond, P: g.Stops[0].Point}) {
		t.Errorf("pause %+v", s)
	}
	if s := steps[len(steps)-1]; s.P != g.To() {
		t.Errorf("last step %+v", s)
	}
	// Easing: the first move is short, the middle one long, and the
	// finger never goes back.
	first, mid := steps[2].P.X-g.From.X, steps[22].P.X-steps[21].P.X
	if first <= 0 || first*4 > mid {
		t.Errorf("first move %d px, middle move %d px", first, mid)
	}
	for i := 3; i <= 41; i++ {
		if steps[i].P.X < steps[i-1].P.X {
			t.Fatalf("step %d goes back: %v after %v", i, steps[i].P, steps[i-1].P)
		}
	}
}

// TestDragStepsEdges checks a drag with no hold and no stops, and moves
// shorter than a step.
func TestDragStepsEdges(t *testing.T) {
	p := screen.Point{X: 10, Y: 20}
	if s := (Drag{From: p}).Steps(10 * time.Millisecond); len(s) != 1 || s[0] != (DragStep{P: p}) {
		t.Errorf("no stops: %+v", s)
	}
	q := screen.Point{X: 300, Y: 20}
	s := (Drag{From: p, Stops: []DragStop{{Point: q, Move: 3 * time.Millisecond}}}).Steps(10 * time.Millisecond)
	if len(s) != 2 || s[1] != (DragStep{Wait: 3 * time.Millisecond, P: q}) {
		t.Errorf("short move: %+v", s)
	}
	s = (Drag{From: p, Stops: []DragStop{{Point: q, Move: time.Second}}}).Steps(0)
	if len(s) != 2 || s[1] != (DragStep{Wait: time.Second, P: q}) {
		t.Errorf("no step: %+v", s)
	}
}
