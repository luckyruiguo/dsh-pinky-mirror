//go:build device

package android

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// TestEmulatorIdentity checks the identity marks (identity.go) against a
// real adb server and an emulator reached over the network: a TCP proxy on
// one of the computer's network addresses stands in for a phone on Wi-Fi
// (a connection on this computer needs no check). It covers a phone Pinky
// has not used, the first check of a known one, the guard after adb made
// the connection again in place to a device without the mark (the
// emulator with its mark removed, as a reset phone or another device), and
// trusting it again. It removes its mark and its connection afterwards.
//
//	PINKY_EMULATOR=emulator-5690 [ANDROID_ADB_SERVER_PORT=5099]
func TestEmulatorIdentity(t *testing.T) {
	emu := os.Getenv("PINKY_EMULATOR")
	n, err := strconv.Atoi(strings.TrimPrefix(emu, "emulator-"))
	if !strings.HasPrefix(emu, "emulator-") || err != nil {
		t.Skip("PINKY_EMULATOR=emulator-<port> names the emulator to use")
	}
	ip := lanAddr()
	if ip == "" {
		t.Skip("no network address to stand in for a phone's")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c := &adb.Client{}
	px := startProxy(t, ip, net.JoinHostPort("127.0.0.1", strconv.Itoa(n+1)))
	addr := px.ln.Addr().String()
	if err := c.Connect(ctx, addr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Disconnect(context.Background(), addr) })
	waitLong(t, "the connection through the proxy", func() bool {
		es, _ := c.Devices(ctx)
		for _, e := range es {
			if e.Serial == addr && e.State == adb.StateDevice {
				return true
			}
		}
		return false
	})

	p := NewProvider(Config{Client: c, VerifyWiFi: true, IdentityFile: filepath.Join(t.TempDir(), "ids.json"), Logf: t.Logf})
	p.usbNodes = nil
	host, err := p.ids.store.host()
	if err != nil {
		t.Fatal(err)
	}
	markPath := markDir + markName + host
	t.Cleanup(func() { c.Shell(context.Background(), emu, "rm -f "+markPath) })

	// The phone over the network, by its id (ro.serialno).
	overWiFi := func() driver.Info {
		t.Helper()
		infos, err := p.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range infos {
			if in.Connection == "wifi" && in.ID != emu {
				return in
			}
		}
		t.Fatalf("the emulator is not listed over the network: %+v", infos)
		return driver.Info{}
	}
	in := overWiFi()
	id := in.ID
	t.Logf("%s over the network: %s, %s/%s", id, addr, in.State, in.Setup)
	if in.State != driver.StateNeedsSetup || in.Setup != driver.SetupConfirmWiFi {
		t.Fatalf("a phone Pinky has not used: %s/%s (%s)", in.State, in.Setup, in.Detail)
	}

	p.UseOverWiFi(id)
	if in := overWiFi(); in.State != driver.StateOnline {
		t.Fatalf("a known phone at its first check: %s (%s)", in.State, in.Detail)
	}
	ls, _ := c.Shell(ctx, emu, "ls -ln "+markPath)
	t.Logf("the mark: %s", strings.TrimSpace(string(ls)))
	if !strings.HasPrefix(string(ls), "-rw------- 1 2000 2000 32 ") {
		t.Fatalf("the mark is not 32 bytes for the shell user only: %q", ls)
	}

	d, err := p.OpenDevice(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := d.client.Shell(ctx, addr, "echo hi"); err != nil || strings.TrimSpace(string(out)) != "hi" {
		t.Fatalf("shell through the guard = %q, %v", out, err)
	}
	waitFor(t, "the watch follows the server", func() bool {
		_, ok := p.ids.watch.entry(addr)
		return ok
	})

	// The phone loses its mark, and adb makes the connection again in
	// place: what answers now is not the phone Pinky set up.
	before, _ := p.ids.watch.entry(addr)
	st, _ := p.ids.watch.stamp(addr)
	if _, err := c.Shell(ctx, emu, "rm -f "+markPath); err != nil {
		t.Fatal(err)
	}
	px.drop()
	waitLong(t, "adb to make the connection again", func() bool {
		e, ok := p.ids.watch.entry(addr)
		now, _ := p.ids.watch.stamp(addr)
		return ok && e.State == adb.StateDevice && now != st
	})
	after, _ := p.ids.watch.entry(addr)
	t.Logf("transport id before %s, after %s", before.TransportID, after.TransportID)
	if _, err := d.client.Shell(ctx, addr, "echo private"); !errors.Is(err, driver.ErrOffline) {
		t.Fatalf("shell to the phone without its mark = %v, want ErrOffline", err)
	}
	if in := overWiFi(); in.State != driver.StateNeedsSetup || in.Setup != driver.SetupPhoneChanged {
		t.Fatalf("without its mark: %s/%s (%s)", in.State, in.Setup, in.Detail)
	}
	if _, err := p.OpenDevice(ctx, id); !errors.Is(err, driver.ErrOffline) {
		t.Fatalf("OpenDevice without its mark = %v, want ErrOffline", err)
	}

	// The person trusts it.
	if err := p.TrustPhone(ctx, id); err != nil {
		t.Fatal(err)
	}
	if in := overWiFi(); in.State != driver.StateOnline {
		t.Fatalf("after trusting: %s (%s)", in.State, in.Detail)
	}
	if out, err := d.client.Shell(ctx, addr, "echo again"); err != nil || strings.TrimSpace(string(out)) != "again" {
		t.Fatalf("shell after trusting = %q, %v", out, err)
	}
}

// lanAddr returns one of the computer's IPv4 addresses that is not on
// loopback.
func lanAddr() string {
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
				return n.IP.String()
			}
		}
	}
	return ""
}

// tcpProxy forwards connections on a network address to an address on
// this computer; drop cuts the ones open, as a network that drops does.
type tcpProxy struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func startProxy(t *testing.T, ip, to string) *tcpProxy {
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatal(err)
	}
	px := &tcpProxy{ln: ln}
	t.Cleanup(func() { ln.Close(); px.drop() })
	go func() {
		for {
			a, err := ln.Accept()
			if err != nil {
				return
			}
			b, err := net.Dial("tcp", to)
			if err != nil {
				a.Close()
				continue
			}
			px.mu.Lock()
			px.conns = append(px.conns, a, b)
			px.mu.Unlock()
			go func() { io.Copy(a, b); a.Close() }()
			go func() { io.Copy(b, a); b.Close() }()
		}
	}()
	return px
}

func (px *tcpProxy) drop() {
	px.mu.Lock()
	defer px.mu.Unlock()
	for _, c := range px.conns {
		c.Close()
	}
	px.conns = nil
}

// waitLong polls cond for up to a minute: adb takes its time to make a
// connection again.
func waitLong(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
