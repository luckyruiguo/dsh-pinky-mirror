package gateway

import (
	"context"
	"testing"

	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestApprovalPoint checks where an approval says the finger goes, which
// the approval panel marks on its screenshot: a tap's point, a swipe's
// start and end, and nothing for a key.
func TestApprovalPoint(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t)
	if err := g.Policy().SetPreset(policy.PresetEvery); err != nil {
		t.Fatal(err)
	}
	s, _ := connect(t, g, "Calculator")
	dev.Show("calc")
	obs := s.Observe(ctx, ObserveRequest{})
	seven := ref(t, obs, "7")
	var bounds screen.Rect
	for _, e := range obs.Screen.Elements {
		if e.Ref == seven {
			bounds = e.Bounds
		}
	}
	w, h := obs.Screen.Width, obs.Screen.Height
	at := screen.Point{X: bounds.X + 3, Y: bounds.Y + 4}
	from, to := screen.Point{X: w / 2, Y: h * 3 / 4}, screen.Point{X: w / 2, Y: h / 4}
	up, upTo := swipeLine(screen.Rect{W: w, H: h}, "up")

	for _, c := range []struct {
		name      string
		act       Action
		point, to screen.Point
		target    screen.Rect
	}{
		{"tap by ref", Action{Tool: "tap", Ref: seven}, bounds.Center(), screen.Point{}, bounds},
		{"tap by point", Action{Tool: "tap", Point: &at}, at, screen.Point{}, bounds},
		{"swipe", Action{Tool: "swipe", From: &from, To: &to}, from, to, screen.Rect{}},
		{"swipe up", Action{Tool: "swipe", Direction: "up"}, up, upTo, screen.Rect{}},
		{"key", Action{Tool: "key", Key: "back"}, screen.Point{}, screen.Point{}, screen.Rect{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan NeedView, 1)
			answer(t, g, Deny, func(n NeedView) { done <- n })
			res := s.Act(ctx, Call{}, c.act)
			if res.IsError() || res.Status != StatusDenied {
				t.Fatalf("want approval_denied, got %q %q: %s", res.Status, res.Error, res.Message)
			}
			n := <-done
			if n.Point != c.point || n.To != c.to {
				t.Errorf("point %v, to %v; want %v, %v", n.Point, n.To, c.point, c.to)
			}
			if !c.target.Empty() && n.Target != c.target {
				t.Errorf("target %v, want %v", n.Target, c.target)
			}
		})
	}
}
