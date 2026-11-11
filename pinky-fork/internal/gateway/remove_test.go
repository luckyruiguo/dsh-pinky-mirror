package gateway

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
)

// netProvider lists demo phones, over a cable or the network as conn says,
// pairs them (driver.Pairer, driver.WiFiConnector) and records what the
// gateway tells it about removed phones (driver.Forgetter).
type netProvider struct {
	*demo.Provider

	mu          sync.Mutex
	conn        map[string]string // Info.Connection by id; the demo's is "usb"
	unplugged   map[string]bool   // not listed
	forgotten   map[string]bool
	calls       []string                 // "forget id", "remember id", "disconnect id"
	gate        map[string]chan struct{} // Open of these phones waits for the channel to close
	openedClose []*closeCounter          // the gated phones' sessions
}

func newNetProvider(devs ...*demo.Device) *netProvider {
	return &netProvider{Provider: demo.NewProvider(driver.Android, devs...), conn: map[string]string{},
		unplugged: map[string]bool{}, forgotten: map[string]bool{}, gate: map[string]chan struct{}{}}
}

func (p *netProvider) List(ctx context.Context) ([]driver.Info, error) {
	infos, err := p.Provider.List(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []driver.Info
	for _, info := range infos {
		if p.unplugged[info.ID] {
			continue
		}
		if c := p.conn[info.ID]; c != "" {
			info.Connection = c
		}
		out = append(out, info)
	}
	return out, err
}

// closeCounter is a phone that counts its Close calls.
type closeCounter struct {
	driver.Device
	mu     sync.Mutex
	closes int
}

func (c *closeCounter) Close() error {
	c.mu.Lock()
	c.closes++
	c.mu.Unlock()
	return c.Device.Close()
}

func (p *netProvider) Open(ctx context.Context, id string) (driver.Device, error) {
	dev, err := p.Provider.Open(ctx, id)
	p.mu.Lock()
	gate := p.gate[id]
	p.mu.Unlock()
	if gate == nil || err != nil {
		return dev, err
	}
	select {
	case <-gate:
	case <-ctx.Done():
	}
	c := &closeCounter{Device: dev}
	p.mu.Lock()
	p.openedClose = append(p.openedClose, c)
	p.mu.Unlock()
	return c, nil
}

func (p *netProvider) set(fn func(p *netProvider)) {
	p.mu.Lock()
	fn(p)
	p.mu.Unlock()
}

func (p *netProvider) Forget(id string) {
	p.mu.Lock()
	p.forgotten[id] = true
	p.calls = append(p.calls, "forget "+id)
	p.mu.Unlock()
}

func (p *netProvider) Remember(id string) {
	p.mu.Lock()
	delete(p.forgotten, id)
	p.calls = append(p.calls, "remember "+id)
	p.mu.Unlock()
}

func (p *netProvider) Disconnect(_ context.Context, id string) error {
	p.mu.Lock()
	p.calls = append(p.calls, "disconnect "+id)
	p.mu.Unlock()
	return nil
}

func (p *netProvider) callLog() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

func (p *netProvider) info(id string) driver.Info {
	infos, _ := p.List(context.Background())
	for _, info := range infos {
		if info.ID == id {
			return info
		}
	}
	return driver.Info{}
}

func (p *netProvider) StartPairing(context.Context) (driver.Pairing, error) {
	return &pairedNow{info: p.info("mi")}, nil
}

func (p *netProvider) PairWithCode(context.Context, string, string) (driver.Info, error) {
	return p.info("mi"), nil
}

func (p *netProvider) ConnectWiFi(context.Context, string) (driver.Info, error) {
	return p.info("mi"), nil
}

// pairedNow is a QR pairing the phone answers at once.
type pairedNow struct{ info driver.Info }

func (q *pairedNow) QR() string                                { return "WIFI:T:ADB;S:pinky-x;P:y;;" }
func (q *pairedNow) Wait(context.Context) (driver.Info, error) { return q.info, nil }
func (q *pairedNow) Close() error                              { return nil }

// twoPhones is a gateway with a Pixel on a cable and a Mi over Wi-Fi.
func twoPhones(t *testing.T, dir string, p driver.Provider) *Gateway {
	t.Helper()
	g, err := New(Options{Providers: []driver.Provider{p}, Policy: testPolicy(t), DataDir: dir, TrustNewAgents: true,
		ApprovalTimeout: 3 * time.Second, MaxBlock: 2 * time.Second, SettleMax: 200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond, NoScreenshots: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

func newTwoPhones() (*netProvider, *demo.Device, *demo.Device) {
	pixel := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	mi := demo.NewDevice("mi", "Mi 10 Pro", driver.Android)
	p := newNetProvider(pixel, mi)
	p.conn["mi"] = "wifi"
	return p, pixel, mi
}

func ids(g *Gateway) []string {
	var out []string
	for _, d := range g.Devices() {
		out = append(out, d.Info.ID)
	}
	slices.Sort(out)
	return out
}

func waitIDs(t *testing.T, g *Gateway, want ...string) {
	t.Helper()
	slices.Sort(want)
	deadline := time.Now().Add(3 * time.Second)
	for !slices.Equal(ids(g), want) {
		if time.Now().After(deadline) {
			t.Fatalf("devices %q, want %q", ids(g), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitOpen waits until the gateway has the phone open.
func waitOpen(t *testing.T, g *Gateway, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		g.mu.Lock()
		d := g.devices[id]
		open := d != nil && d.online && d.dev != nil
		g.mu.Unlock()
		if open {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not come online: %+v", id, g.Devices())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRemoveDevice: removing the Mi, which the provider lists over Wi-Fi,
// stops its task, forgets its settings, drops its connections and keeps
// it off the list, across restarts, until the person pairs it again. Its
// history stays.
func TestRemoveDevice(t *testing.T) {
	dir := t.TempDir()
	p, _, _ := newTwoPhones()
	g := twoPhones(t, dir, p)
	ctx := context.Background()
	waitOpen(t, g, "mi")
	if err := g.SetBackground("mi", false); err != nil {
		t.Fatal(err)
	}
	if err := g.SetReadOnly("mi", true); err != nil {
		t.Fatal(err)
	}
	s := g.OpenSession(agent)
	defer s.Close()
	res := s.Connect(ctx, ConnectRequest{Call: Call{Device: "mi"}, Task: "Look at the menu"})
	mustStatus(t, res, StatusSettled)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	g.Flush()

	if err := g.RemoveDevice(ctx, "mi", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := ids(g); !slices.Equal(got, []string{"pixel"}) {
		t.Fatalf("devices after removing the Mi: %q", got)
	}
	if got := p.callLog(); !slices.Equal(got, []string{"forget mi", "disconnect mi"}) {
		t.Errorf("the provider heard %q", got)
	}
	if res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}); res.Status != StatusStopped {
		t.Errorf("the agent's next call: %+v", res)
	}
	var task *TaskView
	for _, tv := range g.Tasks() {
		if tv.DeviceID == "mi" {
			task = &tv
		}
	}
	if task == nil || task.State != TaskStopped {
		t.Fatalf("the Mi's task: %+v", task)
	}
	if g.Background("mi") || g.Policy().ReadOnly("mi") {
		t.Error("the Mi's settings stayed")
	}
	hist, err := g.History(ctx, HistoryQuery{})
	if err != nil || len(hist) != 1 || hist[0].DeviceID != "mi" || len(hist[0].Steps) == 0 {
		t.Fatalf("history: %+v, %v", hist, err)
	}
	// The provider keeps listing it over Wi-Fi: it stays off the list.
	time.Sleep(100 * time.Millisecond)
	if got := ids(g); !slices.Equal(got, []string{"pixel"}) {
		t.Fatalf("a removed phone came back by itself: %q", got)
	}
	if err := g.RemoveDevice(ctx, "mi", RemoveOptions{}); !errors.Is(err, ErrUnknownDevice) {
		t.Errorf("removing it twice: %v", err)
	}

	// After a restart too, and the provider hears so before it lists.
	g.Close()
	p2, _, _ := newTwoPhones()
	g = twoPhones(t, dir, p2)
	waitIDs(t, g, "pixel")
	time.Sleep(100 * time.Millisecond)
	if got := ids(g); !slices.Equal(got, []string{"pixel"}) {
		t.Fatalf("after a restart: %q", got)
	}
	if got := p2.callLog(); !slices.Equal(got, []string{"forget mi"}) {
		t.Errorf("at start the provider heard %q", got)
	}
	if hist, err := g.History(ctx, HistoryQuery{}); err != nil || len(hist) != 1 {
		t.Errorf("history after a restart: %d tasks, %v", len(hist), err)
	}

	// Paired again: it is back, with its settings as a new phone's.
	if _, err := g.PairWithCode(ctx, "192.168.1.42:37123", "123456"); err != nil {
		t.Fatal(err)
	}
	waitIDs(t, g, "mi", "pixel")
	waitOpen(t, g, "mi")
	if g.Policy().ReadOnly("mi") {
		t.Error("the Mi came back read-only")
	}
	if got := p2.callLog(); !slices.Equal(got, []string{"forget mi", "remember mi"}) {
		t.Errorf("the provider heard %q", got)
	}
	g.Close()
	p3, _, _ := newTwoPhones()
	g = twoPhones(t, dir, p3)
	waitIDs(t, g, "mi", "pixel")
	if got := p3.callLog(); len(got) != 0 {
		t.Errorf("a phone added back was forgotten at start: %q", got)
	}
}

// TestRemoveDeviceAddBack: a removed phone is back when the person pairs it
// by QR code or connects it by address.
func TestRemoveDeviceAddBack(t *testing.T) {
	for _, how := range []string{"qr", "address"} {
		t.Run(how, func(t *testing.T) {
			p, _, _ := newTwoPhones()
			g := twoPhones(t, t.TempDir(), p)
			ctx := context.Background()
			waitIDs(t, g, "mi", "pixel")
			if err := g.RemoveDevice(ctx, "mi", RemoveOptions{}); err != nil {
				t.Fatal(err)
			}
			waitIDs(t, g, "pixel")
			switch how {
			case "qr":
				pg, err := g.StartPairing(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pg.Wait(ctx); err != nil {
					t.Fatal(err)
				}
			case "address":
				if _, err := g.ConnectWiFi(ctx, "192.168.1.42:41000"); err != nil {
					t.Fatal(err)
				}
			}
			waitIDs(t, g, "mi", "pixel")
		})
	}
}

// TestRemoveDeviceOnACable: a phone on a cable comes back at the next
// discovery, as a new phone; once it does, it is no longer removed.
func TestRemoveDeviceOnACable(t *testing.T) {
	dir := t.TempDir()
	p, _, _ := newTwoPhones()
	g := twoPhones(t, dir, p)
	ctx := context.Background()
	waitOpen(t, g, "pixel")
	if err := g.SetBackground("pixel", true); err != nil {
		t.Fatal(err)
	}
	if err := g.RemoveDevice(ctx, "pixel", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitIDs(t, g, "mi", "pixel")
	waitOpen(t, g, "pixel")
	if g.Background("pixel") {
		t.Error("the Pixel came back with its old background choice") // the demo phone's default is off
	}
	// Forgotten first; dropping its connections and remembering it as it
	// comes back may come in either order.
	if got := p.callLog(); len(got) != 3 || got[0] != "forget pixel" || !slices.Contains(got, "disconnect pixel") ||
		!slices.Contains(got, "remember pixel") {
		t.Errorf("the provider heard %q", got)
	}

	// Unplugged when removed, then plugged in again.
	p.set(func(p *netProvider) { p.unplugged["pixel"] = true })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if d := g.Devices(); len(d) == 2 && d[1].Info.ID == "pixel" && d[1].Status == DeviceOffline {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Pixel did not go offline: %+v", g.Devices())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := g.RemoveDevice(ctx, "pixel", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitIDs(t, g, "mi")
	g.Close()
	p2, _, _ := newTwoPhones()
	g = twoPhones(t, dir, p2)
	waitIDs(t, g, "mi", "pixel") // plugged in while Pinky was closed
	g.Close()
	p3, _, _ := newTwoPhones()
	g = twoPhones(t, dir, p3)
	waitIDs(t, g, "mi", "pixel")
	if got := p3.callLog(); len(got) != 0 {
		t.Errorf("a phone plugged in again was forgotten at the next start: %q", got)
	}
}

// TestRemoveDeviceHistory: with History, the phone's tasks go from
// Activity, the recent tasks and the store; the other phone's stay.
func TestRemoveDeviceHistory(t *testing.T) {
	p, _, _ := newTwoPhones()
	g := twoPhones(t, t.TempDir(), p)
	ctx := context.Background()
	waitOpen(t, g, "mi")
	waitOpen(t, g, "pixel")
	for _, dev := range []string{"mi", "pixel"} {
		s := g.OpenSession(agent)
		res := s.Connect(ctx, ConnectRequest{Call: Call{Device: dev}, Task: "Look at " + dev})
		mustStatus(t, res, StatusSettled)
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
		if dev == "pixel" {
			s.Release(ctx, ReleaseRequest{Outcome: "done"})
		}
		defer s.Close()
	}
	g.Flush()
	if err := g.RemoveDevice(ctx, "mi", RemoveOptions{History: true}); err != nil {
		t.Fatal(err)
	}
	hist, err := g.History(ctx, HistoryQuery{})
	if err != nil || len(hist) != 1 || hist[0].DeviceID != "pixel" {
		t.Fatalf("history: %+v, %v", hist, err)
	}
	for _, tv := range g.Tasks() {
		if tv.DeviceID == "mi" {
			t.Errorf("the Mi's task still shows: %+v", tv)
		}
	}
}

// TestRemoveDeviceWhileOpening: a phone removed while the gateway opens it
// does not come online, and the session the driver opened is closed.
func TestRemoveDeviceWhileOpening(t *testing.T) {
	p, _, _ := newTwoPhones()
	p.unplugged["mi"] = true
	g := twoPhones(t, t.TempDir(), p)
	waitOpen(t, g, "pixel")
	gate := make(chan struct{})
	p.set(func(p *netProvider) {
		p.gate["mi"] = gate
		delete(p.unplugged, "mi")
	})
	waitIDs(t, g, "mi", "pixel") // listed, and being opened
	if err := g.RemoveDevice(context.Background(), "mi", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	close(gate)
	time.Sleep(100 * time.Millisecond)
	if got := ids(g); !slices.Equal(got, []string{"pixel"}) {
		t.Fatalf("devices %q", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		p.mu.Lock()
		open := 0
		for _, c := range p.openedClose {
			c.mu.Lock()
			if c.closes == 0 {
				open++
			}
			c.mu.Unlock()
		}
		n := len(p.openedClose)
		p.mu.Unlock()
		if n == 1 && open == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Mi's sessions: %d opened, %d left open; want one, closed", n, open)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRemoveDeviceForgetsConnection: removing a phone drops how the person
// chose to connect it, in the provider and in the store, without
// reconnecting it, so a phone that comes back starts with the driver's own
// rule.
func TestRemoveDeviceForgetsConnection(t *testing.T) {
	dir := t.TempDir()
	cp := &choosingProvider{Provider: demo.NewProvider(driver.Android, demo.NewDevice("pixel", "Pixel 9", driver.Android))}
	g, _ := newTestGateway(t, func(o *Options) { o.Providers = []driver.Provider{cp}; o.DataDir = dir })
	if err := g.SetConnection("pixel", "wda"); err != nil {
		t.Fatal(err)
	}
	waitIDs(t, g, "pixel")
	waitOpen(t, g, "pixel")
	_, opens := cp.state()
	if err := g.RemoveDevice(context.Background(), "pixel", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if way, _ := cp.state(); way != driver.ConnectionAuto {
		t.Errorf("the provider still connects it %q", way)
	}
	g.Flush()
	ways, err := g.store.DeviceConnections(context.Background())
	if err != nil || len(ways) != 0 {
		t.Errorf("stored connections %v, %v", ways, err)
	}
	// A demo phone counts as plugged in: it comes back once, opened the
	// driver's own way, and not before removal finished.
	waitOpen(t, g, "pixel")
	if _, now := cp.state(); now != opens+1 {
		t.Errorf("opened %d times after the removal, want 1", now-opens)
	}
}
