//go:build device

package adb

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Run with: scripts/devlock.sh <id> env PINKY_DEVICE=<id> go test -tags device ./internal/driver/android/adb/

func deviceSerial(t *testing.T) string {
	t.Helper()
	s := os.Getenv("PINKY_DEVICE")
	if s == "" {
		t.Skip("PINKY_DEVICE not set")
	}
	return s
}

func TestDeviceBasics(t *testing.T) {
	serial := deviceSerial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := &Client{}
	v, err := c.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("server version %d", v)
	ds, err := c.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range ds {
		t.Logf("%+v", d)
		if d.Serial == serial {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s not listed", serial)
	}
	p, err := c.Props(ctx, serial)
	if err != nil || p["ro.build.version.sdk"] == "" {
		t.Fatalf("props: %v (%d keys)", err, len(p))
	}
	t.Logf("model %q, Android %s (SDK %s)", p["ro.product.model"], p["ro.build.version.release"], p["ro.build.version.sdk"])

	res, err := c.Run(ctx, serial, "echo out; echo err >&2; exit 7")
	if err != nil || strings.TrimSpace(string(res.Stdout)) != "out" || strings.TrimSpace(string(res.Stderr)) != "err" || res.ExitCode != 7 {
		t.Fatalf("Run = %+v, %v", res, err)
	}

	out, err := c.Exec(ctx, serial, "printf 'a\\nb\\r\\n'")
	if err != nil || string(out) != "a\nb\r\n" {
		t.Fatalf("Exec = %q, %v", out, err)
	}
}

func TestDeviceSync(t *testing.T) {
	serial := deviceSerial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := &Client{}
	remote := "/data/local/tmp/pinky-adb-test.bin"
	data := bytes.Repeat([]byte{0, 1, 2, 3, 0xff, '\n', '\r'}, 40000)
	if err := c.Push(ctx, serial, bytes.NewReader(data), remote, 0o600, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	defer c.Shell(context.Background(), serial, "rm -f "+remote)
	fi, err := c.Stat(ctx, serial, remote)
	// adbd applies its own permissions, so only size and time are checked.
	if err != nil || fi.Size != int64(len(data)) || !fi.Mode.IsRegular() || fi.ModTime.Unix() != 1700000000 {
		t.Fatalf("Stat = %+v, %v", fi, err)
	}
	var buf bytes.Buffer
	if _, err := c.Pull(ctx, serial, remote, &buf); err != nil || !bytes.Equal(buf.Bytes(), data) {
		t.Fatalf("Pull: %v, equal=%v", err, bytes.Equal(buf.Bytes(), data))
	}
}

func TestDeviceForward(t *testing.T) {
	serial := deviceSerial(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := &Client{}
	local, err := c.Forward(ctx, serial, "tcp:0", "localabstract:pinky-adb-test", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("forward %s", local)
	fs, err := c.Forwards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, f := range fs {
		if f.Serial == serial && f.Local == local {
			listed = true
		}
	}
	if !listed {
		t.Errorf("forward %s not listed in %+v", local, fs)
	}
	if err := c.RemoveForward(ctx, serial, local); err != nil {
		t.Fatal(err)
	}
}
