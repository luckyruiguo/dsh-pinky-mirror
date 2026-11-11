package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestPinchGesture checks the fingers' path: inside the area all the way,
// as far apart as the room allows at their widest, apart for a zoom in and
// together for a zoom out, and no pinch where there is no room.
func TestPinchGesture(t *testing.T) {
	full := pinchArea(1080, 2400)
	for _, c := range []struct {
		name          string
		area          screen.Rect
		c             screen.Point
		scale, rotate float64
	}{
		{"zoom in", full, full.Center(), 2, 0},
		{"zoom out", full, full.Center(), 0.5, 0},
		{"most", full, full.Center(), MaxPinchScale, 0},
		{"turn", full, full.Center(), 1, 90},
		{"zoom and turn back", full, full.Center(), 1.5, -45},
		{"near the top", full, screen.Point{X: 540, Y: full.Y + 150}, 2, 0},
		{"near a side", full, screen.Point{X: full.X + 120, Y: 1200}, 0.5, 0},
		{"wide element", intersect(full, screen.Rect{X: 0, Y: 600, W: 1080, H: 300}), screen.Point{X: 540, Y: 750}, 3, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, ok := pinchGesture(c.area, c.c, c.scale, c.rotate, time.Second)
			if !ok {
				t.Fatal("no pinch")
			}
			if g.Center != c.c || g.Rotate != c.rotate {
				t.Fatalf("gesture %+v", g)
			}
			wide, narrow := max(g.From, g.To), min(g.From, g.To)
			if wide < minPinchSpan {
				t.Errorf("span %d at the widest", wide)
			}
			if s := max(c.scale, 1/c.scale); abs(float64(wide)/float64(narrow)-s) > 0.02 {
				t.Errorf("spans %d and %d for scale %g", g.From, g.To, c.scale)
			}
			if c.scale > 1 && g.To <= g.From || c.scale < 1 && g.To >= g.From {
				t.Errorf("from %d to %d for scale %g", g.From, g.To, c.scale)
			}
			for i := 0; i <= 40; i++ {
				a, b := g.At(float64(i) / 40)
				if !c.area.Contains(a) || !c.area.Contains(b) {
					t.Fatalf("at %d/40 the fingers are at %v and %v, outside %v", i, a, b, c.area)
				}
			}
		})
	}

	// Too little room: a small element, or a point at the edge.
	if g, ok := pinchGesture(screen.Rect{X: 100, Y: 100, W: 120, H: 120}, screen.Point{X: 160, Y: 160}, 2, 0, time.Second); ok {
		t.Errorf("pinch inside a small element: %+v", g)
	}
	if g, ok := pinchGesture(full, screen.Point{X: full.X + 5, Y: full.Y + 5}, 2, 0, time.Second); ok {
		t.Errorf("pinch in a corner: %+v", g)
	}
	if _, ok := pinchGesture(full, screen.Point{X: 5, Y: 5}, 2, 0, time.Second); ok {
		t.Error("pinch outside the area")
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// TestPinchArguments checks what phone_pinch refuses before it touches
// the phone.
func TestPinchArguments(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t)
	s, res := connect(t, g, "Settings")
	clock := ref(t, res, "9:41")
	pt := screen.Point{X: 500, Y: 1200}
	for _, c := range []struct {
		act  Action
		want string
	}{
		{Action{Tool: "pinch", Ref: clock, Point: &pt, Scale: 2}, "not both"},
		{Action{Tool: "pinch", Scale: 5}, "scale must be between 0.25 and 4"},
		{Action{Tool: "pinch", Scale: 0.1}, "scale must be between"},
		{Action{Tool: "pinch", Scale: -1}, "scale must be between"},
		{Action{Tool: "pinch", Rotation: 200}, "rotation must be between -180 and 180"},
		{Action{Tool: "pinch"}, "needs a scale other than 1"},
		{Action{Tool: "pinch", Scale: 1}, "needs a scale other than 1"},
		{Action{Tool: "pinch", Scale: 2, DurationMS: 50}, "duration_ms must be between 200 and 2000"},
		{Action{Tool: "pinch", Scale: 2, Point: &screen.Point{X: 5000, Y: 10}}, "outside the"},
		{Action{Tool: "pinch", Scale: 2, Ref: clock}, "no room for two fingers around ["}, // the clock is in the status bar
	} {
		res := s.Act(ctx, Call{}, c.act)
		if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, c.want) {
			t.Errorf("%+v: %q %q, want %q", c.act, res.Error, res.Message, c.want)
		}
	}
	for _, in := range dev.Inputs() {
		if in.Kind == "pinch" {
			t.Fatalf("a refused pinch ran: %+v", in)
		}
	}
}

// TestPinch pinches the demo phone: the fingers touch down where the
// gesture plans, history says what it did, and the approval card has both
// fingers.
func TestPinch(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t, func(o *Options) { o.DataDir = t.TempDir() })
	s, res := connect(t, g, "Settings")
	w, h := res.Screen.Width, res.Screen.Height
	area := pinchArea(w, h)

	res = s.Act(ctx, Call{}, Action{Tool: "pinch", Scale: 2})
	mustStatus(t, res, StatusSettled)
	want, _ := pinchGesture(area, area.Center(), 2, 0, defaultPinch)
	a, b := want.At(0)
	ins := dev.Inputs()
	if in := ins[len(ins)-1]; in.Kind != "pinch" || in.P != a || in.To != b {
		t.Fatalf("input %+v, want a pinch from %v and %v", in, a, b)
	}

	pt := screen.Point{X: w / 2, Y: h / 3}
	res = s.Act(ctx, Call{}, Action{Tool: "pinch", Point: &pt, Scale: 0.5, Rotation: 30, DurationMS: 300})
	mustStatus(t, res, StatusSettled)

	// Every action asks: the card has both fingers, and its title says
	// the pinch.
	if err := g.Policy().SetPreset(policy.PresetEvery); err != nil {
		t.Fatal(err)
	}
	asked := make(chan NeedView, 1)
	answer(t, g, Deny, func(n NeedView) { asked <- n })
	res = s.Act(ctx, Call{}, Action{Tool: "pinch", Scale: 3})
	mustStatus(t, res, StatusDenied)
	n := <-asked
	want, _ = pinchGesture(area, area.Center(), 3, 0, defaultPinch)
	a, b = want.At(0)
	a1, b1 := want.At(1)
	if n.Point != a || n.Point2 != b || n.To != a1 || n.To2 != b1 {
		t.Errorf("card: fingers %v→%v and %v→%v, want %v→%v and %v→%v", n.Point, n.To, n.Point2, n.To2, a, a1, b, b1)
	}
	if !strings.HasPrefix(n.Title, "Zoomed in") || !strings.HasSuffix(n.Title, "?") {
		t.Errorf("title %q", n.Title)
	}

	s.Release(ctx, ReleaseRequest{})
	g.Flush()
	tasks, err := g.History(ctx, HistoryQuery{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("history: %v %v", tasks, err)
	}
	var texts []string
	for _, st := range tasks[0].Steps {
		if st.Tool == "phone_pinch" {
			texts = append(texts, st.Text)
			if st.Point.X == 0 && st.Point.Y == 0 {
				t.Errorf("step %q has no point", st.Text)
			}
		}
	}
	if len(texts) != 3 || !strings.HasPrefix(texts[0], "Zoomed in") || !strings.HasPrefix(texts[1], "Zoomed out") {
		t.Fatalf("pinch steps %q", texts)
	}
}

// TestPinchRulesBothFingers pinches near Place order: the rules judge the
// elements under both fingers, so a pinch whose centre is elsewhere asks
// when either finger touches down on Pay.
func TestPinchRulesBothFingers(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t)
	s, _ := connect(t, g, "Lunch")
	cart := toCart(t, s)
	var place screen.Rect
	for _, e := range cart.Screen.Elements {
		if e.Label == "Place order · $12.80" {
			place = e.Bounds
		}
	}
	area := pinchArea(cart.Screen.Width, cart.Screen.Height)

	// A centre off the button with a finger on it, and one with neither.
	var on, off *screen.Point
	for y := area.Y; y < area.Y+area.H && (on == nil || off == nil); y += 20 {
		c := screen.Point{X: area.Center().X, Y: y}
		if place.Contains(c) {
			continue
		}
		pg, ok := pinchGesture(area, c, 0.5, 0, defaultPinch)
		if !ok {
			continue
		}
		a, b := pg.At(0)
		switch hit := place.Contains(a) || place.Contains(b); {
		case hit && on == nil:
			on = &c
		case !hit && off == nil && hitTest(cart.Screen, c) != nil && !strings.Contains(hitTest(cart.Screen, c).Label, "Place order"):
			off = &c
		}
	}
	if on == nil || off == nil {
		t.Fatalf("no centre found: on %v, off %v, button %v", on, off, place)
	}

	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "pinch", Point: off, Scale: 0.5}), StatusSettled)

	asked := make(chan NeedView, 1)
	answer(t, g, Deny, func(n NeedView) { asked <- n })
	res := s.Act(ctx, Call{}, Action{Tool: "pinch", Point: on, Scale: 0.5})
	mustStatus(t, res, StatusDenied)
	if n := <-asked; n.Rule != "Payments" {
		t.Fatalf("asked under %q: %+v", n.Rule, n)
	}
	pinches := 0
	for _, in := range dev.Inputs() {
		if in.Kind == "pinch" {
			pinches++
		}
	}
	if pinches != 1 {
		t.Fatalf("%d pinches ran, want 1", pinches)
	}
}
