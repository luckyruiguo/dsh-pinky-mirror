package gateway

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// textDev is a demo phone whose reads with OCR add lines of text that the
// element tree lacks, as on a bank's web page, and count themselves.
type textDev struct {
	*demo.Device
	mu       sync.Mutex
	lines    []screen.Element
	ocrReads int
}

func (d *textDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, err := d.Device.Observe(ctx, opt)
	if err != nil || !opt.OCR {
		return s, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ocrReads++
	s.Elements = append(s.Elements, d.lines...)
	return s, nil
}

// text puts a line that only OCR reads on the screen.
func (d *textDev) text(label string, r screen.Rect) {
	d.mu.Lock()
	d.lines = append(d.lines, screen.Element{Role: screen.RoleText, Label: label, Bounds: r, Source: screen.SourceOCR})
	d.mu.Unlock()
}

func (d *textDev) reads() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ocrReads
}

// newDevGateway runs a gateway on one wrapped demo phone.
func newDevGateway(t *testing.T, base *demo.Device, dev driver.Device, opts ...func(*Options)) *Gateway {
	t.Helper()
	pol := policy.Default()
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	o := Options{
		Providers: []driver.Provider{videoProvider{demo.NewProvider(driver.Android, base), dev}}, Policy: pol,
		TrustNewAgents: true, ApprovalTimeout: 3 * time.Second, MaxBlock: 2 * time.Second, SettleMax: 200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, DiscoverEvery: time.Hour, NoScreenshots: true,
	}
	for _, f := range opts {
		f(&o)
	}
	g, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

// A tap on text that only OCR reads is checked like a button with that
// label (field test #4): 掌上生活's bill pages are web views whose 还款
// exists only as OCR lines. The tap lands next to the letters, inside the
// button around them, and still asks.
func TestTapOnOCRTextAsks(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	base.Show("settings") // nothing in the tree below y=772
	dev := &textDev{Device: base}
	pay := screen.Rect{X: 100, Y: 900, W: 400, H: 60}
	dev.text("确认支付 ¥5,527.07", pay)
	dev.text("账单详情", screen.Rect{X: 100, Y: 1600, W: 300, H: 60})
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Look at the bill")

	asked := make(chan NeedView, 1)
	go func() {
		n := waitNeed(t, g)
		asked <- n
		g.Decide(n.ID, Deny, "")
	}()
	res := s.Act(ctx, Call{}, Action{Tool: "tap", Point: &screen.Point{X: 300, Y: 985}})
	mustStatus(t, res, StatusDenied)
	n := <-asked
	if n.Rule != "Payments" || !strings.Contains(n.Title, "确认支付") {
		t.Fatalf("need: rule %q, title %q", n.Rule, n.Title)
	}
	for _, in := range dev.Inputs() {
		if in.Kind == "tap" {
			t.Fatalf("the tap ran: %+v", in)
		}
	}

	// Text elsewhere on the page does not count.
	res = s.Act(ctx, Call{}, Action{Tool: "tap", Point: &screen.Point{X: 300, Y: 1250}})
	mustStatus(t, res, StatusSettled)
	// Nor does harmless text under the finger.
	res = s.Act(ctx, Call{}, Action{Tool: "tap", Point: &screen.Point{X: 250, Y: 1630}})
	mustStatus(t, res, StatusSettled)

	// A swipe that starts on the text asks as well: "Slide to pay".
	go func() {
		n := waitNeed(t, g)
		g.Decide(n.ID, Deny, "")
	}()
	res = s.Act(ctx, Call{}, Action{Tool: "swipe", From: &screen.Point{X: 150, Y: 930}, To: &screen.Point{X: 900, Y: 930}})
	mustStatus(t, res, StatusDenied)
}

// The second read with OCR happens only where the tree says little about
// the point: a tap on a labelled button needs none, and neither does a
// phone that has never returned OCR text (Android).
func TestOCRReadOnlyWhereTheTreeIsVague(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	base.Show("settings")
	dev := &textDev{Device: base}
	dev.text("Pay now", screen.Rect{X: 100, Y: 900, W: 400, H: 60})
	g := newDevGateway(t, base, dev)
	s, res := connect(t, g, "Settings")

	n := dev.reads()
	tap(s, ref(t, res, "About phone"))
	// The action's own read after the tap uses OCR; the one before it does not.
	if got := dev.reads() - n; got != 1 {
		t.Fatalf("%d reads with OCR for a tap on a labelled button, want 1", got)
	}

	plain := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	plain.Show("settings")
	cnt := &textDev{Device: plain} // no OCR lines: the phone never reads text
	g2 := newDevGateway(t, plain, cnt)
	s2, _ := connect(t, g2, "Settings")
	n = cnt.reads()
	res = s2.Act(ctx, Call{}, Action{Tool: "tap", Point: &screen.Point{X: 300, Y: 1250}})
	mustStatus(t, res, StatusSettled)
	if got := cnt.reads() - n; got != 1 {
		t.Fatalf("%d reads with OCR for a tap on a phone without OCR, want 1", got)
	}
}

// ocrReach grows a line's box by its height on every side.
func TestOCRReach(t *testing.T) {
	r := ocrReach(screen.Rect{X: 100, Y: 900, W: 400, H: 60})
	for _, c := range []struct {
		p    screen.Point
		want bool
	}{
		{screen.Point{X: 300, Y: 930}, true},
		{screen.Point{X: 41, Y: 841}, true},
		{screen.Point{X: 559, Y: 1019}, true},
		{screen.Point{X: 39, Y: 930}, false},
		{screen.Point{X: 300, Y: 1021}, false},
	} {
		if got := r.Contains(c.p); got != c.want {
			t.Errorf("%v in %v: %v, want %v", c.p, r, got, c.want)
		}
	}
}
