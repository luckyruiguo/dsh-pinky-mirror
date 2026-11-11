package adb

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestPackagedADB starts an adb server with the adb that a package of
// Pinky carries, as Pinky would, and asks it for its version and devices.
// The release workflow runs it for each package:
//
//	PINKY_ADB_APP=<Pinky.app, or the folder of an unpacked Windows or Linux package> \
//	  go test -run TestPackagedADB ./internal/driver/android/adb/
//
// The server listens on a port of its own and is stopped at the end.
// Outside CI it leaves USB and emulators alone (ADB_USB=0, ADB_EMU=0), so
// that it never connects to the phones and emulators other tools use.
func TestPackagedADB(t *testing.T) {
	app := os.Getenv("PINKY_ADB_APP")
	if app == "" {
		t.Skip("set PINKY_ADB_APP to a Pinky.app or the folder of an unpacked package")
	}
	path := filepath.Join(app, pinkyFiles(runtime.GOOS)[0])
	if fi, err := os.Stat(filepath.Join(app, "Contents")); err == nil && fi.IsDir() {
		path = filepath.Join(app, "Contents", "Helpers", "adb")
	}
	t.Setenv("PINKY_HOME", t.TempDir())
	t.Setenv("ADB_SERVER_SOCKET", "")
	t.Setenv("ANDROID_ADB_SERVER_ADDRESS", "")
	t.Setenv("ANDROID_ADB_SERVER_PORT", freePort(t))
	if os.Getenv("CI") == "" {
		t.Setenv("ADB_USB", "0")
		t.Setenv("ADB_EMU", "0")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	tool, err := check(ctx, Tool{Path: path, Source: FromPinky})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s is adb %s, run from %s", path, tool.Version, tool.Path)
	if !strings.HasSuffix(tool.Version, "-pinky") {
		t.Errorf("version %q is not Pinky's own build", tool.Version)
	}
	start := time.Now()
	if err := spawnServer(ctx, tool.Path, ServerOptions{}); err != nil {
		t.Fatalf("starting a server: %v", err)
	}
	t.Logf("the server started in %v", time.Since(start).Round(time.Millisecond))
	t.Cleanup(func() {
		if conn, err := net.Dial("tcp", ServerAddr()); err == nil {
			writeRequest(conn, "host:kill")
			readStatus(conn)
			conn.Close()
		}
	})
	c := &Client{}
	v, err := c.Version(ctx)
	if err != nil || v != 41 {
		t.Fatalf("the server's version: %d %v", v, err)
	}
	devices, err := c.Devices(ctx)
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	t.Logf("server version %d, %d devices", v, len(devices))
}
