package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests work on fake bundles in temporary folders; they never touch
// an installed Pinky.app or the running one.

// fakeBundle makes dir/name, a bundle of Pinky at version with a shell
// script for its executable.
func fakeBundle(t *testing.T, dir, name, id, version string) string {
	t.Helper()
	app := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>` + id + `</string>
<key>CFBundleShortVersionString</key><string>` + version + `</string>
<key>CFBundleExecutable</key><string>Pinky</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "Pinky"), []byte("#!/bin/sh\necho "+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return app
}

// zipBundle zips a bundle as the release does: ditto -c -k --keepParent.
func zipBundle(t *testing.T, app string) string {
	t.Helper()
	zip := filepath.Join(t.TempDir(), "Pinky.zip")
	if out, err := exec.Command("/usr/bin/ditto", "-c", "-k", "--keepParent", app, zip).CombinedOutput(); err != nil {
		t.Fatalf("ditto: %v: %s", err, out)
	}
	return zip
}

func acceptAll(context.Context, string) error { return nil }

func installedVersion(t *testing.T, app string) string {
	t.Helper()
	info, err := readBundleInfo(app)
	if err != nil {
		t.Fatal(err)
	}
	return info.Version
}

func TestStageChecksTheBundle(t *testing.T) {
	apps := t.TempDir()
	installed := fakeBundle(t, apps, "Pinky.app", BundleID, "2026.1107.1")
	src := t.TempDir()
	r := Release{Feed: Feed{Version: "2026.1107.2"}}

	good := zipBundle(t, fakeBundle(t, src, "Pinky.app", BundleID, "2026.1107.2"))
	in := &BundleInstaller{Bundle: installed, Verify: acceptAll}
	st, err := in.Stage(context.Background(), good, r)
	if err != nil {
		t.Fatal(err)
	}
	sb := st.(*stagedBundle)
	if !strings.HasPrefix(filepath.Base(sb.work), ".Pinky.app.update-") || filepath.Dir(sb.work) != apps {
		t.Errorf("staged in %s, not next to the app", sb.work)
	}
	if installedVersion(t, sb.app) != "2026.1107.2" {
		t.Errorf("the staged app")
	}
	st.Discard()
	if _, err := os.Stat(sb.work); !os.IsNotExist(err) {
		t.Errorf("Discard left %s", sb.work)
	}

	for _, c := range []struct {
		name, zip, want string
	}{
		{"another version", zipBundle(t, fakeBundle(t, t.TempDir(), "Pinky.app", BundleID, "2026.1107.1")), "version"},
		{"another app", zipBundle(t, fakeBundle(t, t.TempDir(), "Pinky.app", "com.example.other", "2026.1107.2")), "not Pinky"},
		{"not a zip", writeFile(t, "not a zip"), "unpack"},
	} {
		if _, err := in.Stage(context.Background(), c.zip, r); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	two := t.TempDir()
	fakeBundle(t, two, "Pinky.app", BundleID, "2026.1107.2")
	fakeBundle(t, two, "Other.app", BundleID, "2026.1107.2")
	zip := filepath.Join(t.TempDir(), "two.zip")
	if out, err := exec.Command("/usr/bin/ditto", "-c", "-k", two, zip).CombinedOutput(); err != nil {
		t.Fatalf("ditto: %v: %s", err, out)
	}
	if _, err := in.Stage(context.Background(), zip, r); err == nil || !strings.Contains(err.Error(), "2 apps") {
		t.Errorf("two apps: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(apps, ".Pinky.app.update-*"))
	if len(leftovers) != 0 {
		t.Errorf("failed stages left %v", leftovers)
	}

	// The real check refuses a bundle that Pinky's team did not sign.
	in.Verify = nil
	if _, err := in.Stage(context.Background(), good, r); !errors.Is(err, ErrSignature) {
		t.Errorf("an unsigned bundle: %v", err)
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSignatureOfARelease checks the requirements against a Pinky.app signed
// with the Developer ID, when one is at hand: PINKY_SIGNED_APP, a release
// (notarized and stapled, as a release's zip holds it), else make release's
// build in the repository's build folder, which is not notarized. With
// PINKY_E2E_SIGN, the Developer ID itself, a changed copy signed again is
// Pinky's team's but not notarized, and must not pass as an update.
//
//	PINKY_SIGNED_APP=<unzipped release>/Pinky.app \
//	PINKY_E2E_SIGN="Developer ID Application: NATURAL SELECTION LABS PTE. LTD. (492J8Q67PF)" \
//	  go test -run TestSignatureOfARelease -v ./internal/update/
func TestSignatureOfARelease(t *testing.T) {
	ctx := context.Background()
	app := os.Getenv("PINKY_SIGNED_APP")
	release := app != ""
	if app == "" {
		app = filepath.Join("..", "..", "build", "pinky", "darwin-arm64", "Pinky.app")
	}
	if _, err := os.Stat(app); err != nil {
		t.Skip("no signed Pinky.app; set PINKY_SIGNED_APP")
	}
	out, _ := exec.Command("/usr/bin/codesign", "-dvv", app).CombinedOutput()
	if !strings.Contains(string(out), "TeamIdentifier="+TeamID) {
		t.Skipf("%s is not signed by Pinky's team", app)
	}
	if err := verifySignature(ctx, app, true); err != nil {
		t.Errorf("a release does not pass: %v", err)
	}
	if release && notarizationRequired {
		if err := verifyUpdate(ctx, app); err != nil {
			t.Errorf("a notarized release does not pass as an update: %v", err)
		}
	}
	// The same app copied and changed fails.
	cp := filepath.Join(t.TempDir(), "Pinky.app")
	if out, err := exec.Command("/usr/bin/ditto", app, cp).CombinedOutput(); err != nil {
		t.Fatalf("ditto: %v: %s", err, out)
	}
	if err := verifySignature(ctx, cp, true); err != nil {
		t.Errorf("an unchanged copy does not pass: %v", err)
	}
	if release && notarizationRequired {
		if err := verifyUpdate(ctx, cp); err != nil {
			t.Errorf("an unchanged copy of a release does not pass as an update: %v", err)
		}
	}
	os.WriteFile(filepath.Join(cp, "Contents", "Resources", "extra.txt"), []byte("added"), 0o644)
	if err := verifySignature(ctx, cp, true); !errors.Is(err, ErrSignature) {
		t.Errorf("a changed copy passes: %v", err)
	}

	// Signed again with Pinky's Developer ID, the changed copy is the
	// team's, but Apple never notarized it.
	sign := os.Getenv("PINKY_E2E_SIGN")
	if sign == "" || !notarizationRequired {
		return
	}
	os.Remove(filepath.Join(cp, "Contents", "CodeResources")) // the stapled ticket, of the release's code
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--deep", "--timestamp=none", "--options", "runtime", "--sign", sign, cp).CombinedOutput(); err != nil {
		t.Fatalf("sign the changed copy: %v: %s", err, out)
	}
	if err := verifySignature(ctx, cp, true); err != nil {
		t.Errorf("the changed copy, signed again, is not the team's: %v", err)
	}
	if err := verifyUpdate(ctx, cp); !errors.Is(err, ErrNotarized) {
		t.Errorf("a copy that Apple did not notarize passes as an update: %v", err)
	}
	if out, err := codesign(ctx, cp, updateRequirement(), true); err == nil {
		t.Errorf("the helper's requirement accepts a copy that is not notarized: %s", out)
	}
}

// fakeApp is a process standing in for the running Pinky, which the helper
// waits for.
func fakeApp(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

// fakeOpen stands in for /usr/bin/open: it records each start in starts
// and, as the new version would, runs CheckSwap's and Confirm's work for
// the version it finds at the bundle it is given. With crash set, the new
// version launches and exits before it is up.
func fakeOpen(t *testing.T, starts string, crash bool) []string {
	t.Helper()
	script := `app=$1; shift
v=$(sed -n 's:.*<key>CFBundleShortVersionString</key><string>\([^<]*\)</string>.*:\1:p' "$app/Contents/Info.plist")
echo "$v $*" >>` + strconv.Quote(starts) + `
for w in "$(dirname "$app")"/.Pinky.app.update-*; do
	[ -f "$w/pending.json" ] || continue
	grep -q "\"to\":\"$v\"" "$w/pending.json" || continue
	if [ "$CRASH" = 1 ]; then
		sh -c 'exit 0' & echo $! >"$w/launched"
	else
		echo $$ >"$w/launched"; : >"$w/started"
	fi
done
`
	env := "CRASH=0"
	if crash {
		env = "CRASH=1"
	}
	return []string{"/usr/bin/env", env, "/bin/sh", "-c", script, "fake-open"}
}

func waitUntil(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSwap(t *testing.T) {
	// A folder with spaces and quotes, as on an external disk.
	apps := filepath.Join(t.TempDir(), `My "Apps" $HOME`)
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	installed := fakeBundle(t, apps, "Pinky.app", BundleID, "2026.1107.1")
	os.WriteFile(filepath.Join(installed, "Contents", "old-only"), []byte("x"), 0o644)
	zip := zipBundle(t, fakeBundle(t, t.TempDir(), "Pinky.app", BundleID, "2026.1107.2"))
	running := fakeApp(t)
	starts := filepath.Join(t.TempDir(), "starts")
	log := filepath.Join(t.TempDir(), "update.log")
	in := &BundleInstaller{Bundle: installed, Verify: acceptAll, PID: running.Process.Pid, Open: fakeOpen(t, starts, false), Log: log}
	st, err := in.Stage(context.Background(), zip, Release{Feed: Feed{Version: "2026.1107.2"}})
	if err != nil {
		t.Fatal(err)
	}
	work := st.(*stagedBundle).work
	if err := st.Swap(StartOptions{Args: []string{"--background"}}); err != nil {
		t.Fatal(err)
	}
	// Nothing changes while the old version runs.
	time.Sleep(300 * time.Millisecond)
	if installedVersion(t, installed) != "2026.1107.1" {
		t.Fatal("swapped while Pinky still ran")
	}
	running.Process.Kill()
	running.Wait()
	waitUntil(t, "the swap", 10*time.Second, func() bool {
		_, err := os.Stat(work)
		return os.IsNotExist(err)
	})
	if v := installedVersion(t, installed); v != "2026.1107.2" {
		t.Errorf("installed %s", v)
	}
	if _, err := os.Stat(filepath.Join(installed, "Contents", "old-only")); !os.IsNotExist(err) {
		t.Error("the old bundle's files are in the new one")
	}
	data, _ := os.ReadFile(starts)
	if strings.TrimSpace(string(data)) != "2026.1107.2 --background" {
		t.Errorf("starts: %q", data)
	}
	logged, _ := os.ReadFile(log)
	if !strings.Contains(string(logged), "the new version is up") {
		t.Errorf("log:\n%s", logged)
	}
}

func TestSwapPutsTheOldVersionBack(t *testing.T) {
	apps := t.TempDir()
	installed := fakeBundle(t, apps, "Pinky.app", BundleID, "2026.1107.1")
	zip := zipBundle(t, fakeBundle(t, t.TempDir(), "Pinky.app", BundleID, "2026.1107.2"))
	running := fakeApp(t)
	starts := filepath.Join(t.TempDir(), "starts")
	log := filepath.Join(t.TempDir(), "update.log")
	in := &BundleInstaller{Bundle: installed, Verify: acceptAll, PID: running.Process.Pid, Open: fakeOpen(t, starts, true), Log: log}
	st, err := in.Stage(context.Background(), zip, Release{Feed: Feed{Version: "2026.1107.2"}})
	if err != nil {
		t.Fatal(err)
	}
	work := st.(*stagedBundle).work
	if err := st.Swap(StartOptions{}); err != nil {
		t.Fatal(err)
	}
	running.Process.Kill()
	running.Wait()
	waitUntil(t, "the old version to come back", 10*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(work, "rolled-back"))
		return err == nil
	})
	waitUntil(t, "the old version to start again", 5*time.Second, func() bool {
		data, _ := os.ReadFile(starts)
		return strings.Count(string(data), "\n") == 2
	})
	if v := installedVersion(t, installed); v != "2026.1107.1" {
		t.Errorf("installed %s after a failed start", v)
	}
	data, _ := os.ReadFile(starts)
	if got := strings.Fields(string(data)); len(got) != 2 || got[0] != "2026.1107.2" || got[1] != "2026.1107.1" {
		t.Errorf("starts: %q", data)
	}
	// The old version, started again, learns that the update failed and
	// clears what is left.
	sw := checkSwap(installed, "2026.1107.1", os.Getpid(), time.Now())
	if sw == nil || !sw.Failed || sw.To != "2026.1107.2" || sw.From != "2026.1107.1" {
		t.Fatalf("checkSwap = %+v", sw)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Errorf("%s is left", work)
	}
	logged, _ := os.ReadFile(log)
	if !strings.Contains(string(logged), "did not start; putting the old one back") {
		t.Errorf("log:\n%s", logged)
	}
}

func TestSwapWhenTheNewVersionNeverLaunches(t *testing.T) {
	apps := t.TempDir()
	installed := fakeBundle(t, apps, "Pinky.app", BundleID, "2026.1107.1")
	zip := zipBundle(t, fakeBundle(t, t.TempDir(), "Pinky.app", BundleID, "2026.1107.2"))
	running := fakeApp(t)
	starts := filepath.Join(t.TempDir(), "starts")
	open := []string{"/bin/sh", "-c", `echo started >>` + strconv.Quote(starts), "fake-open"}
	in := &BundleInstaller{Bundle: installed, Verify: acceptAll, PID: running.Process.Pid, Open: open,
		Log: filepath.Join(t.TempDir(), "log"), LaunchWait: time.Second}
	st, err := in.Stage(context.Background(), zip, Release{Feed: Feed{Version: "2026.1107.2"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Swap(StartOptions{}); err != nil {
		t.Fatal(err)
	}
	running.Process.Kill()
	running.Wait()
	waitUntil(t, "the old version to come back", 10*time.Second, func() bool {
		data, _ := os.ReadFile(starts)
		return strings.Count(string(data), "\n") == 2
	})
	if v := installedVersion(t, installed); v != "2026.1107.1" {
		t.Errorf("installed %s", v)
	}
}

func TestCheckSwap(t *testing.T) {
	apps := t.TempDir()
	bundle := fakeBundle(t, apps, "Pinky.app", BundleID, "2026.1107.2")
	now := time.Now()
	mk := func(name, from, to string, at time.Time, files ...string) string {
		dir := filepath.Join(apps, ".Pinky.app.update-"+name)
		os.MkdirAll(dir, 0o700)
		if to != "" {
			os.WriteFile(filepath.Join(dir, "pending.json"), []byte(fmt.Sprintf(`{"from":%q,"to":%q,"at":%q}`, from, to, at.UTC().Format(time.RFC3339))), 0o600)
		}
		for _, f := range files {
			if strings.HasSuffix(f, ".app") {
				os.MkdirAll(filepath.Join(dir, f), 0o755)
			} else {
				os.WriteFile(filepath.Join(dir, f), nil, 0o600)
			}
		}
		os.Chtimes(dir, at, at)
		return dir
	}
	// A helper that gave up before swapping left a folder for the same
	// version; the swap that happened has the old bundle in its folder.
	abandoned := mk("a", "2026.1107.1", "2026.1107.2", now.Add(-time.Hour))
	current := mk("b", "2026.1107.1", "2026.1107.2", now, "old.app")
	stale := mk("c", "", "", now.Add(-48*time.Hour))
	fresh := mk("d", "", "", now.Add(-time.Minute))
	oldSwap := mk("e", "1", "2", now.Add(-72*time.Hour), "old.app")

	sw := checkSwap(bundle, "2026.1107.2", 4242, now)
	if sw == nil || sw.Failed || sw.From != "2026.1107.1" || sw.To != "2026.1107.2" || sw.dir != current {
		t.Fatalf("checkSwap = %+v", sw)
	}
	if data, _ := os.ReadFile(filepath.Join(current, "launched")); string(data) != "4242" {
		t.Errorf("launched = %q", data)
	}
	if _, err := os.Stat(filepath.Join(abandoned, "launched")); err == nil {
		t.Error("launched went to the abandoned folder")
	}
	if err := sw.Confirm(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(current, "started")); err != nil {
		t.Errorf("Confirm wrote nothing: %v", err)
	}
	for dir, want := range map[string]bool{stale: false, fresh: true, oldSwap: false, abandoned: true} {
		_, err := os.Stat(dir)
		if exists := err == nil; exists != want {
			t.Errorf("%s exists = %v, want %v", filepath.Base(dir), exists, want)
		}
	}
	if sw := checkSwap(bundle, "2026.1201.1", 1, now); sw != nil {
		t.Errorf("another version: %+v", sw)
	}

	// The update was rolled back: the old version learns it; the new one,
	// should it run from where it was moved, leaves the folder alone.
	back := mk("f", "2026.1107.1", "2026.1107.3", now, "rolled-back", "failed.app")
	if sw := checkSwap(bundle, "2026.1107.3", 1, now); sw != nil {
		t.Errorf("the failed version: %+v", sw)
	}
	if _, err := os.Stat(filepath.Join(back, "failed.app")); err != nil {
		t.Error("the failed version removed the bundle it may run from")
	}
	if sw := checkSwap(bundle, "2026.1107.1", 1, now); sw == nil || !sw.Failed || sw.To != "2026.1107.3" {
		t.Errorf("the old version: %+v", sw)
	}
	if _, err := os.Stat(back); !os.IsNotExist(err) {
		t.Error("the old version left the rolled-back folder")
	}
}

// TestSwapAfterAZombie: Pinky started by `pinky mcp` is that shim's child,
// and a zombie once it exits until the shim reaps it; the helper must not
// wait for it.
func TestSwapAfterAZombie(t *testing.T) {
	apps := t.TempDir()
	installed := fakeBundle(t, apps, "Pinky.app", BundleID, "2026.1107.1")
	zip := zipBundle(t, fakeBundle(t, t.TempDir(), "Pinky.app", BundleID, "2026.1107.2"))
	child := exec.Command("/bin/sh", "-c", "sleep 0.5")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Wait() // reaps it at the end only
	starts := filepath.Join(t.TempDir(), "starts")
	in := &BundleInstaller{Bundle: installed, Verify: acceptAll, PID: child.Process.Pid, Open: fakeOpen(t, starts, false), Log: filepath.Join(t.TempDir(), "log")}
	st, err := in.Stage(context.Background(), zip, Release{Feed: Feed{Version: "2026.1107.2"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Swap(StartOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the swap after the zombie", 10*time.Second, func() bool {
		// Between the helper's two renames there is no bundle there.
		info, err := readBundleInfo(installed)
		return err == nil && info.Version == "2026.1107.2"
	})
}

// swapThatFails swaps with a helper that cannot do its work, and returns
// what the old version reads afterwards.
func swapThatFails(t *testing.T, prepare func(in *BundleInstaller, installed string)) (*Swapped, string) {
	t.Helper()
	apps := t.TempDir()
	installed := fakeBundle(t, apps, "Pinky.app", BundleID, "2026.1107.1")
	zip := zipBundle(t, fakeBundle(t, t.TempDir(), "Pinky.app", BundleID, "2026.1107.2"))
	running := fakeApp(t)
	starts := filepath.Join(t.TempDir(), "starts")
	log := filepath.Join(t.TempDir(), "log")
	in := &BundleInstaller{Bundle: installed, Verify: acceptAll, PID: running.Process.Pid, Open: fakeOpen(t, starts, false), Log: log}
	st, err := in.Stage(context.Background(), zip, Release{Feed: Feed{Version: "2026.1107.2"}})
	if err != nil {
		t.Fatal(err)
	}
	prepare(in, installed)
	if err := st.Swap(StartOptions{}); err != nil {
		t.Fatal(err)
	}
	running.Process.Kill()
	running.Wait()
	waitUntil(t, "the old version to start again", 10*time.Second, func() bool {
		data, _ := os.ReadFile(starts)
		return strings.HasPrefix(string(data), "2026.1107.1")
	})
	if v := installedVersion(t, installed); v != "2026.1107.1" {
		t.Errorf("installed %s", v)
	}
	logged, _ := os.ReadFile(log)
	return checkSwap(installed, "2026.1107.1", os.Getpid(), time.Now()), string(logged)
}

func TestSwapThatCannotMoveTheApp(t *testing.T) {
	sw, log := swapThatFails(t, func(_ *BundleInstaller, installed string) {
		// An immutable bundle cannot be renamed.
		if out, err := exec.Command("/usr/bin/chflags", "uchg", installed).CombinedOutput(); err != nil {
			t.Skipf("chflags: %v: %s", err, out)
		}
		t.Cleanup(func() { exec.Command("/usr/bin/chflags", "nouchg", installed).Run() })
	})
	if sw == nil || !sw.Failed || sw.To != "2026.1107.2" {
		t.Errorf("the old version reads %+v; it would try the same update again", sw)
	}
	if !strings.Contains(log, "could not move") {
		t.Errorf("log:\n%s", log)
	}
}

func TestSwapChecksTheSignatureAgain(t *testing.T) {
	// Staged as checked, then the bundle is not Pinky's team's when the
	// helper looks again (here: never signed).
	sw, log := swapThatFails(t, func(in *BundleInstaller, _ string) { in.Verify = nil })
	if sw == nil || !sw.Failed {
		t.Errorf("the old version reads %+v", sw)
	}
	if !strings.Contains(log, "signature does not check out") {
		t.Errorf("log:\n%s", log)
	}
}

func TestLocate(t *testing.T) {
	ok := func(string) error { return nil }
	unsigned := func(string) error { return ErrSignature }
	defer func(f func(string) error) { checkRunning = f }(checkRunning)
	checkRunning = ok
	exeOf := func(app string) string { return filepath.Join(app, "Contents", "MacOS", "Pinky") }

	apps := t.TempDir()
	app := fakeBundle(t, apps, "Pinky.app", BundleID, "2026.1107.1")
	if l := locate(exeOf(app)); !l.CanReplace() || l.App != app {
		t.Errorf("a writable bundle: %+v", l)
	}
	checkRunning = unsigned
	if l := locate(exeOf(app)); l.CanReplace() || !strings.Contains(l.Problem, "isn't signed") {
		t.Errorf("an ad hoc build: %+v", l)
	}
	checkRunning = ok
	if l := locate(filepath.Join(t.TempDir(), "pinky")); l.CanReplace() || l.Problem == "" || l.Method != MethodDownload {
		t.Errorf("not a bundle: %+v", l)
	}
	if l := locate("/private/var/folders/xx/T/AppTranslocation/1234/d/Pinky.app/Contents/MacOS/Pinky"); l.CanReplace() || !strings.Contains(l.Problem, "temporary copy") {
		t.Errorf("translocated: %+v", l)
	}
	ro := t.TempDir()
	roApp := fakeBundle(t, ro, "Pinky.app", BundleID, "2026.1107.1")
	os.Chmod(ro, 0o555)
	defer os.Chmod(ro, 0o755)
	if l := locate(exeOf(roApp)); l.CanReplace() || !strings.Contains(l.Problem, "can't write") {
		t.Errorf("a read-only folder: %+v", l)
	}
	if l := locate("/opt/homebrew/Cellar/pinky/1.0/Pinky.app/Contents/MacOS/Pinky"); l.Method != MethodManager || l.Command != "brew upgrade pinky" {
		t.Errorf("a Homebrew formula: %+v", l)
	}
	// Pinky's cask moves Pinky.app to Applications and declares
	// auto_updates: it updates itself.
	if l := locate(exeOf(app)); !l.CanReplace() {
		t.Errorf("a cask's Pinky.app: %+v", l)
	}
}

func TestSwapNow(t *testing.T) {
	apps := t.TempDir()
	installed := fakeBundle(t, apps, "Pinky.app", BundleID, "2026.1107.1")
	zip := zipBundle(t, fakeBundle(t, t.TempDir(), "Pinky.app", BundleID, "2026.1107.2"))
	in := &BundleInstaller{Bundle: installed, Verify: acceptAll}
	st, err := in.Stage(context.Background(), zip, Release{Feed: Feed{Version: "2026.1107.2"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.(SwapNower).SwapNow(); err != nil {
		t.Fatal(err)
	}
	if v := installedVersion(t, installed); v != "2026.1107.2" {
		t.Errorf("installed %s", v)
	}
	if left, _ := filepath.Glob(filepath.Join(apps, ".Pinky.app.update-*")); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}
