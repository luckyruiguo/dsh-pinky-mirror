//go:build device

package gateway

// An agent reads the logs and the crash of Crash Test, a tiny app that
// crashes on a button (internal/driver/android/testdata/crashtest), through
// a task, on an emulator. Install a debug or a release build first: a
// release build asks the person, which the test answers.
//
//	go test -c -tags device -o /tmp/pinky-gw/gateway.test ./internal/gateway
//	scripts/devlock.sh emulator-5560 env PINKY_EMULATOR=emulator-5560 /tmp/pinky-gw/gateway.test -test.run TestEmulatorAppLogs -test.v

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android"
	"github.com/DIYgod/pinky/internal/policy"
)

func TestEmulatorAppLogs(t *testing.T) {
	id := emulatorID(t)
	const pkg = "dev.pinky.crashtest"
	if !strings.Contains(adbShell(t, id, "pm", "list", "packages", pkg), "package:"+pkg) {
		t.Skip("Crash Test is not installed: see internal/driver/android/testdata/crashtest/build.sh")
	}
	debug := strings.Contains(adbShell(t, id, "dumpsys", "package", pkg), "DEBUGGABLE")
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	prov := &oneDevice{Provider: android.NewProvider(android.Config{}), id: id}
	g, err := New(Options{Providers: []driver.Provider{prov}, DataDir: t.TempDir(),
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
	adbShell(t, id, "am", "force-stop", pkg)
	defer adbShell(t, id, "am", "force-stop", pkg)
	unlockForTest(t, g, id)

	s := g.OpenSession(AgentInfo{Name: "Pinky emulator test"})
	defer s.Close()
	call := Call{Device: id}
	if res := s.Connect(ctx, ConnectRequest{Call: call, Task: "Crash Test logs", Apps: []string{"Crash Test"}}); res.IsError() {
		t.Fatalf("connect: %+v", res)
	}
	res := s.Act(ctx, call, Action{Tool: "launch", App: "Crash Test"})
	if res.IsError() {
		t.Fatalf("launch: %+v", res)
	}
	var crash *Result
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(time.Second) {
		crash = s.Observe(ctx, ObserveRequest{Call: call})
		if e := findLabel(crash, "Crash"); e != nil {
			res = s.Act(ctx, call, Action{Tool: "tap", Ref: e.Ref})
			if res.IsError() {
				t.Fatalf("tap Crash: %+v", res)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no Crash button: %+v", crash)
		}
	}

	// The first read asks the person, unless the app is a debug build.
	read := func(req LogsRequest) *Result {
		t.Helper()
		req.Call = call
		done := make(chan *Result, 1)
		go func() { done <- s.Logs(ctx, req) }()
		if !debug && req.Kind == "" {
			n := waitNeedKind(t, g, NeedApproval)
			if n.Rule != policy.RuleAppLogs {
				t.Fatalf("approval %+v", n)
			}
			if err := g.Decide(n.ID, AllowTask, ""); err != nil {
				t.Fatal(err)
			}
		}
		res := <-done
		t.Logf("%s %s: %s\n%s", req.Kind, res.Status, res.Message, res.Log)
		if res.IsError() || res.Status != StatusSettled {
			t.Fatalf("%+v", res)
		}
		return res
	}
	logs := read(LogsRequest{App: "Crash Test"})
	if !strings.Contains(logs.Log, "CrashTest: Main created") || !strings.Contains(logs.Log, "IllegalStateException: Crash button tapped") {
		t.Fatalf("lines:\n%s", logs.Log)
	}
	var crashes *Result
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(time.Second) {
		crashes = read(LogsRequest{App: "Crash Test", Kind: "crashes"})
		if strings.Contains(crashes.Log, "IllegalStateException: Crash button tapped") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no crash report:\n%s", crashes.Log)
		}
	}
	steps := taskSteps(g, s)
	var titles []string
	for _, st := range steps {
		titles = append(titles, st.Text)
		if strings.Contains(st.Text+st.Args, "IllegalStateException") {
			t.Errorf("a step holds log text: %q", st.Text)
		}
	}
	all := strings.Join(titles, "\n")
	if !strings.Contains(all, "log lines of Crash Test") || !strings.Contains(all, "crash report") {
		t.Fatalf("steps:\n%s", all)
	}
}
