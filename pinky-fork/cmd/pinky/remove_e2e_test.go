//go:build e2e && !windows

package main

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/gateway"
)

// buildE2E builds the e2e build of the app.
func buildE2E(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("opens the app")
	}
	bin := filepath.Join(t.TempDir(), "pinky")
	if out, err := exec.Command("go", "build", "-tags", "e2e", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// startAppWith runs the e2e build with args and extra environment, and an
// automation socket.
func startAppWith(t *testing.T, bin, home string, args []string, extra ...string) *uiApp {
	t.Helper()
	sock := filepath.Join(home, "ui.sock")
	os.Remove(sock)
	cmd := exec.Command(bin, args...)
	cmd.Env = append(append(env(home), "MYGO_AUTOMATION="+sock), extra...)
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	a := &uiApp{cmd: cmd, done: make(chan struct{}), out: out}
	go func() {
		cmd.Wait()
		close(a.done)
	}()
	t.Cleanup(func() {
		if a.conn != nil {
			a.conn.Close()
		}
		select {
		case <-a.done:
		default:
			cmd.Process.Kill()
			<-a.done
		}
	})
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			a.conn, a.r = conn, bufio.NewReader(conn)
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("no automation socket: %v\n%s", err, out.String())
		}
	}
}

// pressLabel presses the element labelled label, whatever its role, once
// it shows.
func (a *uiApp) pressLabel(t *testing.T, win int, label string) {
	t.Helper()
	a.pressRole(t, win, "", label)
}

// pressRole presses the element with this role ("" for any) and label once
// it shows.
func (a *uiApp) pressRole(t *testing.T, win int, role, label string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		for _, n := range a.nodes(t, win) {
			if (role == "" || n.Role == role) && n.Label == label {
				a.call(t, "act", map[string]any{"window": win, "id": n.ID, "action": "press"}, nil)
				return
			}
		}
	}
	var labels []string
	for _, n := range a.nodes(t, win) {
		labels = append(labels, n.Role+" "+n.Label)
	}
	t.Fatalf("no %s %q in %q", role, label, labels)
}

// pressPrefix presses the first element whose label starts with prefix,
// such as a phone's row in the sidebar, whose label ends with its status.
func (a *uiApp) pressPrefix(t *testing.T, win int, prefix string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		for _, n := range a.nodes(t, win) {
			if strings.HasPrefix(n.Label, prefix) {
				a.call(t, "act", map[string]any{"window": win, "id": n.ID, "action": "press"}, nil)
				return
			}
		}
	}
	t.Fatalf("nothing labelled %q…", prefix)
}

// phonesSettings shows Settings › Phones.
func (a *uiApp) phonesSettings(t *testing.T, win int) {
	t.Helper()
	a.call(t, "menu", map[string]any{"window": win, "path": []string{"Debug", "Show Main Window"}}, nil)
	a.call(t, "menu", map[string]any{"window": win, "path": []string{"Phones", "Settings…"}}, nil)
	a.pressRole(t, win, "tab", "Phones")
}

// gatewayOf reads the running app's gateway over its control socket.
type gatewayOf struct {
	t   *testing.T
	ctl *api.Control
}

func dialGateway(t *testing.T, d api.Dirs) *gatewayOf {
	t.Helper()
	var ctl *api.Control
	waitFor(t, "the control socket", 20*time.Second, func() bool {
		c, err := api.DialControl(d)
		ctl = c
		return err == nil
	})
	t.Cleanup(func() { ctl.Close() })
	return &gatewayOf{t: t, ctl: ctl}
}

func (g *gatewayOf) device(id string) (gateway.DeviceView, bool) {
	var devs []gateway.DeviceView
	if err := g.ctl.Call("devices", nil, &devs); err != nil {
		g.t.Fatal(err)
	}
	for _, d := range devs {
		if d.Info.ID == id {
			return d, true
		}
	}
	return gateway.DeviceView{}, false
}

func (g *gatewayOf) online(id string) bool {
	d, ok := g.device(id)
	return ok && d.Status != gateway.DeviceOffline
}

func (g *gatewayOf) tasksOn(id string) int {
	var tasks []gateway.TaskView
	if err := g.ctl.Call("history", api.HistoryParams{}, &tasks); err != nil {
		g.t.Fatal(err)
	}
	n := 0
	for _, task := range tasks {
		if task.DeviceID == id {
			n++
		}
	}
	return n
}

// TestRemovePhoneInTheApp drives the e2e build with the fake phones over
// MyGo's automation socket. An agent finishes a task on each phone;
// Settings › Phones removes the iPhone with its history, the Pixel's ⋯
// menu removes it keeping its history. The fake phones count as plugged
// in, so each comes back at once as a new phone, as a phone on a cable
// does. It opens windows, so it needs a display with the screen unlocked:
//
//	go test -tags e2e -run TestRemovePhoneInTheApp ./cmd/pinky
func TestRemovePhoneInTheApp(t *testing.T) {
	bin := buildE2E(t)
	home := pinkyHome(t)
	d := dirsFor(t, home)
	backend := newFakeBackend(t)
	app := startApp(t, bin, home, backend.URL, filepath.Join(home, "opened.txt"))
	win := app.window(t)
	app.waitText(t, win, "iPhone 17 (fake)")
	gw := dialGateway(t, d)

	agent := startMCP(t, bin, home, "claude-code")
	for _, phone := range []string{"iPhone 17 (fake)", "Pixel 9 (fake)"} {
		if text, isErr := agent.call(t, "phone_connect", map[string]any{"device": phone, "task": "Look at the home screen"}); isErr {
			t.Fatalf("connect to %s:\n%s", phone, text)
		}
		if text, isErr := agent.call(t, "phone_release", map[string]any{"device": phone, "outcome": "done", "summary": "Looked."}); isErr {
			t.Fatalf("release %s:\n%s", phone, text)
		}
	}
	agent.cs.Close()
	waitFor(t, "the tasks in history", 20*time.Second, func() bool {
		return gw.tasksOn("fake-iphone-17") == 1 && gw.tasksOn("fake-pixel-9") == 1
	})

	// Settings › Phones removes the iPhone with its history.
	app.phonesSettings(t, win)
	app.pressLabel(t, win, "Remove iPhone 17 (fake)")
	app.waitText(t, win, "Remove iPhone 17 (fake)?")
	for _, want := range []string{"It leaves Pinky's list", "Its history stays in Activity.",
		"It's connected with a cable, so it shows up again right away", "The phone keeps trusting this computer"} {
		if !app.hasText(t, win, want) {
			t.Errorf("the dialog lacks %q", want)
		}
	}
	app.pressLabel(t, win, "Also delete its history: tasks, screenshots and recordings")
	app.pressRole(t, win, "button", "Remove")
	waitFor(t, "the iPhone's history to go", 20*time.Second, func() bool { return gw.tasksOn("fake-iphone-17") == 0 })
	waitFor(t, "the dialog to close", 10*time.Second, func() bool { return !app.hasText(t, win, "Remove iPhone 17 (fake)?") })
	// Plugged in, it is back at once, as a new phone.
	waitFor(t, "the iPhone to come back", 20*time.Second, func() bool { return gw.online("fake-iphone-17") })

	// The Pixel's ⋯ menu removes it and keeps its history.
	app.pressPrefix(t, win, "Pixel 9 (fake), ")
	app.pressLabel(t, win, "More phone controls")
	app.pressLabel(t, win, "Remove this phone…")
	app.waitText(t, win, "Remove Pixel 9 (fake)?")
	app.pressRole(t, win, "button", "Remove")
	waitFor(t, "the dialog to close", 10*time.Second, func() bool { return !app.hasText(t, win, "Remove Pixel 9 (fake)?") })
	waitFor(t, "the Pixel to come back", 20*time.Second, func() bool { return gw.online("fake-pixel-9") })
	if n := gw.tasksOn("fake-pixel-9"); n != 1 {
		t.Errorf("the Pixel's history: %d tasks, want it kept", n)
	}
	run(t, bin, home, "quit")
	app.wait(t)
	log := app.out.String()
	for _, want := range []string{`msg="device removed" device=fake-iphone-17`, `msg="device removed" device=fake-pixel-9`,
		`msg="removed device added back" device=fake-iphone-17`, `msg="removed device added back" device=fake-pixel-9`} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q", want)
		}
	}
}

// TestRemovePhoneOnEmulator removes a phone connected over the network, on
// a real adb server and Android driver: an emulator that the test
// connects by address, as a phone on Wi-Fi (emulator_helpers_test.go). The
// person removes it in Settings › Phones; Pinky drops the connection, and
// when the connection comes back, as adb or another tool may make it, the
// phone stays off the list, after a restart too. Its iPhone provider is
// off (PINKY_PLATFORMS), so a plugged-in iPhone is left alone. Like the
// other tests that open the app, it needs the screen unlocked:
//
//	PINKY_E2E_EMULATOR=emulator-5560 scripts/devlock.sh emulator-5560 \
//	  go test -tags e2e -run TestRemovePhoneOnEmulator ./cmd/pinky
func TestRemovePhoneOnEmulator(t *testing.T) {
	_, addr, hardware := testEmulator(t)
	adb := startPrivateADB(t, false)
	adb.connect(t, addr)
	bin := buildE2E(t)
	home := pinkyHome(t)
	d := dirsFor(t, home)
	start := func() (*uiApp, int, *gatewayOf) {
		app := startAppWith(t, bin, home, []string{"--verbose"},
			"ANDROID_ADB_SERVER_PORT="+adb.port, "PINKY_PLATFORMS=android", "DO_NOT_TRACK=1")
		win := app.window(t)
		return app, win, dialGateway(t, d)
	}
	app, win, gw := start()
	waitFor(t, "the emulator to come online", 90*time.Second, func() bool { return gw.online(hardware) })
	dv, _ := gw.device(hardware)
	if dv.Info.Connection != "wifi" {
		t.Fatalf("the emulator is listed over %q, want wifi", dv.Info.Connection)
	}
	name := dv.Info.Name

	app.phonesSettings(t, win)
	app.pressLabel(t, win, "Remove "+name)
	app.waitText(t, win, "Remove "+name+"?")
	if !app.hasText(t, win, "Pinky disconnects from it and won't connect to it again by itself.") {
		t.Error("the dialog lacks the Wi-Fi note")
	}
	app.pressRole(t, win, "button", "Remove")
	waitFor(t, "the emulator to leave the list", 30*time.Second, func() bool { _, ok := gw.device(hardware); return !ok })
	waitFor(t, "Pinky to drop the connection", 15*time.Second, func() bool { _, ok := adb.serials(t)[addr]; return !ok })

	// The connection comes back, as adb or another tool would make it:
	// Pinky keeps the phone off its list.
	adb.connect(t, addr)
	time.Sleep(10 * time.Second) // five discovery passes
	if _, ok := gw.device(hardware); ok {
		t.Fatal("the removed emulator came back by itself")
	}
	// After a restart too.
	run(t, bin, home, "quit")
	app.wait(t)
	app, _, gw = start()
	time.Sleep(10 * time.Second)
	if _, ok := gw.device(hardware); ok {
		t.Fatal("the removed emulator came back after a restart")
	}
	if adb.serials(t)[addr] != "device" {
		t.Errorf("Pinky dropped a connection it did not make, at start: %v", adb.serials(t))
	}
	run(t, bin, home, "quit")
	app.wait(t)
}
