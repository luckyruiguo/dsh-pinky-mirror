//go:build !windows

// Pinky runs its own usbmuxd on Linux and Windows (netmux.go); these
// tests serve it on a Unix socket in /tmp, which Windows does not have.

package ios

import (
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/mdns"
	"github.com/DIYgod/pinky/internal/driver/ios/netmux"
)

type staticSource []mdns.Service

func (s staticSource) Services() []mdns.Service { return s }
func (s staticSource) Err() error               { return nil }
func (s staticSource) Close() error             { return nil }

func muxTestProvider(t *testing.T) *Provider {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pinkymux") // short: socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	// This computer's own pair records (an iPad paired on Linux, say) would
	// match the fake phone too, since pairFn gives every UDID its MAC.
	lockdown := netmux.LockdownDir
	netmux.LockdownDir = t.TempDir()
	t.Cleanup(func() { netmux.LockdownDir = lockdown })
	t.Setenv(ownMuxVar, "") // restored after the test: the provider sets it
	p := NewProvider()
	p.WiFiMux = true
	p.nmux.anyOS = true
	p.nmux.dir = dir
	p.nmux.browseFn = func() (netmux.Source, error) {
		return staticSource{{Instance: "08:87:c7:2a:8a:db@fe80::1-supportsRP-24", Type: netmux.ServiceType, Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.20")}}}, nil
	}
	p.nmux.pairFn = func(udid string) (netmux.PairInfo, error) {
		return netmux.PairInfo{WiFiMAC: "08:87:c7:2a:8a:db"}, nil
	}
	p.nmux.probeFn = func(context.Context, netip.Addr) bool { return true }
	p.known["IPHONE"] = known{}
	return p
}

func TestWiFiMux(t *testing.T) {
	t.Setenv("USBMUXD_SOCKET_ADDRESS", "")
	p := muxTestProvider(t)
	p.SetWiFi(true)
	if v := os.Getenv("USBMUXD_SOCKET_ADDRESS"); !strings.HasPrefix(v, "unix://"+p.nmux.dir) {
		t.Fatalf("USBMUXD_SOCKET_ADDRESS = %q", v)
	}
	if _, err := os.Stat(strings.TrimPrefix(os.Getenv("USBMUXD_SOCKET_ADDRESS"), "unix://")); err != nil {
		t.Fatal(err)
	}
	waitPhones := func(n int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for len(p.nmux.mux.Phones()) != n && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if got := p.nmux.mux.Phones(); len(got) != n {
			t.Fatalf("phones = %v, want %d", got, n)
		}
	}
	waitPhones(1)
	if got := p.nmux.mux.Phones()[0]; got.UDID != "IPHONE" || got.Addr.String() != "192.168.1.20" {
		t.Errorf("phone = %+v", got)
	}
	// Wi-Fi off: the search stops and the phone leaves the list; the
	// usbmuxd stays for phones on a cable.
	p.SetWiFi(false)
	waitPhones(0)
	mux := p.nmux.mux
	p.SetWiFi(true)
	waitPhones(1)
	if p.nmux.mux != mux {
		t.Error("a second usbmuxd started")
	}
	p.SetWiFi(false)
	waitPhones(0)
}

func TestWiFiMuxLeavesAGivenUsbmuxd(t *testing.T) {
	t.Setenv("USBMUXD_SOCKET_ADDRESS", "unix:///run/user/1000/netmuxd.sock")
	p := muxTestProvider(t)
	p.SetWiFi(true)
	if p.nmux.mux != nil || os.Getenv("USBMUXD_SOCKET_ADDRESS") != "unix:///run/user/1000/netmuxd.sock" {
		t.Errorf("started its own usbmuxd in place of the one given: %q", os.Getenv("USBMUXD_SOCKET_ADDRESS"))
	}
}

// TestWiFiMuxAfterAnUpdate: a Pinky started by one that ran its own
// usbmuxd (the update helper's relaunch on Windows) inherits its address,
// at a port or socket that is gone. It starts its own all the same, and
// the processes it starts in turn forget its address.
func TestWiFiMuxAfterAnUpdate(t *testing.T) {
	gone := "tcp://127.0.0.1:1"
	t.Setenv("USBMUXD_SOCKET_ADDRESS", gone)
	p := muxTestProvider(t)
	t.Setenv(ownMuxVar, gone)
	forgetInheritedMux() // as at the start of the process
	p.SetWiFi(true)
	defer p.SetWiFi(false)
	addr := os.Getenv("USBMUXD_SOCKET_ADDRESS")
	if p.nmux.mux == nil || !strings.HasPrefix(addr, "unix://"+p.nmux.dir) {
		t.Fatalf("no usbmuxd of its own: USBMUXD_SOCKET_ADDRESS = %q", addr)
	}
	if own := os.Getenv(ownMuxVar); own != addr {
		t.Errorf("%s = %q, want %q", ownMuxVar, own, addr)
	}
	// A process it starts: the address is forgotten there.
	forgetInheritedMux()
	if v := os.Getenv("USBMUXD_SOCKET_ADDRESS"); v != "" {
		t.Errorf("a child keeps USBMUXD_SOCKET_ADDRESS = %q", v)
	}
}

// TestRemoveOldMuxDirs: the folders of Pinkys that ended go, a running
// Pinky's and one that a Pinky made a moment ago stay.
func TestRemoveOldMuxDirs(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "pinkymux") // short: socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	listen := func(name string) *net.UnixListener {
		dir := filepath.Join(base, "pinky-usbmuxd-"+name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("unix", filepath.Join(dir, "usbmuxd"))
		if err != nil {
			t.Fatal(err)
		}
		return l.(*net.UnixListener)
	}
	running := listen("running")
	defer running.Close()
	ended := listen("ended")
	ended.SetUnlinkOnClose(false) // as a Pinky that was killed leaves it
	ended.Close()
	old := time.Now().Add(-time.Hour)
	for _, name := range []string{"starting", "empty"} {
		if err := os.Mkdir(filepath.Join(base, "pinky-usbmuxd-"+name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(base, "pinky-usbmuxd-empty"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "pinky-usbmuxd-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "other"), 0o700); err != nil {
		t.Fatal(err)
	}

	removeOldMuxDirs(base)

	left, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	want := "other pinky-usbmuxd-file pinky-usbmuxd-running pinky-usbmuxd-starting"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("left: %s, want: %s", got, want)
	}
	if c, err := net.Dial("unix", filepath.Join(base, "pinky-usbmuxd-running", "usbmuxd")); err != nil {
		t.Errorf("the running Pinky's socket: %v", err)
	} else {
		c.Close()
	}
}

func TestWiFiMuxOnlyForTheApp(t *testing.T) {
	t.Setenv("USBMUXD_SOCKET_ADDRESS", "")
	p := muxTestProvider(t)
	p.WiFiMux = false
	p.SetWiFi(true)
	if p.nmux.mux != nil || os.Getenv("USBMUXD_SOCKET_ADDRESS") != "" {
		t.Error("a provider without WiFiMux started a usbmuxd")
	}
}

func TestPairedUDIDsOnly(t *testing.T) {
	p := NewProvider()
	p.known["A"] = known{}
	p.wifi.use = map[string]bool{"B": true}
	got := strings.Join(p.pairedUDIDs(), ",")
	if !strings.Contains(got, "A") || !strings.Contains(got, "B") {
		t.Errorf("UDIDs = %q", got)
	}
	p.Only = map[string]bool{"B": true}
	if got := strings.Join(p.pairedUDIDs(), ","); got != "B" {
		t.Errorf("UDIDs under Only = %q", got)
	}
	// A phone the person removed is not looked for on Wi-Fi.
	p.Only = nil
	p.Forget("B")
	if got := strings.Join(p.pairedUDIDs(), ","); strings.Contains(got, "B") {
		t.Errorf("UDIDs after Forget = %q", got)
	}
}
