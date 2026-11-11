//go:build e2e && !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
)

// TestApprovalShotInTheApp drives the e2e build of the desktop app over
// MyGo's automation socket, in English and in Simplified Chinese, with an
// agent on `pinky mcp` paying for lunch on the fake Pixel. With the main
// window hidden, the floating panel shows the payment with its
// screenshot, taller than a panel without one; a press on the screenshot
// opens the large view left of the panel, its Close closes it, and Allow
// pays. It opens windows, so it needs a display with the screen unlocked:
//
//	go test -tags e2e -run TestApprovalShotInTheApp ./cmd/pinky
//
// With PINKY_SCREENSHOTS set to a folder it captures the panel and the
// large view there.
func TestApprovalShotInTheApp(t *testing.T) {
	if testing.Short() {
		t.Skip("opens the app")
	}
	bin := filepath.Join(t.TempDir(), "pinky")
	build := exec.Command("go", "build", "-tags", "e2e", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	for _, lang := range []string{"en", "zh-Hans"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("MYGO_LOCALE", lang)
			approvalShotInTheApp(t, bin, lang)
		})
	}
}

func approvalShotInTheApp(t *testing.T, bin, lang string) {
	m := locales.In(lang)
	const phone = "Pixel 9 (fake)"
	home := pinkyHome(t)
	backend := newFakeBackend(t)
	app := startApp(t, bin, home, backend.URL, filepath.Join(home, "opened.txt"))
	win := app.window(t)
	app.waitText(t, win, phone)

	agent := startMCP(t, bin, home, "claude-code")
	if text, isErr := agent.call(t, "phone_connect", map[string]any{"device": phone, "task": "Lunch"}); isErr {
		t.Fatalf("connect:\n%s", text)
	}
	text, _ := agent.call(t, "phone_launch", map[string]any{"app": "Dinely"})
	text, _ = agent.call(t, "phone_tap", map[string]any{"ref": refOf(t, text, "Orders")})
	text, _ = agent.call(t, "phone_tap", map[string]any{"ref": refOf(t, text, "Reorder · Chicken rice bowl")})
	text, _ = agent.call(t, "phone_tap", map[string]any{"ref": refOf(t, text, "Cart · 1")})
	place := refOf(t, text, "Place order · $12.80")

	// The window hides, then the payment asks: the floating panel shows it.
	app.call(t, "menu", map[string]any{"window": win, "path": []string{"Debug", "Hide Main Window"}}, nil)
	type result struct {
		text  string
		isErr bool
	}
	done := make(chan result, 1)
	go func() {
		text, isErr := agent.call(t, "phone_tap", map[string]any{"ref": place, "intent": "Pay $12.80 for lunch"})
		done <- result{text, isErr}
	}()
	panel := app.waitWindow(t, m.PanelWindowTitle())
	label := m.PanelShotLabel(phone)
	app.waitNode(t, panel.ID, label)
	panel = app.waitWindow(t, m.PanelWindowTitle()) // sized for the screenshot by now
	if panel.Bounds.W != 400 || panel.Bounds.H <= 330 {
		t.Errorf("the panel is %v×%v, want 400 wide and taller than one without a screenshot", panel.Bounds.W, panel.Bounds.H)
	}
	app.capture(t, panel.ID, "app-panel-"+lang+".png")

	// The screenshot opens the large view, left of the panel.
	app.pressNode(t, panel.ID, label)
	viewer := app.waitWindow(t, m.ShotLabel(phone))
	if viewer.Bounds.X+viewer.Bounds.W > panel.Bounds.X+1 || viewer.Bounds.H < 500 {
		t.Errorf("the large view is at %+v, the panel at %+v", viewer.Bounds, panel.Bounds)
	}
	app.waitText(t, viewer.ID, m.GwAskPay("Place order · $12.80"))
	app.capture(t, viewer.ID, "app-viewer-"+lang+".png")
	app.pressNode(t, viewer.ID, m.ShotClose())
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if _, ok := app.visibleWindow(t, m.ShotLabel(phone)); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the large view did not close")
		}
	}

	// Allow pays.
	app.pressNode(t, panel.ID, m.NeedsAllow())
	select {
	case r := <-done:
		if r.isErr || !strings.HasPrefix(r.text, "settled") {
			t.Fatalf("the payment:\n%s", r.text)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("the payment never returned\n%s", app.out.String())
	}
	agent.cs.Close()
	run(t, bin, home, "quit")
	app.wait(t)
}

type uiWindow struct {
	ID      int
	Title   string
	Visible bool
	Bounds  struct{ X, Y, W, H float64 }
}

// visibleWindow returns the visible window with the title.
func (a *uiApp) visibleWindow(t *testing.T, title string) (uiWindow, bool) {
	t.Helper()
	var res struct {
		Windows []struct {
			ID      int
			Title   string
			Visible bool
			Bounds  struct{ X, Y, Width, Height float64 }
		}
	}
	a.call(t, "windows", nil, &res)
	for _, w := range res.Windows {
		if w.Visible && w.Title == title {
			out := uiWindow{ID: w.ID, Title: w.Title, Visible: true}
			out.Bounds.X, out.Bounds.Y, out.Bounds.W, out.Bounds.H = w.Bounds.X, w.Bounds.Y, w.Bounds.Width, w.Bounds.Height
			return out, true
		}
	}
	return uiWindow{}, false
}

func (a *uiApp) waitWindow(t *testing.T, title string) uiWindow {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if w, ok := a.visibleWindow(t, title); ok {
			return w
		}
	}
	t.Fatalf("no window %q\n%s", title, a.out.String())
	return uiWindow{}
}

// capture writes a window's picture to $PINKY_SCREENSHOTS, when set.
func (a *uiApp) capture(t *testing.T, win int, name string) {
	t.Helper()
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		return
	}
	var res struct{ PNG []byte }
	a.call(t, "capture", map[string]any{"window": win}, &res)
	if err := os.WriteFile(filepath.Join(dir, name), res.PNG, 0o644); err != nil {
		t.Fatal(err)
	}
}
