//go:build e2e && windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/update"
)

// TestWindowsUpdate updates a Pinky that its installer installed, as on a
// person's computer. It installs the oldest of three installers of e2e
// builds quietly, into a temporary folder (which Settings › Apps then
// names), serves the next one from a local feed and leaves Pinky alone in
// the tray. Pinky installs the update by itself: its helper runs the
// installer quietly, starts the new version, which shows What's new, and
// removes the old files; an agent connected through `pinky mcp` stays
// connected. Then the feed offers the newest, which exits as it
// starts (PINKY_E2E_CRASH_VERSION): the helper puts the version before it
// back, files and Settings › Apps entry, and that version does not try the
// broken one again by itself.
//
// It installs Pinky for the current user and uninstalls it at the end, so
// it is for CI's Windows runner (.github/workflows/update-windows.yml):
//
//	PINKY_E2E_SETUPS=<a folder with three pinky-setup-<version>-amd64.exe> \
//	  go test -tags e2e -run TestWindowsUpdate -v -timeout 30m ./cmd/pinky/
func TestWindowsUpdate(t *testing.T) {
	setups := os.Getenv("PINKY_E2E_SETUPS")
	if setups == "" {
		t.Skip("PINKY_E2E_SETUPS (installers of three e2e builds) not set; this test installs Pinky for the current user")
	}
	names, _ := filepath.Glob(filepath.Join(setups, "pinky-setup-*-amd64.exe"))
	setup := map[string]string{}
	var versions []string
	for _, n := range names {
		v := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(n), "pinky-setup-"), "-amd64.exe")
		setup[v] = n
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return update.Compare(versions[i], versions[j]) < 0 })
	if len(versions) != 3 {
		t.Fatalf("want three installers in %s, found %v", setups, versions)
	}
	oldV, newV, brokenV := versions[0], versions[1], versions[2]

	tmp, err := os.MkdirTemp("", "pku-")
	if err != nil {
		t.Fatal(err)
	}
	programs := filepath.Join(tmp, "Programs")
	dir := filepath.Join(programs, "Pinky")
	exe := filepath.Join(dir, "Pinky.exe")
	home := filepath.Join(tmp, "h")
	sock := filepath.Join(tmp, "ui.sock")
	t.Setenv(api.HomeEnv, home)
	dirs, err := api.DefaultDirs()
	if err != nil {
		t.Fatal(err)
	}
	feed := newTestFeed(t)
	// Pinky's environment, and the one of commands that must not take
	// the app's automation socket (every e2e build of Pinky.exe would).
	base := append(os.Environ(), api.HomeEnv+"="+home, update.FeedEnv+"="+feed.srv.URL+"/latest.json",
		"PINKY_E2E_CRASH_VERSION="+brokenV, "DO_NOT_TRACK=1")
	appEnv := append(append([]string(nil), base...), "MYGO_AUTOMATION="+sock)

	var app *exec.Cmd
	t.Cleanup(func() {
		quitPinky(dirs)
		if app != nil && app.Process != nil {
			app.Process.Kill()
		}
		if t.Failed() {
			for _, name := range []string{filepath.Join("h", "data", "pinky.log"), filepath.Join("h", "data", "update.log"), "app.log"} {
				if b, err := os.ReadFile(filepath.Join(tmp, name)); err == nil {
					t.Logf("%s:\n%s", name, tail(string(b), 6000))
				}
			}
		}
		uninstall(t, dir)
		os.RemoveAll(tmp)
	})

	// The older version, installed quietly with its installer.
	installSetup(t, setup[oldV], dir)
	// The runner's temporary folder has short names (RUNNER~1), which the
	// installer keeps; Pinky.exe's own path has the long ones.
	if got := uninstallValue(t, "InstallLocation"); !samePath(got, dir) {
		t.Fatalf("Settings › Apps says Pinky is in %q, not %s", got, dir)
	}
	if got := uninstallValue(t, "DisplayVersion"); got != oldV {
		t.Fatalf("Settings › Apps says version %q", got)
	}

	// Left alone in the tray, it finds the next version and installs it.
	feed.offer(t, newV, setup[newV])
	start := func() <-chan error {
		app = exec.Command(exe, "--fake", "--background")
		app.Env = appEnv
		logf, _ := os.OpenFile(filepath.Join(tmp, "app.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		app.Stdout, app.Stderr = logf, logf
		if err := app.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan error, 1)
		go func() { exited <- app.Wait(); logf.Close() }()
		return exited
	}
	exited := start()
	waitFor(t, "the old version's gateway", 60*time.Second, func() bool { return gatewayVersion(dirs) == oldV })

	// An agent connected through `pinky mcp`, which runs from the
	// Pinky.exe that the update replaces.
	agent := connectAgent(t, exe, base)

	select {
	case <-exited:
	case <-time.After(4 * time.Minute):
		t.Fatal("the old version did not update itself within four minutes")
	}
	waitFor(t, "the new version's gateway", 3*time.Minute, func() bool { return gatewayVersion(dirs) == newV })
	if got := exeVersion(t, exe, base); got != newV {
		t.Errorf("Pinky.exe says %q", got)
	}
	if got := uninstallValue(t, "DisplayVersion"); got != newV {
		t.Errorf("Settings › Apps says version %q", got)
	}
	st := update.LoadState(filepath.Join(dirs.Config, "update.json"))
	if st.LastRun != newV || st.NotesFrom != oldV {
		t.Errorf("update.json: %+v", st)
	}

	// The agent's session lives on, its `pinky mcp` running the old
	// Pinky.exe from the work folder, which goes once it ends.
	agent.check(t)
	agent.cs.Close()
	waitFor(t, "the work folder to go", 90*time.Second, func() bool { return len(workFolders(programs)) == 0 })

	c := dialAutomation(t, sock)
	labels := waitLabels(t, c, 60*time.Second, newsTexts(newV))
	c.conn.Close()
	t.Logf("the new version shows: %s", strings.Join(labels[:min(len(labels), 12)], " · "))

	// The next version exits as it starts: the helper puts this one back.
	quitPinky(dirs)
	waitFor(t, "the new version to quit", 30*time.Second, func() bool { return gatewayVersion(dirs) == "" })
	feed.offer(t, brokenV, setup[brokenV])
	exited = start()
	waitFor(t, "the new version's gateway", 60*time.Second, func() bool { return gatewayVersion(dirs) == newV })
	select {
	case <-exited:
	case <-time.After(4 * time.Minute):
		t.Fatal("the new version did not try the broken one within four minutes")
	}
	waitFor(t, "the update helper to bring the new version back", 4*time.Minute, func() bool {
		st := update.LoadState(filepath.Join(dirs.Config, "update.json"))
		return gatewayVersion(dirs) == newV && st.Failed == brokenV
	})
	st = update.LoadState(filepath.Join(dirs.Config, "update.json"))
	if !strings.Contains(st.FailedWhy, "didn't start") {
		t.Errorf("why the update failed: %q", st.FailedWhy)
	}
	if got := exeVersion(t, exe, base); got != newV {
		t.Errorf("after the rollback, Pinky.exe says %q", got)
	}
	if got := uninstallValue(t, "DisplayVersion"); got != newV {
		t.Errorf("after the rollback, Settings › Apps says version %q", got)
	}
	waitFor(t, "the work folder to go after the rollback", 90*time.Second, func() bool { return len(workFolders(programs)) == 0 })
	// It does not try the broken version again by itself: a minute's tick
	// later nothing is staged.
	time.Sleep(75 * time.Second)
	if w := workFolders(programs); len(w) != 0 || gatewayVersion(dirs) != newV {
		t.Errorf("after a tick: work folders %v, gateway %q", w, gatewayVersion(dirs))
	}
	logs, _ := os.ReadFile(filepath.Join(dirs.Data, "update.log"))
	t.Logf("update.log:\n%s", tail(string(logs), 4000))
}

// installSetup runs an installer quietly into dir, as the helper does.
func installSetup(t *testing.T, setup, dir string) {
	t.Helper()
	cmd := exec.Command(setup)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: syscall.EscapeArg(setup) + " /S /D=" + dir}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install %s: %v %s", filepath.Base(setup), err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "Pinky.exe")); err != nil {
		t.Fatalf("the installer left no Pinky.exe: %v", err)
	}
}

// uninstall removes Pinky quietly, in place (_?=), so that the test waits
// for it, and checks that its entry in Settings › Apps went.
func uninstall(t *testing.T, dir string) {
	un := filepath.Join(dir, "Uninstall.exe")
	if _, err := os.Stat(un); err != nil {
		return
	}
	cmd := exec.Command(un)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: syscall.EscapeArg(un) + " /S _?=" + dir}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("uninstall: %v %s", err, out)
	}
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\`+update.BundleID, registry.QUERY_VALUE); err == nil {
		k.Close()
		t.Error("the uninstaller left Pinky's entry in Settings › Apps")
	}
}

func uninstallValue(t *testing.T, name string) string {
	t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\`+update.BundleID, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("Pinky's entry in Settings › Apps: %v", err)
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return v
}

func samePath(a, b string) bool {
	if l, err := filepath.EvalSymlinks(a); err == nil {
		a = l
	}
	if l, err := filepath.EvalSymlinks(b); err == nil {
		b = l
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
