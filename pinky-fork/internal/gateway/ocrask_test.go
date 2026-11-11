package gateway

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// readerDev is a demo phone that reads text the way the Android driver
// does: a read with OCR adds its lines only when it asks for all the text
// (the tree looks complete), and it says whether this computer reads text
// (driver.TextReader). It keeps the options of every read.
type readerDev struct {
	*demo.Device
	reads bool // ReadsText
	mu    sync.Mutex
	lines []screen.Element
	opts  []driver.ObserveOptions
}

func (d *readerDev) ReadsText() bool { return d.reads }

func (d *readerDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, err := d.Device.Observe(ctx, opt)
	if err != nil {
		return s, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opts = append(d.opts, opt)
	if opt.OCR && opt.OCRAll && d.reads && (opt.MayCapture == nil || opt.MayCapture(s)) {
		s.Elements = append(s.Elements, d.lines...)
	}
	return s, nil
}

func (d *readerDev) text(label string, r screen.Rect) {
	d.mu.Lock()
	d.lines = append(d.lines, screen.Element{Role: screen.RoleText, Label: label, Bounds: r, Source: screen.SourceOCR})
	d.mu.Unlock()
}

// last returns the options of the latest read.
func (d *readerDev) last() driver.ObserveOptions {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.opts[len(d.opts)-1]
}

func (d *readerDev) allReads() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, o := range d.opts {
		if o.OCRAll {
			n++
		}
	}
	return n
}

// phone_observe asks for the text the tree misses; with ocr, for all of
// it. Either way the driver may capture only screens Pinky would take a
// picture of.
func TestObserveAsksForText(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	base.Show("settings")
	dev := &readerDev{Device: base, reads: true}
	dev.text("Wi-Fi 6 · 5 GHz", screen.Rect{X: 100, Y: 1500, W: 400, H: 60})
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Settings")

	res := s.Observe(ctx, ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if o := dev.last(); !o.OCR || o.OCRAll || o.MayCapture == nil || o.Frame == nil {
		t.Fatalf("phone_observe asked %+v", o)
	}
	if strings.Contains(res.Screen.Render(), "Wi-Fi 6") {
		t.Fatal("the text came without being asked for")
	}
	res = s.Observe(ctx, ObserveRequest{OCR: true})
	mustStatus(t, res, StatusSettled)
	o := dev.last()
	if !o.OCR || !o.OCRAll {
		t.Fatalf("phone_observe { ocr: true } asked %+v", o)
	}
	if !strings.Contains(res.Screen.Render(), `ocr "Wi-Fi 6 · 5 GHz"`) {
		t.Fatalf("no OCR line:\n%s", res.Screen.Render())
	}
	if strings.Contains(res.Message, "cannot read text") {
		t.Fatalf("message %q", res.Message)
	}

	// No pictures of a blocked app or of a locked phone, as for screenshots.
	wallet := &screen.Screen{App: screen.App{Name: "Google Wallet", ID: "com.google.android.apps.walletnfcrel"}, Width: 1080, Height: 2400}
	settings := &screen.Screen{App: screen.App{Name: "Settings", ID: "com.android.settings"}, Width: 1080, Height: 2400}
	locked := *settings
	locked.Locked = true
	unknown := &screen.Screen{Width: 1080, Height: 2400}
	if o.MayCapture(wallet) || o.MayCapture(&locked) || o.MayCapture(unknown) || !o.MayCapture(settings) {
		t.Fatal("MayCapture is wrong")
	}
}

// On a phone whose driver reads text, a tap on a spot the tree says little
// about is checked against the text there before any read has returned
// OCR text: "Pay" drawn in an image button asks.
func TestTextReaderChecksVagueTaps(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	base.Show("settings") // nothing in the tree below y=772
	dev := &readerDev{Device: base, reads: true}
	dev.text("Pay ¥5,527.07", screen.Rect{X: 100, Y: 900, W: 400, H: 60})
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Look at the bill")

	asked := make(chan NeedView, 1)
	go func() {
		n := waitNeed(t, g)
		asked <- n
		g.Decide(n.ID, Deny, "")
	}()
	res := s.Act(ctx, Call{}, Action{Tool: "tap", Point: &screen.Point{X: 300, Y: 930}})
	mustStatus(t, res, StatusDenied)
	if n := <-asked; n.Rule != "Payments" {
		t.Fatalf("asked under rule %q", n.Rule)
	}

	// A computer without OCR: no extra read, and phone_observe { ocr: true }
	// says why there is no text.
	plain := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	plain.Show("settings")
	none := &readerDev{Device: plain}
	none.text("Pay ¥5,527.07", screen.Rect{X: 100, Y: 900, W: 400, H: 60})
	g2 := newDevGateway(t, plain, none)
	s2, _ := connect(t, g2, "Look at the bill")
	res = s2.Act(ctx, Call{}, Action{Tool: "tap", Point: &screen.Point{X: 300, Y: 930}})
	mustStatus(t, res, StatusSettled)
	if n := none.allReads(); n != 0 {
		t.Fatalf("%d reads for all the text on a computer without OCR", n)
	}
	res = s2.Observe(ctx, ObserveRequest{OCR: true})
	if !strings.Contains(res.Message, "cannot read text from the screen's pixels") {
		t.Fatalf("message %q", res.Message)
	}
}

// phone_wait_for asks for all the text when the tree lacks it.
func TestWaitForAsksForAllText(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	base.Show("settings")
	dev := &readerDev{Device: base, reads: true}
	dev.text("Delivered", screen.Rect{X: 100, Y: 1500, W: 400, H: 60})
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Settings")
	res := s.WaitFor(ctx, WaitForRequest{Text: "Delivered", TimeoutMS: 2000})
	mustStatus(t, res, StatusSettled)
	if dev.allReads() == 0 {
		t.Fatal("no read asked for all the text")
	}
}

// captureDev reads text the way the Android driver does when the gateway
// lets it capture (MayCapture): it takes a picture in memory, reads it and
// drops it. It counts its own captures, and apart from them the
// screenshots the gateway asks for, by the app in front.
type captureDev struct {
	*demo.Device
	mu       sync.Mutex
	captured map[string]int // the driver's own pictures, for OCR
	shots    map[string]int // pictures the gateway asked for
}

func (d *captureDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, err := d.Device.Observe(ctx, opt)
	if err != nil || !opt.OCR || (opt.MayCapture != nil && !opt.MayCapture(s)) {
		return s, err
	}
	if _, err := d.Device.Screenshot(ctx); err != nil { // in memory, dropped here
		return nil, err
	}
	d.mu.Lock()
	d.captured[s.App.ID]++
	d.mu.Unlock()
	if s.App.ID == wallet {
		s.Elements = append(s.Elements, screen.Element{Role: screen.RoleText, Label: "Balance ¥4,321.09",
			Bounds: screen.Rect{X: 100, Y: 1500, W: 600, H: 60}, Source: screen.SourceOCR})
	}
	return s, nil
}

// Screenshot is the gateway's: a picture of the wallet comes all red, so
// that one stored anywhere would show.
func (d *captureDev) Screenshot(ctx context.Context) (image.Image, error) {
	s, err := d.Device.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.shots[s.App.ID]++
	d.mu.Unlock()
	if s.App.ID == wallet {
		img := image.NewRGBA(image.Rect(0, 0, s.Width, s.Height))
		draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{R: 255, A: 255}), image.Point{}, draw.Src)
		return img, nil
	}
	return d.Device.Screenshot(ctx)
}

// redFiles returns the stored pictures that are the wallet's red.
func redFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, f := range mediaFiles(dir) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		c := img.Bounds().Size().Div(2)
		r, g, bl, _ := img.At(c.X, c.Y).RGBA()
		if r>>8 > 200 && g>>8 < 60 && bl>>8 < 60 {
			out = append(out, f)
		}
	}
	return out
}

func (d *captureDev) count(m map[string]int, app string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return m[app]
}

// A blocked app the person allowed for the task is read by OCR too (the
// person chose so on 2026-11-08): its driver may capture it in memory, and
// the agent gets the text and where it is. No picture of it is kept, stored
// or sent: no screenshot in any result, none taken by the gateway, nothing
// in the history's media or screen text, no recording. Before Allow, and in
// the next task, it is not captured at all.
func TestAllowedBlockedAppReadByOCR(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &captureDev{Device: base, captured: map[string]int{}, shots: map[string]int{}}
	dir := t.TempDir()
	g := newDevGateway(t, base, dev, func(o *Options) { o.DataDir = dir; o.NoScreenshots = false })
	s, _ := connect(t, g, "Bookkeeping")
	var results []*Result
	keep := func(r *Result) *Result { results = append(results, r); return r }

	// In front but not allowed: hidden, and not captured.
	base.Show("wallet")
	if res := keep(s.Observe(ctx, ObserveRequest{OCR: true})); res.Rule == "" {
		t.Fatalf("the wallet is not hidden: %s", res.Screen.Render())
	}
	if n := dev.count(dev.captured, wallet); n != 0 {
		t.Fatalf("captured a blocked app the person did not allow, %d times", n)
	}
	mustStatus(t, keep(s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"})), StatusSettled)

	go func() {
		n := waitNeed(t, g)
		g.Decide(n.ID, AllowTask, "")
	}()
	res := keep(s.Act(ctx, Call{}, Action{Tool: "launch", App: "Google Wallet"}))
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Screen.Render(), `ocr "Balance ¥4,321.09" @400,1530`) {
		t.Fatalf("no OCR text after Allow:\n%s", res.Screen.Render())
	}
	if dev.count(dev.captured, wallet) == 0 {
		t.Fatal("the allowed app was not captured for OCR")
	}
	obs := keep(s.Observe(ctx, ObserveRequest{OCR: true, Screenshot: true}))
	if !strings.Contains(obs.Screen.Render(), "Balance ¥4,321.09") ||
		!strings.Contains(obs.Message, "No screenshot") || !strings.Contains(obs.Message, "ocr lines") {
		t.Fatalf("observe: %s\n%s", obs.Message, obs.Screen.Render())
	}
	keep(s.WaitFor(ctx, WaitForRequest{Text: "Balance", TimeoutMS: 500}))
	// Screenshots, history pictures and recordings still refuse it.
	if recordableScreen(g.policy, &screen.Screen{App: screen.App{Name: "Google Wallet", ID: wallet}}) {
		t.Fatal("a blocked app counts as recordable")
	}
	keep(s.Release(ctx, ReleaseRequest{}))
	g.Flush()

	for i, r := range results {
		if r.Screenshot != nil {
			t.Errorf("result %d carries a screenshot", i)
		}
	}
	// A background screenshot that the wallet came up during is dropped
	// (shots.go); whatever the gateway got, none of it is stored, and the
	// driver's own pictures never leave it.
	if len(mediaFiles(dir)) == 0 {
		t.Fatal("no pictures stored at all: the check below would prove nothing")
	}
	if red := redFiles(t, dir); len(red) > 0 {
		t.Fatalf("pictures of the blocked app stored: %v (the gateway asked for %d)", red, dev.count(dev.shots, wallet))
	}
	tasks, err := g.History(ctx, HistoryQuery{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("history: %v %v", tasks, err)
	}
	steps, err := g.store.Steps(ctx, tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range steps {
		if strings.Contains(st.ScreenText, "4,321.09") || strings.Contains(st.ScreenText, "4242") {
			t.Errorf("the blocked app's text is in history: %+v", st)
		}
	}
	for _, st := range tasks[0].Steps {
		if st.Tool == "phone_launch" && st.After != "" {
			t.Errorf("a picture of the blocked app in history: %+v", st)
		}
	}

	// The next task has not allowed it: not captured.
	before := dev.count(dev.captured, wallet)
	s2, _ := connect(t, g, "Bookkeeping again")
	base.Show("wallet")
	if res := s2.Observe(ctx, ObserveRequest{OCR: true}); res.Rule == "" || strings.Contains(res.Screen.Render(), "Balance") {
		t.Fatalf("the next task sees the wallet: %s", res.Screen.Render())
	}
	if n := dev.count(dev.captured, wallet); n != before {
		t.Fatalf("captured in a task that did not allow it: %d", n-before)
	}
}
