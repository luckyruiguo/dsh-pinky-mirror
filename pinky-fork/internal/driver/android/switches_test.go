package android

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

func TestParseUserRotation(t *testing.T) {
	for in, want := range map[string]string{
		"free\n":   driver.RotationAuto,
		"lock 0\n": driver.RotationPortrait,
		"lock 1":   driver.RotationLandscape,
		"lock 2":   driver.RotationUpsideDown,
		"lock 3":   driver.RotationReverseLandscape,
	} {
		if got, ok := parseUserRotation(in); !ok || got != want {
			t.Errorf("parseUserRotation(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	// Before Android 11: the window command does not know user-rotation.
	for _, in := range []string{"", "lock 4", "Unknown command: user-rotation", "lock x"} {
		if got, ok := parseUserRotation(in); ok {
			t.Errorf("parseUserRotation(%q) = %q, want none", in, got)
		}
	}
}

func TestParseNightMode(t *testing.T) {
	for in, want := range map[string]string{
		"Night mode: no\n":   driver.AppearanceLight,
		"Night mode: yes":    driver.AppearanceDark,
		"Night mode: auto":   driver.AppearanceAuto,
		"Night mode: custom": "custom",
	} {
		if got, err := parseNightMode(in); err != nil || got != want {
			t.Errorf("parseNightMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "Error: unknown", "Night mode: maybe"} {
		if got, err := parseNightMode(in); err == nil {
			t.Errorf("parseNightMode(%q) = %q, want an error", in, got)
		}
	}
}

func TestGPSLocation(t *testing.T) {
	dump := `Location Manager State:
  gps provider:
    last location=Location[gps 35.681200,139.767100 hAcc=5.0 et=+1h2m3s alt=0.0 vAcc=0.5 {Bundle[{satellites=0}]}]
    last coarse location=Location[gps 35.681000,139.767000 hAcc=2000.0]
  network provider:
    last location=Location[network 1.0,2.0 hAcc=20.0]
`
	if got := gpsLocation(dump); got != "35.6812,139.7671" {
		t.Fatalf("gpsLocation = %q", got)
	}
	if got := gpsLocation("Location Manager State:\n  gps provider:\n    last location=null\n"); got != "" {
		t.Fatalf("no fix: %q", got)
	}
	if got := gpsLocation("last location=Location[gps -33.8568,-151.2153]"); got != "-33.8568,-151.2153" {
		t.Fatalf("south and west: %q", got)
	}
}

func TestParseAppLocales(t *testing.T) {
	for in, want := range map[string]string{
		"Locales for com.android.settings for user 0 are [ja-JP]\n": "ja-JP",
		"Locales for com.android.settings for user 0 are []":        "",
		"Locales for com.example for user 10 are [en-DE,fr-FR]":     "en-DE,fr-FR",
	} {
		if got, err := parseAppLocales("pkg", in); err != nil || got != want {
			t.Errorf("parseAppLocales(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := parseAppLocales("pkg", "Unknown package: pkg"); err == nil {
		t.Error("an error read as a language")
	}
}

func TestEmulatorPort(t *testing.T) {
	if port, ok := emulatorPort("emulator-5554"); !ok || port != 5554 {
		t.Errorf("emulator-5554 = %d, %v", port, ok)
	}
	for _, s := range []string{"3e8be48e", "emulator-", "emulator-x", "192.168.1.5:5555", "emulator-99999"} {
		if _, ok := emulatorPort(s); ok {
			t.Errorf("%q has a console", s)
		}
	}
	d := &Device{}
	d.info.ID = "FA6C20303860"
	if err := d.SetSwitch(context.Background(), driver.SwitchLocation, "1,2"); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("a phone's simulated location: %v", err)
	}
}

// fakeConsole answers like an emulator's console: a banner, an optional
// auth, then each command with OK or KO.
func fakeConsole(t *testing.T, token string, reply func(cmd string) string) (int, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 8)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("Android Console: type 'help' for a list of commands\r\n"))
		if token != "" {
			conn.Write([]byte("Android Console: Authentication required\r\nAndroid Console: type 'auth <auth_token>' to authenticate\r\n"))
		}
		conn.Write([]byte("OK\r\n"))
		r := bufio.NewReader(conn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimRight(line, "\r\n")
			got <- cmd
			switch {
			case cmd == "quit":
				return
			case strings.HasPrefix(cmd, "auth "):
				if cmd != "auth "+token {
					conn.Write([]byte("KO: authentication token does not match\r\n"))
					continue
				}
				conn.Write([]byte("Android Console: you can use 'help'\r\nOK\r\n"))
			default:
				conn.Write([]byte(reply(cmd) + "\r\n"))
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, got
}

func TestEmuCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	if err := os.WriteFile(filepath.Join(home, ".emulator_console_auth_token"), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	port, got := fakeConsole(t, "s3cret", func(string) string { return "OK" })
	if err := emuCommand(ctx, port, "geo fix 139.7671 35.6812"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"auth s3cret", "geo fix 139.7671 35.6812", "quit"} {
		if cmd := <-got; cmd != want {
			t.Fatalf("console got %q, want %q", cmd, want)
		}
	}

	port, _ = fakeConsole(t, "", func(string) string { return "KO: bad longitude" })
	if err := emuCommand(ctx, port, "geo fix 999 1"); err == nil || !strings.Contains(err.Error(), "bad longitude") {
		t.Fatalf("KO: %v", err)
	}

	// An emulator reports the place last sent to it, which Pinky remembers:
	// its own last fix is old while no app listens.
	port, _ = fakeConsole(t, "s3cret", func(string) string { return "OK" })
	d := &Device{}
	d.info.ID = "emulator-" + strconv.Itoa(port)
	if err := d.SetSwitch(ctx, driver.SwitchLocation, "35.6812,139.7671"); err != nil {
		t.Fatal(err)
	}
	if v, err := d.Switch(ctx, driver.SwitchLocation); err != nil || v != "35.6812,139.7671" {
		t.Fatalf("location after geo fix = %q, %v", v, err)
	}

	// No console there: the emulator is gone.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	free := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if err := emuCommand(ctx, free, "geo fix 1 2"); err == nil || !strings.Contains(err.Error(), strconv.Itoa(free)) && !strings.Contains(err.Error(), "console") {
		t.Fatalf("no console: %v", err)
	}
}
