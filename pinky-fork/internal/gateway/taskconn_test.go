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
)

// twoWayPhone is an iPhone on iOS 27 that Pinky can reach over CoreDevice
// or through the WebDriverAgent helper, as the iOS provider does.
type twoWayPhone struct {
	*demo.Provider

	mu        sync.Mutex
	way       string        // the provider's way: "", "coredevice" or "wda"
	noHelper  bool          // the helper is not installed
	helperErr error         // opening through the helper fails
	passcode  bool          // the helper waits for the passcode
	askOnOpen bool          // opening through the helper finds the phone asking for its passcode
	slow      time.Duration // how long opening through the helper takes
	bareCore  bool          // over CoreDevice it has no switches and no second finger, as the iOS provider
	opens     []string
}

// coreOnly is a phone over CoreDevice: the device without the switches and
// the second finger the helper adds.
type coreOnly struct{ driver.Device }

func newTwoWayPhone() (*twoWayPhone, *demo.Device) {
	dev := demo.NewDevice("iphone", "iPhone 13 mini", driver.IOS)
	dev.AddApp("com.dbs.sg.dbsmbanking", "digibank")
	dev.AddApp("cmb.pb", "招商银行")
	return &twoWayPhone{Provider: demo.NewProvider(driver.IOS, dev)}, dev
}

func (p *twoWayPhone) backendLocked() string {
	if p.way == wayHelper {
		return wayHelper
	}
	return wayCoreDevice
}

func (p *twoWayPhone) List(ctx context.Context) ([]driver.Info, error) {
	infos, err := p.Provider.List(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range infos {
		infos[i].Backend = p.backendLocked()
		if p.way == wayHelper && p.passcode && infos[i].State == driver.StateOnline {
			infos[i].State, infos[i].Setup, infos[i].Detail = driver.StateNeedsSetup, driver.SetupPasscode, "Enter the passcode on the iPhone."
		}
	}
	return infos, err
}

func (p *twoWayPhone) Open(ctx context.Context, id string) (driver.Device, error) {
	p.mu.Lock()
	b := p.backendLocked()
	p.opens = append(p.opens, b)
	err, slow := p.helperErr, p.slow
	if b == wayHelper && p.askOnOpen {
		// As the iOS provider: the runner waits for the passcode, and the
		// next listing says so (wdaState).
		p.askOnOpen, p.passcode = false, true
		err = &driver.SetupError{Setup: driver.SetupPasscode, Detail: "Enter the passcode on the iPhone.",
			Err: errors.New("the iPhone asks on its screen for its passcode to allow automation")}
	}
	p.mu.Unlock()
	if b == wayHelper && slow > 0 {
		time.Sleep(slow)
	}
	if b == wayHelper && err != nil {
		return nil, err
	}
	dev, err := p.Provider.Open(ctx, id)
	p.mu.Lock()
	bare := p.bareCore
	p.mu.Unlock()
	if err == nil && bare && b == wayCoreDevice {
		return coreOnly{dev}, nil
	}
	return dev, err
}

func (p *twoWayPhone) SetConnection(id, way string) error {
	switch way {
	case "", wayCoreDevice, wayHelper:
	default:
		return driver.ErrUnsupported
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.way = way
	return nil
}

func (p *twoWayPhone) Connections(_ context.Context, id string) driver.Connections {
	p.mu.Lock()
	defer p.mu.Unlock()
	wda := driver.Connection{ID: wayHelper, Available: !p.noHelper}
	if p.noHelper {
		wda.Reason, wda.Detail = "not_installed", "The WebDriverAgent helper is not installed on this iPhone."
	}
	return driver.Connections{Chosen: p.way, Current: p.backendLocked(),
		Ways: []driver.Connection{{ID: "", Available: true}, {ID: wayCoreDevice, Available: true}, wda}}
}

func (p *twoWayPhone) state() (string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.way, append([]string(nil), p.opens...)
}

func twoWayGateway(t *testing.T, p *twoWayPhone, opts ...func(*Options)) *Gateway {
	t.Helper()
	g, _ := newTestGateway(t, append([]func(*Options){func(o *Options) { o.Providers = []driver.Provider{p} }}, opts...)...)
	waitUntil(t, "the iPhone online", func() bool { ds := g.Devices(); return len(ds) == 1 && ds[0].Status == DeviceIdle })
	return g
}

func backendOf(g *Gateway) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	d := g.devices["iphone"]
	if !d.online {
		return ""
	}
	return d.info.Backend
}

func shortSwitchBack(t *testing.T) {
	old := switchBackAfter
	switchBackAfter = 50 * time.Millisecond
	t.Cleanup(func() { switchBackAfter = old })
}

// launchAllowed launches app, allowing it for the task when the person is
// asked (the banks are finance apps).
func launchAllowed(g *Gateway, s *Session, app string) *Result {
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(2 * time.Millisecond):
			}
			if needs := g.Needs(); len(needs) > 0 {
				g.Decide(needs[0].ID, AllowTask, "")
				return
			}
		}
	}()
	return s.Act(context.Background(), Call{}, Action{Tool: "launch", App: app})
}

func TestTaskConnectionWords(t *testing.T) {
	for in, want := range map[string]string{"": "", "auto": "", "Helper": wayHelper, "wda": wayHelper, " coredevice ": wayCoreDevice} {
		if got, ok := TaskConnection(in); !ok || got != want {
			t.Errorf("TaskConnection(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := TaskConnection("usb"); ok {
		t.Error("an unknown word is taken")
	}
	for _, app := range []string{"com.dbs.sg.dbsmbanking", "digibank", "招商银行", "cmb.pb", "com.cmbchina.MPBBank", "China Merchants Bank"} {
		if _, ok := refusesSharing(app); !ok {
			t.Errorf("%s is not known to refuse screen sharing", app)
		}
	}
	if _, ok := refusesSharing("com.apple.calculator"); ok {
		t.Error("Calculator refuses screen sharing?")
	}
}

func TestConnectAsksForTheHelper(t *testing.T) {
	shortSwitchBack(t)
	p, _ := newTwoWayPhone()
	g := twoWayGateway(t, p)
	s := g.OpenSession(agent)
	defer s.Close()
	res := s.Connect(context.Background(), ConnectRequest{Task: "Balances", Connection: "helper"})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "WebDriverAgent helper") || res.Screen == nil {
		t.Fatalf("connect: %q, screen %v", res.Message, res.Screen != nil)
	}
	if b := backendOf(g); b != wayHelper {
		t.Fatalf("backend %q during the task", b)
	}
	// Nothing is saved: the person's choice stays Automatic.
	if c, _ := g.Connections(context.Background(), "iphone"); c.Chosen != wayHelper {
		// The provider holds the task's way; the gateway's record of the
		// person's choice does not.
		t.Fatalf("provider way %q", c.Chosen)
	}
	g.mu.Lock()
	_, saved := g.chosen["iphone"]
	g.mu.Unlock()
	if saved {
		t.Fatal("the task's connection was taken for the person's choice")
	}
	mustStatus(t, s.Release(context.Background(), ReleaseRequest{}), StatusSettled)
	waitUntil(t, "the phone back on CoreDevice", func() bool { return backendOf(g) == wayCoreDevice })
	if way, _ := p.state(); way != "" {
		t.Fatalf("provider way %q after the task", way)
	}
}

func TestLaunchMovesToTheHelper(t *testing.T) {
	shortSwitchBack(t)
	p, _ := newTwoWayPhone()
	g := twoWayGateway(t, p)
	s, _ := connect(t, g, "Bookkeeping")
	if b := backendOf(g); b != wayCoreDevice {
		t.Fatalf("a task that names no such app moved to %q", b)
	}
	res := launchAllowed(g, s, "招商银行")
	if !strings.Contains(res.Message, "China Merchants Bank refuses to run while the screen is shared") {
		t.Fatalf("launch: %+v", res)
	}
	if b := backendOf(g); b != wayHelper {
		t.Fatalf("backend %q after launching the bank", b)
	}
	// A second launch stays there and says nothing more.
	res = launchAllowed(g, s, "cmb.pb")
	if strings.Contains(res.Message, "moved") {
		t.Fatalf("launched again: %q", res.Message)
	}
	// The next task within switchBackAfter keeps it; the phone goes back
	// once no task holds it.
	mustStatus(t, s.Release(context.Background(), ReleaseRequest{}), StatusSettled)
	waitUntil(t, "the phone back on CoreDevice", func() bool { return backendOf(g) == wayCoreDevice })
}

func TestAppListMovesNothing(t *testing.T) {
	p, _ := newTwoWayPhone()
	g := twoWayGateway(t, p)
	s := g.OpenSession(agent)
	defer s.Close()
	// The person may still refuse the app: only its launch moves the task.
	mustStatus(t, s.Connect(context.Background(), ConnectRequest{Task: "DBS balance", Apps: []string{"digibank"}}), StatusSettled)
	if b := backendOf(g); b != wayCoreDevice {
		t.Fatalf("backend %q after naming the bank", b)
	}
	launchAllowed(g, s, "digibank")
	if b := backendOf(g); b != wayHelper {
		t.Fatalf("backend %q after launching the bank", b)
	}
	// The agent can ask to go back.
	res := s.Connect(context.Background(), ConnectRequest{Task: "DBS balance", Connection: "coredevice"})
	mustStatus(t, res, StatusSettled)
	if b := backendOf(g); b != wayCoreDevice {
		t.Fatalf("backend %q after asking for CoreDevice", b)
	}
}

// A launch the person refuses, or one that waits for them, leaves the
// connection alone; one they allow moves it before it runs.
func TestRefusedLaunchKeepsConnection(t *testing.T) {
	p, _ := newTwoWayPhone()
	g := twoWayGateway(t, p)
	s, _ := connect(t, g, "Bookkeeping")
	_, before := p.state()
	go func() {
		for len(g.Needs()) == 0 {
			time.Sleep(2 * time.Millisecond)
		}
		g.Decide(g.Needs()[0].ID, Deny, "")
	}()
	if res := s.Act(context.Background(), Call{}, Action{Tool: "launch", App: "cmb.pb"}); res.Status != StatusDenied {
		t.Fatalf("denied launch: %+v", res)
	}
	if _, opens := p.state(); len(opens) != len(before) || backendOf(g) != wayCoreDevice {
		t.Fatalf("a denied launch reconnected: %v after %v", opens, before)
	}
	mustStatus(t, s.Release(context.Background(), ReleaseRequest{}), StatusSettled)

	s, _ = connect(t, g, "Bookkeeping again")
	res := launchAllowed(g, s, "cmb.pb")
	if res.Error != "" || !strings.Contains(res.Message, "WebDriverAgent helper") {
		t.Fatalf("allowed launch: %+v", res)
	}
	if b := backendOf(g); b != wayHelper {
		t.Fatalf("backend %q after an allowed launch", b)
	}
}

// Nothing moves while the person is in control or the agent may only
// watch: a move would cut the person's session for nothing.
func TestNoMoveWhilePersonHoldsIt(t *testing.T) {
	p, _ := newTwoWayPhone()
	g := twoWayGateway(t, p)
	if err := g.TakeOver("iphone"); err != nil {
		t.Fatal(err)
	}
	s := g.OpenSession(agent)
	defer s.Close()
	res := s.Connect(context.Background(), ConnectRequest{Task: "Balances", Connection: "helper"})
	if !strings.Contains(res.Message, "the person is in control") {
		t.Fatalf("connect while taken over: %+v", res)
	}
	if res := s.Act(context.Background(), Call{}, Action{Tool: "launch", App: "digibank"}); res.Status != StatusPaused && res.Status != StatusTookOver {
		t.Fatalf("launch while taken over: %+v", res)
	}
	if _, opens := p.state(); len(opens) != 1 || backendOf(g) != wayCoreDevice {
		t.Fatalf("moved while taken over: %v", opens)
	}
	s.Close()
	if err := g.HandBack("iphone", ""); err != nil {
		t.Fatal(err)
	}

	g.SetAgentScope(agent.Name, Scope{Mode: ScopeWatchOnly})
	w := g.OpenSession(agent)
	defer w.Close()
	res = w.Connect(context.Background(), ConnectRequest{Task: "Look", Connection: "helper"})
	if !strings.Contains(res.Message, "may only watch") {
		t.Fatalf("connect watch-only: %+v", res)
	}
	if res := w.Act(context.Background(), Call{}, Action{Tool: "launch", App: "digibank"}); res.Error != ErrScopeDenied {
		t.Fatalf("launch watch-only: %+v", res)
	}
	if _, opens := p.state(); len(opens) != 1 {
		t.Fatalf("moved for a watch-only agent: %v", opens)
	}
}

// A helper that takes longer than the offline grace to fail does not end
// the task: it falls back to CoreDevice.
func TestSwitchOutlastsGrace(t *testing.T) {
	p, _ := newTwoWayPhone()
	p.helperErr, p.slow = errors.New("the runner quit"), 400*time.Millisecond
	g := twoWayGateway(t, p, func(o *Options) { o.ReconnectGrace = 100 * time.Millisecond })
	s, _ := connect(t, g, "Bookkeeping")
	res := launchAllowed(g, s, "digibank")
	if !strings.Contains(res.Message, "stays on CoreDevice") {
		t.Fatalf("launch: %+v", res)
	}
	mustStatus(t, s.Observe(context.Background(), ObserveRequest{}), StatusSettled)
	if b := backendOf(g); b != wayCoreDevice {
		t.Fatalf("backend %q", b)
	}
}

// A connection the person chooses while a task's move waits is theirs:
// the task neither undoes it nor puts the provider back on Automatic.
func TestPersonChoosesDuringSwitch(t *testing.T) {
	p, _ := newTwoWayPhone()
	p.helperErr, p.slow = errors.New("the runner quit"), 300*time.Millisecond
	g := twoWayGateway(t, p)
	s, _ := connect(t, g, "Bookkeeping")
	done := make(chan *Result, 1)
	go func() { done <- launchAllowed(g, s, "digibank") }()
	waitUntil(t, "the switch", func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.devices["iphone"].switching != ""
	})
	if err := g.SetConnection("iphone", wayCoreDevice); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if !strings.Contains(res.Message, "keeps their choice") {
		t.Fatalf("launch: %+v", res)
	}
	waitUntil(t, "the phone on CoreDevice", func() bool { return backendOf(g) == wayCoreDevice })
	if way, _ := p.state(); way != wayCoreDevice {
		t.Fatalf("provider way %q after the person chose CoreDevice", way)
	}
	g.mu.Lock()
	chosen := g.chosen["iphone"]
	g.mu.Unlock()
	if chosen != wayCoreDevice {
		t.Fatalf("chosen %q", chosen)
	}
}

func TestPersonsConnectionIsKept(t *testing.T) {
	p, _ := newTwoWayPhone()
	g := twoWayGateway(t, p)
	if err := g.SetConnection("iphone", wayCoreDevice); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the phone back", func() bool { return backendOf(g) == wayCoreDevice })
	_, before := p.state()
	s := g.OpenSession(agent)
	defer s.Close()
	res := s.Connect(context.Background(), ConnectRequest{Task: "Balances", Connection: "helper"})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "Settings › Phones") {
		t.Fatalf("connect: %q", res.Message)
	}
	launchAllowed(g, s, "digibank")
	if _, opens := p.state(); len(opens) != len(before) || backendOf(g) != wayCoreDevice {
		t.Fatalf("opens %v after %v", opens, before)
	}
}

func TestHelperMissingOrFailing(t *testing.T) {
	p, _ := newTwoWayPhone()
	p.noHelper = true
	g := twoWayGateway(t, p)
	s, _ := connect(t, g, "Bookkeeping")
	res := launchAllowed(g, s, "digibank")
	if !strings.Contains(res.Message, "not installed") {
		t.Fatalf("no helper: %q", res.Message)
	}
	if _, opens := p.state(); len(opens) != 1 {
		t.Fatalf("opened %v without a helper", opens)
	}

	// The helper is there but does not start: back on CoreDevice.
	p.mu.Lock()
	p.noHelper, p.helperErr = false, errors.New("the runner quit")
	p.mu.Unlock()
	res = launchAllowed(g, s, "digibank")
	if !strings.Contains(res.Message, "stays on CoreDevice") || !strings.Contains(res.Message, "the runner quit") {
		t.Fatalf("failing helper: %+v", res)
	}
	if b := backendOf(g); b != wayCoreDevice {
		t.Fatalf("backend %q after the helper failed", b)
	}
	if way, _ := p.state(); way != "" {
		t.Fatalf("provider way %q after the helper failed", way)
	}
}

func TestHelperWaitsForPasscode(t *testing.T) {
	p, _ := newTwoWayPhone()
	p.passcode = true
	g := twoWayGateway(t, p, func(o *Options) { o.ReconnectGrace = time.Minute })
	s := g.OpenSession(agent)
	defer s.Close()
	start := time.Now()
	res := s.Connect(context.Background(), ConnectRequest{Task: "Balances", Connection: "helper"})
	if res.Status != StatusNeedsSetup || !strings.Contains(res.Message, "passcode") || res.Task == "" {
		t.Fatalf("connect: %+v", res)
	}
	if time.Since(start) > switchWait/2 {
		t.Fatalf("waited %s for a phone that waits for the person", time.Since(start))
	}
	// The person enters it: the task goes on through the helper.
	p.mu.Lock()
	p.passcode = false
	p.mu.Unlock()
	waitUntil(t, "the phone on the helper", func() bool { return backendOf(g) == wayHelper })
	mustStatus(t, s.Observe(context.Background(), ObserveRequest{}), StatusSettled)
}

func TestPersonChoiceEndsTaskConnection(t *testing.T) {
	p, _ := newTwoWayPhone()
	g := twoWayGateway(t, p)
	s := g.OpenSession(agent)
	defer s.Close()
	mustStatus(t, s.Connect(context.Background(), ConnectRequest{Task: "Balances", Connection: "helper"}), StatusSettled)
	if err := g.SetConnection("iphone", driver.ConnectionAuto); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	way := g.devices["iphone"].taskWay
	g.mu.Unlock()
	if way != "" {
		t.Fatalf("task way %q after the person chose", way)
	}
	waitUntil(t, "the phone on CoreDevice", func() bool { return backendOf(g) == wayCoreDevice })
}

func TestTaskConnectionOnAndroid(t *testing.T) {
	g, _ := newTestGateway(t)
	s := g.OpenSession(agent)
	defer s.Close()
	res := s.Connect(context.Background(), ConnectRequest{Task: "Lunch", Connection: "helper"})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "connects only one way") {
		t.Fatalf("connect: %q", res.Message)
	}
	if res := s.Connect(context.Background(), ConnectRequest{Task: "Lunch", Connection: "usb"}); res.Error != ErrInvalidArgument {
		t.Fatalf("an unknown connection: %+v", res)
	}
}

// The app phone_launch opens decides, not the words the agent used: the
// bank's iOS id, or an id the table does not know for an app with a known
// name.
func TestLaunchJudgesTheResolvedApp(t *testing.T) {
	for _, tc := range []struct{ id, name, ask string }{
		{"com.cmbchina.MPBBank", "招商银行", "com.cmbchina.MPBBank"},
		{"com.example.bank", "China Merchants Bank", "com.example.bank"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			p, dev := newTwoWayPhone()
			dev.AddApp(tc.id, tc.name)
			g := twoWayGateway(t, p)
			s, _ := connect(t, g, "Bookkeeping")
			res := launchAllowed(g, s, tc.ask)
			if !strings.Contains(res.Message, "China Merchants Bank refuses to run while the screen is shared") {
				t.Fatalf("launch %s: %+v", tc.ask, res)
			}
			if b := backendOf(g); b != wayHelper {
				t.Fatalf("backend %q after launching the bank", b)
			}
		})
	}
}

// The phone asks for its passcode as the helper starts (field test,
// 2026.1110.27): the launch says so with device_needs_setup, Pinky shows
// a card, and the task goes on through the helper once it is entered.
func TestLaunchWaitsForThePasscode(t *testing.T) {
	p, _ := newTwoWayPhone()
	p.askOnOpen = true
	g := twoWayGateway(t, p, func(o *Options) { o.ReconnectGrace = time.Minute })
	s, _ := connect(t, g, "Bookkeeping")
	res := launchAllowed(g, s, "招商银行")
	if res.Status != StatusNeedsSetup || res.Setup != driver.SetupPasscode {
		t.Fatalf("launch: %+v", res)
	}
	if !strings.Contains(res.Message, "passcode") || !strings.Contains(res.Message, "phone_launch again") ||
		strings.Contains(res.Message, "stays on CoreDevice") {
		t.Fatalf("launch: %q", res.Message)
	}
	var card *NeedView
	for _, n := range g.Needs() {
		if n.Kind == NeedSetup {
			card = &n
		}
	}
	if card == nil || card.DeviceID != "iphone" || !strings.Contains(card.Title, "passcode") {
		t.Fatalf("needs: %+v", g.Needs())
	}
	if err := g.Decide(card.ID, Deny, ""); err != nil {
		t.Fatalf("decide a setup card: %v", err)
	}
	if way, _ := p.state(); way != wayHelper {
		t.Fatalf("provider way %q while the phone waits", way)
	}
	// The person enters it: the helper comes up, the card goes, and the
	// launch runs.
	p.mu.Lock()
	p.passcode = false
	p.mu.Unlock()
	waitUntil(t, "the phone on the helper", func() bool { return backendOf(g) == wayHelper })
	for _, n := range g.Needs() {
		if n.Kind == NeedSetup {
			t.Fatalf("the card stayed: %+v", n)
		}
	}
	mustStatus(t, launchAllowed(g, s, "招商银行"), StatusSettled)
}
