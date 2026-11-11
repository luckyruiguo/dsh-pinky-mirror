//go:build !windows

package adb

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestStopOwnServer: a server StartServer started is Pinky's, and
// StopOwnServer stops it; one started as adb's own start-server starts
// one ("adb" for the program, the pipe at 4) is not, and keeps running.
func TestStopOwnServer(t *testing.T) {
	t.Setenv("ADB_SERVER_SOCKET", "")
	t.Setenv("ANDROID_ADB_SERVER_ADDRESS", "")
	port := freePort(t)
	t.Setenv("ANDROID_ADB_SERVER_PORT", port)
	t.Setenv("PINKY_HOME", t.TempDir())
	t.Setenv("PINKY_FAKE_ADB", "1")
	t.Setenv("PINKY_FAKE_ADB_VERSION", "37.0.0-fake")
	adb := fakeADB(t, t.TempDir())
	t.Setenv(EnvADB, adb)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := &Client{}
	t.Cleanup(func() {
		if conn, err := net.Dial("tcp", ServerAddr()); err == nil {
			writeRequest(conn, "host:kill")
			readStatus(conn)
			conn.Close()
		}
	})

	if _, err := StartServer(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.OwnServer(); !ok {
		t.Fatal("the server StartServer started is not Pinky's")
	}
	if err := c.StopOwnServer(ctx); err != nil {
		t.Fatalf("StopOwnServer: %v", err)
	}
	if _, err := c.Version(ctx); !errors.Is(err, ErrServerNotRunning) {
		t.Fatalf("after StopOwnServer, the server answers: %v", err)
	}
	// As adb's own start-server runs it.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := &exec.Cmd{Path: adb, Args: []string{"adb", "-L", "tcp:" + port, "fork-server", "server", "--reply-fd", "4"},
		ExtraFiles: []*os.File{nil, w}}
	// fd 3 is a placeholder; the pipe is fd 4.
	cmd.ExtraFiles[0], _ = os.Open(os.DevNull)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	var b [3]byte
	if _, err := r.Read(b[:]); err != nil || string(b[:]) != "OK\n" {
		t.Fatalf("the other server: %q %v", b, err)
	}
	if _, ok := c.OwnServer(); ok {
		t.Error("a server adb's own start-server started counts as Pinky's")
	}
	if err := c.StopOwnServer(ctx); !errors.Is(err, ErrNotOwnServer) {
		t.Errorf("StopOwnServer on another's server: %v", err)
	}
	if _, err := c.Version(ctx); err != nil {
		t.Errorf("the other server stopped: %v", err)
	}
}
