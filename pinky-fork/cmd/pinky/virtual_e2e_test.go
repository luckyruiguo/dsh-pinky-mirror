//go:build e2e && !windows

package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/gateway"
)

// TestVirtualSimulatorInTheApp starts an iOS Simulator from Add phone ›
// Virtual phone in the e2e build, as the person would. PINKY_DEVICES names
// no phone, so the Simulator joins Pinky only because the person started
// it, and usbmuxd is out of reach, so no iPhone or iPad on this Mac is
// touched. The first start builds WebDriverAgent into the test's own data
// folder; then an agent opens Settings on it in a task, Shut down takes it
// offline, and Start on its page brings it back with the helper built.
// The Simulator must exist, shut down; the test shuts it down again:
//
//	PINKY_E2E_SIMULATOR=<udid> PINKY_E2E_SIMULATOR_OS=26.5 \
//	  go test -tags e2e -run TestVirtualSimulatorInTheApp -timeout 30m ./cmd/pinky
//
// PINKY_SCREENSHOTS=<dir> keeps pictures of the phone's page.
// PINKY_E2E_WDA_ARCHIVE=<dir>/WebDriverAgent-<version>.tar.gz puts the
// source Pinky would download in its data folder first, for a computer
// where a firewall asks about each new test binary: Pinky still checks
// and builds it.
func TestVirtualSimulatorInTheApp(t *testing.T) {
	udid := os.Getenv("PINKY_E2E_SIMULATOR")
	if udid == "" {
		t.Skip("PINKY_E2E_SIMULATOR names no Simulator")
	}
	t.Cleanup(func() { exec.Command("xcrun", "simctl", "shutdown", udid).Run() })
	out, err := exec.Command("xcrun", "simctl", "list", "devices").Output()
	if err != nil {
		t.Fatal(err)
	}
	var name string
	for _, l := range strings.Split(string(out), "\n") {
		if i := strings.Index(l, " ("+udid+")"); i > 0 {
			name = strings.TrimSpace(l[:i])
		}
	}
	if name == "" {
		t.Fatalf("no Simulator %s", udid)
	}
	virtualInTheApp(t, virtualCase{
		platform: "ios",
		id:       udid,
		name:     name,
		kind:     "Simulator",
		line:     "iOS " + os.Getenv("PINKY_E2E_SIMULATOR_OS"),
		phoneID:  func(*gatewayOf) string { return udid },
		running: func() bool {
			out, _ := exec.Command("xcrun", "simctl", "list", "devices").Output()
			for _, l := range strings.Split(string(out), "\n") {
				if strings.Contains(l, udid) {
					return strings.Contains(l, "(Booted)")
				}
			}
			return false
		},
		screen: "General", // in Settings
		boot:   15 * time.Minute,
		setup: func(d api.Dirs) {
			archive := os.Getenv("PINKY_E2E_WDA_ARCHIVE")
			if archive == "" {
				return
			}
			b, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(d.Data, "WebDriverAgent")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, filepath.Base(archive)), b, 0o644); err != nil {
				t.Fatal(err)
			}
		},
	})
}

// TestVirtualEmulatorInTheApp does the same with an Android emulator: an
// AVD that exists and does not run, of Android 11 or later (the Android 10
// emulator's video encoder fails, so its tasks have no recording). The
// emulator Pinky starts must come back on the same adb serial after Shut
// down. Pinky runs against an adb server of the test's own that finds
// emulators but no phone over USB or the network
// (emulator_helpers_test.go), so a phone plugged into this computer is not
// even asked about itself; other emulators on it stay hidden by
// PINKY_DEVICES, and the test presses nothing of theirs.
//
//	PINKY_E2E_AVD=<name> go test -tags e2e -run TestVirtualEmulatorInTheApp -timeout 30m ./cmd/pinky
func TestVirtualEmulatorInTheApp(t *testing.T) {
	avd := os.Getenv("PINKY_E2E_AVD")
	if avd == "" {
		t.Skip("PINKY_E2E_AVD names no AVD")
	}
	adb := startPrivateADB(t, true)
	// The emulator's crash reports go to a folder of the test's own: after
	// a crash, the next windowed emulator on this computer would first ask
	// whether to send them.
	crashes := t.TempDir()
	var serial string
	t.Cleanup(func() {
		if serial != "" {
			adb.run(t, "-s", serial, "emu", "kill")
		}
	})
	virtualInTheApp(t, virtualCase{
		platform: "android",
		env:      []string{"ANDROID_ADB_SERVER_PORT=" + adb.port, "ANDROID_EMU_CRASH_REPORTING_DATABASE=" + crashes},
		id:       avd,
		name:     strings.ReplaceAll(avd, "_", " "), // with no display name of its own
		kind:     "Emulator",
		line:     "Android",
		phoneID: func(gw *gatewayOf) string {
			for _, d := range gw.devices() {
				if d.Info.Virtual && d.Info.VirtualID == avd {
					if serial != "" && d.Info.ID != serial {
						gw.t.Errorf("the emulator came back as %s, was %s", d.Info.ID, serial)
					}
					serial = d.Info.ID
					return d.Info.ID
				}
			}
			return ""
		},
		running: func() bool { return serial != "" && adb.serials(t)[serial] == "device" },
		screen:  "Network",
		boot:    5 * time.Minute,
	})
}

type virtualCase struct {
	platform string   // PINKY_PLATFORMS
	env      []string // more of the app's environment
	id       string   // the VirtualPhone's ID
	name     string   // its name in the list
	kind     string   // "Simulator" or "Emulator", as its page says
	line     string   // in its row's line, to tell same-named ones apart
	// phoneID returns the id of its phone in Pinky, "" while there is none.
	phoneID func(*gatewayOf) string
	running func() bool // whether it runs on the computer
	screen  string      // a word Settings shows
	boot    time.Duration
	setup   func(api.Dirs) // before the app starts
}

func virtualInTheApp(t *testing.T, vc virtualCase) {
	bin := buildE2E(t)
	home := pinkyHome(t)
	d := dirsFor(t, home)
	if dir := os.Getenv("PINKY_SCREENSHOTS"); dir != "" {
		// The app's log goes there too, before the home goes.
		t.Cleanup(func() {
			if b, err := os.ReadFile(filepath.Join(d.Data, "pinky.log")); err == nil {
				os.WriteFile(filepath.Join(dir, vc.platform+"-virtual-pinky.log"), b, 0o600)
			}
		})
	}
	if vc.setup != nil {
		vc.setup(d)
	}
	app := startAppWith(t, bin, home, []string{"--verbose"}, append([]string{
		"PINKY_DEVICES=none", "PINKY_PLATFORMS=" + vc.platform, "DO_NOT_TRACK=1",
		// No iPhone or iPad on this Mac is reached.
		"USBMUXD_SOCKET_ADDRESS=unix:///nonexistent/no-usbmuxd", "GO_IOS_AGENT_HOST=127.0.0.1", "GO_IOS_AGENT_PORT=1"},
		vc.env...)...)
	win := app.window(t)
	gw := dialGateway(t, d)

	virtualList := func() {
		app.call(t, "menu", map[string]any{"window": win, "path": []string{"Phones", "Add Phone…"}}, nil)
		// Add phone stays on the step it showed last.
		time.Sleep(500 * time.Millisecond)
		if !app.hasText(t, win, "Only you can start them") {
			app.pressLabel(t, win, "Virtual phone")
		}
		app.waitText(t, win, "Only you can start them")
	}
	// waitOnline waits for its phone to be online after press started it,
	// logging what it shows on the way.
	waitOnline := func(what string, limit time.Duration, press func()) (string, time.Duration) {
		press()
		start := time.Now()
		var id, last string
		waitFor(t, what, limit, func() bool {
			// Add phone and the phone's page say so when it quit while it
			// started, as an emulator that hung.
			if why := app.textWith(t, win, "Couldn't start"); why != "" {
				t.Fatalf("%s: %s", what, why)
			}
			if id == "" {
				id = vc.phoneID(gw)
			}
			if id == "" {
				return false
			}
			dv, ok := gw.device(id)
			if !ok {
				return false
			}
			now := string(dv.Status) + " " + string(dv.Info.State) + " " + string(dv.Info.Setup) + " " + dv.Info.Detail
			if now != last {
				t.Logf("%5.1fs %s: %s", time.Since(start).Seconds(), id, now)
				last = now
			}
			return dv.Status != gateway.DeviceOffline && dv.Info.State == "online"
		})
		return id, time.Since(start)
	}

	// Add phone › Virtual phone › Start.
	virtualList()
	id, took := waitOnline("the virtual phone online", vc.boot, func() { app.pressIn(t, win, vc.id, vc.name, vc.line, "Start") })
	t.Logf("started and online in %s", took.Round(time.Second))
	dv, _ := gw.device(id)
	if !dv.Info.Virtual || dv.Info.VirtualID != vc.id {
		t.Errorf("the phone is not marked virtual: %+v", dv.Info)
	}
	name := dv.Info.Name
	// Named as in the list, not from what a booting phone answered.
	if name != vc.name {
		t.Errorf("the phone is named %q, not %q", name, vc.name)
	}

	// Open shows its page, with the mirror.
	virtualList()
	app.pressIn(t, win, vc.id, vc.name, vc.line, "Open")
	app.waitText(t, win, vc.kind)
	// The window comes to the front first: MyGo draws no frames for a
	// covered window.
	app.call(t, "menu", map[string]any{"window": win, "path": []string{"Debug", "Show Main Window"}}, nil)
	time.Sleep(5 * time.Second)
	app.capture(t, win, vc.platform+"-virtual-page.png")

	// An agent works on it in a task, once the person allows its first
	// connection: approvals are the same as on a phone.
	agent := startMCP(t, bin, home, "claude-code")
	connect := map[string]any{"device": name, "task": "Open Settings"}
	text, isErr := agent.call(t, "phone_connect", connect)
	if isErr || !strings.HasPrefix(text, "approval_pending") || !strings.Contains(text, "rule: New agent") {
		t.Fatalf("the first connect did not ask the person:\n%s", text)
	}
	// The window comes to the front first: MyGo draws no frames for a
	// covered window.
	app.call(t, "menu", map[string]any{"window": win, "path": []string{"Debug", "Show Main Window"}}, nil)
	app.pressNode(t, win, "Approve")
	// phone_connect takes no approval_id: the same call finds the
	// person's answer.
	if text, isErr = agent.call(t, "phone_connect", connect); isErr || strings.HasPrefix(text, "approval_") {
		t.Fatalf("connect:\n%s", text)
	}
	text, isErr = agent.call(t, "phone_launch", map[string]any{"app": "Settings"})
	if isErr {
		t.Fatalf("launch Settings:\n%s", text)
	}
	if !strings.Contains(text, vc.screen) {
		text, _ = agent.call(t, "phone_observe", map[string]any{"wait_ms": 2000})
	}
	if !strings.Contains(text, vc.screen) {
		t.Errorf("Settings shows no %q:\n%s", vc.screen, text)
	}
	app.call(t, "menu", map[string]any{"window": win, "path": []string{"Debug", "Show Main Window"}}, nil)
	time.Sleep(2 * time.Second)
	app.capture(t, win, vc.platform+"-virtual-task.png")
	if text, isErr := agent.call(t, "phone_release", map[string]any{"outcome": "done", "summary": "Opened Settings."}); isErr {
		t.Fatalf("release:\n%s", text)
	}
	agent.cs.Close()
	waitFor(t, "the task in history", 20*time.Second, func() bool { return gw.tasksOn(id) == 1 })
	// The recording's last packets go to the file once the task's last
	// read vouches for them, which can be after the task is in history.
	var videos []string
	for deadline := time.Now().Add(30 * time.Second); len(videos) == 0 && time.Now().Before(deadline); {
		time.Sleep(500 * time.Millisecond)
		filepath.WalkDir(filepath.Join(d.Data, "recordings"), func(path string, e fs.DirEntry, err error) error {
			if err == nil && !e.IsDir() && strings.HasSuffix(path, ".mp4") {
				videos = append(videos, path)
			}
			return nil
		})
	}
	t.Logf("recordings: %q", videos)
	if len(videos) == 0 {
		t.Error("the task left no recording")
	}

	// Shut down in Add phone › Virtual phone takes it offline.
	virtualList()
	app.pressIn(t, win, vc.id, vc.name, vc.line, "Shut down")
	waitFor(t, "the phone offline", 2*time.Minute, func() bool { return !gw.online(id) })
	waitFor(t, "it to stop running", 2*time.Minute, func() bool { return !vc.running() })

	// Its page starts it again.
	app.pressPrefix(t, win, name+", ")
	app.waitText(t, win, "It isn't running.")
	again, took := waitOnline("the virtual phone back", vc.boot, func() { app.pressRole(t, win, "button", "Start") })
	t.Logf("started again and online in %s", took.Round(time.Second))
	if again != id {
		t.Errorf("it came back as %s, was %s", again, id)
	}

	virtualList()
	app.pressIn(t, win, vc.id, vc.name, vc.line, "Shut down")
	waitFor(t, "it to stop running", 2*time.Minute, func() bool { return !vc.running() })
	run(t, bin, home, "quit")
	app.wait(t)
}

// pressIn presses the button labelled button in the row of the virtual
// phone with this ID (the row's key) or name (its label) whose line has
// line in it. The tree lists a row's children right after it.
func (a *uiApp) pressIn(t *testing.T, win int, id, name, line, button string) {
	t.Helper()
	var nodes []uiNode
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		nodes = a.nodes(t, win)
		if !hasRow(nodes, id, name) {
			// It may be past the first rows.
			for _, n := range nodes {
				if n.Role == "button" && strings.HasPrefix(n.Label, "Show all") {
					a.call(t, "act", map[string]any{"window": win, "id": n.ID, "action": "press"}, nil)
					break
				}
			}
			continue
		}
		for i, n := range nodes {
			if n.Role != "listItem" || !rowIs(n, id, name) {
				continue
			}
			end := i + 1
			for end < len(nodes) && nodes[end].Role != "listItem" {
				end++
			}
			span := nodes[i+1 : end]
			has := false
			for _, m := range span {
				has = has || strings.Contains(m.Label, line)
			}
			if !has {
				continue
			}
			for _, m := range span {
				if m.Role == "button" && m.Label == button {
					a.call(t, "act", map[string]any{"window": win, "id": m.ID, "action": "press"}, nil)
					return
				}
			}
		}
	}
	var labels []string
	for _, n := range nodes {
		labels = append(labels, n.Role+" "+n.Label)
	}
	t.Fatalf("no %q in the row of %s (%s): %q", button, name, line, labels)
}

func hasRow(nodes []uiNode, id, name string) bool {
	for _, n := range nodes {
		if n.Role == "listItem" && rowIs(n, id, name) {
			return true
		}
	}
	return false
}

// rowIs: a row's key is the virtual phone's ID; its label is the name.
func rowIs(n uiNode, id, name string) bool {
	k, _ := n.Key.(string)
	return k == id || n.Label == name
}

// devices returns the phones in the gateway.
func (g *gatewayOf) devices() []gateway.DeviceView {
	var devs []gateway.DeviceView
	if err := g.ctl.Call("devices", nil, &devs); err != nil {
		g.t.Fatal(err)
	}
	return devs
}

// textWith is the first label or value in the window with text in it, ""
// when there is none.
func (a *uiApp) textWith(t *testing.T, win int, text string) string {
	for _, n := range a.nodes(t, win) {
		for _, s := range []string{n.Label, n.Value} {
			if strings.Contains(s, text) {
				return s
			}
		}
	}
	return ""
}
