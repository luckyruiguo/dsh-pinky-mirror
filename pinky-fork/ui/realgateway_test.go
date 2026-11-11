package ui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/policy"
)

// TestApprovePaymentWithRealGateway runs the client on the real gateway
// with the scripted phones: an agent's payment waits for the person, and
// Allow in the right panel lets the tap go through.
func TestApprovePaymentWithRealGateway(t *testing.T) {
	providers, _ := demo.World()
	pol := policy.Default()
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	g, err := gateway.New(gateway.Options{Providers: providers, Policy: pol, TrustNewAgents: true, ApprovalTimeout: time.Minute,
		SettleMax: 100 * time.Millisecond, PollInterval: 5 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond, NoScreenshots: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	a := newApp(g, Options{NoTray: true, Shortcut: "-"})
	testHooks(t, a)
	a.videoErr = errors.New("no video in tests")
	var mu sync.Mutex
	var q []func()
	a.postHook = func(fn func()) { mu.Lock(); q = append(q, fn); mu.Unlock() }
	t.Cleanup(a.end)
	syncUI := func() {
		mu.Lock()
		qq := q
		q = nil
		mu.Unlock()
		for _, fn := range qq {
			fn()
		}
		a.devices, a.tasks = g.Devices(), g.Tasks()
		a.setNeeds(g.Needs())
		first := !a.loaded
		a.loaded = true
		a.afterRefresh(first)
	}

	ctx := context.Background()
	s := g.OpenSession(gateway.AgentInfo{Name: "Claude Code"})
	defer s.Close()
	res := s.Connect(ctx, gateway.ConnectRequest{Call: gateway.Call{Device: "fake-pixel-9"}, Task: "Lunch"})
	if res.IsError() {
		t.Fatal(res.Message)
	}
	ref := func(label string) int {
		for _, e := range res.Screen.Elements {
			if e.Label == label {
				return e.Ref
			}
		}
		t.Fatalf("no %q", label)
		return 0
	}
	res = s.Act(ctx, gateway.Call{}, gateway.Action{Tool: "launch", App: "Dinely"})
	for _, l := range []string{"Orders", "Reorder · Chicken rice bowl", "Cart · 1"} {
		r := s.Act(ctx, gateway.Call{}, gateway.Action{Tool: "tap", Ref: ref(l)})
		if r.IsError() {
			t.Fatalf("%s: %s", l, r.Message)
		}
		if r.Screen != nil {
			res = r
		}
	}
	done := make(chan *gateway.Result, 1)
	place := ref("Place order · $12.80")
	go func() { done <- s.Act(ctx, gateway.Call{}, gateway.Action{Tool: "tap", Ref: place}) }()
	for i := 0; ; i++ {
		syncUI()
		if len(a.needs) > 0 {
			break
		}
		if i > 200 {
			t.Fatal("no need")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if a.needs[0].Rule != "Payments" {
		t.Fatalf("need: %+v", a.needs[0])
	}
	tt := mg.NewTester(a.view, 1280, 820)
	if err := tt.Click("Allow"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for i := 0; i < 300; i++ {
		syncUI()
		select {
		case r := <-done:
			if r.IsError() || r.Status != gateway.StatusSettled {
				t.Fatalf("tap after Allow: %s %s %s", r.Status, r.Error, r.Message)
			}
			return
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the payment was not decided; needs: %d", len(g.Needs()))
}
