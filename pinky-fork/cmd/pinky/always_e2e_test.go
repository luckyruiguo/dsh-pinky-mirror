//go:build e2e && !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/api"
)

// TestAlwaysAllowInTheApp drives the e2e build of the desktop app over
// MyGo's automation socket, with an agent on `pinky mcp`: the person
// answers the lunch payment with More options › Always allow payments in
// Dinely, the next payment goes through without asking, Settings › Safety
// lists the answer after a restart, and its × makes payments ask again. It
// opens windows, so it needs a display with the screen unlocked:
//
//	go test -tags e2e -run TestAlwaysAllowInTheApp ./cmd/pinky
func TestAlwaysAllowInTheApp(t *testing.T) {
	if testing.Short() {
		t.Skip("opens the app")
	}
	bin := filepath.Join(t.TempDir(), "pinky")
	build := exec.Command("go", "build", "-tags", "e2e", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := pinkyHome(t)
	d := dirsFor(t, home)
	backend := newFakeBackend(t)
	urls := filepath.Join(home, "opened.txt")

	app := startApp(t, bin, home, backend.URL, urls)
	win := app.window(t)
	app.waitText(t, win, "Pixel 9 (fake)")

	agent := startMCP(t, bin, home, "claude-code")
	if text, isErr := agent.call(t, "phone_connect", map[string]any{"device": "Pixel 9 (fake)", "task": "Lunch"}); isErr {
		t.Fatalf("connect:\n%s", text)
	}
	// payment walks to Dinely's cart and taps Place order, in the
	// background, since the tap may wait for the person.
	type result struct {
		text  string
		isErr bool
	}
	payment := func(ag *mcpClient) <-chan result {
		text, _ := ag.call(t, "phone_launch", map[string]any{"app": "Dinely"})
		text, _ = ag.call(t, "phone_tap", map[string]any{"ref": refOf(t, text, "Orders")})
		text, _ = ag.call(t, "phone_tap", map[string]any{"ref": refOf(t, text, "Reorder · Chicken rice bowl")})
		text, _ = ag.call(t, "phone_tap", map[string]any{"ref": refOf(t, text, "Cart · 1")})
		place := refOf(t, text, "Place order · $12.80")
		done := make(chan result, 1)
		go func() {
			text, isErr := ag.call(t, "phone_tap", map[string]any{"ref": place, "intent": "Pay $12.80 for lunch"})
			done <- result{text, isErr}
		}()
		return done
	}
	got := func(done <-chan result) result {
		t.Helper()
		select {
		case r := <-done:
			return r
		case <-time.After(60 * time.Second):
			t.Fatalf("the payment never returned\n%s", app.out.String())
		}
		return result{}
	}

	// The payment asks; the person answers on the card. The window comes
	// to the front first: MyGo draws no frames for a covered window.
	done := payment(agent)
	app.call(t, "menu", map[string]any{"window": win, "path": []string{"Debug", "Show Main Window"}}, nil)
	app.pressNode(t, win, "More options")
	app.pressNode(t, win, "Always allow payments in Dinely")
	if r := got(done); r.isErr || !strings.HasPrefix(r.text, "settled") || !strings.Contains(r.text, `from now on "Payments" no longer asks in Dinely`) {
		t.Fatalf("the approved payment:\n%s", r.text)
	}
	rules, err := os.ReadFile(d.Rules())
	if err != nil || !strings.Contains(string(rules), "\nalways_allow:") || !strings.Contains(string(rules), "rule: Payments") ||
		!strings.Contains(string(rules), "app: com.dinely.app") {
		t.Fatalf("rules.yaml (%v):\n%s", err, rules)
	}

	// The next payment goes through without asking.
	if r := got(payment(agent)); r.isErr || !strings.HasPrefix(r.text, "settled") ||
		!strings.Contains(r.text, `No approval needed: the person always allows "Payments" in Dinely.`) {
		t.Fatalf("the second payment:\n%s", r.text)
	}

	// After a restart, Settings › Safety lists it.
	agent.cs.Close()
	run(t, bin, home, "quit")
	app.wait(t)
	app = startApp(t, bin, home, backend.URL, urls)
	win = app.window(t)
	app.waitText(t, win, "Pixel 9 (fake)")
	app.call(t, "menu", map[string]any{"window": win, "path": []string{"Debug", "Show Main Window"}}, nil)
	app.call(t, "menu", map[string]any{"window": win, "path": []string{"Phones", "Settings…"}}, nil)
	app.waitText(t, win, "Always allowed")
	app.waitNode(t, win, "Ask again about payments in Dinely")
	for _, want := range []string{"Dinely", "Payments"} {
		if !app.hasText(t, win, want) {
			t.Errorf("no %q in Settings › Safety", want)
		}
	}

	// Its × makes payments in Dinely ask again.
	app.pressNode(t, win, "Ask again about payments in Dinely")
	app.waitText(t, win, "None yet.")
	if _, ok := app.node(t, win, "Ask again about payments in Dinely"); ok {
		t.Fatal("the row is still there")
	}
	if rules, _ := os.ReadFile(d.Rules()); strings.Contains(string(rules), "\nalways_allow:") {
		t.Fatalf("rules.yaml still always allows:\n%s", rules)
	}
	agent = startMCP(t, bin, home, "claude-code")
	if text, isErr := agent.call(t, "phone_connect", map[string]any{"device": "Pixel 9 (fake)", "task": "Lunch again"}); isErr {
		t.Fatalf("connect:\n%s", text)
	}
	done = payment(agent)
	ctl, err := api.DialControl(d)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	var needs []api.NeedInfo
	for deadline := time.Now().Add(20 * time.Second); len(needs) == 0 && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := ctl.Call("needs", nil, &needs); err != nil {
			t.Fatal(err)
		}
	}
	if len(needs) != 1 || needs[0].Rule != "Payments" {
		t.Fatalf("needs after taking it back: %+v", needs)
	}
	run(t, bin, home, "decide", needs[0].ID, "deny")
	if r := got(done); !strings.HasPrefix(r.text, "approval_denied") {
		t.Fatalf("the payment after taking it back:\n%s", r.text)
	}
	agent.cs.Close()
	run(t, bin, home, "quit")
	app.wait(t)
}

// node returns the element labelled label, whatever its role: a menu
// button, an item of a menu drawn in the window, a button.
func (a *uiApp) node(t *testing.T, win int, label string) (uiNode, bool) {
	for _, n := range a.nodes(t, win) {
		if n.Label == label {
			return n, true
		}
	}
	return uiNode{}, false
}

func (a *uiApp) waitNode(t *testing.T, win int, label string) uiNode {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if n, ok := a.node(t, win, label); ok {
			return n
		}
	}
	var labels []string
	for _, n := range a.nodes(t, win) {
		labels = append(labels, n.Role+" "+n.Label)
	}
	t.Fatalf("no %q in %q", label, labels)
	return uiNode{}
}

// pressNode presses the element labelled label once it shows.
func (a *uiApp) pressNode(t *testing.T, win int, label string) {
	t.Helper()
	n := a.waitNode(t, win, label)
	a.call(t, "act", map[string]any{"window": win, "id": n.ID, "action": "press"}, nil)
}
