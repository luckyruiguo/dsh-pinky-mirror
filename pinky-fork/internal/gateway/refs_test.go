package gateway

import (
	"context"
	"sync"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// flickerDev is a demo phone whose element ancestry changes on every read,
// as iOS apps report it when their views get re-wrapped: the same button,
// with the same label and place, under another path.
type flickerDev struct {
	*demo.Device
	mu    sync.Mutex
	reads int
}

func (f *flickerDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, err := f.Device.Observe(ctx, opt)
	if err != nil {
		return s, err
	}
	f.mu.Lock()
	f.reads++
	odd := f.reads%2 == 1
	f.mu.Unlock()
	if odd {
		for i := range s.Elements {
			s.Elements[i].Path += "/wrapper"
		}
	}
	return s, nil
}

// A tap on a ref from the last screen works when the fresh read gave that
// element another ref: its role, label, id and place are the same.
func TestRefSurvivesAncestryFlicker(t *testing.T) {
	ctx := context.Background()
	dev := &flickerDev{Device: demo.NewDevice("pixel", "Pixel 9", driver.Android)}
	pol := policy.Default()
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	g, err := New(Options{
		Providers: []driver.Provider{videoProvider{demo.NewProvider(driver.Android, dev.Device), dev}}, Policy: pol,
		TrustNewAgents: true, DataDir: t.TempDir(), SettleMax: 50e6, PollInterval: 5e6, DiscoverEvery: 3600e9,
		NoScreenshots: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	s, _ := connect(t, g, "Lunch")
	res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"})
	mustStatus(t, res, StatusSettled)
	orders := ref(t, res, "Orders")
	res = tap(s, orders)
	mustStatus(t, res, StatusSettled)
	if n := len(dev.Inputs()); n == 0 || dev.Inputs()[n-1].Kind != "tap" {
		t.Fatalf("the tap did not reach the phone: %+v", dev.Inputs())
	}
}

func TestRelocate(t *testing.T) {
	btn := func(ref int, label string, x int) screen.Element {
		return screen.Element{Ref: ref, Role: screen.RoleButton, Label: label, Source: screen.SourceTree,
			Bounds: screen.Rect{X: x, Y: 100, W: 80, H: 40}}
	}
	seen := &screen.Screen{Elements: []screen.Element{btn(7, "Left", 10), btn(8, "Filter", 500)}}
	cases := []struct {
		name string
		cur  []screen.Element
		want int // 0: not found
	}{
		{"renumbered", []screen.Element{btn(3, "Left", 12), btn(8, "Filter", 500)}, 3},
		{"moved away", []screen.Element{btn(3, "Left", 200)}, 0},
		{"relabelled", []screen.Element{btn(3, "Back", 10)}, 0},
		{"two candidates", []screen.Element{btn(3, "Left", 10), btn(4, "Left", 11)}, 0},
		{"now text", []screen.Element{{Ref: 5, Role: screen.RoleText, Label: "Left", Source: screen.SourceTree, Bounds: screen.Rect{X: 10, Y: 100, W: 80, H: 40}}}, 5},
		{"button and its text twin", []screen.Element{
			{Ref: 5, Role: screen.RoleText, Label: "Left", Source: screen.SourceTree, Bounds: screen.Rect{X: 10, Y: 100, W: 80, H: 40}},
			btn(6, "Left", 10)}, 6},
		{"text and other twins", []screen.Element{
			{Ref: 5, Role: screen.RoleText, Label: "Left", Source: screen.SourceTree, Bounds: screen.Rect{X: 10, Y: 100, W: 80, H: 40}},
			{Ref: 6, Role: screen.RoleOther, Label: "Left", Source: screen.SourceTree, Bounds: screen.Rect{X: 10, Y: 100, W: 80, H: 40}}}, 0},
		{"ocr only", []screen.Element{{Role: screen.RoleText, Label: "Left", Source: screen.SourceOCR, Bounds: screen.Rect{X: 10, Y: 100, W: 80, H: 40}}}, 0},
	}
	for _, c := range cases {
		e, ok := relocate(seen, &screen.Screen{Elements: c.cur}, 7)
		got := 0
		if ok {
			got = e.Ref
		}
		if got != c.want {
			t.Errorf("%s: got [%d], want [%d]", c.name, got, c.want)
		}
	}
	if _, ok := relocate(nil, &screen.Screen{}, 7); ok {
		t.Error("found an element with no screen seen")
	}
}
