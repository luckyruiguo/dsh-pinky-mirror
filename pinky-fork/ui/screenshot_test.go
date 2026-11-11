package ui

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestPanelScreenshots renders the approval panel in light and dark into
// $PINKY_SCREENSHOTS, for the notes. It has no video, so a tester draws
// it as the window would.
func TestPanelScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS to a folder to write the panel's screenshots")
	}
	ta := newTestAppTimeout(t, fakeapi.ThreePhones, 45*time.Second)
	ta.waitFor(t, "two approvals", func() bool { return len(ta.approvals()) == 2 })
	for _, dark := range []bool{false, true} {
		w, h := ta.panelSize()
		tt := mg.NewTester(ta.panelView, w, h)
		tt.SetScale(2)
		tt.SetDark(dark)
		tt.Frame()
		name := "approval-panel-light.png"
		if dark {
			name = "approval-panel-dark.png"
		}
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
