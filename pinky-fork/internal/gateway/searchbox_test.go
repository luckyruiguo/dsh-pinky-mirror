package gateway

import (
	"context"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// An app that draws its own search box shows only its placeholder (Xianyu,
// GitHub issue #76): typing by its ref taps it and types, read-only mode
// included, while other text still is no field.
func TestTypeIntoSearchPlaceholder(t *testing.T) {
	g, dev := newTestGateway(t)
	ctx := context.Background()
	s, _ := connect(t, g, "Search")
	dev.Show("settings")
	dev.SetOverlay([]screen.Element{
		{Role: screen.RoleText, Label: "搜索宝贝/帖子/用户", Source: screen.SourceTree, Bounds: screen.Rect{X: 120, Y: 160, W: 700, H: 80}},
	})
	if err := g.SetReadOnly("pixel", true); err != nil {
		t.Fatal(err)
	}
	obs := s.Observe(ctx, ObserveRequest{})
	before := len(dev.Inputs())
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "type", Ref: ref(t, obs, "搜索宝贝/帖子/用户"), Text: "奥特曼"}), StatusSettled)
	ins := dev.Inputs()[before:]
	if len(ins) != 2 || ins[0].Kind != "tap" || ins[1].Kind != "type" || ins[1].Text != "奥特曼" {
		t.Fatalf("inputs %+v", ins)
	}
	res := s.Act(ctx, Call{}, Action{Tool: "type", Ref: ref(t, s.Observe(ctx, ObserveRequest{}), "Wi-Fi"), Text: "x"})
	if res.Error != ErrInvalidArgument {
		t.Fatalf("typing into a switch: %+v", res)
	}
}
