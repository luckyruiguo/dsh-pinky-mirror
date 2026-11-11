package gateway

import (
	"context"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// Regression tests for races and bypasses found in review. hookDev lets a
// test block or change a read or screenshot at the worst moment.

type hookDev struct {
	*demo.Device
	mu        sync.Mutex
	onObserve func(s *screen.Screen) // may block
	onShot    func()                 // may block
}

func (h *hookDev) Observe(ctx context.Context, o driver.ObserveOptions) (*screen.Screen, error) {
	s, err := h.Device.Observe(ctx, o)
	h.mu.Lock()
	f := h.onObserve
	h.mu.Unlock()
	if err == nil && f != nil {
		f(s)
	}
	return s, err
}

func (h *hookDev) Screenshot(ctx context.Context) (image.Image, error) {
	h.mu.Lock()
	f := h.onShot
	h.mu.Unlock()
	if f != nil {
		f()
	}
	return h.Device.Screenshot(ctx)
}

func (h *hookDev) setObserve(f func(*screen.Screen)) { h.mu.Lock(); h.onObserve = f; h.mu.Unlock() }
func (h *hookDev) setShot(f func())                  { h.mu.Lock(); h.onShot = f; h.mu.Unlock() }

type hookProvider struct{ d *hookDev }

func (p hookProvider) Platform() driver.Platform { return driver.Android }
func (p hookProvider) List(context.Context) ([]driver.Info, error) {
	return []driver.Info{p.d.Info()}, nil
}
func (p hookProvider) Open(context.Context, string) (driver.Device, error) { return p.d, nil }

func newHookGateway(t *testing.T, opts ...func(*Options)) (*Gateway, *hookDev) {
	t.Helper()
	hd := &hookDev{Device: demo.NewDevice("pixel", "Pixel 9", driver.Android)}
	pol := testPolicy(t)
	o := Options{
		Providers: []driver.Provider{hookProvider{hd}}, Policy: pol, TrustNewAgents: true,
		ApprovalTimeout: 3 * time.Second, MaxBlock: 2 * time.Second, SettleMax: 200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond, NoScreenshots: true,
	}
	for _, f := range opts {
		f(&o)
	}
	g, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g, hd
}

// blockOnce makes the next call of a hook wait until release is called.
func blockOnce() (hook func(), entered <-chan struct{}, release func()) {
	in, out := make(chan struct{}), make(chan struct{})
	var once sync.Once
	return func() { once.Do(func() { close(in); <-out }) }, in, func() { close(out) }
}

func TestTakeoverSurvivesNewTask(t *testing.T) {
	g, dev := newTestGateway(t)
	ctx := context.Background()
	s, _ := connect(t, g, "Lunch")
	res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"})
	acct := ref(t, res, "Account")
	g.TakeOver("pixel")
	mustStatus(t, tap(s, acct), StatusTookOver)

	// Another task name (refused: it never ends the running task), release
	// and connect, or another session: the person stays in control.
	if res := s.Connect(ctx, ConnectRequest{Task: "Something else"}); res.Error != ErrLeaseHeld {
		t.Fatalf("another task: %+v", res)
	}
	mustStatus(t, s.Act(ctx, Call{Device: "pixel"}, Action{Tool: "tap", Ref: acct}), StatusPaused)
	s.Release(ctx, ReleaseRequest{Device: "pixel"})
	if v := g.Devices()[0]; v.Status != DeviceInControl {
		t.Fatalf("after release: %s", v.Status)
	}
	mustStatus(t, s.Connect(ctx, ConnectRequest{Task: "Something else"}), StatusPaused)
	s.Release(ctx, ReleaseRequest{Device: "pixel"})
	other := g.OpenSession(AgentInfo{Name: "Codex"})
	defer other.Close()
	mustStatus(t, other.Connect(ctx, ConnectRequest{Task: "Sneaky"}), StatusPaused)
	mustStatus(t, tap(other, acct), StatusPaused)
	if dev.Current() != "dinely" {
		t.Fatalf("an agent acted while the person was in control: %s", dev.Current())
	}
	// Only hand back returns control.
	g.HandBack("pixel", "")
	mustStatus(t, tap(other, acct), StatusSettled)
}

func TestTakeoverDuringInflightAction(t *testing.T) {
	g, hd := newHookGateway(t)
	s, _ := connect(t, g, "Lunch")
	res := s.Act(context.Background(), Call{}, Action{Tool: "launch", App: "Dinely"})
	acct := ref(t, res, "Account")
	hook, entered, release := blockOnce()
	hd.setObserve(func(*screen.Screen) { hook() })
	done := make(chan *Result, 1)
	go func() { done <- tap(s, acct) }()
	<-entered
	g.TakeOver("pixel")
	hd.setObserve(nil)
	release()
	res = <-done
	mustStatus(t, res, StatusTookOver)
	if hd.Current() != "dinely" {
		t.Fatalf("the tap ran after the person took over: %s", hd.Current())
	}
}

func TestStopDuringInflightAction(t *testing.T) {
	g, hd := newHookGateway(t)
	s, _ := connect(t, g, "Lunch")
	res := s.Act(context.Background(), Call{}, Action{Tool: "launch", App: "Dinely"})
	acct := ref(t, res, "Account")
	hook, entered, release := blockOnce()
	hd.setObserve(func(*screen.Screen) { hook() })
	done := make(chan *Result, 1)
	go func() { done <- tap(s, acct) }()
	<-entered
	g.Stop("pixel")
	hd.setObserve(nil)
	release()
	mustStatus(t, <-done, StatusStopped)
	if hd.Current() != "dinely" {
		t.Fatalf("the tap ran after Stop: %s", hd.Current())
	}
}

func TestNoOrphanApprovalAfterStop(t *testing.T) {
	g, hd := newHookGateway(t)
	ctx := context.Background()
	s, _ := connect(t, g, "Lunch")
	cart := toCart(t, s)
	hook, entered, release := blockOnce()
	hd.setShot(hook)
	done := make(chan *Result, 1)
	go func() { done <- tap(s, ref(t, cart, "Place order · $12.80")) }()
	<-entered
	g.Stop("pixel")
	hd.setShot(nil)
	release()
	mustStatus(t, <-done, StatusStopped)
	if needs := g.Needs(); len(needs) != 0 {
		t.Fatalf("orphan approval after Stop: %+v", needs)
	}
	for _, tv := range g.Tasks() {
		if !tv.Ended.IsZero() && tv.State == TaskWaiting {
			t.Fatal("ended task shows waiting")
		}
	}
	s2 := g.OpenSession(AgentInfo{Name: "Other"})
	defer s2.Close()
	mustStatus(t, s2.Connect(ctx, ConnectRequest{Task: "New"}), StatusSettled)
	mustStatus(t, s2.Act(ctx, Call{}, Action{Tool: "key", Key: "back"}), StatusSettled)
}

func TestNoApprovalAfterTakeover(t *testing.T) {
	g, hd := newHookGateway(t)
	s, _ := connect(t, g, "Lunch")
	cart := toCart(t, s)
	hook, entered, release := blockOnce()
	hd.setShot(hook)
	done := make(chan *Result, 1)
	go func() { done <- tap(s, ref(t, cart, "Place order · $12.80")) }()
	<-entered
	g.TakeOver("pixel")
	hd.setShot(nil)
	release()
	mustStatus(t, <-done, StatusTookOver)
	if needs := g.Needs(); len(needs) != 0 {
		t.Fatalf("approval open while the person is in control: %+v", needs)
	}
}

func TestBlockedAppEventsNeverReachAgent(t *testing.T) {
	g, hd := newHookGateway(t)
	ctx := context.Background()
	s, _ := connect(t, g, "Lunch")
	hd.Show("wallet")
	var once sync.Once
	hd.setObserve(func(sc *screen.Screen) {
		if strings.Contains(sc.App.ID, "wallet") {
			once.Do(func() {
				sc.Events = append(sc.Events,
					screen.Event{Kind: screen.EventDialog, Text: "Sent $500.00 to Alex. Balance $4,210.33"},
					screen.Event{Kind: screen.EventToast, Text: "Balance $4,210.33"})
			})
		}
	})
	res := s.WaitFor(ctx, WaitForRequest{Text: "Balance", TimeoutMS: 100})
	if *res.Matched {
		t.Fatal("wait_for matched a blocked app's dialog")
	}
	res = s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Screen.Render(), "Balance") {
		t.Fatalf("blocked app's events shown after leaving it:\n%s", res.Screen.Render())
	}
}

func TestScreenshotNeverShowsBlockedApp(t *testing.T) {
	g, hd := newHookGateway(t)
	s, _ := connect(t, g, "Lunch")
	var once sync.Once
	hd.setShot(func() { once.Do(func() { hd.Show("wallet") }) })
	res := s.Observe(context.Background(), ObserveRequest{Screenshot: true})
	if res.Screenshot != nil {
		t.Fatal("screenshot taken while the wallet came to the front was returned")
	}
	if !strings.Contains(res.Message, "screen changed") {
		t.Fatalf("message: %q", res.Message)
	}
}

func TestChildInsideCommitButton(t *testing.T) {
	g, hd := newHookGateway(t, func(o *Options) { o.MaxBlock = 20 * time.Millisecond })
	s, _ := connect(t, g, "Lunch")
	cart := toCart(t, s)
	var btn screen.Element
	for _, e := range cart.Screen.Elements {
		if e.Label == "Place order · $12.80" {
			btn = e
		}
	}
	// The driver reports a price label inside the button.
	hd.setObserve(func(sc *screen.Screen) {
		for _, e := range sc.Elements {
			if e.Label == "Place order · $12.80" {
				sc.Elements = append(sc.Elements, screen.Element{Role: screen.RoleText, Label: "$12.80", Path: "cart/place",
					ID: "dinely:place:price", Bounds: screen.Rect{X: e.Bounds.X + e.Bounds.W - 200, Y: e.Bounds.Y + 20, W: 160, H: e.Bounds.H - 40},
					Source: screen.SourceTree})
				return
			}
		}
	})
	pt := screen.Point{X: btn.Bounds.X + btn.Bounds.W - 120, Y: btn.Bounds.Y + btn.Bounds.H/2}
	res := s.Act(context.Background(), Call{}, Action{Tool: "tap", Point: &pt})
	mustStatus(t, res, StatusPending)
	if hd.Current() != "cart" {
		t.Fatal("the payment went through without approval")
	}
	n := g.Needs()[0]
	if n.Rule != "Payments" || !strings.Contains(n.Title, "Place order") || n.Target != btn.Bounds {
		t.Fatalf("approval names the child, not the button: %+v", n)
	}
	// By ref too.
	obs := s.Observe(context.Background(), ObserveRequest{})
	g.Decide(n.ID, Deny, "")
	time.Sleep(10 * time.Millisecond)
	res = s.Act(context.Background(), Call{}, Action{Tool: "tap", Ref: ref(t, obs, "$12.80")})
	mustStatus(t, res, StatusPending)
}

func TestTaskGrantIsPerAppAndRule(t *testing.T) {
	g, dev := newTestGateway(t)
	ctx := context.Background()
	s, _ := connect(t, g, "Chat")
	dev.Show("chats")
	res := tap(s, ref(t, s.Observe(ctx, ObserveRequest{}), "Sam"))
	input := ref(t, res, "Message")
	send := func(text string) *Result {
		return s.Act(ctx, Call{}, Action{Tool: "type", Ref: input, Text: text, Submit: true})
	}
	go func() {
		n := waitNeed(t, g)
		g.Decide(n.ID, AllowTask, "")
	}()
	mustStatus(t, send("one"), StatusSettled)
	// Allowed for the rest of the task in Chatter: no question.
	mustStatus(t, send("two"), StatusSettled)
	if len(g.Needs()) != 0 {
		t.Fatal("asked again after Allow for this task")
	}
	// Another rule that asks is not covered by the grant.
	if err := g.SetTaskApps("pixel", []string{"Dinely"}); err != nil {
		t.Fatal(err)
	}
	g.opt.MaxBlock = 20 * time.Millisecond
	res = send("three")
	mustStatus(t, res, StatusPending)
	if chat := dev.Chat(); chat[len(chat)-1] != "Me: two" {
		t.Fatalf("chat: %v", chat)
	}
}

func TestSameApprovalIDTwice(t *testing.T) {
	g, dev := newTestGateway(t, func(o *Options) { o.MaxBlock = 300 * time.Millisecond })
	s, _ := connect(t, g, "Lunch")
	cart := toCart(t, s)
	place := ref(t, cart, "Place order · $12.80")
	r := tap(s, place)
	mustStatus(t, r, StatusPending)
	id := r.ApprovalID
	var wg sync.WaitGroup
	res := make([]*Result, 2)
	for i := range res {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i] = s.Act(context.Background(), Call{ApprovalID: id}, Action{Tool: "tap", Ref: place})
		}()
	}
	time.Sleep(50 * time.Millisecond)
	g.Decide(id, AllowOnce, "")
	wg.Wait()
	settled, refused := 0, 0
	for _, x := range res {
		switch {
		case x.Status == StatusSettled:
			settled++
		case x.Error == ErrInvalidArgument:
			refused++
		}
	}
	if settled != 1 || refused != 1 || dev.Current() != "done" {
		t.Fatalf("results: %+v %+v", res[0], res[1])
	}
	var pay []StepView
	for _, st := range g.Tasks()[0].Steps {
		if strings.Contains(st.Text, "Place order") {
			pay = append(pay, st)
		}
	}
	if len(pay) != 1 || pay[0].Status != string(StatusSettled) {
		t.Fatalf("history of the payment: %+v", pay)
	}
}

// The client sets the scope the person picked on a New agent card, then
// allows it; the agent picking up the answer must not reset the scope.
func TestAgentScopeChosenBeforeAllow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	g, _ := newTestGateway(t, func(o *Options) { o.TrustNewAgents = false; o.DataDir = dir })
	narrow := Scope{Mode: ScopeApps, Apps: []string{"Dinely"}}
	allow := func(name string, scope *Scope) {
		n := waitNeed(t, g)
		if n.Rule != "New agent" || n.Agent != name {
			t.Errorf("need %+v", n)
			return
		}
		if scope != nil {
			g.SetAgentScope(name, *scope)
		}
		if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
			t.Error(err)
		}
	}

	s := g.OpenSession(AgentInfo{Name: "Codex", Client: "codex 1.0"})
	defer s.Close()
	go allow("Codex", &narrow)
	mustStatus(t, s.Connect(ctx, ConnectRequest{Task: "Lunch"}), StatusSettled)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	if res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Chatter"}); res.Error != ErrScopeDenied {
		t.Fatalf("launch outside the chosen scope: %+v", res)
	}

	// Without a chosen scope, an allowed agent gets the default.
	s.Release(ctx, ReleaseRequest{})
	other := g.OpenSession(AgentInfo{Name: "Other"})
	defer other.Close()
	go allow("Other", nil)
	mustStatus(t, other.Connect(ctx, ConnectRequest{Task: "Chat"}), StatusSettled)
	mustStatus(t, other.Act(ctx, Call{}, Action{Tool: "launch", App: "Chatter"}), StatusSettled)

	// History keeps the chosen scope, after the approval was written too.
	g.Flush()
	agents, err := g.Agents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]AgentView{}
	for _, a := range agents {
		got[a.Name] = a
	}
	if a := got["Codex"]; !a.Approved || a.Scope.String() != narrow.String() {
		t.Fatalf("Codex: %+v", a)
	}
	if a := got["Other"]; !a.Approved || a.Scope.Mode != ScopeEverything {
		t.Fatalf("Other: %+v", a)
	}
	g.Close()
	g2, err := New(Options{Providers: nil, DataDir: dir, DiscoverEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	stored, err := g2.Agents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range stored {
		if a.Name == "Codex" && (!a.Approved || a.Scope.String() != narrow.String()) {
			t.Fatalf("stored: %+v", a)
		}
	}
}

func TestAgentApprovalRace(t *testing.T) {
	g, _ := newTestGateway(t, func(o *Options) { o.TrustNewAgents = false; o.MaxBlock = time.Second })
	a := AgentInfo{Name: "Racer"}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := g.OpenSession(a)
			defer s.Close()
			s.Connect(context.Background(), ConnectRequest{Task: "t"})
		}()
	}
	time.Sleep(100 * time.Millisecond)
	for _, n := range g.Needs() {
		g.Decide(n.ID, AllowOnce, "")
		break
	}
	g.SetAgentScope("Racer", Scope{Mode: ScopeEverything})
	wg.Wait()
}

func TestTypeWithUnknownFocusNearPassword(t *testing.T) {
	g, dev := newTestGateway(t)
	s, _ := connect(t, g, "Sign in")
	dev.Show("account")
	s.Observe(context.Background(), ObserveRequest{})
	res := s.Act(context.Background(), Call{}, Action{Tool: "type", Text: "hunter2"})
	mustStatus(t, res, StatusBlocked)
	if res.Rule != policy.RuleCredentials {
		t.Fatalf("rule %q", res.Rule)
	}
}

type countingDevice struct {
	*demo.Device
	mu       sync.Mutex
	observes int
}

func (d *countingDevice) Observe(ctx context.Context, o driver.ObserveOptions) (*screen.Screen, error) {
	d.mu.Lock()
	d.observes++
	d.mu.Unlock()
	return d.Device.Observe(ctx, o)
}

func (d *countingDevice) WaitSettled(context.Context, time.Duration) (bool, error) { return true, nil }

type countingProvider struct {
	*demo.Provider
	d *countingDevice
}

func (p countingProvider) Open(context.Context, string) (driver.Device, error) { return p.d, nil }

func TestBatchReusesTheLastRead(t *testing.T) {
	cd := &countingDevice{Device: demo.NewDevice("pixel", "Pixel 9", driver.Android)}
	g, err := New(Options{Providers: []driver.Provider{countingProvider{demo.NewProvider(driver.Android, cd.Device), cd}},
		TrustNewAgents: true, NoScreenshots: true, DiscoverEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	s, _ := connect(t, g, "Batch")
	cd.mu.Lock()
	cd.observes = 0
	cd.mu.Unlock()
	res := s.Batch(context.Background(), BatchRequest{Steps: []Action{
		{Tool: "launch", App: "Dinely"}, {Tool: "key", Key: "back"}, {Tool: "key", Key: "home"},
	}})
	mustStatus(t, res, StatusSettled)
	cd.mu.Lock()
	n := cd.observes
	cd.mu.Unlock()
	// One read after each step: the launch needs none before it, and the
	// steps after it start from the read that ended the step before.
	if n != 3 {
		t.Fatalf("%d reads for a three-step batch, want 3", n)
	}
	// A separate call reads the screen again.
	mustStatus(t, s.Act(context.Background(), Call{}, Action{Tool: "key", Key: "home"}), StatusSettled)
	cd.mu.Lock()
	n = cd.observes
	cd.mu.Unlock()
	if n != 5 {
		t.Fatalf("%d reads after a separate call, want 5", n)
	}
}

func TestUnplacedRefReadsAgain(t *testing.T) {
	g, hd := newHookGateway(t)
	s, first := connect(t, g, "Lunch")
	dinely := ref(t, first, "Dinely")
	// The next read loses the row's position, as OCR placement can.
	var once sync.Once
	hd.setObserve(func(sc *screen.Screen) {
		once.Do(func() {
			for i := range sc.Elements {
				if sc.Elements[i].Label == "Dinely" {
					sc.Elements[i].Bounds = screen.Rect{}
				}
			}
		})
	})
	mustStatus(t, tap(s, dinely), StatusSettled)
	if hd.Current() != "dinely" {
		t.Fatalf("tap did not land: %s", hd.Current())
	}
}
