package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestDragArguments checks what phone_drag refuses before it touches the
// phone, and that drag's arguments stay with phone_drag.
func TestDragArguments(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t)
	s, res := connect(t, g, "Home screen")
	calc := ref(t, res, "Calculator")
	from, to := screen.Point{X: 540, Y: 648}, screen.Point{X: 540, Y: 1600}
	five := 5001
	vias := make([]Waypoint, MaxDragVia+1)
	long := make([]Waypoint, MaxDragVia)
	for i := range long {
		long[i] = Waypoint{Point: to, PauseMS: MaxDragRestMS}
	}
	for _, c := range []struct {
		act  Action
		want string
	}{
		{Action{Tool: "drag", To: &to}, "either ref or from"},
		{Action{Tool: "drag", Ref: calc, From: &from, To: &to}, "either ref or from"},
		{Action{Tool: "drag", Ref: calc}, "needs to"},
		{Action{Tool: "drag", Ref: calc, To: &to, Via: vias}, "at most 8 points"},
		{Action{Tool: "drag", Ref: calc, To: &to, DurationMS: 50}, "duration_ms must be between 100 and 3000"},
		{Action{Tool: "drag", Ref: calc, To: &to, HoldMS: -1}, "hold_ms must be between 0 and 5000"},
		{Action{Tool: "drag", Ref: calc, To: &to, PauseMS: &five}, "pause_ms must be between"},
		{Action{Tool: "drag", Ref: calc, To: &to, Via: []Waypoint{{Point: to, PauseMS: five}}}, "via[0].pause_ms must be between"},
		{Action{Tool: "drag", Ref: calc, To: &to, Via: long, DurationMS: MaxDragMoveMS}, "the limit is 30 s"},
		{Action{Tool: "drag", Ref: calc, To: &screen.Point{X: 5000, Y: 10}}, "5000,10 is outside the 1080x2400 screen"},
		{Action{Tool: "drag", Ref: calc, To: &to, Via: []Waypoint{{Point: screen.Point{X: -1, Y: 900}}}}, "-1,900 is outside"},
		{Action{Tool: "swipe", Ref: calc, Direction: "up", HoldMS: 500}, "go with phone_drag only"},
		{Action{Tool: "swipe", Ref: calc, Direction: "up", Via: []Waypoint{{Point: to}}}, "go with phone_drag only"},
		{Action{Tool: "tap", Ref: calc, PauseMS: new(int)}, "go with phone_drag only"},
	} {
		res := s.Act(ctx, Call{}, c.act)
		if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, c.want) {
			t.Errorf("%+v: %q %q, want %q", c.act, res.Error, res.Message, c.want)
		}
	}
	for _, in := range dev.Inputs() {
		if in.Kind == "drag" || in.Kind == "swipe" || in.Kind == "tap" {
			t.Fatalf("a refused action ran: %+v", in)
		}
	}
}

// TestDrag drags on the demo phone: the finger presses the element or the
// point, history says what was dragged, and an approval shows every point
// the finger goes through.
func TestDrag(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t, func(o *Options) { o.DataDir = t.TempDir() })
	s, res := connect(t, g, "Tidy the home screen")
	calc := res.Screen.Elements[ref(t, res, "Calculator")-1]
	to := screen.Point{X: 540, Y: 1700}
	edge := screen.Point{X: 1070, Y: 1200}

	res = s.Act(ctx, Call{}, Action{Tool: "drag", Ref: calc.Ref, To: &to, HoldMS: 800, Via: []Waypoint{{Point: edge, PauseMS: 1000}}})
	mustStatus(t, res, StatusSettled)
	ins := dev.Inputs()
	if in := ins[len(ins)-1]; in.Kind != "drag" || in.P != calc.Bounds.Center() || in.To != to {
		t.Fatalf("input %+v, want a drag from %v to %v", in, calc.Bounds.Center(), to)
	}

	from := screen.Point{X: 200, Y: 2000} // nothing under it
	res = s.Act(ctx, Call{}, Action{Tool: "drag", From: &from, To: &to, DurationMS: 200})
	mustStatus(t, res, StatusSettled)

	// Every action asks: the card has the whole route, and its title
	// says the drag.
	if err := g.Policy().SetPreset(policy.PresetEvery); err != nil {
		t.Fatal(err)
	}
	asked := make(chan NeedView, 1)
	answer(t, g, Deny, func(n NeedView) { asked <- n })
	res = s.Act(ctx, Call{}, Action{Tool: "drag", Ref: calc.Ref, To: &to, Via: []Waypoint{{Point: edge}}})
	mustStatus(t, res, StatusDenied)
	n := <-asked
	if n.Point != calc.Bounds.Center() || n.To != to {
		t.Errorf("card: %v → %v", n.Point, n.To)
	}
	if want := []screen.Point{calc.Bounds.Center(), edge, to}; len(n.Path) != 3 || n.Path[0] != want[0] || n.Path[1] != want[1] || n.Path[2] != want[2] {
		t.Errorf("card path %v, want %v", n.Path, want)
	}
	if n.Title != "Drag “Calculator”?" {
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
		if st.Tool == "phone_drag" {
			texts = append(texts, st.Text)
			if st.Point.X == 0 && st.Point.Y == 0 {
				t.Errorf("step %q has no point", st.Text)
			}
		}
	}
	want := []string{"Dragged “Calculator”", "Dragged from 200,2000 to 540,1700", "Waiting for you: Dragged “Calculator”"}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("drag steps %q, want %q", texts, want)
	}
}

// TestDragRulesEveryPoint drags across the Dinely cart: the rules judge
// the elements under every point the finger goes through, so a drag that
// rests on Place order on its way, or lets go there, asks, and one that
// stays clear of it runs.
func TestDragRulesEveryPoint(t *testing.T) {
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
	if place.Empty() {
		t.Fatal("no Place order button")
	}
	on := place.Center()
	// A point under nothing, or under something that is not the button.
	var off screen.Point
	for y := 300; y < cart.Screen.Height; y += 50 {
		p := screen.Point{X: 60, Y: y}
		if e := hitTest(cart.Screen, p); !place.Contains(p) && (e == nil || !strings.Contains(e.Label, "Place order")) {
			off = p
			break
		}
	}
	off2 := screen.Point{X: off.X + 40, Y: off.Y}

	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "drag", From: &off, To: &off2}), StatusSettled)

	for _, act := range []Action{
		{Tool: "drag", From: &off, To: &off2, Via: []Waypoint{{Point: on, PauseMS: 200}}},
		{Tool: "drag", From: &off, To: &on},
	} {
		asked := make(chan NeedView, 1)
		answer(t, g, Deny, func(n NeedView) { asked <- n })
		res := s.Act(ctx, Call{}, act)
		mustStatus(t, res, StatusDenied)
		if n := <-asked; n.Rule != "Payments" {
			t.Fatalf("%+v asked under %q: %+v", act, n.Rule, n)
		}
	}
	drags := 0
	for _, in := range dev.Inputs() {
		if in.Kind == "drag" {
			drags++
		}
	}
	if drags != 1 {
		t.Fatalf("%d drags ran, want 1", drags)
	}
}

// TestDragBatch drags as a step of phone_batch.
func TestDragBatch(t *testing.T) {
	g, dev := newTestGateway(t)
	s, res := connect(t, g, "Batch")
	calc := ref(t, res, "Calculator")
	to := screen.Point{X: 540, Y: 1700}
	res = s.Batch(context.Background(), BatchRequest{Steps: []Action{
		{Tool: "drag", Ref: calc, To: &to, HoldMS: 600, Via: []Waypoint{{Point: screen.Point{X: 1070, Y: 1200}, PauseMS: 900}}},
		{Tool: "key", Key: "home"},
	}})
	mustStatus(t, res, StatusSettled)
	var got *demo.Input
	for _, in := range dev.Inputs() {
		if in.Kind == "drag" {
			got = &in
		}
	}
	if got == nil || got.To != to {
		t.Fatalf("no drag ran: %+v", dev.Inputs())
	}
}

// noDragDevice is a phone whose driver cannot drag.
type noDragDevice struct{ driver.Device }

type noDragProvider struct {
	*demo.Provider
	d noDragDevice
}

func (p noDragProvider) Open(context.Context, string) (driver.Device, error) { return p.d, nil }

// TestDragUnsupported drags on a phone that cannot: the agent hears so
// before anyone is asked, and nothing runs.
func TestDragUnsupported(t *testing.T) {
	dev := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	g, err := New(Options{
		Providers:      []driver.Provider{noDragProvider{demo.NewProvider(driver.Android, dev), noDragDevice{dev}}},
		Policy:         testPolicy(t),
		TrustNewAgents: true, NoScreenshots: true, DiscoverEvery: time.Hour, SettleMax: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if err := g.Policy().SetPreset(policy.PresetEvery); err != nil {
		t.Fatal(err)
	}
	s, res := connect(t, g, "Home screen")
	to := screen.Point{X: 540, Y: 1700}
	res = s.Act(context.Background(), Call{}, Action{Tool: "drag", Ref: ref(t, res, "Calculator"), To: &to})
	if res.Error != ErrUnsupported || !strings.Contains(res.Message, "cannot drag") {
		t.Fatalf("%q %q", res.Error, res.Message)
	}
	if len(g.Needs()) != 0 {
		t.Fatalf("asked: %+v", g.Needs())
	}
	for _, in := range dev.Inputs() {
		if in.Kind == "drag" {
			t.Fatalf("a drag ran: %+v", in)
		}
	}
}
