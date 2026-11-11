//go:build device

package gateway

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android"
)

// GitHub issue #30: phone_launch of an app started cold on the background
// display returns its screen, not an empty one (Clock, Settings and the
// emulator's Files, each force-stopped first).
//
//	scripts/devlock.sh emulator-5590 env PINKY_EMULATOR=emulator-5590 PINKY_TEST_PIN=1234 /tmp/pinky-gw/gateway.test -test.run TestEmulatorBackgroundColdLaunch -test.v
func TestEmulatorBackgroundColdLaunch(t *testing.T) {
	id := emulatorID(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	g, err := New(Options{Providers: []driver.Provider{&oneDevice{Provider: android.NewProvider(android.Config{}), id: id}},
		TrustNewAgents: true, Logger: log, NoScreenshots: true, PIN: testPIN})
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
	if err := g.SetBackground(id, true); err != nil {
		t.Fatal(err)
	}
	s := g.OpenSession(AgentInfo{Name: "Pinky emulator test"})
	defer s.Close()
	if res := s.Connect(ctx, ConnectRequest{Call: Call{Device: id}, Task: "Cold launches"}); res.IsError() {
		t.Fatalf("connect: %+v", res)
	}
	if d := g.Devices()[0]; !strings.Contains(d.StatusLine, "in the background") {
		t.Skipf("not on a background display: %q", d.StatusLine)
	}
	for _, app := range []struct{ name, pkg string }{
		{"Clock", "com.google.android.deskclock"}, {"Settings", "com.android.settings"}, {"Files", "com.google.android.documentsui"},
	} {
		adbShell(t, id, "am", "force-stop", app.pkg)
		time.Sleep(400 * time.Millisecond)
		start := time.Now()
		res := s.Act(ctx, Call{Device: id}, Action{Tool: "launch", App: app.pkg})
		sc := screenOf(res)
		t.Logf("launch %s: %s%s in %.1f s, %s, %d elements · %s", app.name, res.Status, res.Error, time.Since(start).Seconds(), sc.App.ID, len(sc.Elements), res.Message)
		if res.Status != StatusSettled || sc.App.ID != app.pkg || len(sc.Elements) == 0 {
			t.Errorf("%s: not readable after its launch", app.name)
		}
		if d := g.Devices()[0]; !strings.Contains(d.StatusLine, "in the background") {
			t.Fatalf("the task left the background display: %q", d.StatusLine)
		}
	}
}
