//go:build unix

package api

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// TestRuntimeDirWithoutXDG: a Pinky started without XDG_RUNTIME_DIR (by a
// service, say) uses the user's /run/user/<uid> when it is theirs alone, so
// it takes the same lock and socket as one started in the desktop session.
func TestRuntimeDirWithoutXDG(t *testing.T) {
	base := t.TempDir()
	old := runtimeBase
	runtimeBase = base
	t.Cleanup(func() { runtimeBase = old })
	home := t.TempDir()
	state := filepath.Join(home, ".local", "state")
	mine := filepath.Join(base, strconv.Itoa(os.Getuid()))

	if got := runtimeDir(home); got != state {
		t.Errorf("without /run/user/<uid>: %s", got)
	}
	if err := os.Mkdir(mine, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := runtimeDir(home); got != state {
		t.Errorf("with a /run/user/<uid> others can read: %s", got)
	}
	if err := os.Chmod(mine, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := runtimeDir(home); got != mine {
		t.Errorf("with the user's own /run/user/<uid>: %s", got)
	}

	if runtime.GOOS != "linux" {
		return
	}
	t.Setenv(HomeEnv, "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", mine)
	session, err := DefaultDirs()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	service, err := DefaultDirs()
	if err != nil {
		t.Fatal(err)
	}
	if service.Socket() != session.Socket() || service.LockFile() != session.LockFile() {
		t.Errorf("without XDG_RUNTIME_DIR: %s, in the session: %s", service.Socket(), session.Socket())
	}
}

// TestOneGatewayPerDataFolder: the lock goes with the data folder, so a
// gateway whose runtime folder differs (one started after logind removed
// /run/user/<uid> at logout, say) does not start beside another on the same
// data.
func TestOneGatewayPerDataFolder(t *testing.T) {
	a := shortDirs(t)
	b := a
	b.Run = a.Run + "2"
	ln, err := Listen(a)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var running *RunningError
	if ln2, err := Listen(b); !errors.As(err, &running) {
		if err == nil {
			ln2.Close()
		}
		t.Fatalf("a second gateway on the same data: %v", err)
	}
	ln.Close()
	ln2, err := Listen(b)
	if err != nil {
		t.Fatalf("after the first one stopped: %v", err)
	}
	ln2.Close()
}

// TestLiveSocketStays: a gateway on other data that shares the runtime
// folder (or an older Pinky that kept its lock there) keeps its socket.
func TestLiveSocketStays(t *testing.T) {
	a := shortDirs(t)
	b := a
	b.Data = a.Data + "2"
	ln, err := Listen(a)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	var running *RunningError
	if ln2, err := Listen(b); !errors.As(err, &running) {
		if err == nil {
			ln2.Close()
		}
		t.Fatalf("a second gateway on the same socket: %v", err)
	}
	if _, err := os.Stat(a.Socket()); err != nil {
		t.Fatalf("the first gateway lost its socket: %v", err)
	}
	ln.Close()
	ln2, err := Listen(b)
	if err != nil {
		t.Fatalf("after the first one stopped: %v", err)
	}
	ln2.Close()
}
