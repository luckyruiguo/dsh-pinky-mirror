package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestHiddenKeypadTaps: an app's passcode keypad drawn where screenshots do
// not reach (Trust, iOS) takes no taps, by ref, by point or in a batch, Face
// ID key included (field run of 2026-11-10: Trust counted that tap as a
// wrong passcode).
func TestHiddenKeypadTaps(t *testing.T) {
	g, dev := newTestGateway(t)
	s, _ := connect(t, g, "Bookkeeping")
	labels := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "FaceID", "0", "Delete"}
	var keys []screen.Element
	for i, l := range labels {
		keys = append(keys, screen.Element{Role: screen.RoleButton, Label: l, Source: screen.SourceTree,
			States: []string{screen.StateNotInScreenshot}, Bounds: screen.Rect{X: 100 + i%3*250, Y: 1300 + i/3*160, W: 250, H: 160}})
	}
	dev.SetOverlay(keys)
	obs := s.Observe(context.Background(), ObserveRequest{})
	blocked := func(what string, res *Result) {
		t.Helper()
		mustStatus(t, res, StatusBlocked)
		if res.Rule != policy.RuleCredentials || !strings.Contains(res.Message, "phone_handoff") {
			t.Fatalf("%s: %+v", what, res)
		}
	}
	blocked("FaceID by ref", tap(s, ref(t, obs, "FaceID")))
	blocked("digit by ref", tap(s, ref(t, obs, "5")))
	blocked("digit by point", s.Act(context.Background(), Call{}, Action{Tool: "tap", Point: &screen.Point{X: 220, Y: 1380}}))
	res := s.Batch(context.Background(), BatchRequest{Steps: []Action{{Tool: "tap", Ref: ref(t, obs, "0")}}})
	if len(res.Steps) != 1 || res.Steps[0].Status != StatusBlocked || res.Steps[0].Rule != policy.RuleCredentials {
		t.Fatalf("batch: %+v", res)
	}
	for _, in := range dev.Inputs() {
		if in.Kind == "tap" {
			t.Fatalf("a key was tapped: %+v", in)
		}
	}

	// The same keys in the screenshot are an ordinary keypad.
	for i := range keys {
		keys[i].States = nil
	}
	dev.SetOverlay(keys)
	obs = s.Observe(context.Background(), ObserveRequest{})
	mustStatus(t, tap(s, ref(t, obs, "5")), StatusSettled)
}
