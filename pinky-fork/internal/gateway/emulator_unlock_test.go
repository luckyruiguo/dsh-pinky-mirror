//go:build device

package gateway

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android"
	"github.com/DIYgod/pinky/internal/policy"
)

// TestEmulatorUnlock locks the emulator and unlocks it through the
// gateway with its PIN, as Unlock with Touch ID does: on AOSP's PIN pad,
// which takes the PIN as key events. Our emulators only:
//
//	go test -c -tags device -o /tmp/pinky-gw/gateway.test ./internal/gateway
//	scripts/devlock.sh emulator-5560 env PINKY_EMULATOR=emulator-5560 PINKY_TEST_PIN=1234 /tmp/pinky-gw/gateway.test -test.run TestEmulatorUnlock -test.v
func TestEmulatorUnlock(t *testing.T) {
	id := emulatorID(t)
	if os.Getenv("PINKY_TEST_PIN") == "" {
		t.Skip("set PINKY_TEST_PIN to the emulator's PIN")
	}
	data, err := os.MkdirTemp("/tmp", "pinky-gw-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(data)
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	prov := &oneDevice{Provider: android.NewProvider(android.Config{Logf: t.Logf}), id: id}
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
	adbShell(t, id, "input", "keyevent", "KEYCODE_SLEEP")
	for deadline := time.Now().Add(20 * time.Second); !keyguardShowing(t, id); time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the emulator did not lock")
		}
	}
	start := time.Now()
	if err := g.Unlock(id); err != nil {
		t.Fatalf("unlock through the gateway: %v", err)
	}
	t.Logf("unlocked through the gateway in %.1f s", time.Since(start).Seconds())
	if keyguardShowing(t, id) {
		t.Error("still locked after Unlock")
	}
}

func keyguardShowing(t *testing.T, id string) bool {
	out := adbShell(t, id, "dumpsys", "window", "policy")
	return strings.Contains(out, "showing=true") || strings.Contains(out, "isKeyguardShowing=true") ||
		strings.Contains(out, "mShowingLockscreen=true")
}
