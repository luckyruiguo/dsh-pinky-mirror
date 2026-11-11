package ui

import (
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestSidebarVibrancy: where the window shows its material, the sidebar
// leaves it to show through and the page and the right panel stay
// opaque; where it shows none, the sidebar is the wings' gray.
func TestSidebarVibrancy(t *testing.T) {
	ta := newTestApp(t, fakeapi.ThreePhones)
	tt := mg.NewTester(ta.view, 1280, 820)
	for _, shows := range []bool{true, false} {
		tt.SetVibrancy(shows)
		tt.Frame()
		img := tt.Image()
		scale := img.Bounds().Dx() / 1280
		alpha := func(x, y int) uint8 { return img.RGBAAt(x*scale, y*scale).A }
		// Below the sidebar's rows, above Activity; in the stage beside
		// the phone; in the right panel below its cards.
		side, stage, panel := alpha(sidebarWidth/2, 600), alpha(sidebarWidth+20, 600), alpha(1280-rightPanelWidth/2, 800)
		if shows && side != 0 {
			t.Errorf("with the material, the sidebar draws a background (alpha %d)", side)
		}
		if !shows && side != 255 {
			t.Errorf("without the material, the sidebar is not opaque (alpha %d)", side)
		}
		if stage != 255 || panel != 255 {
			t.Errorf("material %v: the stage (alpha %d) or the right panel (alpha %d) is not opaque", shows, stage, panel)
		}
	}
}
