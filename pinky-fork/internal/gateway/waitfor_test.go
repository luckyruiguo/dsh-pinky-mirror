package gateway

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// ocrDev is a demo phone with text that only OCR reads find, as on a web
// page without accessibility elements.
type ocrDev struct {
	*demo.Device
	mu   sync.Mutex
	text string
}

func (o *ocrDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, err := o.Device.Observe(ctx, opt)
	if err != nil {
		return s, err
	}
	o.mu.Lock()
	text := o.text
	o.mu.Unlock()
	if opt.OCR && text != "" {
		s.Elements = append(s.Elements, screen.Element{Role: screen.RoleText, Label: text, Source: screen.SourceOCR,
			Bounds: screen.Rect{X: 100, Y: 900, W: 400, H: 60}})
	}
	return s, nil
}

func (o *ocrDev) show(text string) {
	o.mu.Lock()
	o.text = text
	o.mu.Unlock()
}

func newOCRGateway(t *testing.T, dev *ocrDev) *Gateway {
	t.Helper()
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
	return g
}

// phone_wait_for finds text that only OCR sees, and does not call it gone
// while OCR still sees it.
func TestWaitForReadsOCRText(t *testing.T) {
	ctx := context.Background()
	dev := &ocrDev{Device: demo.NewDevice("pixel", "Pixel 9", driver.Android)}
	g := newOCRGateway(t, dev)
	s, _ := connect(t, g, "Check the bill")
	dev.show("预估未出账单 ¥5,527.07")

	res := s.WaitFor(ctx, WaitForRequest{Text: "预估未出账单", TimeoutMS: 3000})
	mustStatus(t, res, StatusSettled)
	if res.Matched == nil || !*res.Matched {
		t.Fatalf("OCR text not found: %s", res.Message)
	}
	res = s.WaitFor(ctx, WaitForRequest{Text: "预估未出账单", Gone: true, TimeoutMS: 1500})
	if res.Matched == nil || *res.Matched {
		t.Fatalf("called gone while OCR still shows it: %s", res.Message)
	}
	dev.show("")
	res = s.WaitFor(ctx, WaitForRequest{Text: "预估未出账单", Gone: true, TimeoutMS: 3000})
	if res.Matched == nil || !*res.Matched {
		t.Fatalf("not gone after it went: %s", res.Message)
	}
}

// phone_wait_for matches whole words in Latin text (field test #24):
// waiting for the Stop button is not answered at once by the Stopwatch tab.
func TestWaitForMatchesWholeWords(t *testing.T) {
	ctx := context.Background()
	dev := &ocrDev{Device: demo.NewDevice("pixel", "Pixel 9", driver.Android)}
	g := newOCRGateway(t, dev)
	s, _ := connect(t, g, "Time the run")
	dev.show("Stopwatch")

	res := s.WaitFor(ctx, WaitForRequest{Text: "Stop", TimeoutMS: 300})
	mustStatus(t, res, StatusSettled)
	if res.Matched == nil || *res.Matched {
		t.Fatalf("“Stop” matched the Stopwatch tab: %s", res.Message)
	}
	res = s.WaitFor(ctx, WaitForRequest{Text: "Stop", Gone: true, TimeoutMS: 300})
	if res.Matched == nil || !*res.Matched {
		t.Fatalf("“Stop” not gone although only Stopwatch shows: %s", res.Message)
	}
	dev.show("Stop")
	res = s.WaitFor(ctx, WaitForRequest{Text: "stop", TimeoutMS: 3000})
	if res.Matched == nil || !*res.Matched {
		t.Fatalf("“stop” not found: %s", res.Message)
	}
}

// phone_wait_for takes several texts, for when several things may come
// next: it ends at the first one that shows and names it, and with gone it
// waits until none of them shows.
func TestWaitForAny(t *testing.T) {
	ctx := context.Background()
	dev := &ocrDev{Device: demo.NewDevice("pixel", "Pixel 9", driver.Android)}
	g := newOCRGateway(t, dev)
	s, _ := connect(t, g, "Order lunch")
	// Blanks and repeats are dropped; three texts are left.
	wait := func(gone bool, timeout int) *Result {
		t.Helper()
		res := s.WaitFor(ctx, WaitForRequest{Text: "Order placed", Any: []string{"Payment failed", " ", "order placed", "Out of stock"},
			Gone: gone, TimeoutMS: timeout})
		mustStatus(t, res, StatusSettled)
		if res.Matched == nil {
			t.Fatalf("no matched: %+v", res)
		}
		return res
	}

	res := wait(false, 300)
	if *res.Matched || !strings.HasPrefix(res.Message, "None of “Order placed”, “Payment failed” or “Out of stock” has appeared after ") {
		t.Fatalf("nothing shows: matched %v: %s", *res.Matched, res.Message)
	}
	dev.show("Payment failed: card declined")
	res = wait(false, 3000)
	if !*res.Matched || !strings.HasPrefix(res.Message, "“Payment failed” appeared after ") {
		t.Fatalf("the second text shows: matched %v: %s", *res.Matched, res.Message)
	}
	steps := taskSteps(g, s)
	if st := steps[len(steps)-1]; st.Text != "Waited for “Order placed / Payment failed / Out of stock”" || !strings.Contains(st.Args, `"text":"Order placed"`) || !strings.Contains(st.Args, `"any":["Payment failed","Out of stock"]`) {
		t.Fatalf("step: %q %s", st.Text, st.Args)
	}
	// any alone, without text.
	res = s.WaitFor(ctx, WaitForRequest{Any: []string{"Out of stock", "Payment failed"}, TimeoutMS: 3000})
	if res.Matched == nil || !*res.Matched || !strings.HasPrefix(res.Message, "“Payment failed” appeared after ") {
		t.Fatalf("any without text: %+v", res)
	}

	// gone names the text that is still there, and ends once none is.
	res = wait(true, 300)
	if *res.Matched || !strings.HasPrefix(res.Message, "“Payment failed” has not disappeared after ") {
		t.Fatalf("gone while one shows: matched %v: %s", *res.Matched, res.Message)
	}
	dev.show("")
	res = wait(true, 3000)
	if !*res.Matched || !strings.HasPrefix(res.Message, "“Order placed”, “Payment failed” and “Out of stock” disappeared after ") {
		t.Fatalf("gone once none shows: matched %v: %s", *res.Matched, res.Message)
	}

	if res := s.WaitFor(ctx, WaitForRequest{Any: []string{" ", ""}}); res.Error != ErrInvalidArgument {
		t.Fatalf("no text at all: %+v", res)
	}
	many := make([]string, MaxWaitTexts+1)
	for i := range many {
		many[i] = strings.Repeat("a", i+1)
	}
	if res := s.WaitFor(ctx, WaitForRequest{Any: many}); res.Error != ErrInvalidArgument {
		t.Fatalf("%d texts accepted: %+v", len(many), res)
	}
	if res := s.WaitFor(ctx, WaitForRequest{Any: many[:MaxWaitTexts], TimeoutMS: 50}); res.IsError() {
		t.Fatalf("%d texts refused: %+v", MaxWaitTexts, res)
	}
}

// With an area, phone_wait_for counts only the text placed inside it: the
// same word in a tab bar or a chat elsewhere on the screen neither answers
// the wait nor keeps a gone wait waiting.
func TestWaitForArea(t *testing.T) {
	ctx := context.Background()
	dev := &ocrDev{Device: demo.NewDevice("pixel", "Pixel 9", driver.Android)}
	g := newOCRGateway(t, dev)
	s, _ := connect(t, g, "Track the parcel")
	dev.show("Deliveries") // ocrDev places it at 100,900 400x60: its centre is 300,930
	top := &screen.Rect{X: 0, Y: 0, W: 1080, H: 800}
	around := &screen.Rect{X: 200, Y: 900, W: 200, H: 100}
	wait := func(area *screen.Rect, gone bool, timeout int) *Result {
		t.Helper()
		res := s.WaitFor(ctx, WaitForRequest{Text: "Deliveries", Area: area, Gone: gone, TimeoutMS: timeout})
		mustStatus(t, res, StatusSettled)
		if res.Matched == nil {
			t.Fatalf("no matched: %+v", res)
		}
		return res
	}

	res := wait(top, false, 300)
	if *res.Matched || !strings.HasPrefix(res.Message, "“Deliveries” has not appeared in the area after ") {
		t.Fatalf("text outside the area: matched %v: %s", *res.Matched, res.Message)
	}
	res = wait(around, false, 3000)
	if !*res.Matched || !strings.HasPrefix(res.Message, "“Deliveries” appeared in the area after ") {
		t.Fatalf("text inside the area: matched %v: %s", *res.Matched, res.Message)
	}
	steps := taskSteps(g, s)
	if st := steps[len(steps)-1]; !strings.Contains(st.Args, `"area":{"height":100,"width":200,"x":200,"y":900}`) {
		t.Fatalf("step args: %s", st.Args)
	}
	if res = wait(top, true, 3000); !*res.Matched {
		t.Fatalf("gone waits for text outside the area: %s", res.Message)
	}
	if res = wait(around, true, 300); *res.Matched {
		t.Fatalf("gone although the area shows it: %s", res.Message)
	}

	for _, a := range []screen.Rect{{X: -1, Y: 0, W: 10, H: 10}, {X: 0, Y: -1, W: 10, H: 10}, {X: 0, Y: 0, W: 0, H: 10}, {X: 0, Y: 0, W: 10, H: -5}} {
		if res := s.WaitFor(ctx, WaitForRequest{Text: "Deliveries", Area: &a}); res.Error != ErrInvalidArgument {
			t.Fatalf("area %+v accepted: %+v", a, res)
		}
	}
	// The demo phone's screen is 1080x2400.
	res = s.WaitFor(ctx, WaitForRequest{Text: "Deliveries", Area: &screen.Rect{X: 1080, Y: 0, W: 10, H: 10}})
	if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "1080x2400") {
		t.Fatalf("area beside the screen: %+v", res)
	}
}

func TestScreenShows(t *testing.T) {
	s := &screen.Screen{Elements: []screen.Element{
		{Label: "Chats", Bounds: screen.Rect{X: 0, Y: 2300, W: 200, H: 80}},
		{Label: "Order placed", Bounds: screen.Rect{X: 100, Y: 400, W: 400, H: 60}},
		{Label: "Unplaced"},
	}}
	evs := []screen.Event{{Kind: screen.EventToast, Text: "Saved"}}
	top := &screen.Rect{X: 0, Y: 0, W: 1080, H: 1000}
	for _, c := range []struct {
		texts []string
		area  *screen.Rect
		want  string
	}{
		{[]string{"Missing"}, nil, ""},
		{[]string{"Missing", "order placed"}, nil, "order placed"},
		{[]string{"Chats", "Order placed"}, nil, "Chats"}, // the order given, not the screen's
		{[]string{"Order placed", "Chats"}, nil, "Order placed"},
		{[]string{"Chats", "Order placed"}, top, "Order placed"},
		{[]string{"Chats"}, top, ""},
		// A toast has no place on the screen, nor has an element without bounds.
		{[]string{"Saved"}, nil, "Saved"},
		{[]string{"Saved"}, top, ""},
		{[]string{"Unplaced"}, nil, "Unplaced"},
		{[]string{"Unplaced"}, top, ""},
		// The centre decides: the element's edge inside the area is not enough.
		{[]string{"Order placed"}, &screen.Rect{X: 0, Y: 0, W: 300, H: 1000}, ""},
		{[]string{"Order placed"}, &screen.Rect{X: 0, Y: 0, W: 301, H: 1000}, "Order placed"},
	} {
		if got := screenShows(s, evs, c.texts, c.area); got != c.want {
			t.Errorf("screenShows(%q, %+v) = %q, want %q", c.texts, c.area, got, c.want)
		}
	}
}

func TestContainsText(t *testing.T) {
	for _, c := range []struct {
		label, text string
		want        bool
	}{
		{"Stopwatch", "Stop", false},
		{"Stop", "Stop", true},
		{"STOP", "stop", true},
		{"Stop timer", "Stop", true},
		{"Nonstop", "stop", false},
		{"Done.", "Done", true},
		{"Order placed", "placed", true},
		{"Your order was placed", "order was", true},
		{"Stopwatch, Stop", "Stop", true}, // the second occurrence is a word
		{"Wi-Fi", "Wi-Fi", true},
		{"Wi-Fi Calling", "Wi-Fi", true},
		{"PayPal", "Pay", false},
		{"Apple Pay", "Pay", true},
		{"Pay $5", "Pay", true},
		{"$50", "$5", false},
		{"Step 1 of 3", "1", true},
		{"15 min", "5", false},
		{"Order #12: ready", "#12", true},
		{"立即还款", "还款", true},
		{"预估未出账单 ¥5,527.07", "预估未出账单", true},
		{"お支払いを確定", "支払い", true},
		{"결제하기", "결제", true},
		{"Café ouvert", "café", true},
		{"Cafés", "café", false},
		{"", "Stop", false},
		{"Stop", "", false},
	} {
		if got := containsText(c.label, c.text); got != c.want {
			t.Errorf("containsText(%q, %q) = %v, want %v", c.label, c.text, got, c.want)
		}
	}
}
