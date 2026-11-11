//go:build (e2e || device) && !windows

package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The checks of removing a phone on an Android emulator run against an
// adb server of their own, which finds no phone by itself (no USB, no mDNS,
// no emulators): the test connects the emulator to it by address, as a
// phone on Wi-Fi, and the phones on the shared server, real ones included,
// stay out of it. The emulator trusts this computer's adb key already, so
// the connection needs no tap on its screen. Run them under the emulator's
// lock: scripts/devlock.sh emulator-5560 go test ...

// emulatorEnv names the emulator, as the shared adb server lists it.
const emulatorEnv = "PINKY_E2E_EMULATOR"

// privateADBPort is the private server's port, unlike the shared 5037 and
// the 5038 other checks use.
const privateADBPort = "5097"

type privateADB struct{ port string }

// startPrivateADB starts the private server, which the test kills at its
// end; withEmulators lets it find emulators by itself, as the shared
// server does, so an emulator shows up as if plugged in.
func startPrivateADB(t *testing.T, withEmulators bool) *privateADB {
	t.Helper()
	s := &privateADB{port: privateADBPort}
	s.run(t, "kill-server") // one left over from a run that was cut short
	cmd := exec.Command("adb", "-P", s.port, "start-server")
	cmd.Env = append(os.Environ(), "ADB_MDNS=0", "ADB_USB=0")
	if !withEmulators {
		cmd.Env = append(cmd.Env, "ADB_EMU=0")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start the private adb server: %v\n%s", err, out)
	}
	t.Cleanup(func() { s.run(t, "kill-server") })
	return s
}

// run runs adb against the private server and returns what it printed.
func (s *privateADB) run(t *testing.T, args ...string) string {
	t.Helper()
	out, _ := exec.Command("adb", append([]string{"-P", s.port}, args...)...).CombinedOutput()
	return string(out)
}

// serials lists the private server's connections and their states.
func (s *privateADB) serials(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range strings.Split(s.run(t, "devices"), "\n")[1:] {
		if f := strings.Fields(line); len(f) >= 2 {
			out[f[0]] = f[1]
		}
	}
	return out
}

// connect connects the private server to addr and waits until it is
// usable.
func (s *privateADB) connect(t *testing.T, addr string) {
	t.Helper()
	if out := s.run(t, "connect", addr); !strings.Contains(out, "connected to") {
		t.Fatalf("adb connect %s: %s", addr, out)
	}
	for deadline := time.Now().Add(20 * time.Second); s.serials(t)[addr] != "device"; time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not come online: %v", addr, s.serials(t))
		}
	}
}

// testEmulator returns the emulator to use, its adb address and its
// hardware serial, which Pinky knows it by over the network, or skips the
// test when none is named.
func testEmulator(t *testing.T) (serial, addr, hardware string) {
	t.Helper()
	serial = strings.TrimSpace(os.Getenv(emulatorEnv))
	if serial == "" {
		t.Skipf("set %s to a running emulator, such as emulator-5560, and hold its lock", emulatorEnv)
	}
	console, err := strconv.Atoi(strings.TrimPrefix(serial, "emulator-"))
	if err != nil {
		t.Fatalf("%s=%q is not an emulator's serial", emulatorEnv, serial)
	}
	out, err := exec.Command("adb", "-s", serial, "shell", "getprop", "ro.serialno").Output()
	if err != nil {
		t.Fatalf("read %s's serial number: %v", serial, err)
	}
	return serial, "127.0.0.1:" + strconv.Itoa(console+1), strings.TrimSpace(string(out))
}
