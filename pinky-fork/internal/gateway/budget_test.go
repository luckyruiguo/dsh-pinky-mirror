package gateway

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// slowDev is a demo phone whose reads take readDelay, as an iPhone's
// through WebDriverAgent over a busy Wi-Fi did, until the call gives up.
type slowDev struct {
	*demo.Device
	mu        sync.Mutex
	readDelay time.Duration
	taps      int
}

func (d *slowDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	d.mu.Lock()
	delay := d.readDelay
	d.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return d.Device.Observe(ctx, opt)
}

func (d *slowDev) Tap(ctx context.Context, p screen.Point, opt driver.TapOptions) error {
	d.mu.Lock()
	d.taps++
	d.mu.Unlock()
	return d.Device.Tap(ctx, p, opt)
}

func (d *slowDev) slow(delay time.Duration) {
	d.mu.Lock()
	d.readDelay = delay
	d.mu.Unlock()
}

func budgetGateway(t *testing.T, budget time.Duration) (*Session, *slowDev, *Result) {
	t.Helper()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &slowDev{Device: base}
	g := newDevGateway(t, base, dev, func(o *Options) { o.CallBudget = budget })
	s, first := connect(t, g, "Budget")
	return s, dev, first
}

// A call answers within its budget however slowly the phone reads: an
// action that ran says uncertain, a call that did nothing says so.
func TestCallBudget(t *testing.T) {
	const budget = 400 * time.Millisecond
	t.Run("read after the action", func(t *testing.T) {
		s, dev, first := budgetGateway(t, budget)
		r := ref(t, first, "Dinely")
		// The read before the tap is quick; the one after it is not.
		go func() {
			for {
				dev.mu.Lock()
				tapped := dev.taps > 0
				if tapped {
					dev.readDelay = time.Minute
				}
				dev.mu.Unlock()
				if tapped {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
		start := time.Now()
		res := tap(s, r)
		if took := time.Since(start); took > budget+time.Second {
			t.Fatalf("the call took %v", took)
		}
		mustStatus(t, res, StatusUncertain)
		if !strings.Contains(res.Message, "action was sent") || !strings.Contains(res.Message, "Observe") {
			t.Fatalf("message: %q", res.Message)
		}
	})
	t.Run("read before the action", func(t *testing.T) {
		s, dev, first := budgetGateway(t, budget)
		r := ref(t, first, "Dinely")
		dev.slow(time.Minute)
		start := time.Now()
		res := tap(s, r)
		if took := time.Since(start); took > budget+time.Second {
			t.Fatalf("the call took %v", took)
		}
		if res.Error != ErrInternal || !strings.Contains(res.Message, "Nothing was done") {
			t.Fatalf("result: %q %q", res.Error, res.Message)
		}
		dev.mu.Lock()
		taps := dev.taps
		dev.mu.Unlock()
		if taps != 0 {
			t.Fatalf("tapped %d times", taps)
		}
	})
	t.Run("observe", func(t *testing.T) {
		s, dev, _ := budgetGateway(t, budget)
		dev.slow(time.Minute)
		start := time.Now()
		res := s.Observe(context.Background(), ObserveRequest{})
		if took := time.Since(start); took > budget+time.Second {
			t.Fatalf("the call took %v", took)
		}
		if res.Error != ErrInternal || !strings.Contains(res.Message, "too slowly") {
			t.Fatalf("result: %q %q", res.Error, res.Message)
		}
	})
	t.Run("batch stops early", func(t *testing.T) {
		s, dev, first := budgetGateway(t, time.Second)
		r := ref(t, first, "Dinely")
		// Each step reads twice or more: a few steps fill the budget.
		dev.slow(150 * time.Millisecond)
		steps := make([]Action, 20)
		for i := range steps {
			steps[i] = Action{Tool: "swipe", Direction: "down", Ref: r}
		}
		start := time.Now()
		res := s.Batch(context.Background(), BatchRequest{Steps: steps})
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("the call took %v", took)
		}
		if res.IsError() || len(res.Steps) == 0 || len(res.Steps) == len(steps) {
			t.Fatalf("ran %d steps: %q %q", len(res.Steps), res.Error, res.Message)
		}
		if !strings.Contains(res.Message, "then stopped") || !strings.Contains(res.Message, "continue") {
			t.Fatalf("message: %q", res.Message)
		}
	})
}
