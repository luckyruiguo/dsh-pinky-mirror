//go:build device

package gateway

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android"
	"github.com/DIYgod/pinky/internal/policy"
)

// phone_launch with links on the background display: a web link and, with
// PINKY_DEEP_LINK, an app's own link (a small test app built for it) each
// ask under "Installs and links", naming the link and the app, and open
// there; a link Pinky never opens is refused before anything opens.
//
//	scripts/devlock.sh emulator-5720 env PINKY_EMULATOR=emulator-5720 PINKY_TEST_PIN=1234 PINKY_DEEP_LINK=pinkytest://order/42 /tmp/pinky-gw/gateway.test -test.run TestEmulatorLinks -test.v
func TestEmulatorLinks(t *testing.T) {
	id := emulatorID(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	e := policy.Default()
	if err := e.SetPreset(policy.PresetCommits); err != nil {
		t.Fatal(err)
	}
	g, err := New(Options{Providers: []driver.Provider{&oneDevice{Provider: android.NewProvider(android.Config{}), id: id}},
		TrustNewAgents: true, Logger: log, NoScreenshots: true, PIN: testPIN, Policy: e})
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
	if res := s.Connect(ctx, ConnectRequest{Call: Call{Device: id}, Task: "Links"}); res.IsError() {
		t.Fatalf("connect: %+v", res)
	}
	if d := g.Devices()[0]; !strings.Contains(d.StatusLine, "in the background") {
		t.Skipf("not on a background display: %q", d.StatusLine)
	}

	links := []struct{ link, pkg, name string }{{"https://example.com/?from=pinky", "com.android.chrome", "Chrome"}}
	if l := os.Getenv("PINKY_DEEP_LINK"); l != "" {
		links = append(links, struct{ link, pkg, name string }{l, "cc.pinky.linktest", "Link Test"})
	}
	for _, l := range links {
		adbShell(t, id, "am", "force-stop", l.pkg)
		want := "Open “" + l.link + "” in " + l.name + "?"
		start := time.Now()
		go func() {
			for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				if needs := g.Needs(); len(needs) > 0 {
					n := needs[0]
					t.Logf("asked after %.1f s: %q under %q", time.Since(start).Seconds(), n.Title, n.Rule)
					if n.Rule != "Installs and links" || n.Title != want {
						t.Errorf("approval %q under %q, want %q", n.Title, n.Rule, want)
					}
					if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
						t.Error(err)
					}
					return
				}
			}
			t.Error("no approval appeared")
		}()
		res := s.Act(ctx, Call{Device: id}, Action{Tool: "launch", App: l.link})
		sc := screenOf(res)
		t.Logf("launch %s: %s%s in %.1f s, %s, %d elements · %s", l.link, res.Status, res.Error, time.Since(start).Seconds(), sc.App.ID, len(sc.Elements), res.Message)
		if res.Status != StatusSettled || sc.App.ID != l.pkg {
			t.Errorf("%s: did not open in %s", l.link, l.pkg)
		}
		if got := lastStep(t, g); !strings.HasPrefix(got, "Opened “"+l.link+"” in "+l.name) {
			t.Errorf("step %q", got)
		}
		if d := g.Devices()[0]; !strings.Contains(d.StatusLine, "in the background") {
			t.Fatalf("the task left the background display: %q", d.StatusLine)
		}
	}
	res := s.Act(ctx, Call{Device: id}, Action{Tool: "launch", App: "intent:#Intent;component=com.android.settings/.Settings;end"})
	if res.Status != StatusDenied && res.Status != StatusBlocked || res.Rule != policy.RuleUnsafeLinks {
		t.Errorf("intent: link: %s %s %s", res.Status, res.Rule, res.Message)
	}
}
