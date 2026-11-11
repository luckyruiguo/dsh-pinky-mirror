//go:build e2e && linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/update"
)

// TestLinuxUpdate updates a Pinky unpacked from the release's archive into
// a folder of its own, as on a person's computer. It builds three e2e
// versions as archives, unpacks the oldest into a temporary folder, serves
// the next one from a local feed and leaves Pinky alone with its window
// closed. Pinky installs the update by itself: its helper moves the new
// files into the folder, starts the new version, which shows What's new,
// and removes the old files; an agent connected through `pinky mcp` stays
// connected. Then the feed offers the newest, which exits as it starts
// (PINKY_E2E_CRASH_VERSION): the helper puts the version before it back,
// and that version does not try the broken one again by itself.
//
// It needs a desktop session (WAYLAND_DISPLAY or DISPLAY) and touches
// nothing outside its temporary folders:
//
//	go test -tags e2e -run TestLinuxUpdate -v -timeout 30m ./cmd/pinky/
func TestLinuxUpdate(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") == "" {
		t.Skip("no desktop session (WAYLAND_DISPLAY or DISPLAY)")
	}
	const oldV, newV, brokenV = "2026.1107.1", "2026.1107.2", "2026.1107.3"
	tmp, err := os.MkdirTemp("", "pku-")
	if err != nil {
		t.Fatal(err)
	}
	apps := filepath.Join(tmp, "Applications")
	dir := filepath.Join(apps, "Pinky")
	exe := filepath.Join(dir, "pinky")
	archive := map[string]string{}
	for _, v := range []string{oldV, newV, brokenV} {
		archive[v] = buildArchive(t, tmp, v)
	}
	home := filepath.Join(tmp, "h")
	sock := filepath.Join(tmp, "ui.sock")
	t.Setenv(api.HomeEnv, home)
	dirs, err := api.DefaultDirs()
	if err != nil {
		t.Fatal(err)
	}
	feed := newTestFeed(t)
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
		os.RemoveAll(tmp)
	})

	// The older version, unpacked from its archive, with a file of the
	// person's next to it.
	if out, err := exec.Command("tar", "-xzf", archive[oldV], "-C", mkdir(t, dir)).CombinedOutput(); err != nil {
		t.Fatalf("unpack %s: %v %s", oldV, err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("the person's"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Left alone, it finds the next version and installs it.
	feed.offer(t, newV, archive[newV])
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

	// An agent connected through `pinky mcp`, which runs from the pinky
	// that the update replaces.
	agent := connectAgent(t, exe, base)

	select {
	case <-exited:
	case <-time.After(4 * time.Minute):
		t.Fatal("the old version did not update itself within four minutes")
	}
	waitFor(t, "the new version's gateway", 3*time.Minute, func() bool { return gatewayVersion(dirs) == newV })
	if got := exeVersion(t, exe, base); got != newV {
		t.Errorf("pinky says %q", got)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); string(data) != "the person's" {
		t.Errorf("the person's file: %q", data)
	}
	st := update.LoadState(filepath.Join(dirs.Config, "update.json"))
	if st.LastRun != newV || st.NotesFrom != oldV {
		t.Errorf("update.json: %+v", st)
	}

	agent.check(t)
	agent.cs.Close()
	waitFor(t, "the work folder to go", 90*time.Second, func() bool { return len(workFolders(apps)) == 0 })

	c := dialAutomation(t, sock)
	labels := waitLabels(t, c, 60*time.Second, newsTexts(newV))
	c.conn.Close()
	t.Logf("the new version shows: %s", strings.Join(labels[:min(len(labels), 12)], " · "))

	// The next version exits as it starts: the helper puts this one back.
	quitPinky(dirs)
	waitFor(t, "the new version to quit", 30*time.Second, func() bool { return gatewayVersion(dirs) == "" })
	feed.offer(t, brokenV, archive[brokenV])
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
		t.Errorf("after the rollback, pinky says %q", got)
	}
	waitFor(t, "the work folder to go after the rollback", 90*time.Second, func() bool { return len(workFolders(apps)) == 0 })
	// It does not try the broken version again by itself: a minute's tick
	// later nothing is staged.
	time.Sleep(75 * time.Second)
	if w := workFolders(apps); len(w) != 0 || gatewayVersion(dirs) != newV {
		t.Errorf("after a tick: work folders %v, gateway %q", w, gatewayVersion(dirs))
	}
	logs, _ := os.ReadFile(filepath.Join(dirs.Data, "update.log"))
	t.Logf("update.log:\n%s", tail(string(logs), 4000))
}

// buildArchive makes the archive of version, as the release pipeline does
// (pinky and MyGo's file list, at its top), with the e2e build of
// cmd/pinky.
func buildArchive(t *testing.T, tmp, version string) string {
	t.Helper()
	files := mkdir(t, filepath.Join(tmp, "build-"+version))
	ldflags := strings.Join([]string{
		"-X main.version=" + version,
		"-X github.com/egoist/mygo.production=1",
		"-X github.com/egoist/mygo.packageName=Pinky",
		"-X github.com/egoist/mygo.packageVersion=" + version,
		"-X github.com/egoist/mygo.packageIdentifier=app.pinky.desktop",
	}, " ")
	b := exec.Command("go", "build", "-tags", "e2e", "-ldflags", ldflags, "-o", filepath.Join(files, "pinky"), ".")
	b.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := b.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, out)
	}
	if err := os.WriteFile(filepath.Join(files, ".mygo-files"), []byte("pinky\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(mkdir(t, filepath.Join(tmp, "srv")), "pinky-"+version+"-linux-amd64.tar.gz")
	if msg, err := exec.Command("tar", "-czf", out, "-C", files, "pinky", ".mygo-files").CombinedOutput(); err != nil {
		t.Fatalf("archive %s: %v %s", version, err, msg)
	}
	return out
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}
