package gateway

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// lagDev is a demo phone that, like an iPhone, keeps naming the app the
// agent left for a moment after it pressed home or opened another app.
type lagDev struct {
	*demo.Device
	lag   time.Duration
	mu    sync.Mutex
	stale *screen.Screen
	until time.Time
}

func (d *lagDev) Key(ctx context.Context, k driver.Key) error {
	return d.leave(ctx, func() error { return d.Device.Key(ctx, k) })
}

func (d *lagDev) Launch(ctx context.Context, app string) error {
	return d.leave(ctx, func() error { return d.Device.Launch(ctx, app) })
}

func (d *lagDev) leave(ctx context.Context, act func() error) error {
	before, err := d.Device.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		return err
	}
	if err := act(); err != nil {
		return err
	}
	d.mu.Lock()
	d.stale, d.until = before, time.Now().Add(d.lag)
	d.mu.Unlock()
	return nil
}

func (d *lagDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	d.mu.Lock()
	stale, until := d.stale, d.until
	d.mu.Unlock()
	if stale == nil || !time.Now().Before(until) {
		return d.Device.Observe(ctx, opt)
	}
	cp := *stale
	cp.Elements = append([]screen.Element(nil), stale.Elements...)
	cp.Events = nil
	cp.CapturedAt = time.Now()
	return &cp, nil
}

// Home from a blocked app returns the home screen even when the phone
// names the blocked app a little longer, and the next action is not
// refused as if the blocked app were in front (field test #25).
func TestHomeFromABlockedAppWaitsForTheLauncher(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &lagDev{Device: base, lag: 300 * time.Millisecond}
	g := newDevGateway(t, base, dev, func(o *Options) { o.SettleMax = time.Second })
	s, _ := connect(t, g, "Look around")
	base.Show("wallet") // Google Wallet, a blocked finance app

	res := s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"})
	mustStatus(t, res, StatusSettled)
	if res.Rule != "" || res.Screen.App.Name != "Pixel Launcher" {
		t.Fatalf("after home: rule %q, app %+v: %s", res.Rule, res.Screen.App, res.Message)
	}
	res = s.Act(ctx, Call{}, Action{Tool: "swipe", Direction: "up"})
	mustStatus(t, res, StatusSettled)

	// Opening another app from the blocked one waits the same way, also
	// when the launch does not read the screen first (launchUnread).
	base.Show("wallet")
	if res = s.Observe(ctx, ObserveRequest{}); res.Rule == "" {
		t.Fatalf("Wallet not blocked: %+v", res)
	}
	res = s.Act(ctx, Call{}, Action{Tool: "launch", App: "Calculator"})
	mustStatus(t, res, StatusSettled)
	if res.Rule != "" || res.Screen.App.Name != "Calculator" {
		t.Fatalf("after the launch: rule %q, app %+v: %s", res.Rule, res.Screen.App, res.Message)
	}
}

// Home on the home screen does not wait for an app to leave.
func TestHomeOnTheLauncherDoesNotWait(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &lagDev{Device: base, lag: time.Millisecond}
	g := newDevGateway(t, base, dev, func(o *Options) { o.SettleMax = 2 * time.Second })
	s, _ := connect(t, g, "Look around")
	start := time.Now()
	res := s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"})
	mustStatus(t, res, StatusSettled)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("home on the launcher took %v", d)
	}
}
