package android

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/tailnet"
)

const (
	tailAddr   = "100.101.2.2"
	tailSerial = tailAddr + ":5555"
	wifiAddr   = "192.168.1.42"
	lanSerial  = wifiAddr + ":5555"  // adb tcpip at the phone's Wi-Fi address
	localWiFi  = wifiAddr + ":41000" // the phone over Wireless debugging
	piHW       = "R5CN20ABC"         // its hardware serial
	usbLine    = piHW + "\tdevice usb:1-1 product:x model:Pixel_9 device:x transport_id:5"
	tlsLine    = localWiFi + "\tdevice product:x model:Pixel_9 device:x transport_id:7"
	phoneProps = "Pixel 9\n16\n36\nPixel 9\n" + piHW + "\n" // deviceProps' getprop line
)

// fakeTailnet is Tailscale on the computer, as Config.Tailnet reads it.
type fakeTailnet struct {
	mu  sync.Mutex
	st  tailnet.Status
	err error
}

func (f *fakeTailnet) read(context.Context) (tailnet.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st, f.err
}

func (f *fakeTailnet) set(fn func(st *tailnet.Status)) {
	f.mu.Lock()
	fn(&f.st)
	f.mu.Unlock()
}

func runningTailnet() *fakeTailnet {
	return &fakeTailnet{st: tailnet.Status{
		State: tailnet.Running, Tailnet: "someone@example.com",
		Self: tailnet.Device{Name: "macbook", Addrs: []netip.Addr{netip.MustParseAddr("100.101.1.1")}},
		Peers: []tailnet.Device{
			{ID: "nPixel", Name: "pixel-9", OS: "android", Online: true, Addrs: []netip.Addr{netip.MustParseAddr(tailAddr)}},
			{ID: "nNAS", Name: "nas", OS: "linux", Online: true, Addrs: []netip.Addr{netip.MustParseAddr("100.101.4.4")}},
		},
	}}
}

// fakePhone is the phone's side of the services network.go uses: its
// interfaces, and adb listening on the network.
type fakePhone struct {
	mu       sync.Mutex
	f        *fakeADB
	addrs    string // `ip -o addr show`
	port     int    // service.adb.tcp.port
	restarts []string
}

func (ph *fakePhone) run(serial, service string) (string, bool) {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	switch {
	case strings.HasPrefix(service, "shell:ip -o addr show"):
		return ph.addrs, true
	case service == "shell:getprop service.adb.tcp.port":
		if ph.port == 0 {
			return "\n", true
		}
		return strconv.Itoa(ph.port) + "\n", true
	case strings.HasPrefix(service, "tcpip:"):
		ph.restarts = append(ph.restarts, service)
		ph.port = 5555
		go ph.f.set(func(f *fakeADB) { f.connectOK[tailSerial], f.connectOK[lanSerial] = true, true })
		return "restarting in TCP mode port: 5555\n", true
	case service == "usb:":
		ph.restarts = append(ph.restarts, service+serial)
		ph.port = 0
		go ph.f.set(func(f *fakeADB) {
			delete(f.connectOK, tailSerial)
			delete(f.connectOK, lanSerial)
			f.devices = slices.DeleteFunc(f.devices, func(l string) bool {
				return strings.HasPrefix(l, tailSerial+"\t") || strings.HasPrefix(l, lanSerial+"\t")
			})
		})
		return "restarting in USB mode\n", true
	}
	return "", false
}

func (ph *fakePhone) restarted() []string {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	return slices.Clone(ph.restarts)
}

const phoneAddrs = "1: lo    inet 127.0.0.1/8 scope host lo\n" +
	"30: wlan0    inet " + wifiAddr + "/24 brd 192.168.1.255 scope global wlan0\n" +
	"31: tun0    inet " + tailAddr + "/32 scope global tun0\n" +
	"31: tun0    inet6 fd7a:115c:a1e0::2/128 scope global\n"

func newNetFake(t *testing.T) (*fakeADB, *fakePhone, *fakeTailnet, *Provider) {
	oldLook, oldUp := netLook, upWait
	netLook, upWait = 300*time.Millisecond, 3*time.Second
	t.Cleanup(func() { netLook, upWait = oldLook, oldUp })
	f := startFakeADB(t)
	ts := runningTailnet()
	ph := &fakePhone{f: f, addrs: phoneAddrs}
	f.set(func(f *fakeADB) {
		f.props = map[string]string{piHW: phoneProps, localWiFi: phoneProps, tailSerial: phoneProps, lanSerial: phoneProps}
		f.connectOK = map[string]bool{}
		f.run = ph.run
	})
	cfg := f.config()
	cfg.Tailnet = ts.read
	cfg.NetworkFile = filepath.Join(t.TempDir(), "network-phones.json")
	p := NewProvider(cfg)
	// What answers at an address is the fake server's to say.
	p.link.probe = func(ctx context.Context, serial string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.probes = append(f.probes, serial)
		switch {
		case f.unreachable[serial]:
			return errors.New("dial tcp " + serial + ": i/o timeout")
		case f.connectOK != nil && !f.connectOK[serial]:
			return connectErr(serial, errors.New("dial tcp "+serial+": connect: connection refused"))
		}
		return nil
	}
	return f, ph, ts, p
}

func (f *fakeADB) connected() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.connects)
}

// tried returns the addresses the network code tried, connected or not.
func (f *fakeADB) tried() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.probes)
}

func TestParsePhoneAddrs(t *testing.T) {
	out := "1: lo    inet 127.0.0.1/8 scope host lo\\       valid_lft forever preferred_lft forever\n" +
		"30: wlan0    inet 192.168.5.137/24 brd 192.168.5.255 scope global wlan0\n" +
		"31: tun0    inet6 fd7a:115c:a1e0::2/128 scope global\n" +
		"31: tun0    inet 100.101.2.2/32 scope global tun0\n" +
		"40: rmnet_data1    inet 100.70.8.9/30 scope global rmnet_data1\n" // carrier NAT shares the range
	wifi, tail := parsePhoneAddrs(out)
	want := []netip.Addr{netip.MustParseAddr("100.101.2.2"), netip.MustParseAddr("fd7a:115c:a1e0::2")}
	if wifi != netip.MustParseAddr("192.168.5.137") || !slices.Equal(tail, want) {
		t.Fatalf("wifi %v, tailnet %v", wifi, tail)
	}
	if wifi, tail := parsePhoneAddrs("40: rmnet_data1    inet 10.1.2.3/30 scope global rmnet_data1\n"); wifi.IsValid() || len(tail) != 0 {
		t.Fatalf("mobile data only: %v, %v", wifi, tail)
	}
}

func TestSplitTailnetAddr(t *testing.T) {
	st := runningTailnet().st
	st.Peers[0].DNSName = "pixel-9.tail1234.ts.net"
	for in, want := range map[string]string{
		"100.101.2.2":             "100.101.2.2:5555",
		"100.101.2.2:41000":       "100.101.2.2:41000",
		"pixel-9":                 "100.101.2.2:5555",
		"pixel-9.tail1234.ts.net": "100.101.2.2:5555",
		"PIXEL-9:37000":           "100.101.2.2:37000",
	} {
		h, p, err := splitTailnetAddr(in, st)
		if err != nil || h+":"+strconv.Itoa(p) != want {
			t.Errorf("%q: %s:%d, %v; want %s", in, h, p, err, want)
		}
	}
	for _, in := range []string{"192.168.1.42:5555", "100.101.2.2:x", "galaxy", ""} {
		if h, p, err := splitTailnetAddr(in, st); err == nil {
			t.Errorf("%q: %s:%d, want an error", in, h, p)
		}
	}
}

func TestViaTailnet(t *testing.T) {
	if !viaTailnet(tailSerial) || viaTailnet(localWiFi) || viaTailnet("adb-x._adb-tls-connect._tcp") || viaTailnet("3e8be48e") {
		t.Fatal("viaTailnet")
	}
	e := adb.ParseDevices(tailSerial + "\tdevice product:x model:Pixel_9 device:x transport_id:8")
	if info := infoFromEntry(e[0]); info.Connection != "wifi" || info.Via != driver.ViaTailscale {
		t.Fatalf("info = %+v", info)
	}
	// The local network before Tailscale, USB before both.
	local := driver.Info{State: driver.StateOnline, Connection: "wifi"}
	tail := driver.Info{State: driver.StateOnline, Connection: "wifi", Via: driver.ViaTailscale}
	usb := driver.Info{State: driver.StateOnline, Connection: "usb"}
	if !better(local, tail) || better(tail, local) || !better(usb, tail) || better(tail, usb) || !better(usb, local) {
		t.Fatal("better")
	}
	if !better(tail, driver.Info{State: driver.StateOffline, Connection: "usb"}) {
		t.Fatal("an online Tailscale connection before an offline cable")
	}
	if !legacyInstance("adb-FA6C20303860", "FA6C20303860") || !legacyInstance("adb-3e8be48e-kIY2HE", "3e8be48e") ||
		legacyInstance("adb-K9307038251L00335", "FA6C20303860") || legacyInstance("adb-", "") {
		t.Fatal("legacyInstance")
	}
}

func TestNetStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "network-phones.json")
	s := &netStore{path: path, logf: t.Logf}
	if err := s.update(piHW, func(n *netPhone) { n.Name, n.Node, n.Tailnet = "Pixel 9", "nPixel", tailAddr }); err != nil {
		t.Fatal(err)
	}
	if err := s.update("other", func(n *netPhone) { n.LAN = "192.168.1.7" }); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("file: %v, %v", fi, err)
	}
	// Read back from the file.
	again := &netStore{path: path, logf: t.Logf}
	got, ok := again.get(piHW)
	if !ok || got.Node != "nPixel" || got.tailSerial() != tailSerial || got.lanSerial() != "" || len(again.list()) != 2 {
		t.Fatalf("read back %+v, %v; list %+v", got, ok, again.list())
	}
	if o, _ := again.get("other"); o.lanSerial() != "192.168.1.7:5555" {
		t.Fatalf("no port means 5555: %s", o.lanSerial())
	}
	// A phone left with no address is forgotten.
	if err := again.update(piHW, func(n *netPhone) { n.Tailnet = "" }); err != nil {
		t.Fatal(err)
	}
	third := &netStore{path: path, logf: t.Logf}
	if _, ok := third.get(piHW); ok || len(third.list()) != 1 {
		t.Fatalf("after clearing: %+v", third.list())
	}
	// A broken file is no phones, not a failure.
	os.WriteFile(path, []byte("{"), 0o600)
	if l := (&netStore{path: path, logf: t.Logf}).list(); len(l) != 0 {
		t.Fatalf("broken file: %+v", l)
	}
}

func TestTailnetList(t *testing.T) {
	f, _, ts, p := newNetFake(t)
	ctx := context.Background()
	st, err := p.Tailnet(ctx)
	if err != nil || st.State != driver.TailnetRunning || st.Tailnet != "someone@example.com" || st.Self != "macbook" {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if len(st.Phones) != 1 || st.Phones[0].Name != "pixel-9" || st.Phones[0].Addr != tailAddr || !st.Phones[0].Online ||
		st.Phones[0].Connected || st.Phones[0].ID != "" {
		t.Fatalf("phones = %+v", st.Phones)
	}
	// Connected through Tailscale: Pinky knows which phone it is.
	f.set(func(f *fakeADB) {
		f.devices = []string{tailSerial + "\tdevice product:x model:Pixel_9 device:x transport_id:8"}
	})
	st, _ = p.Tailnet(ctx)
	if !st.Phones[0].Connected || st.Phones[0].ID != piHW {
		t.Fatalf("connected: %+v", st.Phones)
	}
	// Not installed is a state, not an error.
	ts.set(func(s *tailnet.Status) { *s = tailnet.Status{} })
	ts.mu.Lock()
	ts.err = tailnet.ErrNotInstalled
	ts.mu.Unlock()
	if st, err := p.Tailnet(ctx); err != nil || st.State != driver.TailnetNotInstalled {
		t.Fatalf("not installed: %+v, %v", st, err)
	}
}

func TestConnectTailnet(t *testing.T) {
	f, _, ts, p := newNetFake(t)
	ctx := context.Background()
	// adb does not listen there.
	if _, err := p.ConnectTailnet(ctx, tailAddr); !errors.Is(err, driver.ErrNotListening) {
		t.Fatalf("refused: %v", err)
	}
	if _, kept := p.net.get(piHW); kept {
		t.Fatal("kept a phone that refused")
	}
	// It does: connected, and kept as set up for away from home.
	f.set(func(f *fakeADB) { f.connectOK[tailSerial] = true })
	info, err := p.ConnectTailnet(ctx, "pixel-9")
	if err != nil || info.ID != piHW || info.Via != driver.ViaTailscale || info.State != driver.StateOnline {
		t.Fatalf("connect: %+v, %v", info, err)
	}
	if n, kept := p.net.get(piHW); !kept || n.Node != "nPixel" || n.tailSerial() != tailSerial || n.LAN != "" {
		t.Fatalf("kept %+v, %v", n, kept)
	}
	// At Wireless debugging's port: connected, not kept (the port changes).
	p.net.update(piHW, func(n *netPhone) { *n = netPhone{} })
	f.set(func(f *fakeADB) { f.connectOK[tailAddr+":37000"] = true; f.props[tailAddr+":37000"] = phoneProps })
	if _, err := p.ConnectTailnet(ctx, tailAddr+":37000"); err != nil {
		t.Fatal(err)
	}
	if _, kept := p.net.get(piHW); kept {
		t.Fatal("kept a phone reached at Wireless debugging's port")
	}
	// Not an address on the tailnet; Tailscale off.
	if _, err := p.ConnectTailnet(ctx, "192.168.1.42:5555"); err == nil {
		t.Fatal("connected to a local address")
	}
	ts.set(func(s *tailnet.Status) { s.State = tailnet.Stopped })
	if _, err := p.ConnectTailnet(ctx, tailAddr); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("Tailscale off: %v", err)
	}
}

func TestSetAway(t *testing.T) {
	f, ph, ts, p := newNetFake(t)
	ctx := context.Background()
	f.set(func(f *fakeADB) { f.devices = []string{tlsLine} })
	a, err := p.Away(ctx, piHW)
	if err != nil || a.On || a.Listening || a.Addr != tailAddr || a.Missing != "" {
		t.Fatalf("before: %+v, %v", a, err)
	}
	a, err = p.SetAway(ctx, piHW, true)
	if err != nil || !a.On || !a.Listening || a.Addr != tailAddr || a.Port != 5555 {
		t.Fatalf("on: %+v, %v", a, err)
	}
	if r := ph.restarted(); !slices.Equal(r, []string{"tcpip:5555"}) {
		t.Fatalf("restarts = %q", r)
	}
	if n, ok := p.net.get(piHW); !ok || n.Node != "nPixel" || n.Name != "Pixel 9" || n.LAN != "" {
		t.Fatalf("kept %+v", n)
	}
	// On again: adb listens already, nothing restarts.
	if _, err := p.SetAway(ctx, piHW, true); err != nil {
		t.Fatal(err)
	}
	if n := len(ph.restarted()); n != 1 {
		t.Fatalf("restarted adb again: %d", n)
	}
	// Off: adb stops listening, the Tailscale connection goes.
	a, err = p.SetAway(ctx, piHW, false)
	if err != nil || a.On {
		t.Fatalf("off: %+v, %v", a, err)
	}
	if r := ph.restarted(); r[len(r)-1] != "usb:"+localWiFi {
		t.Fatalf("restarts = %q", r)
	}
	f.mu.Lock()
	disc := slices.Clone(f.disconnects)
	f.mu.Unlock()
	if !slices.Contains(disc, tailSerial) {
		t.Fatalf("disconnects = %q", disc)
	}
	if _, kept := p.net.get(piHW); kept {
		t.Fatal("still kept")
	}

	// The phone has no Tailscale: say so, change nothing.
	ph.mu.Lock()
	ph.addrs = "30: wlan0    inet 192.168.1.42/24 scope global wlan0\n"
	ph.mu.Unlock()
	if a, _ := p.Away(ctx, piHW); a.Missing != driver.AwayNoTailscaleThere {
		t.Fatalf("no Tailscale on the phone: %+v", a)
	}
	if _, err := p.SetAway(ctx, piHW, true); err == nil {
		t.Fatal("set up a phone without Tailscale")
	}
	// Tailscale on another tailnet, with its IPv6 address; a VPN in
	// 100.64.0.0/10 alone may be another one, as Cloudflare WARP.
	for _, c := range []struct{ addrs, missing string }{
		{"31: tun0    inet 100.99.9.9/32 scope global tun0\n31: tun0    inet6 fd7a:115c:a1e0::99/128 scope global\n", driver.AwayOtherTailnet},
		{"31: tun0    inet 100.96.0.5/32 scope global tun0\n", driver.AwayNoTailscaleThere},
	} {
		ph.mu.Lock()
		ph.addrs = "30: wlan0    inet 192.168.1.42/24 scope global wlan0\n" + c.addrs
		ph.mu.Unlock()
		if a, _ := p.Away(ctx, piHW); a.Missing != c.missing {
			t.Fatalf("%q: %+v, want %s", c.addrs, a, c.missing)
		}
		if _, err := p.SetAway(ctx, piHW, true); err == nil {
			t.Fatalf("%q: set up a phone not on this tailnet", c.addrs)
		}
	}
	// None here.
	ts.set(func(s *tailnet.Status) { s.State = tailnet.NotRunning })
	if a, _ := p.Away(ctx, piHW); a.Missing != driver.AwayNoTailscaleHere {
		t.Fatalf("no Tailscale here: %+v", a)
	}
	// Not connected at all.
	ts.set(func(s *tailnet.Status) { s.State = tailnet.Running })
	f.set(func(f *fakeADB) { f.devices = nil })
	if a, _ := p.Away(ctx, piHW); a.Missing != driver.AwayNotConnected {
		t.Fatalf("not connected: %+v", a)
	}
	if _, err := p.SetAway(ctx, piHW, true); !errors.Is(err, driver.ErrOffline) {
		t.Fatalf("set up a phone that is not connected: %v", err)
	}
}

// TestSetWiFi moves a phone on a cable to Wi-Fi and back.
func TestSetWiFi(t *testing.T) {
	f, ph, _, p := newNetFake(t)
	ctx := context.Background()
	// Not plugged in.
	if w, err := p.PhoneWiFi(ctx, piHW); err != nil || w.On || w.Missing != driver.WiFiNeedsCable {
		t.Fatalf("not plugged in: %+v, %v", w, err)
	}
	if _, err := p.SetPhoneWiFi(ctx, piHW, true); !errors.Is(err, driver.ErrOffline) {
		t.Fatalf("moved a phone that is not plugged in: %v", err)
	}
	f.set(func(f *fakeADB) { f.devices = []string{usbLine} })
	w, err := p.PhoneWiFi(ctx, piHW)
	if err != nil || w.On || w.Listening || w.Addr != wifiAddr || w.Missing != "" {
		t.Fatalf("plugged in: %+v, %v", w, err)
	}
	w, err = p.SetPhoneWiFi(ctx, piHW, true)
	if err != nil || !w.On || !w.Connected || !w.Listening || w.Addr != wifiAddr || w.Stopped {
		t.Fatalf("on: %+v, %v", w, err)
	}
	if n, ok := p.net.get(piHW); !ok || n.LAN != wifiAddr || n.Tailnet != "" || n.Name != "Pixel 9" {
		t.Fatalf("kept %+v", n)
	}
	// Unplugged: listed over Wi-Fi, by its own id.
	f.set(func(f *fakeADB) {
		f.devices = slices.DeleteFunc(f.devices, func(l string) bool { return l == usbLine })
	})
	infos, err := p.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].ID != piHW || infos[0].Connection != "wifi" || infos[0].Via != "" ||
		infos[0].State != driver.StateOnline {
		t.Fatalf("unplugged: %+v, %v", infos, err)
	}
	// Away from home too; Wi-Fi off ends both.
	if _, err := p.SetAway(ctx, piHW, true); err != nil {
		t.Fatal(err)
	}
	if w, _ := p.PhoneWiFi(ctx, piHW); !w.Away {
		t.Fatalf("away not said: %+v", w)
	}
	w, err = p.SetPhoneWiFi(ctx, piHW, false)
	if err != nil || w.On {
		t.Fatalf("off: %+v, %v", w, err)
	}
	if r := ph.restarted(); r[len(r)-1] != "usb:"+lanSerial {
		t.Fatalf("restarts = %q", r)
	}
	if _, kept := p.net.get(piHW); kept {
		t.Fatal("still kept")
	}
	// Not on Wi-Fi.
	f.set(func(f *fakeADB) { f.devices = []string{usbLine} })
	ph.mu.Lock()
	ph.addrs = "40: rmnet_data1    inet 10.1.2.3/30 scope global rmnet_data1\n"
	ph.mu.Unlock()
	if w, _ := p.PhoneWiFi(ctx, piHW); w.Missing != driver.WiFiNoWiFi {
		t.Fatalf("no Wi-Fi: %+v", w)
	}
	if _, err := p.SetPhoneWiFi(ctx, piHW, true); err == nil {
		t.Fatal("moved a phone that has no Wi-Fi")
	}
}

// TestSetWiFiUnreachable: a phone that cannot be reached at its Wi-Fi
// address is left with its adb not listening.
func TestSetWiFiUnreachable(t *testing.T) {
	f, ph, _, p := newNetFake(t)
	ctx := context.Background()
	f.set(func(f *fakeADB) { f.devices = []string{usbLine} })
	ph.mu.Lock()
	ph.addrs = "30: wlan0    inet 10.9.9.9/24 scope global wlan0\n" // another network
	ph.mu.Unlock()
	if _, err := p.SetPhoneWiFi(ctx, piHW, true); err == nil {
		t.Fatal("moved to an address that does not answer")
	}
	if r := ph.restarted(); !slices.Equal(r, []string{"tcpip:5555", "usb:" + piHW}) {
		t.Fatalf("restarts = %q", r)
	}
	if _, kept := p.net.get(piHW); kept {
		t.Fatal("kept")
	}
}

// TestLANReconnect: a phone moved to Wi-Fi is connected again at its
// address, at a new one mDNS finds, and listed offline with why while it
// cannot be.
func TestLANReconnect(t *testing.T) {
	f, ph, _, p := newNetFake(t)
	ctx := context.Background()
	now := time.Now()
	p.link.now = func() time.Time { return now }
	p.net.update(piHW, func(n *netPhone) { n.Name, n.LAN = "Pixel 9", wifiAddr })
	// Not listening (it restarted): offline, plug it in.
	ph.mu.Lock()
	ph.port = 0
	ph.mu.Unlock()
	if missing := p.link.reconnect(ctx, p.net.list(), entries(t, p)); !missing {
		t.Fatal("not missing")
	}
	infos, _ := p.List(ctx)
	if len(infos) != 1 || infos[0].ID != piHW || infos[0].Name != "Pixel 9" || infos[0].State != driver.StateOffline ||
		infos[0].Setup != driver.SetupNetworkOff {
		t.Fatalf("restarted: %+v", infos)
	}
	if w, _ := p.PhoneWiFi(ctx, piHW); !w.On || !w.Stopped {
		t.Fatalf("restarted: %+v", w)
	}
	// Listening at a new address that mDNS finds.
	now = now.Add(netFirst + time.Second)
	f.set(func(f *fakeADB) {
		f.connectOK["192.168.1.77:5555"] = true
		f.props["192.168.1.77:5555"] = phoneProps
		f.own = []string{"adb-K9307038251L00335\t_adb._tcp\t192.168.1.143:5555", "adb-" + piHW + "\t_adb._tcp\t192.168.1.77:5555"}
	})
	if missing := p.link.reconnect(ctx, p.net.list(), entries(t, p)); missing {
		t.Fatalf("missing after the new address; connects %q", f.connected())
	}
	if n, _ := p.net.get(piHW); n.LAN != "192.168.1.77" {
		t.Fatalf("kept %+v", n)
	}
	if c := f.tried(); slices.Contains(c, "192.168.1.143:5555") {
		t.Fatalf("tried another phone's adb: %q", c)
	}
	infos, _ = p.List(ctx)
	if len(infos) != 1 || infos[0].State != driver.StateOnline || infos[0].ID != piHW {
		t.Fatalf("back: %+v", infos)
	}
	// Gone from the network: offline, back by itself.
	f.set(func(f *fakeADB) {
		f.devices, f.connectOK, f.own = nil, map[string]bool{}, nil
		f.unreachable = map[string]bool{"192.168.1.77:5555": true}
	})
	now = now.Add(time.Hour)
	p.link.reconnect(ctx, p.net.list(), entries(t, p))
	infos, _ = p.List(ctx)
	if len(infos) != 1 || infos[0].State != driver.StateOffline || infos[0].Setup != driver.SetupNetworkAway {
		t.Fatalf("away: %+v", infos)
	}
	// Removed from Pinky: not listed, not connected.
	p.Forget(piHW)
	if infos, _ := p.List(ctx); len(infos) != 0 {
		t.Fatalf("forgotten: %+v", infos)
	}
}

func TestTailReconnect(t *testing.T) {
	f, _, ts, p := newNetFake(t)
	ctx := context.Background()
	now := time.Now()
	p.link.now = func() time.Time { return now }
	p.net.update(piHW, func(n *netPhone) { n.Node, n.Tailnet = "nPixel", tailAddr })
	// adb does not listen: a failed try, then none until the wait is over.
	if missing := p.link.reconnect(ctx, p.net.list(), entries(t, p)); !missing || len(f.tried()) != 1 || len(f.connected()) != 0 {
		t.Fatalf("refused: missing %v, tried %q, connects %q", missing, f.tried(), f.connected())
	}
	p.link.reconnect(ctx, p.net.list(), entries(t, p))
	if len(f.tried()) != 1 {
		t.Fatalf("tried again at once: %q", f.tried())
	}
	now = now.Add(netFirst + time.Second)
	f.set(func(f *fakeADB) { f.connectOK[tailSerial] = true })
	if missing := p.link.reconnect(ctx, p.net.list(), entries(t, p)); missing || len(f.connected()) != 1 {
		t.Fatalf("listening: missing %v, connects %q", missing, f.connected())
	}
	// Connected: nothing to do.
	if missing := p.link.reconnect(ctx, p.net.list(), entries(t, p)); missing || len(f.tried()) != 2 {
		t.Fatalf("connected: missing %v, tried %q", missing, f.tried())
	}
	// Offline on the tailnet: no try.
	f.set(func(f *fakeADB) { f.devices = nil })
	ts.set(func(s *tailnet.Status) { s.Peers[0].Online = false })
	if missing := p.link.reconnect(ctx, p.net.list(), entries(t, p)); !missing || len(f.tried()) != 2 {
		t.Fatalf("offline: missing %v, tried %q", missing, f.tried())
	}
	// A new address: kept, and used.
	ts.set(func(s *tailnet.Status) {
		s.Peers[0].Online = true
		s.Peers[0].Addrs = []netip.Addr{netip.MustParseAddr("100.101.9.9")}
	})
	f.set(func(f *fakeADB) { f.connectOK["100.101.9.9:5555"] = true })
	p.link.reconnect(ctx, p.net.list(), entries(t, p))
	if c := f.connected(); c[len(c)-1] != "100.101.9.9:5555" {
		t.Fatalf("connects %q", c)
	}
	if n, _ := p.net.get(piHW); n.Tailnet != "100.101.9.9" {
		t.Fatalf("kept %+v", n)
	}
	// Removed from Pinky: left alone.
	f.set(func(f *fakeADB) { f.devices = nil })
	p.Forget(piHW)
	n := len(f.tried())
	if missing := p.link.reconnect(ctx, p.net.list(), entries(t, p)); missing || len(f.tried()) != n {
		t.Fatalf("forgotten: missing %v, tried %q", missing, f.tried())
	}
	// Tailscale off here: nothing to try.
	p.Remember(piHW)
	ts.set(func(s *tailnet.Status) { s.State = tailnet.Stopped })
	if missing := p.link.reconnect(ctx, p.net.list(), entries(t, p)); !missing || len(f.tried()) != n {
		t.Fatalf("Tailscale off: missing %v, tried %q", missing, f.tried())
	}
}

// TestRemoveNetworkPhone: removing a phone reached over the network stops
// its adb listening and forgets its addresses.
func TestRemoveNetworkPhone(t *testing.T) {
	f, ph, _, p := newNetFake(t)
	ctx := context.Background()
	f.set(func(f *fakeADB) { f.devices = []string{tlsLine} })
	if _, err := p.SetAway(ctx, piHW, true); err != nil {
		t.Fatal(err)
	}
	p.Forget(piHW)
	if err := p.Disconnect(ctx, piHW); err != nil {
		t.Fatal(err)
	}
	if _, kept := p.net.get(piHW); kept {
		t.Fatal("still kept")
	}
	if r := ph.restarted(); r[len(r)-1] != "usb:"+localWiFi {
		t.Fatalf("restarts %q", r)
	}
}

// TestTailnetStream: a phone reached through Tailscale streams lighter
// than one on the local network, unless the configuration says otherwise.
func TestTailnetStream(t *testing.T) {
	local := newDevice(Config{}, driver.Info{ID: localWiFi, Connection: "wifi"}, deviceProps{})
	tail := newDevice(Config{}, driver.Info{ID: tailSerial, Connection: "wifi", Via: driver.ViaTailscale}, deviceProps{})
	if o := local.baseOptions(); o.VideoBitRate != wifiBitRate || o.MaxFPS != wifiMaxFPS {
		t.Fatalf("local: %+v", o)
	}
	if o := tail.baseOptions(); o.VideoBitRate != tailnetBitRate || o.MaxFPS != tailnetMaxFPS || o.VideoCodec != "h265" {
		t.Fatalf("through Tailscale: %+v", o)
	}
	set := newDevice(Config{VideoBitRate: 4_000_000, MaxFPS: 60}, driver.Info{ID: tailSerial, Connection: "wifi", Via: driver.ViaTailscale}, deviceProps{})
	if o := set.baseOptions(); o.VideoBitRate != 4_000_000 || o.MaxFPS != 60 {
		t.Fatalf("configured: %+v", o)
	}
}
