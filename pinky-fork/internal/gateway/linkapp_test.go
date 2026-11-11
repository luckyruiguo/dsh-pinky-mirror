package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

var (
	safari = driver.App{ID: "com.apple.mobilesafari", Name: "Safari"}
	chrome = driver.App{ID: "com.google.chrome.ios", Name: "Chrome"}
)

// linkPhone is a demo phone whose links open in the app handler names,
// which a test may change, as a driver's LinkApp would find it.
type linkPhone struct {
	driver.Device
	mu       sync.Mutex
	handler  func(link string) (driver.App, error)
	asked    []string // the links LinkApp was asked about
	opened   []string // "link in app id", as OpenLink got them
	launched []string
}

func (b *linkPhone) LinkApp(ctx context.Context, link string) (driver.App, error) {
	b.mu.Lock()
	b.asked = append(b.asked, link)
	h := b.handler
	b.mu.Unlock()
	return h(link)
}

func (b *linkPhone) OpenLink(ctx context.Context, link string, app driver.App) error {
	b.mu.Lock()
	b.opened = append(b.opened, link+" in "+app.ID)
	b.mu.Unlock()
	return nil
}

func (b *linkPhone) setHandler(h func(link string) (driver.App, error)) {
	b.mu.Lock()
	b.handler = h
	b.mu.Unlock()
}

func (b *linkPhone) Apps(ctx context.Context) ([]driver.App, error) {
	apps, err := b.Device.Apps(ctx)
	return append(apps, safari, chrome), err
}

func (b *linkPhone) Launch(ctx context.Context, app string) error {
	b.mu.Lock()
	b.launched = append(b.launched, app)
	b.mu.Unlock()
	return nil
}

func (b *linkPhone) state() (asked, opened, launched []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.asked...), append([]string(nil), b.opened...), append([]string(nil), b.launched...)
}

func opensIn(app driver.App) func(string) (driver.App, error) {
	return func(string) (driver.App, error) { return app, nil }
}

type linkProvider struct {
	*demo.Provider
	phone *linkPhone
}

func (p linkProvider) Open(ctx context.Context, id string) (driver.Device, error) {
	d, err := p.Provider.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	p.phone.Device = d
	return p.phone, nil
}

// newLinkGateway returns a gateway with one demo iPhone whose links open
// as phone says.
func newLinkGateway(t *testing.T, phone *linkPhone, opts ...func(*Options)) *Gateway {
	t.Helper()
	dev := demo.NewDevice("iphone", "iPhone", driver.IOS)
	if phone.handler == nil {
		phone.handler = opensIn(safari)
	}
	g, _ := newTestGateway(t, append([]func(*Options){func(o *Options) {
		o.Providers = []driver.Provider{linkProvider{demo.NewProvider(driver.IOS, dev), phone}}
	}}, opts...)...)
	return g
}

// lastStep returns the text of the newest step of the newest task.
func lastStep(t *testing.T, g *Gateway) string {
	t.Helper()
	tasks := g.Tasks()
	if len(tasks) == 0 || len(tasks[0].Steps) == 0 {
		t.Fatal("no steps")
	}
	steps := tasks[0].Steps
	return steps[len(steps)-1].Text
}

// A link is judged as a launch of the app that opens it: blocking Safari
// blocks its links, allowing Safari for the task allows them again, and a
// link still asks as a link. The approval and the step name the link and
// the app.
func TestLinkIsJudgedByTheAppThatOpensIt(t *testing.T) {
	ctx := context.Background()
	phone := &linkPhone{}
	g := newLinkGateway(t, phone)
	s, _ := connect(t, g, "Links")

	answer(t, g, AllowOnce, func(n NeedView) {
		if n.Rule != "Installs and links" || n.Title != "Open “https://example.com/” in Safari?" {
			t.Errorf("need %+v", n)
		}
	})
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: " https://example.com/ "}), StatusSettled)
	if _, opened, launched := phone.state(); len(opened) != 1 || opened[0] != "https://example.com/ in "+safari.ID || len(launched) != 0 {
		t.Fatalf("opened %v, launched %v", opened, launched)
	}
	if got := lastStep(t, g); !strings.HasPrefix(got, "Opened “https://example.com/” in Safari") {
		t.Errorf("step %q", got)
	}

	if err := g.SetAppBlocked(screen.App{ID: safari.ID, Name: safari.Name}, true); err != nil {
		t.Fatal(err)
	}
	res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "https://example.com/other"})
	if res.Status != StatusBlocked && res.Status != StatusDenied || res.Rule != policy.RuleBlocked {
		t.Fatalf("a link in a blocked Safari: %+v", res)
	}
	if _, opened, _ := phone.state(); len(opened) != 1 {
		t.Fatalf("the blocked link opened: %v", opened)
	}
	if len(g.Needs()) != 0 {
		t.Fatalf("a blocked link asked: %+v", g.Needs())
	}

	// The person allows the blocked Safari for this task: its links open
	// again, and still ask as links.
	go func() {
		n := waitNeed(t, g)
		if !n.Unblock || n.Rule != policy.RuleBlocked {
			t.Errorf("unblock need: %+v", n)
		}
		g.Decide(n.ID, AllowTask, "")
	}()
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Safari"}), StatusSettled)
	answer(t, g, AllowOnce, func(n NeedView) {
		if n.Rule != "Installs and links" || n.Unblock {
			t.Errorf("link need %+v", n)
		}
	})
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "https://example.com/third"}), StatusSettled)
	if _, opened, _ := phone.state(); len(opened) != 2 || opened[1] != "https://example.com/third in "+safari.ID {
		t.Fatalf("opened %v", opened)
	}
}

// Links Pinky never opens are refused before any app is looked for; a
// link no app opens, or one the phone would ask the person about, is
// refused too, and a phone that cannot open links says so.
func TestLinksThatDoNotOpen(t *testing.T) {
	ctx := context.Background()
	phone := &linkPhone{}
	g := newLinkGateway(t, phone)
	s, _ := connect(t, g, "Links")

	for _, link := range []string{"javascript:alert(1)", "intent:#Intent;component=com.android.settings/.Settings;end",
		"file:///etc/hosts", "data:text/html,hi", "content://contacts/people"} {
		res := s.Act(ctx, Call{}, Action{Tool: "launch", App: link})
		if res.Status != StatusBlocked && res.Status != StatusDenied || res.Rule != policy.RuleUnsafeLinks {
			t.Errorf("%s: %+v", link, res)
		}
	}
	if asked, opened, _ := phone.state(); len(asked)+len(opened) != 0 {
		t.Fatalf("refused links reached the phone: asked %v, opened %v", asked, opened)
	}

	phone.setHandler(func(link string) (driver.App, error) {
		return driver.App{}, errors.Join(errors.New("no activity"), driver.ErrNotFound)
	})
	if res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "nothing://here"}); res.Error != ErrNotFound || !strings.Contains(res.Message, "nothing://here") {
		t.Errorf("no app: %+v", res)
	}
	phone.setHandler(func(link string) (driver.App, error) {
		return driver.App{}, &driver.LinkChoiceError{Link: link, Apps: []driver.App{safari, chrome}}
	})
	if res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "https://example.com/"}); res.Error != ErrUnsupported || !strings.Contains(res.Message, "Safari") || !strings.Contains(res.Message, "Chrome") {
		t.Errorf("a choice: %+v", res)
	}
	if _, opened, _ := phone.state(); len(opened) != 0 || len(g.Needs()) != 0 {
		t.Fatalf("opened %v, needs %+v", opened, g.Needs())
	}
}

// noLinks hides a phone's LinkOpener.
type noLinks struct{ driver.Device }

func TestPhoneWithoutLinks(t *testing.T) {
	ctx := context.Background()
	dev := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	g := newDevGateway(t, dev, noLinks{dev})
	s, _ := connect(t, g, "Links")
	if res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "https://dinely.app/"}); res.Error != ErrUnsupported {
		t.Errorf("%+v", res)
	}
}

// A payment link asks under the default preset, as a tap on Pay does.
func TestPaymentLinkAsks(t *testing.T) {
	ctx := context.Background()
	phone := &linkPhone{handler: opensIn(driver.App{ID: demo.Dinely, Name: "Dinely"})}
	g := newLinkGateway(t, phone, func(o *Options) { o.Policy = policy.Default() })
	s, _ := connect(t, g, "Pay")
	answer(t, g, Deny, func(n NeedView) {
		if n.Rule != "Payments" || n.Title != "Open “upi://pay?pa=shop@upi&am=100” in Dinely?" {
			t.Errorf("need %+v", n)
		}
	})
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "upi://pay?pa=shop@upi&am=100"}), StatusDenied)
	// A plain link does not ask under the default preset.
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "dinely://order/42"}), StatusSettled)
	if _, opened, _ := phone.state(); len(opened) != 1 || opened[0] != "dinely://order/42 in "+demo.Dinely {
		t.Fatalf("opened %v", opened)
	}
}

// An approval for a link is stale when another app would open it by the
// time the person answers: they approved opening it in the first.
func TestLinkApprovalStaleWhenItsAppChanges(t *testing.T) {
	ctx := context.Background()
	phone := &linkPhone{}
	g := newLinkGateway(t, phone)
	s, _ := connect(t, g, "Links")
	go func() {
		n := waitNeed(t, g)
		phone.setHandler(opensIn(chrome))
		g.Decide(n.ID, AllowOnce, "")
	}()
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "https://example.com/"}), StatusStale)
	if _, opened, _ := phone.state(); len(opened) != 0 {
		t.Fatalf("opened %v", opened)
	}
}

// cloneLinks records the links a clone phone opens.
type cloneLinks struct {
	*cloneDev
	mu     sync.Mutex
	opened []string
}

func (c *cloneLinks) OpenLink(ctx context.Context, link string, app driver.App) error {
	c.mu.Lock()
	c.opened = append(c.opened, link+" in "+app.ID)
	c.mu.Unlock()
	return c.cloneDev.OpenLink(ctx, link, app)
}

func (c *cloneLinks) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.opened) == 0 {
		return ""
	}
	return c.opened[len(c.opened)-1]
}

// On a phone with app clones, a link opens in the clone the agent opened
// while that app is in front, and in the app itself otherwise.
func TestLinkOpensInTheCloneInFront(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &cloneLinks{cloneDev: &cloneDev{Device: base}}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Clones")
	clone := driver.CloneID(demo.Chatter, 999)

	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Chatter (clone)"}), StatusSettled)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "chatter://chat/1"}), StatusSettled)
	if got := dev.last(); got != "chatter://chat/1 in "+clone {
		t.Fatalf("opened %q", got)
	}
	if got := lastStep(t, g); !strings.HasPrefix(got, "Opened “chatter://chat/1” in Chatter (clone)") {
		t.Errorf("step %q", got)
	}
	// Another app in front: the link opens in Dinely itself.
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "dinely://order/1"}), StatusSettled)
	if got := dev.last(); got != "dinely://order/1 in "+demo.Dinely {
		t.Fatalf("opened %q", got)
	}
	// The agent opens Chatter itself: its links stay there.
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Chatter"}), StatusSettled)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "chatter://chat/2"}), StatusSettled)
	if got := dev.last(); got != "chatter://chat/2 in "+demo.Chatter {
		t.Fatalf("opened %q", got)
	}
}
