package ui

import (
	"image"
	"strings"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"
)

// TestDurationsDoNotWrap checks that elapsed times hold no space a line
// can break at, and that MyGo keeps a no-break space: where "Pixel 9 · 38 s"
// fits up to "38" but not the "s", a plain space leaves the "s" alone on
// the second line, and a no-break space takes "38 s" there together. The
// task card's "Claude Code · sdk_gphone64_arm64 · 38 s" showed the first.
func TestDurationsDoNotWrap(t *testing.T) {
	for _, d := range []time.Duration{0, 38 * time.Second, 62 * time.Second, 2*time.Hour + 5*time.Minute} {
		if s := elapsed(d); strings.ContainsAny(s, " \t") {
			t.Errorf("elapsed(%v) = %q has a breakable space", d, s)
		}
	}

	const size = 40
	plain, joined := "Pixel 9 · 38 s", "Pixel 9 · 38"+nbsp+"s"
	measure := mg.NewTester(func(c *mg.Context) {
		mg.Column(c).AlignItems(mg.Start).Children(func() {
			mg.Text(c, "Pixel 9 · 38").FontSize(size).NoWrap()
			mg.Text(c, plain).FontSize(size).NoWrap()
			mg.Text(c, "38 s").FontSize(size).NoWrap()
		})
	}, 800, 400)
	upTo38, ok1 := measure.Find("Pixel 9 · 38")
	whole, ok2 := measure.Find(plain)
	unit, ok3 := measure.Find("38 s")
	if !ok1 || !ok2 || !ok3 || whole.W <= upTo38.W {
		t.Fatalf("cannot measure: %+v %+v %+v", upTo38, whole, unit)
	}
	w := (upTo38.W + whole.W) / 2 // room up to "38", not for the "s"
	tt := mg.NewTester(func(c *mg.Context) {
		mg.Column(c).AlignItems(mg.Start).Gap(30).Padding(10).Children(func() {
			mg.Text(c, plain).FontSize(size).Width(w)
			mg.Text(c, joined).FontSize(size).Width(w)
		})
	}, 800, 400)
	img := tt.Image()
	scale := float32(img.Rect.Dx()) / 800
	// secondLine returns how wide the ink on a text's second line is.
	secondLine := func(s string) float32 {
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("no %q in %q", s, tt.Texts())
		}
		if r.H < 1.5*whole.H {
			t.Fatalf("%q did not wrap at %.1f", s, w)
		}
		minX, maxX := -1, -1
		for y := int((r.Y + r.H/2) * scale); y < int((r.Y+r.H)*scale); y++ {
			for x := int(r.X * scale); x < int((r.X+r.W+20)*scale) && x < img.Rect.Dx(); x++ {
				if ink(img, x, y) {
					if minX < 0 || x < minX {
						minX = x
					}
					maxX = max(maxX, x)
				}
			}
		}
		if minX < 0 {
			t.Fatalf("no ink on the second line of %q", s)
		}
		return float32(maxX-minX) / scale
	}
	if got := secondLine(plain); got > unit.W*0.6 {
		t.Fatalf("with a plain space the second line is %.1f wide, not the lone “s”; this proves nothing", got)
	}
	if got := secondLine(joined); got < unit.W*0.8 {
		t.Errorf("with a no-break space the second line is %.1f wide, less than “38 s” (%.1f): MyGo broke at it", got, unit.W)
	}
}

func ink(img *image.RGBA, x, y int) bool {
	i := img.PixOffset(x, y)
	return img.Pix[i+3] > 100 && (int(img.Pix[i])+int(img.Pix[i+1])+int(img.Pix[i+2]))/3 < 140
}
