package android

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/ocr"
	"github.com/DIYgod/pinky/internal/screen"
)

const scrW, scrH = 1080, 2400

func node(cls string, l, t, r, b int, kids ...*companion.Node) *companion.Node {
	return &companion.Node{Class: cls, Bounds: companion.Bounds{l, t, r, b}, Children: kids}
}

func text(s string, l, t, r, b int) *companion.Node {
	n := node("android.widget.TextView", l, t, r, b)
	n.Text = s
	return n
}

// appTree is a phone screen with a status bar, an app window holding
// root, and optionally the keyboard.
func appTree(root *companion.Node, keyboard bool) *companion.Tree {
	t := &companion.Tree{Display: companion.Display{Width: scrW, Height: scrH, DensityDPI: 440}}
	t.Windows = append(t.Windows, companion.Window{Type: companion.WindowSystem, Package: "com.android.systemui",
		Bounds: companion.Bounds{0, 0, scrW, 100}, Root: node("android.widget.FrameLayout", 0, 0, scrW, 100)})
	if keyboard {
		t.Windows = append(t.Windows, companion.Window{Type: companion.WindowInputMethod, Package: "com.google.android.inputmethod.latin",
			Bounds: companion.Bounds{0, 1600, scrW, scrH}})
	}
	t.Windows = append(t.Windows, companion.Window{Type: companion.WindowApplication, Package: "com.example.app",
		Focused: true, Active: true, Bounds: companion.Bounds{0, 0, scrW, scrH}, Root: root})
	return t
}

// header is a native title bar with three labels.
func header() *companion.Node {
	return node("android.widget.LinearLayout", 0, 100, scrW, 300,
		text("Back", 0, 100, 200, 300), text("Mini program", 200, 100, 800, 300), text("More", 800, 100, scrW, 300))
}

func TestScanSparseWebView(t *testing.T) {
	// A mini-program: a web view whose page gives one node.
	web := node("com.uc.webview.export.WebView", 0, 300, scrW, scrH, text("Mini program", 0, 300, scrW, 400))
	sc := scanCompanion(appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header(), web), false))
	if len(sc.areas) != 1 || sc.areas[0].kind != "web" || sc.areas[0].rect != (screen.Rect{X: 0, Y: 300, W: scrW, H: 2100}) {
		t.Fatalf("areas = %+v", sc.areas)
	}
	if len(sc.bars) != 1 || sc.bars[0] != (screen.Rect{W: scrW, H: 100}) || sc.keyboard {
		t.Fatalf("bars = %+v, keyboard %v", sc.bars, sc.keyboard)
	}
	// What the status bar covers is not the web view's.
	if a := sc.areas[0]; a.holds(screen.Point{X: 500, Y: 50}) || !a.holds(screen.Point{X: 500, Y: 1000}) {
		t.Error("holds is wrong")
	}
}

func TestScanRichWebView(t *testing.T) {
	// An article: paragraphs cover most of the page.
	var paras []*companion.Node
	for y := 300; y < scrH; y += 200 {
		paras = append(paras, text(fmt.Sprintf("Paragraph %d", y), 40, y, scrW-40, y+180))
	}
	web := node("android.webkit.WebView", 0, 300, scrW, scrH, node("android.view.View", 0, 300, scrW, scrH, paras...))
	web.Text = "Article title" // the page's title on the web view itself counts for nothing
	sc := scanCompanion(appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header(), web), false))
	if len(sc.areas) != 0 {
		t.Fatalf("a rich page gave areas %+v", sc.areas)
	}
	// The same page with only a few small links: sparse by cover.
	links := node("android.view.View", 0, 300, scrW, scrH,
		text("Home", 0, 300, 100, 340), text("News", 100, 300, 200, 340), text("Help", 200, 300, 300, 340), text("Login", 300, 300, 400, 340))
	web = node("android.webkit.WebView", 0, 300, scrW, scrH, links)
	sc = scanCompanion(appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header(), web), false))
	if len(sc.areas) != 1 || sc.areas[0].kind != "web" {
		t.Fatalf("a page of four links gave areas %+v", sc.areas)
	}
	// A labelled container spanning the page does not cover it.
	cont := node("android.view.View", 0, 300, scrW, scrH, links.Children...)
	cont.Desc = "Main"
	web = node("android.webkit.WebView", 0, 300, scrW, scrH, cont)
	sc = scanCompanion(appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header(), web), false))
	if len(sc.areas) != 1 {
		t.Fatalf("a labelled container covered the page: %+v", sc.areas)
	}
	// A small web view (an ad) does not count.
	web = node("android.webkit.WebView", 0, 2200, scrW, scrH)
	sc = scanCompanion(appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header(), web), false))
	if len(sc.areas) != 0 {
		t.Fatalf("a small web view gave areas %+v", sc.areas)
	}
}

func TestScanCanvas(t *testing.T) {
	// A game: its surface is a leaf; the score is labelled.
	surface := node("android.view.View", 0, 0, scrW, scrH)
	root := node("android.widget.FrameLayout", 0, 0, scrW, scrH, surface,
		text("Score 120", 0, 100, 400, 200), text("Pause", 900, 100, scrW, 200), text("Level 3", 400, 100, 900, 200))
	sc := scanCompanion(appTree(root, false))
	if len(sc.areas) != 1 || sc.areas[0].kind != "canvas" || sc.areas[0].path != "application:com.example.app>FrameLayout>View" {
		t.Fatalf("areas = %+v", sc.areas)
	}
	// Big images, lists and fields are not canvases.
	for _, cls := range []string{"android.widget.ImageView", "android.widget.EditText"} {
		leaf := node(cls, 0, 300, scrW, scrH)
		leaf.Editable = cls == "android.widget.EditText"
		sc := scanCompanion(appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header(), leaf), false))
		if len(sc.areas) != 0 {
			t.Errorf("%s gave areas %+v", cls, sc.areas)
		}
	}
	list := node("androidx.recyclerview.widget.RecyclerView", 0, 300, scrW, scrH)
	list.Scrollable = true
	if sc := scanCompanion(appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header(), list), false)); len(sc.areas) != 0 {
		t.Errorf("an empty list gave areas %+v", sc.areas)
	}
	// Chrome's page when it exposes none of it: a leaf described "Web
	// View". A big leaf with text of its own is read from the tree.
	page := node("android.widget.FrameLayout", 0, 300, scrW, scrH)
	page.Desc = "Web View"
	if sc := scanCompanion(appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header(), page), false)); len(sc.areas) != 1 || sc.areas[0].kind != "canvas" {
		t.Errorf("Chrome's page gave areas %+v", sc.areas)
	}
	article := text("A long article in one view", 0, 300, scrW, scrH)
	if sc := scanCompanion(appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header(), article), false)); len(sc.areas) != 0 {
		t.Errorf("a text view gave areas %+v", sc.areas)
	}
	// A launcher's backdrop: a full-screen leaf with the icons drawn over
	// it is not a canvas; a game's surface with a few small labels is.
	backdrop := node("android.view.View", 0, 100, scrW, scrH)
	icons := []*companion.Node{backdrop}
	for i := 0; i < 20; i++ {
		x, y := (i%4)*270, 200+(i/4)*400
		icons = append(icons, text(fmt.Sprintf("App %d", i), x, y, x+270, y+330))
	}
	if sc := scanCompanion(appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, icons...), false)); len(sc.areas) != 0 {
		t.Errorf("a launcher gave areas %+v", sc.areas)
	}
	// A small custom view is not either.
	small := node("android.view.View", 0, 300, 400, 600)
	if sc := scanCompanion(appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header(), small), false)); len(sc.areas) != 0 {
		t.Errorf("a small view gave areas %+v", sc.areas)
	}
}

func TestScanEmptyWindow(t *testing.T) {
	// Unlabelled pieces only, each too small to be a canvas: the window
	// is the area.
	var kids []*companion.Node
	for i := 0; i < 8; i++ {
		kids = append(kids, node("android.view.View", 0, 300*i, scrW, 300*i+300))
	}
	root := node("android.widget.FrameLayout", 0, 0, scrW, scrH, text("Title", 0, 0, 500, 100))
	root.Children = append(root.Children, kids...)
	sc := scanCompanion(appTree(root, true))
	if len(sc.areas) != 1 || sc.areas[0].kind != "window" || sc.areas[0].rect != (screen.Rect{W: scrW, H: scrH}) {
		t.Fatalf("areas = %+v", sc.areas)
	}
	if !sc.keyboard || len(sc.bars) != 2 {
		t.Fatalf("bars = %+v, keyboard %v", sc.bars, sc.keyboard)
	}
	// The keyboard and the status bar are above the app: not its area.
	a := sc.areas[0]
	if a.holds(screen.Point{X: 500, Y: 2000}) || a.holds(screen.Point{X: 500, Y: 50}) || !a.holds(screen.Point{X: 500, Y: 1000}) {
		t.Error("holds is wrong")
	}
	// A native screen has none.
	if sc := scanCompanion(appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header()), false)); len(sc.areas) != 0 {
		t.Errorf("a native screen gave areas %+v", sc.areas)
	}
}

func TestScanDump(t *testing.T) {
	tree, err := parseUIDump([]byte(`<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">
<node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="com.example.app" content-desc="" bounds="[0,0][1080,2400]">
<node index="0" text="Back" resource-id="" class="android.widget.TextView" package="com.example.app" content-desc="" bounds="[0,100][200,300]" />
<node index="1" text="Mini program" resource-id="" class="android.widget.TextView" package="com.example.app" content-desc="" bounds="[200,100][800,300]" />
<node index="2" text="" resource-id="" class="android.widget.ImageButton" package="com.example.app" content-desc="More" bounds="[800,100][1080,300]" />
<node index="3" text="" resource-id="com.example.app:id/web" class="android.webkit.WebView" package="com.example.app" content-desc="" bounds="[0,300][1080,2400]" />
</node></hierarchy>`))
	if err != nil {
		t.Fatal(err)
	}
	sc := scanDump(tree, scrW, scrH)
	if len(sc.areas) != 1 || sc.areas[0].kind != "web" || sc.areas[0].id != "com.example.app:id/web" {
		t.Fatalf("areas = %+v", sc.areas)
	}
}

// ocrStub replaces the recognizer for a test.
type ocrStub struct {
	supported error
	lines     []ocr.Line
	images    int // RecognizeImage calls
	encoded   int // Recognize calls
}

func stubOCR(t *testing.T, s *ocrStub) {
	t.Helper()
	sup, img, enc := ocrSupported, ocrImage, ocrEncoded
	ocrSupported = func() error { return s.supported }
	ocrImage = func(image.Image, ocr.Options) ([]ocr.Line, error) { s.images++; return s.lines, nil }
	ocrEncoded = func([]byte, ocr.Options) ([]ocr.Line, error) { s.encoded++; return s.lines, nil }
	t.Cleanup(func() { ocrSupported, ocrImage, ocrEncoded = sup, img, enc })
}

// line is an OCR line at pixel x, y (its middle) of the test screen.
func line(s string, x, y int) ocr.Line {
	return ocr.Line{Text: s, Confidence: 0.9, X: float64(x-50) / scrW, Y: float64(y-20) / scrH, W: 100.0 / scrW, H: 40.0 / scrH}
}

func miniProgram(keyboard bool) (*companion.Tree, *screen.Screen) {
	web := node("com.uc.webview.export.WebView", 0, 300, scrW, scrH)
	t := appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header(), web), keyboard)
	s := t.Screen(companion.ScreenOptions{})
	s.Keyboard = keyboard
	return t, s
}

func frameOf(w, h int) func(context.Context) (image.Image, error) {
	return func(context.Context) (image.Image, error) { return image.NewRGBA(image.Rect(0, 0, w, h)), nil }
}

func ocrLabels(s *screen.Screen) []string {
	var out []string
	for _, e := range s.Elements {
		if e.Source == screen.SourceOCR {
			out = append(out, e.Label)
		}
	}
	return out
}

func TestAddText(t *testing.T) {
	stub := &ocrStub{lines: []ocr.Line{
		line("12:30", 100, 50),         // the status bar
		line("Mini program", 500, 200), // the title, which the tree has
		line("Balance 4,321.09", 500, 800),
		line("Repay now", 500, 1500),
	}}
	stubOCR(t, stub)
	tree, s := miniProgram(false)
	sc := scanCompanion(tree)
	ctx := context.Background()
	opt := driver.ObserveOptions{OCR: true, Frame: frameOf(scrW, scrH)}
	st := addText(ctx, s, sc, opt, time.Now(), textSource{frame: opt.Frame})
	if got := ocrLabels(s); fmt.Sprint(got) != "[Balance 4,321.09 Repay now]" {
		t.Fatalf("OCR elements = %q (%+v)", got, st)
	}
	if st.From != "video" || st.Lines != 4 || st.Added != 2 || stub.images != 1 {
		t.Fatalf("stats = %+v, %d images", st, stub.images)
	}
	if c := s.Elements[len(s.Elements)-1].Bounds.Center(); c.X != 500 || c.Y != 1500 {
		t.Errorf("Repay now at %v", c)
	}

	// Not asked: nothing captured.
	_, s = miniProgram(false)
	if st := addText(ctx, s, sc, driver.ObserveOptions{}, time.Now(), textSource{frame: opt.Frame}); st.Skipped == "" || len(ocrLabels(s)) != 0 {
		t.Errorf("without OCR: %+v", st)
	}
	// A native screen with OCR asked: nothing captured, unless all is.
	native := appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header()), false)
	s = native.Screen(companion.ScreenOptions{})
	stub.images = 0
	if st := addText(ctx, s, scanCompanion(native), opt, time.Now(), textSource{frame: opt.Frame}); stub.images != 0 || st.Skipped == "" {
		t.Errorf("a native screen was read: %+v", st)
	}
	all := opt
	all.OCRAll = true
	addText(ctx, s, scanCompanion(native), all, time.Now(), textSource{frame: opt.Frame})
	// All of it but the status bar and what the tree shows.
	if got := ocrLabels(s); fmt.Sprint(got) != "[Balance 4,321.09 Repay now]" || stub.images != 1 {
		t.Errorf("OCRAll on a native screen = %q", got)
	}
}

func TestAddTextRefuses(t *testing.T) {
	stub := &ocrStub{lines: []ocr.Line{line("Repay now", 500, 1500)}}
	stubOCR(t, stub)
	ctx := context.Background()
	opt := driver.ObserveOptions{OCR: true, Frame: frameOf(scrW, scrH)}
	tree, _ := miniProgram(false)
	sc := scanCompanion(tree)
	for name, tc := range map[string]struct {
		edit  func(s *screen.Screen, o *driver.ObserveOptions, src *textSource)
		want  string
		wantN int
	}{
		"locked": {edit: func(s *screen.Screen, _ *driver.ObserveOptions, _ *textSource) { s.Locked = true }, want: "locked"},
		"blocked": {edit: func(_ *screen.Screen, o *driver.ObserveOptions, _ *textSource) {
			o.MayCapture = func(*screen.Screen) bool { return false }
		}, want: "no picture"},
		"moved": {edit: func(_ *screen.Screen, _ *driver.ObserveOptions, src *textSource) {
			src.moved = func(context.Context, time.Time, screen.App) bool { return true }
		}, want: "front"},
		"allowed": {edit: func(_ *screen.Screen, o *driver.ObserveOptions, _ *textSource) {
			o.MayCapture = func(s *screen.Screen) bool { return s.App.ID == "com.example.app" }
		}, wantN: 1},
	} {
		_, s := miniProgram(false)
		o := opt
		src := textSource{frame: opt.Frame}
		tc.edit(s, &o, &src)
		st := addText(ctx, s, sc, o, time.Now(), src)
		if n := len(ocrLabels(s)); n != tc.wantN || !strings.Contains(st.Skipped, tc.want) {
			t.Errorf("%s: %d OCR elements, %+v", name, n, st)
		}
	}
}

func TestAddTextPictures(t *testing.T) {
	stub := &ocrStub{lines: []ocr.Line{line("Repay now", 500, 1500)}}
	stubOCR(t, stub)
	ctx := context.Background()
	tree, _ := miniProgram(false)
	sc := scanCompanion(tree)
	png := func(context.Context) ([]byte, error) { return []byte("\x89PNG..."), nil }

	// A frame of another shape (the phone turned): the screenshot instead.
	_, s := miniProgram(false)
	st := addText(ctx, s, sc, driver.ObserveOptions{OCR: true}, time.Now(), textSource{frame: frameOf(2400, 1080), shot: png})
	if st.From != "screenshot" || stub.encoded != 1 || len(ocrLabels(s)) != 1 {
		t.Fatalf("stats = %+v", st)
	}
	// A frame rounded to the encoder's multiple still fits.
	if !fits(image.Rect(0, 0, 1080, 2392), scrW, scrH) || fits(image.Rect(0, 0, 1080, 1920), scrW, scrH) {
		t.Error("fits is wrong")
	}
	// No video and no screenshot (a background display without FFmpeg).
	_, s = miniProgram(false)
	fail := func(context.Context) (image.Image, error) { return nil, driver.ErrUnsupported }
	st = addText(ctx, s, sc, driver.ObserveOptions{OCR: true}, time.Now(), textSource{frame: fail})
	if st.Err == nil || !errors.Is(st.Err, driver.ErrUnsupported) || len(ocrLabels(s)) != 0 {
		t.Fatalf("without a picture: %+v", st)
	}
}

func TestAddTextKeyboard(t *testing.T) {
	keys := []ocr.Line{line("Repay now", 500, 1200)}
	for i, k := range "QWERTYUIOP" {
		keys = append(keys, line(string(k), 54+i*108, 1900))
	}
	stub := &ocrStub{lines: keys}
	stubOCR(t, stub)
	ctx := context.Background()
	opt := driver.ObserveOptions{OCR: true, Frame: frameOf(scrW, scrH)}
	// The companion lists the keyboard's window.
	tree, s := miniProgram(true)
	addText(ctx, s, scanCompanion(tree), opt, time.Now(), textSource{frame: opt.Frame})
	if got := ocrLabels(s); fmt.Sprint(got) != "[Repay now]" {
		t.Fatalf("with the keyboard's window: %q", got)
	}
	// uiautomator does not: the keys give it away.
	tree, s = miniProgram(true)
	sc := scanCompanion(tree)
	sc.bars, sc.keyboard = nil, false
	addText(ctx, s, sc, opt, time.Now(), textSource{frame: opt.Frame})
	if got := ocrLabels(s); fmt.Sprint(got) != "[Repay now]" {
		t.Fatalf("without it: %q", got)
	}
}

func TestNoOCRMarks(t *testing.T) {
	stub := &ocrStub{supported: fmt.Errorf("no OCR: %w", driver.ErrUnsupported)}
	stubOCR(t, stub)
	ctx := context.Background()
	tree, s := miniProgram(false)
	sc := scanCompanion(tree)
	before := len(s.Elements)
	// Marked on every read, with OCR asked or not, so states do not change
	// between reads.
	for _, opt := range []driver.ObserveOptions{{}, {OCR: true}} {
		_, s = miniProgram(false)
		st := addText(ctx, s, sc, opt, time.Now(), textSource{frame: frameOf(scrW, scrH)})
		if len(s.Elements) != before+1 || stub.images != 0 || st.Skipped == "" {
			t.Fatalf("elements %d (was %d), %+v", len(s.Elements), before, st)
		}
		e := s.Elements[len(s.Elements)-1]
		if e.Role != screen.RoleOther || !e.Has(stateNoOCR) || e.Bounds != (screen.Rect{Y: 300, W: scrW, H: 2100}) || e.Source != screen.SourceTree {
			t.Fatalf("mark = %+v", e)
		}
	}
	// The element of a listed area takes the state instead.
	web := node("android.webkit.WebView", 0, 300, scrW, scrH)
	web.Scrollable = true
	tree = appTree(node("android.widget.FrameLayout", 0, 0, scrW, scrH, header(), web), false)
	s = tree.Screen(companion.ScreenOptions{})
	n := len(s.Elements)
	addText(ctx, s, scanCompanion(tree), driver.ObserveOptions{}, time.Now(), textSource{})
	if len(s.Elements) != n || !s.Elements[n-1].Has(stateNoOCR) || s.Elements[n-1].Role != screen.RoleList {
		t.Fatalf("elements = %+v", s.Elements)
	}
}

// A screen that keeps moving gives no pause and, within a second of the
// last request, no fresh key frame: capture takes what the video has at
// frameWait instead of waiting for it.
func TestCaptureWaitsForAFrameBriefly(t *testing.T) {
	old := frameWait
	frameWait = 50 * time.Millisecond
	t.Cleanup(func() { frameWait = old })
	_, s := miniProgram(false)
	moving := func(ctx context.Context) (image.Image, error) {
		<-ctx.Done() // as frameShot: the newest picture once its context ends
		return image.NewRGBA(image.Rect(0, 0, scrW, scrH)), nil
	}
	start := time.Now()
	pic, err := capture(context.Background(), s, textSource{frame: moving})
	if err != nil || pic.from != "video" || time.Since(start) > time.Second {
		t.Fatalf("capture = %v, %v after %v", pic.from, err, time.Since(start))
	}
}
