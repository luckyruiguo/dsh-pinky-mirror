//go:build device

package gateway

// An emulator run of phone_app: it installs, updates, stops and uninstalls
// the tiny test app, built here with the SDK's own tools (no downloaded
// APKs), on our emulators only.
//
//	go test -c -tags device -o /tmp/pinky-gw/gateway.test ./internal/gateway
//	cd internal/gateway && ../../scripts/devlock.sh emulator-5560 env PINKY_EMULATOR=emulator-5560 PINKY_TEST_PIN=1234 /tmp/pinky-gw/gateway.test -test.run TestEmulatorApp -test.v

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android"
	"github.com/DIYgod/pinky/internal/policy"
)

const emuTestApp = "app.pinky.testapp"

// buildTestApp builds the test app into dir; dir's keystore is its signer.
func buildTestApp(t *testing.T, dir, code, name string) string {
	t.Helper()
	script, err := filepath.Abs("../apppkg/testdata/testapp/build.sh")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", script, dir, code, name, "Pinky Emulator Test").CombinedOutput(); err != nil {
		t.Fatalf("build the test app %s: %v\n%s", name, err, out)
	}
	return filepath.Join(dir, "pinky-test-"+code+".apk")
}

// waitAppNeed waits for an approval card, longer than waitNeedKind: the
// emulator reads the package first.
func waitAppNeed(t *testing.T, g *Gateway) NeedView {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		for _, n := range g.Needs() {
			if n.Kind == NeedApproval {
				return n
			}
		}
	}
	t.Fatalf("no approval card: %+v", g.Needs())
	return NeedView{}
}

// emuVersion reads the installed version of the test app, "" when absent.
func emuVersion(t *testing.T, id string) string {
	t.Helper()
	out := adbShell(t, id, "dumpsys", "package", emuTestApp)
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "versionName=") {
			return strings.TrimPrefix(l, "versionName=")
		}
	}
	return ""
}

func emuRunning(id string) bool {
	out, _ := exec.Command("adb", "-s", id, "shell", "pidof", emuTestApp).Output()
	return strings.TrimSpace(string(out)) != ""
}

func TestEmulatorApp(t *testing.T) {
	id := emulatorID(t)
	ctx := context.Background()
	builds := t.TempDir()
	v1 := buildTestApp(t, builds, "1", "1.0")
	v2 := buildTestApp(t, builds, "2", "2.0")
	split := filepath.Join(builds, "pinky-test-2-de.apk")
	other := buildTestApp(t, t.TempDir(), "3", "3.0") // another keystore: another signer

	// The test app is ours; start without it.
	exec.Command("adb", "-s", id, "uninstall", emuTestApp).Run()

	data, err := os.MkdirTemp("/tmp", "pinky-gw-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(data)
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	prov := &oneDevice{Provider: android.NewProvider(android.Config{}), id: id}
	g, err := New(Options{Providers: []driver.Provider{prov}, DataDir: data,
		TrustNewAgents: true, Policy: policy.Default(), Logger: log, NoScreenshots: true, PIN: testPIN})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for deadline := time.Now().Add(60 * time.Second); !deviceOnline(g, id); time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not come online", id)
		}
	}
	adbShell(t, id, "input", "keyevent", "KEYCODE_WAKEUP")
	unlockForTest(t, g, id)

	put := func(rel, src string) {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(g.FilesDir(id), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("build/v1.apk", v1)
	put("build/v2.apk", v2)
	put("build/v2-de.apk", split)
	put("other/v3.apk", other)

	s, _ := connect(t, g, "Try the new build")
	app := func(req AppRequest) chan *Result {
		done := make(chan *Result, 1)
		go func() { done <- s.App(ctx, req) }()
		return done
	}
	wait := func(done chan *Result) *Result {
		select {
		case res := <-done:
			return res
		case <-time.After(3 * time.Minute):
			t.Fatal("phone_app did not return")
			return nil
		}
	}
	noNeed := func() {
		if needs := g.Needs(); len(needs) > 0 {
			t.Fatalf("asked: %+v", needs)
		}
	}

	// Install asks, with the app's name, version and signer.
	done := app(AppRequest{Action: "install", Paths: []string{"build/v1.apk"}})
	n := waitAppNeed(t, g)
	t.Logf("card: %s | %s", n.Title, n.Detail)
	if n.Rule != policy.RuleInstall || !strings.HasPrefix(n.Title, "Install “Pinky Emulator Test” 1.0 (1) on ") ||
		!strings.Contains(n.Detail, "app.pinky.testapp · signed by Pinky Test (") || n.AlwaysAllow || !n.AllowTask {
		t.Fatalf("card %+v", n)
	}
	if err := g.Decide(n.ID, AllowTask, ""); err != nil {
		t.Fatal(err)
	}
	res := wait(done)
	mustStatus(t, res, StatusSettled)
	t.Log(res.Message)
	if v := emuVersion(t, id); v != "1.0" {
		t.Fatalf("installed version %q", v)
	}

	// Run it, then update it: Allow for this task covers the same app and
	// signer, and the split APKs go in one session.
	time.Sleep(300 * time.Millisecond)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Pinky Emulator Test"}), StatusSettled)
	time.Sleep(300 * time.Millisecond)
	res = wait(app(AppRequest{Action: "install", Paths: []string{"build/v2.apk", "build/v2-de.apk"}}))
	mustStatus(t, res, StatusSettled)
	noNeed()
	t.Log(res.Message)
	if !strings.Contains(res.Message, "from 1.0 (1) to 2.0 (2)") {
		t.Fatalf("update: %s", res.Message)
	}
	if v := emuVersion(t, id); v != "2.0" {
		t.Fatalf("updated version %q", v)
	}

	// An older build of the same app runs without asking, and the phone
	// refuses it in plain words.
	time.Sleep(300 * time.Millisecond)
	res = wait(app(AppRequest{Action: "install", Paths: []string{"build/v1.apk"}}))
	noNeed()
	t.Log(res.Message)
	if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "older") {
		t.Fatalf("downgrade: %+v", res)
	}

	// Another signer asks again, and the phone refuses the key.
	time.Sleep(300 * time.Millisecond)
	done = app(AppRequest{Action: "install", Paths: []string{"other/v3.apk"}})
	n = waitAppNeed(t, g)
	t.Logf("card: %s | %s", n.Title, n.Detail)
	if !strings.HasPrefix(n.Title, "Update “Pinky Emulator Test” to 3.0 (3) on ") || !strings.Contains(n.Detail, "replaces 2.0 (2)") {
		t.Fatalf("card %+v", n)
	}
	if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	res = wait(done)
	t.Log(res.Message)
	if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "another key") {
		t.Fatalf("another signer: %+v", res)
	}
	if v := emuVersion(t, id); v != "2.0" {
		t.Fatalf("version after the refused install %q", v)
	}

	// Stop does not ask.
	time.Sleep(300 * time.Millisecond)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Pinky Emulator Test"}), StatusSettled)
	if !emuRunning(id) {
		t.Fatal("the test app is not running after phone_launch")
	}
	time.Sleep(300 * time.Millisecond)
	res = wait(app(AppRequest{Action: "stop", App: "Pinky Emulator Test"}))
	mustStatus(t, res, StatusSettled)
	noNeed()
	t.Log(res.Message)
	if emuRunning(id) {
		t.Fatal("the test app still runs after stop")
	}

	// Pinky's own and system apps are refused without asking.
	for _, name := range []string{"Settings", "app.pinky.companion"} {
		time.Sleep(300 * time.Millisecond)
		res = wait(app(AppRequest{Action: "uninstall", App: name}))
		noNeed()
		t.Logf("uninstall %s: %s %s %s", name, res.Status, res.Error, res.Message)
		if res.Status != StatusBlocked && res.Error != ErrNotFound {
			t.Fatalf("uninstall %s: %+v", name, res)
		}
	}

	// Uninstall asks.
	time.Sleep(300 * time.Millisecond)
	done = app(AppRequest{Action: "uninstall", App: "Pinky Emulator Test"})
	n = waitAppNeed(t, g)
	t.Logf("card: %s | %s", n.Title, n.Detail)
	if n.Rule != policy.RuleUninstall || !strings.HasPrefix(n.Title, "Uninstall “Pinky Emulator Test” from ") ||
		!strings.Contains(n.Detail, "its data on the phone is deleted too") {
		t.Fatalf("card %+v", n)
	}
	if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	res = wait(done)
	mustStatus(t, res, StatusSettled)
	t.Log(res.Message)
	if v := emuVersion(t, id); v != "" {
		t.Fatalf("still installed: %q", v)
	}

	for _, st := range appSteps(g, s) {
		t.Log(st)
	}
	adbShell(t, id, "input", "keyevent", "KEYCODE_HOME")
}
