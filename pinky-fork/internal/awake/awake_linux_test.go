package awake

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHoldWaitsForLogind runs Hold with stand-ins for systemd-inhibit that
// answer at once, late, as logind does while polkit starts, or never: Hold
// returns once the lock is held, or with the refusal, however long logind
// takes, and leaves nothing running when it fails or is released.
func TestHoldWaitsForLogind(t *testing.T) {
	defer func(w time.Duration) { inhibitWait = w }(inhibitWait)
	inhibitWait = 2 * time.Second
	const refuse = "echo 'Failed to inhibit: Access denied' >&2\nexit 1\n"
	for _, c := range []struct {
		name, body, err string
		least           time.Duration // how long Hold must take at least
	}{
		{"refused", refuse, "Access denied", 0},
		{"refused after a moment", "sleep 1\n" + refuse, "Access denied", time.Second},
		{"given after a moment", "sleep 1\nshift 4\nexec \"$@\"\n", "", time.Second},
		{"no answer", "exec sleep 60\n", "no answer from logind", inhibitWait},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := standIn(t, c.body)
			reason := "Pinky test: " + c.name
			start := time.Now()
			release, err := Hold(reason)
			took := time.Since(start)
			if took < c.least {
				t.Errorf("Hold returned after %v, before the answer", took)
			}
			pid := standInPID(t, dir)
			if c.err != "" {
				if err == nil {
					release()
					t.Fatal("Hold held a lock that was not given")
				}
				if !strings.Contains(err.Error(), c.err) {
					t.Fatalf("Hold: %v; want %q", err, c.err)
				}
				gone(t, pid)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if syscall.Kill(pid, 0) != nil {
				t.Error("the lock's holder is not running")
			}
			args, err := os.ReadFile(filepath.Join(dir, "args"))
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"--what=idle:sleep", "--who=Pinky", "--why=" + reason, "--mode=block", "cat"}
			if got := strings.Split(strings.TrimSpace(string(args)), "\n"); !slices.Equal(got, want) {
				t.Errorf("systemd-inhibit %q, want %q", got, want)
			}
			release()
			release()
			gone(t, pid)
		})
	}
}

// standIn puts a shell script with body in place of systemd-inhibit. It
// writes its process ID and arguments to dir first.
func standIn(t *testing.T, body string) (dir string) {
	dir = t.TempDir()
	script := "#!/bin/sh\necho $$ > '" + dir + "/pid'\nprintf '%s\\n' \"$@\" > '" + dir + "/args'\n" + body
	if err := os.WriteFile(filepath.Join(dir, "systemd-inhibit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func standInPID(t *testing.T, dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// gone checks that the stand-in has ended and been waited for.
func gone(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("systemd-inhibit (%d) still there: %v", pid, err)
	}
}
