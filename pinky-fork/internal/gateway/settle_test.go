package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// movingDev is a demo phone whose screen never stops moving, as in a game
// or a video: it notes how long each action waits for the screen to
// settle, and the wait always runs out.
type movingDev struct {
	*demo.Device
	mu     sync.Mutex
	limits []time.Duration
}

func (d *movingDev) WaitSettled(ctx context.Context, limit time.Duration) (bool, error) {
	d.mu.Lock()
	d.limits = append(d.limits, limit)
	d.mu.Unlock()
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case <-t.C:
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// waits returns the limits asked for since the last call.
func (d *movingDev) waits() []time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.limits
	d.limits = nil
	return out
}

func settleMS(n int) *time.Duration {
	d := time.Duration(n) * time.Millisecond
	return &d
}

func sameWaits(t *testing.T, what string, got []time.Duration, want ...time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s waited %v for the screen to settle, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s waited %v for the screen to settle, want %v", what, got, want)
		}
	}
}

// settle_ms lowers the wait for a screen that never settles; it never
// raises it above SettleMax, and 0 reads the screen without waiting.
func TestSettleMSLowersTheWait(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &movingDev{Device: base}
	g := newDevGateway(t, base, dev, func(o *Options) { o.SettleMax = 400 * time.Millisecond })
	s, _ := connect(t, g, "Play a game")
	dev.waits()

	res := s.Act(ctx, Call{}, Action{Tool: "swipe", Direction: "up"})
	mustStatus(t, res, StatusSettled)
	if res.Settle != "timeout" {
		t.Fatalf("settle %q, want timeout", res.Settle)
	}
	sameWaits(t, "a swipe without settle_ms", dev.waits(), 400*time.Millisecond)

	res = s.Act(ctx, Call{}, Action{Tool: "swipe", Direction: "down", Settle: settleMS(50)})
	mustStatus(t, res, StatusSettled)
	if res.Settle != "timeout" {
		t.Fatalf("settle %q, want timeout", res.Settle)
	}
	sameWaits(t, "settle_ms 50", dev.waits(), 50*time.Millisecond)

	// The next action first waits out what settle_ms cut short (no more
	// than the 350 ms left), then its own limit: settle_ms never raises it.
	res = s.Act(ctx, Call{}, Action{Tool: "swipe", Direction: "up", Settle: settleMS(2000)})
	mustStatus(t, res, StatusSettled)
	waits := dev.waits()
	if len(waits) != 2 || waits[0] <= 0 || waits[0] > 350*time.Millisecond {
		t.Fatalf("settle_ms above SettleMax, after settle_ms 50, waited %v, want the rest of the earlier wait and then 400ms", waits)
	}
	sameWaits(t, "settle_ms above SettleMax", waits[1:], 400*time.Millisecond)

	res = s.Act(ctx, Call{}, Action{Tool: "swipe", Direction: "down", Settle: settleMS(0)})
	mustStatus(t, res, StatusSettled)
	if res.Settle != "timeout" {
		t.Fatalf("settle %q, want timeout", res.Settle)
	}
	sameWaits(t, "settle_ms 0", dev.waits())
}

func TestSettleMSOutOfRange(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &movingDev{Device: base}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Play a game")
	for _, n := range []int{-1, 3001} {
		res := s.Act(ctx, Call{}, Action{Tool: "swipe", Direction: "up", Settle: settleMS(n)})
		if res.Error != ErrInvalidArgument {
			t.Fatalf("settle_ms %d: %+v, want invalid_argument", n, res)
		}
	}
}

// Each step of a batch waits as long as its own settle_ms says, after the
// rest of a wait the step before cut short.
func TestSettleMSPerBatchStep(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &movingDev{Device: base}
	g := newDevGateway(t, base, dev, func(o *Options) { o.SettleMax = 300 * time.Millisecond })
	s, _ := connect(t, g, "Play a game")
	dev.waits()

	res := s.Batch(ctx, BatchRequest{Steps: []Action{
		{Tool: "swipe", Direction: "up", Settle: settleMS(20)},
		{Tool: "swipe", Direction: "down"},
		{Tool: "swipe", Direction: "up", Settle: settleMS(0)},
	}})
	if res.IsError() || res.Status != StatusSettled {
		t.Fatalf("batch: %+v", res)
	}
	// Step 2 waits out the 280 ms step 1 left, then its own 300 ms; step 3
	// owes nothing, as step 2 waited the whole limit.
	waits := dev.waits()
	if len(waits) != 3 || waits[1] <= 0 || waits[1] > 280*time.Millisecond {
		t.Fatalf("the batch waited %v for the screen to settle, want 20ms, the rest of 280ms, 300ms", waits)
	}
	sameWaits(t, "the batch", []time.Duration{waits[0], waits[2]}, 20*time.Millisecond, 300*time.Millisecond)
}

// laggyDev is a demo phone that takes lag to show what a tap or a launch
// did, as a real phone's transitions do: reads until then still show the
// screen from before, while the next input already lands on the new one.
// WaitSettled reports the screen settled once the change shows.
type laggyDev struct {
	*demo.Device
	lag time.Duration

	mu     sync.Mutex
	stale  *screen.Screen
	until  time.Time
	limits []time.Duration
	// tapErr is what the next tap returns after it went out.
	tapErr error
	// failRead makes the next read fail.
	failRead bool
}

func (d *laggyDev) behind(ctx context.Context, input func() error) error {
	before, err := d.Device.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		return err
	}
	if err := input(); err != nil {
		return err
	}
	before.Events = nil
	d.mu.Lock()
	d.stale, d.until = before, time.Now().Add(d.lag)
	d.mu.Unlock()
	return nil
}

func (d *laggyDev) Tap(ctx context.Context, p screen.Point, opt driver.TapOptions) error {
	if err := d.behind(ctx, func() error { return d.Device.Tap(ctx, p, opt) }); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	err := d.tapErr
	d.tapErr = nil
	return err
}

func (d *laggyDev) Launch(ctx context.Context, app string) error {
	return d.behind(ctx, func() error { return d.Device.Launch(ctx, app) })
}

func (d *laggyDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	d.mu.Lock()
	stale, until, fail := d.stale, d.until, d.failRead
	d.failRead = false
	d.mu.Unlock()
	if fail {
		return nil, errors.New("the read failed")
	}
	if stale == nil || !time.Now().Before(until) {
		return d.Device.Observe(ctx, opt)
	}
	cp := *stale
	cp.Elements = append([]screen.Element(nil), stale.Elements...)
	cp.CapturedAt = time.Now()
	return &cp, nil
}

func (d *laggyDev) WaitSettled(ctx context.Context, limit time.Duration) (bool, error) {
	d.mu.Lock()
	d.limits = append(d.limits, limit)
	left := time.Until(d.until)
	d.mu.Unlock()
	settled := left <= limit
	if !settled {
		left = limit
	}
	if left > 0 {
		t := time.NewTimer(left)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return settled, nil
}

// waits returns the limits asked for since the last call.
func (d *laggyDev) waits() []time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.limits
	d.limits = nil
	return out
}

// laggyCheckout is Dinely's order list with one item in the cart, on a
// phone that takes 300 ms to show what a tap did. It returns the point
// where Place order shows once the cart is open; on the list nothing is
// there.
func laggyCheckout(t *testing.T, opts ...func(*Options)) (*Gateway, *laggyDev, screen.Point) {
	t.Helper()
	ctx := context.Background()
	probe := demo.NewDevice("probe", "Probe", driver.Android)
	probe.Show("cart")
	probe.AddToCart()
	cart, _ := probe.Observe(ctx, driver.ObserveOptions{})
	var place screen.Point
	for _, e := range cart.Elements {
		if e.Label == "Place order · $12.80" {
			place = e.Bounds.Center()
		}
	}
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	base.Show("orders")
	base.AddToCart()
	list, _ := base.Observe(ctx, driver.ObserveOptions{})
	for _, e := range list.Elements {
		if e.Bounds.Contains(place) {
			t.Fatalf("%q is where Place order will be", e.Label)
		}
	}
	dev := &laggyDev{Device: base, lag: 300 * time.Millisecond}
	g := newDevGateway(t, base, dev, append([]func(*Options){func(o *Options) {
		o.SettleMax = time.Second
		o.MaxBlock = 100 * time.Millisecond
	}}, opts...)...)
	return g, dev, place
}

// notPlaced fails when the order went through.
func notPlaced(t *testing.T, dev *laggyDev) {
	t.Helper()
	if dev.Cart() != 1 || dev.Current() != "cart" {
		t.Fatalf("the order was placed without asking: cart %d, screen %q", dev.Cart(), dev.Current())
	}
}

// A batch step that ends early with settle_ms must not let the next step
// be judged against the screen from before it: tapping the cart and then
// where Place order appears asks for the payment (#26 review).
func TestSettleMSBatchStillAsksBeforePaying(t *testing.T) {
	ctx := context.Background()
	g, dev, place := laggyCheckout(t)
	s, first := connect(t, g, "Lunch")
	res := s.Batch(ctx, BatchRequest{Steps: []Action{
		{Tool: "tap", Ref: ref(t, first, "Cart · 1"), Settle: settleMS(0)},
		{Tool: "tap", Point: &place, Settle: settleMS(0)},
	}})
	mustStatus(t, res, StatusPending)
	if len(res.Steps) != 2 || res.Rule != "Payments" {
		t.Fatalf("batch: %+v", res)
	}
	notPlaced(t, dev)
}

// The same with two calls made in parallel: the second waits for the
// first, and must then read the screen the first one led to.
func TestSettleMSParallelCallsStillAskBeforePaying(t *testing.T) {
	ctx := context.Background()
	g, dev, place := laggyCheckout(t)
	s, first := connect(t, g, "Lunch")
	second := make(chan *Result, 1)
	var once sync.Once
	dev.OnInput(func(demo.Input) {
		once.Do(func() {
			go func() { second <- s.Act(ctx, Call{}, Action{Tool: "tap", Point: &place, Settle: settleMS(0)}) }()
		})
	})
	res := s.Act(ctx, Call{}, Action{Tool: "tap", Ref: ref(t, first, "Cart · 1"), Settle: settleMS(0)})
	mustStatus(t, res, StatusSettled)
	res = <-second
	mustStatus(t, res, StatusPending)
	if res.Rule != "Payments" {
		t.Fatalf("second call: %+v", res)
	}
	notPlaced(t, dev)
}

// A call that ends before the screen settles, without settle_ms, leaves the
// next call waiting for the screen the same way: one cancelled while it
// waits, and one whose tap went out but reported an error. Tapping the
// cart and then where Place order appears still asks for the payment.
func TestCallEndedBeforeSettlingStillAsksBeforePaying(t *testing.T) {
	ctx := context.Background()
	t.Run("cancelled", func(t *testing.T) {
		g, dev, place := laggyCheckout(t)
		s, first := connect(t, g, "Lunch")
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		dev.OnInput(func(demo.Input) { time.AfterFunc(20*time.Millisecond, cancel) })
		start := time.Now()
		s.Act(cctx, Call{}, Action{Tool: "tap", Ref: ref(t, first, "Cart · 1")})
		if time.Since(start) >= dev.lag {
			t.Fatal("the cancelled call waited for the change to show")
		}
		dev.OnInput(nil)
		res := s.Act(ctx, Call{}, Action{Tool: "tap", Point: &place})
		mustStatus(t, res, StatusPending)
		notPlaced(t, dev)
	})
	t.Run("input failed after it went out", func(t *testing.T) {
		g, dev, place := laggyCheckout(t)
		s, first := connect(t, g, "Lunch")
		dev.mu.Lock()
		dev.tapErr = errors.New("the phone did not confirm the tap")
		dev.mu.Unlock()
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "tap", Ref: ref(t, first, "Cart · 1")}), StatusUncertain)
		res := s.Act(ctx, Call{}, Action{Tool: "tap", Point: &place})
		mustStatus(t, res, StatusPending)
		notPlaced(t, dev)
	})
}

// A phone_batch step starts from the read that ended the step before only
// while no input went out since. Here a call made in parallel taps the cart
// between two steps and its read fails: the next step must read the screen
// again, not judge a tap where Place order appears against the order list.
func TestBatchDoesNotReuseAReadFromBeforeAnotherTap(t *testing.T) {
	ctx := context.Background()
	g, dev, place := laggyCheckout(t)
	s, first := connect(t, g, "Lunch")
	// batchStep runs a phone_batch step after the first, as Session.Batch
	// does; a real batch leaves no room to place the other call in between.
	batchStep := func(a Action) *Result {
		l, res := s.acquire("")
		if res != nil {
			t.Fatalf("acquire: %+v", res)
		}
		defer s.done(l)
		return g.finish(l, g.act(ctx, l, Call{reuse: true}, a))
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "swipe", Direction: "up"}), StatusSettled)
	dev.OnInput(func(demo.Input) {
		dev.mu.Lock()
		dev.failRead = true
		dev.mu.Unlock()
	})
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "tap", Ref: ref(t, first, "Cart · 1")}), StatusUncertain)
	dev.OnInput(nil)
	res := batchStep(Action{Tool: "tap", Point: &place})
	mustStatus(t, res, StatusPending)
	notPlaced(t, dev)
}

// A read that settle_ms made early says so: it may come before what the
// action did shows, so its "no change" proves nothing, and history does not
// show the picture from before the action as the one after it.
func TestSettleMSEarlyResult(t *testing.T) {
	ctx := context.Background()
	g, dev, _ := laggyCheckout(t, func(o *Options) { o.DataDir = t.TempDir(); o.NoScreenshots = false })
	s, first := connect(t, g, "Lunch")
	g.Flush()
	res := s.Act(ctx, Call{}, Action{Tool: "tap", Ref: ref(t, first, "Cart · 1")})
	mustStatus(t, res, StatusSettled)
	if res.Early || res.Settle != "" {
		t.Fatalf("a tap without settle_ms: %+v", res)
	}
	back := ref(t, res, "Back")
	g.Flush()

	res = s.Act(ctx, Call{}, Action{Tool: "tap", Ref: back, Settle: settleMS(0)})
	mustStatus(t, res, StatusSettled)
	if !res.Early || res.Settle != "timeout" || !strings.Contains(res.Message, "Read before the screen settled") {
		t.Fatalf("a tap read early: %+v", res)
	}
	if res.Full || len(res.Diff) != 0 {
		t.Fatalf("the read came after the change: %q", res.Diff)
	}
	if dev.Current() != "orders" {
		t.Fatalf("the tap did not go back: %s", dev.Current())
	}
	steps := toolSteps(t, g, "Lunch", "phone_tap")
	if len(steps) != 2 {
		t.Fatalf("steps %+v", steps)
	}
	if steps[0].Before == "" || steps[0].After == "" || steps[1].Before == "" {
		t.Fatalf("pictures missing: %+v", steps)
	}
	if steps[1].After != "" {
		t.Fatalf("the early step shows a picture after it: %+v", steps[1])
	}
}

// An action the person approved, or allowed for the task, waits for the
// screen to settle whatever its settle_ms says: the agent should see what
// it did before it decides to do it again, and a payment allowed for the
// task does not ask again. The approval_id may be repeated with another
// settle_ms.
func TestSettleMSIgnoredWhenThePersonAllows(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &movingDev{Device: base}
	g := newDevGateway(t, base, dev, func(o *Options) {
		o.SettleMax = 100 * time.Millisecond
		o.MaxBlock = 50 * time.Millisecond
	})
	s, _ := connect(t, g, "Lunch")
	cart := toCart(t, s)
	place := ref(t, cart, "Place order · $12.80")
	res := s.Act(ctx, Call{}, Action{Tool: "tap", Ref: place, Settle: settleMS(0)})
	mustStatus(t, res, StatusPending)
	if err := g.Decide(waitNeed(t, g).ID, AllowTask, ""); err != nil {
		t.Fatal(err)
	}
	dev.waits()
	res = s.Act(ctx, Call{ApprovalID: res.ApprovalID}, Action{Tool: "tap", Ref: place, Settle: settleMS(20)})
	mustStatus(t, res, StatusSettled)
	if base.Current() != "done" || res.Early {
		t.Fatalf("approved payment: screen %q, %+v", base.Current(), res)
	}
	sameWaits(t, "the approved payment", dev.waits(), 100*time.Millisecond)

	cart = toCart(t, s)
	dev.waits()
	res = s.Act(ctx, Call{}, Action{Tool: "tap", Ref: ref(t, cart, "Place order · $12.80"), Settle: settleMS(0)})
	mustStatus(t, res, StatusSettled)
	if base.Current() != "done" || res.Early {
		t.Fatalf("payment allowed for the task: screen %q, %+v", base.Current(), res)
	}
	sameWaits(t, "a payment allowed for the task", dev.waits(), 100*time.Millisecond)

	if !personAllowed(policy.Verdict{Decision: policy.Allow, Exempted: []string{"Payments"}}) {
		t.Fatal("an action the person always allows may cut its wait short")
	}
	if personAllowed(policy.Verdict{Decision: policy.Allow}) {
		t.Fatal("an allowed action may not cut its wait short")
	}
}

// phone_launch with settle_ms still waits for the app to come to the front
// (awaitLeave); only the wait for it to settle after that is cut short.
func TestSettleMSLaunchWaitsForTheApp(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &laggyDev{Device: base, lag: 150 * time.Millisecond}
	g := newDevGateway(t, base, dev, func(o *Options) { o.SettleMax = time.Second })
	s, _ := connect(t, g, "Lunch")
	dev.waits()
	res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely", Settle: settleMS(0)})
	mustStatus(t, res, StatusSettled)
	if res.Screen == nil || res.Screen.App.ID != demo.Dinely {
		t.Fatalf("the launch returned before Dinely came to the front: %+v", res.Screen)
	}
	if !res.Early {
		t.Fatalf("launch: %+v", res)
	}
	if w := dev.waits(); len(w) != 0 {
		t.Fatalf("the launch waited %v for the screen to settle, want no wait", w)
	}
}

// phone_observe's wait_ms waits for a change, then for the screen to settle
// up to the whole limit: settle_ms is for actions only.
func TestObserveWaitKeepsTheSettleLimit(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &movingDev{Device: base}
	g := newDevGateway(t, base, dev, func(o *Options) { o.SettleMax = 100 * time.Millisecond })
	s, _ := connect(t, g, "Lunch")
	dev.waits()
	time.AfterFunc(50*time.Millisecond, func() { base.Show("dinely") })
	mustStatus(t, s.Observe(ctx, ObserveRequest{WaitMS: 2000}), StatusSettled)
	sameWaits(t, "phone_observe with wait_ms", dev.waits(), 100*time.Millisecond)
}
