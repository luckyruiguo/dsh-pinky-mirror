package ocr

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

func TestExtras(t *testing.T) {
	els := []screen.Element{
		{Role: screen.RoleButton, Label: "Settings, button", Bounds: screen.Rect{X: 0, Y: 0, W: 500, H: 100}, Source: screen.SourceTree},
	}
	lines := []Line{
		{Text: "Settings", Confidence: 0.9, X: 0.1, Y: 0.01, W: 0.2, H: 0.02},          // shown by the button
		{Text: "Delivery in 25 min", Confidence: 0.9, X: 0.1, Y: 0.5, W: 0.5, H: 0.02}, // only OCR
		{Text: "  ", Confidence: 0.9, X: 0.1, Y: 0.6, W: 0.1, H: 0.02},
		{Text: "noise", Confidence: 0.1, X: 0.1, Y: 0.7, W: 0.1, H: 0.02},
	}
	got := Extras(els, lines, 1000, 2000)
	if len(got) != 1 || got[0].Label != "Delivery in 25 min" || got[0].Source != screen.SourceOCR || got[0].Role != screen.RoleText {
		t.Fatalf("Extras = %+v", got)
	}
	if want := (screen.Rect{X: 100, Y: 1000, W: 500, H: 40}); got[0].Bounds != want {
		t.Fatalf("bounds = %+v, want %+v", got[0].Bounds, want)
	}
}

func TestKeyboardTop(t *testing.T) {
	var lines []Line
	for i, l := range "QWERTYU" {
		lines = append(lines, Line{Text: string(l), X: float64(i) / 7, Y: 0.68 + float64(i%2)*0.07, W: 0.03})
	}
	top, ok := KeyboardTop(lines)
	if !ok || top < 0.65 || top > 0.67 {
		t.Fatalf("KeyboardTop = %v, %v", top, ok)
	}
	if _, ok := KeyboardTop(lines[:4]); ok {
		t.Fatal("four letters made a keyboard")
	}
}

// TestEncodeBMP checks the BMP against the pixels it was made from.
func TestEncodeBMP(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(2, 1, color.RGBA{B: 255, A: 255})
	b := encodeBMP(img)
	stride := 12 // 3 pixels of 3 bytes, padded to 4
	if len(b) != 54+stride*2 || string(b[:2]) != "BM" {
		t.Fatalf("BMP of %d bytes, header %q", len(b), b[:2])
	}
	// Bottom-up: the image's top row is the file's last.
	top, bottom := b[54+stride:], b[54:]
	if top[0] != 0 || top[1] != 0 || top[2] != 255 { // red, as BGR
		t.Fatalf("top left = %v", top[:3])
	}
	if bottom[6] != 255 || bottom[7] != 0 || bottom[8] != 0 { // blue
		t.Fatalf("bottom right = %v", bottom[6:9])
	}
}

// textImage draws lines of text, black on white, 64 px high, one per 160
// px from the top, and returns where each starts.
func textImage(t *testing.T, w, h int, lines ...string) *image.RGBA {
	t.Helper()
	f, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: 48, DPI: 96, Hinting: font.HintingFull})
	if err != nil {
		t.Fatal(err)
	}
	defer face.Close()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	d := &font.Drawer{Dst: img, Src: image.Black, Face: face}
	for i, l := range lines {
		d.Dot = fixed.P(60, 160*(i+1))
		d.DrawString(l)
	}
	return img
}

// TestRecognize reads drawn text with the computer's recognizer, where it
// has one.
func TestRecognize(t *testing.T) {
	if err := Supported(); err != nil {
		if !errors.Is(err, driver.ErrUnsupported) {
			t.Fatalf("Supported = %v, which is not ErrUnsupported", err)
		}
		if runtime.GOOS == "darwin" {
			t.Fatalf("no OCR on macOS: %v", err)
		}
		if _, err := RecognizeImage(image.NewRGBA(image.Rect(0, 0, 8, 8)), Options{}); !errors.Is(err, driver.ErrUnsupported) {
			t.Fatalf("RecognizeImage without OCR = %v", err)
		}
		t.Skipf("no OCR here: %v", err)
	}
	// English alone: with Chinese first (Languages), Vision reads some
	// fonts' "d" as "a" (see TestRecognizeChinese).
	en := Options{Languages: []string{"en-US"}}
	img := textImage(t, 1080, 600, "Pinky reads this line", "Order 42 now")
	check := func(name string, lines []Line, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got []string
		found := 0
		for _, l := range lines {
			got = append(got, l.Text)
			switch {
			case strings.Contains(l.Text, "Pinky reads this line"):
				found++
				// Drawn from x 60, baseline y 160 of 600: the box's top is
				// above the baseline and its middle a little above too.
				if l.X < 0.03 || l.X > 0.08 || l.Y > 160.0/600 || l.Y+l.H < 120.0/600 {
					t.Errorf("%s: box of %q = %+v", name, l.Text, l)
				}
			case strings.Contains(l.Text, "Order 42"):
				found++
			}
		}
		if found != 2 {
			t.Fatalf("%s read %q", name, got)
		}
	}
	start := time.Now()
	lines, err := RecognizeImage(img, en)
	check("RecognizeImage", lines, err)
	t.Logf("first recognition: %v", time.Since(start))

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	lines, err = Recognize(buf.Bytes(), en)
	check("Recognize", lines, err)
	t.Logf("from a PNG: %v", time.Since(start))

	// A part of a larger picture, as a frame cropped to the screen.
	big := textImage(t, 1200, 700, "Pinky reads this line", "Order 42 now")
	sub := big.SubImage(image.Rect(0, 0, 1080, 600))
	lines, err = RecognizeImage(sub, en)
	check("RecognizeImage of a sub-image", lines, err)
}

// TestRecognizeChinese reads Chinese next to English with Pinky's
// languages, drawn with a system font of macOS.
func TestRecognizeChinese(t *testing.T) {
	if Supported() != nil {
		t.Skip("no OCR here")
	}
	data, err := os.ReadFile("/System/Library/Fonts/Hiragino Sans GB.ttc")
	if err != nil {
		t.Skipf("no Chinese font: %v", err)
	}
	coll, err := opentype.ParseCollection(data)
	if err != nil {
		t.Fatal(err)
	}
	f, err := coll.Font(0)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: 30, DPI: 96})
	if err != nil {
		t.Fatal(err)
	}
	defer face.Close()
	want := []string{"支付宝 小程序 立即还款", "余额宝 Balance 4,321.09 元"}
	img := image.NewRGBA(image.Rect(0, 0, 1080, 400))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	d := &font.Drawer{Dst: img, Src: image.Black, Face: face}
	for i, s := range want {
		d.Dot = fixed.P(40, 120*(i+1))
		d.DrawString(s)
	}
	lines, err := RecognizeImage(img, Options{Languages: Languages})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range lines {
		got = append(got, l.Text)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || norm(g) == norm(w)
		}
		if !found {
			t.Errorf("read %q, want a line %q", got, w)
		}
	}
}

// TestWarmPicture checks that the picture Warm reads has text in it.
func TestWarmPicture(t *testing.T) {
	if Supported() != nil {
		t.Skip("no OCR here")
	}
	lines, err := Recognize(warmPicture, Options{Languages: Languages})
	if err != nil || len(lines) == 0 {
		t.Fatalf("Recognize(warm.png) = %v, %v", lines, err)
	}
	Warm()
	Warm()
}

func TestJoinWords(t *testing.T) {
	for _, c := range []struct {
		words []string
		want  string
	}{
		{[]string{"Order", "42", "now"}, "Order 42 now"},
		{[]string{"立即", "还", "款"}, "立即还款"},
		{[]string{"余额宝", "Balance", "4,321.09", "元"}, "余额宝 Balance 4,321.09 元"},
		{[]string{"设置", "，", "完成"}, "设置，完成"},
		{[]string{"", " Pay ", ""}, "Pay"},
	} {
		if got := joinWords(c.words); got != c.want {
			t.Errorf("joinWords(%q) = %q, want %q", c.words, got, c.want)
		}
	}
}

func TestBGRA(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	img.Set(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	img.Set(1, 0, color.RGBA{R: 100, G: 100, B: 150, A: 255})
	pix, w, h := bgra(img, 0)
	if w != 4 || h != 2 || len(pix) != 32 || pix[0] != 50 || pix[1] != 100 || pix[2] != 200 || pix[3] != 255 {
		t.Fatalf("bgra = %v, %dx%d", pix[:8], w, h)
	}
	// Longer than the engine takes: scaled down by a whole factor,
	// averaging the pixels it folds together.
	pix, w, h = bgra(img, 3)
	if w != 2 || h != 1 || pix[0] != 50 || pix[1] != 50 || pix[2] != 75 {
		t.Fatalf("scaled = %v, %dx%d", pix, w, h)
	}
}
