package tailnet

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// statusJSON is the shape of `tailscale status --json` (1.104), with made-up
// devices.
const statusJSON = `{
  "Version": "1.104.5-t788384763-gd9218e278",
  "BackendState": "Running",
  "Health": [],
  "MagicDNSSuffix": "tail1234.ts.net",
  "CurrentTailnet": {"Name": "someone@example.com", "MagicDNSSuffix": "tail1234.ts.net", "MagicDNSEnabled": true},
  "Self": {
    "ID": "nSelf", "HostName": "Someone's MacBook", "DNSName": "macbook.tail1234.ts.net.", "OS": "macOS",
    "TailscaleIPs": ["fd7a:115c:a1e0::1", "100.101.1.1"], "Online": true
  },
  "Peer": {
    "nodekey:bb": {
      "ID": "nPixel", "HostName": "pixel-9", "DNSName": "pixel-9.tail1234.ts.net.", "OS": "android",
      "TailscaleIPs": ["100.101.2.2", "fd7a:115c:a1e0::2"], "Online": true, "CurAddr": "203.0.113.7:41641",
      "Relay": "sin", "RxBytes": 1200, "TxBytes": 3400, "LastSeen": "2026-11-08T06:00:00Z"
    },
    "nodekey:aa": {
      "ID": "nMi", "HostName": "Mi 10 Pro", "DNSName": "mi-10-pro.tail1234.ts.net.", "OS": "android",
      "TailscaleIPs": ["100.101.3.3"], "Online": false, "Relay": "hkg"
    },
    "nodekey:cc": {
      "ID": "nNAS", "HostName": "nas", "DNSName": "nas.tail1234.ts.net.", "OS": "linux",
      "TailscaleIPs": ["100.101.4.4"], "Online": true
    },
    "nodekey:dd": {
      "ID": "nTab", "HostName": "Galaxy Tab", "DNSName": "", "OS": "android",
      "TailscaleIPs": ["fd7a:115c:a1e0::5"], "Online": true
    }
  }
}`

func TestParse(t *testing.T) {
	st, err := Parse([]byte(statusJSON))
	if err != nil {
		t.Fatal(err)
	}
	if st.State != Running || !st.Running() || st.Tailnet != "someone@example.com" || st.Detail != "" {
		t.Fatalf("status = %+v", st)
	}
	if st.Self.Name != "Someone's MacBook" || st.Self.DNSName != "macbook.tail1234.ts.net" ||
		st.Self.IPv4() != netip.MustParseAddr("100.101.1.1") {
		t.Fatalf("self = %+v", st.Self)
	}
	if len(st.Peers) != 4 {
		t.Fatalf("peers = %+v", st.Peers)
	}
	px, ok := st.Device("100.101.2.2")
	if !ok || px.ID != "nPixel" || px.Direct != "203.0.113.7:41641" || px.Relay != "sin" || !px.Online ||
		px.RxBytes != 1200 || px.TxBytes != 3400 || px.LastSeen.IsZero() {
		t.Fatalf("pixel = %+v, %v", px, ok)
	}
	if d, ok := st.Device("nMi"); !ok || d.Name != "Mi 10 Pro" {
		t.Fatalf("by id: %+v, %v", d, ok)
	}
	if _, ok := st.Device("100.101.9.9"); ok {
		t.Fatal("found an address no device has")
	}
	// Android devices with an IPv4 address, online first, then by name.
	var names []string
	for _, d := range st.AndroidPeers() {
		names = append(names, d.Name)
	}
	if !slices.Equal(names, []string{"pixel-9", "Mi 10 Pro"}) {
		t.Fatalf("android peers = %v", names)
	}
}

func TestParseStates(t *testing.T) {
	for in, want := range map[string]State{
		"Running": Running, "Stopped": Stopped, "Starting": Starting,
		"NeedsLogin": NeedsLogin, "NeedsMachineAuth": NeedsLogin, "NoState": NotRunning, "InUseOtherUser": NotRunning,
	} {
		st, err := Parse([]byte(`{"BackendState": "` + in + `", "Health": ["Tailscale is stopped."]}`))
		if err != nil || st.State != want {
			t.Errorf("%s: %v, %v; want %v", in, st.State, err, want)
		}
		if want != Running && st.Detail != "Tailscale is stopped." {
			t.Errorf("%s: detail %q", in, st.Detail)
		}
	}
	if _, err := Parse([]byte(`{}`)); err == nil {
		t.Error("parsed a status without a backend state")
	}
	if _, err := Parse([]byte(`failed to connect`)); err == nil {
		t.Error("parsed text")
	}
}

func TestContains(t *testing.T) {
	for s, want := range map[string]bool{
		"100.64.0.1": true, "100.127.255.254": true, "100.128.0.1": false, "192.168.5.137": false,
		"fd7a:115c:a1e0::9834:7761": true, "fd7a:115c:a1e1::1": false, "::ffff:100.101.2.2": true,
	} {
		if got := Contains(netip.MustParseAddr(s)); got != want {
			t.Errorf("Contains(%s) = %v", s, got)
		}
	}
}

func TestFind(t *testing.T) {
	none := func(string) (string, error) { return "", exec.ErrNotFound }
	env := func(k string) string {
		return map[string]string{"ProgramFiles": `C:\Program Files`}[k]
	}
	files := map[string]bool{"/Applications/Tailscale.app/Contents/MacOS/Tailscale": true}
	has := func(p string) bool { return files[p] }
	if p, err := find("darwin", env, none, has); err != nil || p != "/Applications/Tailscale.app/Contents/MacOS/Tailscale" {
		t.Fatalf("darwin: %q, %v", p, err)
	}
	if _, err := find("linux", env, none, has); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("linux without tailscale: %v", err)
	}
	onPath := func(string) (string, error) { return "/usr/bin/tailscale", nil }
	if p, err := find("darwin", env, onPath, has); err != nil || p != "/usr/bin/tailscale" {
		t.Fatalf("PATH first: %q, %v", p, err)
	}
	if c := candidates("windows", env); c[0] != `C:\Program Files/Tailscale/tailscale.exe` && c[0] != `C:\Program Files\Tailscale\tailscale.exe` {
		t.Fatalf("windows: %v", c)
	}
}

func TestReader(t *testing.T) {
	var runs atomic.Int32
	out, fail := []byte(statusJSON), error(nil)
	r := &Reader{
		MaxAge: time.Hour,
		Find:   func() (string, error) { return "/usr/bin/tailscale", nil },
		Run: func(ctx context.Context, path string, args ...string) ([]byte, error) {
			runs.Add(1)
			if path != "/usr/bin/tailscale" || !slices.Equal(args, []string{"status", "--json"}) {
				t.Errorf("ran %s %v", path, args)
			}
			return out, fail
		},
	}
	ctx := context.Background()
	for range 3 {
		st, err := r.Status(ctx)
		if err != nil || st.State != Running || st.Command != "/usr/bin/tailscale" {
			t.Fatalf("status = %+v, %v", st, err)
		}
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("ran %d times; want once within MaxAge", n)
	}

	// Its service does not answer: not running, with what it said.
	r.Forget()
	out, fail = []byte("failed to connect to local Tailscale service; is Tailscale running?\n"), errors.New("exit status 1")
	st, err := r.Status(ctx)
	if err != nil || st.State != NotRunning || st.Detail != "failed to connect to local Tailscale service; is Tailscale running?" {
		t.Fatalf("not running: %+v, %v", st, err)
	}

	// Stopped prints the status and exits 1 on some systems.
	r.Forget()
	out = []byte(`{"BackendState": "Stopped", "Health": ["Tailscale is stopped."]}`)
	if st, err = r.Status(ctx); err != nil || st.State != Stopped {
		t.Fatalf("stopped: %+v, %v", st, err)
	}

	// Not installed.
	nr := &Reader{Find: func() (string, error) { return "", ErrNotInstalled }}
	if st, err := nr.Status(ctx); !errors.Is(err, ErrNotInstalled) || st.State != NotInstalled {
		t.Fatalf("not installed: %+v, %v", st, err)
	}
}
