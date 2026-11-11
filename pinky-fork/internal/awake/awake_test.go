package awake

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestHold holds the computer awake for a moment and checks, where the
// system tells without an administrator, that it lists the hold under its
// reason, and no longer once released.
func TestHold(t *testing.T) {
	reason := fmt.Sprintf("Pinky test: awake %d", os.Getpid())
	release, err := Hold(reason)
	if err != nil {
		if errors.Is(err, ErrUnsupported) || runtime.GOOS == "linux" {
			// A Linux without systemd, or where logind refuses the lock
			// to a process outside a login session (CI).
			t.Skipf("no hold here: %v", err)
		}
		t.Fatal(err)
	}
	listed := holds(t)
	if listed == nil {
		release()
		release() // a second call does nothing
		return
	}
	now := listed()
	if !strings.Contains(now, reason) {
		release()
		t.Fatalf("the system does not list %q:\n%s", reason, now)
	}
	for _, line := range strings.Split(now, "\n") {
		if strings.Contains(line, reason) {
			t.Log(strings.TrimSpace(line))
		}
	}
	release()
	release()
	deadline := time.Now().Add(3 * time.Second)
	for strings.Contains(listed(), reason) {
		if time.Now().After(deadline) {
			t.Fatalf("still listed after release:\n%s", listed())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// holds returns what the system lists as keeping it awake, or nil where
// reading that needs an administrator (Windows).
func holds(t *testing.T) func() string {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "pmset", []string{"-g", "assertions"}
	case "linux":
		name, args = "systemd-inhibit", []string{"--list", "--no-pager", "--no-legend"}
	default:
		return nil
	}
	return func() string {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
		return string(out)
	}
}
