package local

import (
	"errors"
	"slices"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/screen"
)

// A synthetic Settings screen: one row at the top, one at the bottom.
func settings(overlays ...companion.Window) *companion.Tree {
	row := func(text string, top int) *companion.Node {
		return &companion.Node{Class: "android.widget.LinearLayout", Clickable: true,
			Bounds: companion.Bounds{0, top, 1080, top + 190},
			Children: []*companion.Node{{Class: "android.widget.TextView", Text: text,
				Bounds: companion.Bounds{40, top + 60, 1000, top + 130}}}}
	}
	app := companion.Window{ID: 1, Type: companion.WindowApplication, Bounds: companion.Bounds{0, 0, 1080, 2400},
		Active: true, Focused: true, Package: "com.android.settings", App: "Settings",
		Root: &companion.Node{Class: "android.widget.FrameLayout", Bounds: companion.Bounds{0, 0, 1080, 2400},
			Children: []*companion.Node{row("Brightness", 300), row("Colors", 2100)}}}
	return &companion.Tree{Display: companion.Display{Width: 1080, Height: 2400, DensityDPI: 420},
		Windows: append(overlays, app)}
}

func labels(s *screen.Screen) []string {
	var out []string
	for _, e := range s.Elements {
		out = append(out, e.Label)
	}
	return out
}

// Pinky's approval card lies over Settings' bottom row. The agent reads
// Settings as it is without the card, the card's area counts as covered,
// and another service's overlay still hides what it lies over.
func TestAgentScreenLeavesOutOwnOverlays(t *testing.T) {
	pkg := Standalone.Package
	card := companion.Window{ID: 9, Type: companion.WindowAccessibilityOverlay, Layer: 3, Title: pkg,
		Bounds: companion.Bounds{31, 1847, 1048, 2306}}
	pill := companion.Window{ID: 8, Type: companion.WindowAccessibilityOverlay, Layer: 2, Title: pkg,
		Bounds: companion.Bounds{186, 149, 894, 270}}

	bare := agentScreen(settings(), pkg)
	with := agentScreen(settings(card, pill), pkg)
	if !slices.Equal(labels(with), labels(bare)) || !slices.Contains(labels(with), "Colors") {
		t.Fatalf("with Pinky's card the agent reads %q, without it %q", labels(with), labels(bare))
	}
	for _, r := range []screen.Rect{{X: 31, Y: 1847, W: 1017, H: 459}, {X: 186, Y: 149, W: 708, H: 121}} {
		if !slices.Contains(with.Covered, r) {
			t.Errorf("covered %v, want %v in it", with.Covered, r)
		}
	}
	if with.App.ID != "com.android.settings" {
		t.Errorf("app %q, want Settings", with.App.ID)
	}

	other := card
	other.Title = "Some other service"
	other.Bounds = companion.Bounds{0, 1847, 1080, 2400}
	if s := agentScreen(settings(other), pkg); slices.Contains(labels(s), "Colors") {
		t.Errorf("another service's overlay hid nothing: %q", labels(s))
	}
}

func TestOwnUIRefusalIsNotDone(t *testing.T) {
	d := &Device{app: Standalone}
	err := d.wrap(&companion.Error{Code: "own_ui", Message: "Pinky's own controls"})
	if !errors.Is(err, ErrOwnUI) || !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("own_ui became %v; want ErrOwnUI, which the gateway reports as not done", err)
	}
}
