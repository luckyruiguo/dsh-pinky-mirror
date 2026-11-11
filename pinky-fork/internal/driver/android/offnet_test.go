package android

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// cutOffFake is a reconnecting driver whose adb server cannot reach the
// network (fakeADB.noRoute) while Pinky can. own says whether Pinky
// started that server: stopping it then gives a server that can.
type cutOffFake struct {
	f             *fakeADB
	p             *Provider
	stops, starts atomic.Int32
	dials         atomic.Int32
}

func newCutOffFake(t *testing.T, own bool) *cutOffFake {
	t.Helper()
	c := &cutOffFake{}
	f := startFakeADB(t)
	cfg := f.config()
	cfg.ReconnectWiFi = true
	cfg.KnownHosts = func() ([]string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return slices.Clone(f.known), nil
	}
	cfg.StopServer = func(ctx context.Context) error {
		c.stops.Add(1)
		if !own {
			return adb.ErrNotOwnServer
		}
		f.set(func(f *fakeADB) { f.noRoute = false })
		return nil
	}
	cfg.StartServer = func(ctx context.Context) error {
		c.starts.Add(1)
		return nil
	}
	old, oldChecks, oldWait := reconnectSearch, cutOffChecks, restartTryWait
	reconnectSearch, cutOffChecks, restartTryWait = 300*time.Millisecond, true, 10*time.Millisecond
	t.Cleanup(func() { reconnectSearch, cutOffChecks, restartTryWait = old, oldChecks, oldWait })
	c.f, c.p = f, NewProvider(cfg)
	// The tests run reconnect themselves: List (ConnectWiFi, PairWithCode)
	// must not start passes that outlive the test and read the globals.
	c.p.wifi.running = true
	c.p.dial = func(ctx context.Context, addr string) error {
		c.dials.Add(1)
		return nil // Pinky reaches every address
	}
	f.set(func(f *fakeADB) {
		f.noRoute = true
		f.known = []string{guidPixel}
		f.own = []string{guidPixel + "\t_adb-tls-connect._tcp\t192.168.1.42:41000"}
	})
	return c
}

// TestReconnectServerCutOff: the adb server Pinky started gets no route to
// a paired phone that Pinky reaches, as one that lost macOS's Local
// Network access when an update replaced the Pinky that started it. The
// driver stops it, starts another and connects the phone through that
// one, and the restart neither counts as another program's nor holds the
// phone back.
func TestReconnectServerCutOff(t *testing.T) {
	c := newCutOffFake(t, true)
	ctx := context.Background()
	away, connected := c.p.wifi.reconnect(ctx, entries(t, c.p))
	if !connected || away {
		t.Errorf("reconnect = away %v, connected %v", away, connected)
	}
	if c.stops.Load() != 1 || c.starts.Load() != 1 {
		t.Errorf("stops %d, starts %d, want one each", c.stops.Load(), c.starts.Load())
	}
	if _, connects := c.f.counts(); !slices.Equal(connects, []string{"192.168.1.42:41000", "192.168.1.42:41000"}) {
		t.Errorf("connects %q", connects)
	}
	if h := c.p.CheckHost(); h.Missing != "" {
		t.Errorf("CheckHost = %+v", h)
	}
	if c.p.wifi.holdBack(guidPixel, "192.168.1.42:41000") {
		t.Error("the phone is held back for the server's failure")
	}
}

// TestReconnectServerCutOffAgain: a server Pinky started anew that cannot
// reach the network either is never restarted again in this run, also
// after offNetRetry (every restart drops every connection on the server),
// and CheckHost says so.
func TestReconnectServerCutOffAgain(t *testing.T) {
	c := newCutOffFake(t, true)
	c.p.cfg.StopServer = func(ctx context.Context) error { c.stops.Add(1); return nil } // the new one is cut off too
	ctx := context.Background()
	if _, connected := c.p.wifi.reconnect(ctx, entries(t, c.p)); connected {
		t.Fatal("connected through a server that cannot reach the network")
	}
	c.p.wifi.reconnect(ctx, entries(t, c.p))
	c.p.server.mu.Lock()
	c.p.server.triedAt = c.p.server.triedAt.Add(-2 * offNetRetry)
	c.p.server.restartedAt = c.p.server.restartedAt.Add(-2 * offNetRetry)
	c.p.server.mu.Unlock()
	c.p.wifi.reconnect(ctx, entries(t, c.p))
	if c.stops.Load() != 1 || c.starts.Load() != 1 {
		t.Errorf("stops %d, starts %d, want one each", c.stops.Load(), c.starts.Load())
	}
	h := c.p.CheckHost()
	if h.Missing != hostADBNoNetwork || !strings.Contains(h.Detail, "after Pinky restarted it") {
		t.Errorf("CheckHost = %+v", h)
	}
}

// TestReconnectOthersServerCutOff: a server another program started is
// left running; CheckHost says what to do, the phone is tried again at the
// next pass rather than held back, and once a server reaches it the
// report goes.
func TestReconnectOthersServerCutOff(t *testing.T) {
	c := newCutOffFake(t, false)
	ctx := context.Background()
	if _, connected := c.p.wifi.reconnect(ctx, entries(t, c.p)); connected {
		t.Fatal("connected through a server that cannot reach the network")
	}
	if c.stops.Load() != 1 || c.starts.Load() != 0 {
		t.Errorf("stops %d, starts %d", c.stops.Load(), c.starts.Load())
	}
	h := c.p.CheckHost()
	if h.Missing != hostADBNoNetwork || !strings.Contains(h.Detail, "Pinky did not start this one") || !strings.Contains(h.Detail, "192.168.1.42:41000") {
		t.Errorf("CheckHost = %+v", h)
	}
	if c.p.wifi.holdBack(guidPixel, "192.168.1.42:41000") {
		t.Error("the phone is held back for the server's failure")
	}
	// The next pass tries the phone, and asks about the server no more.
	c.p.wifi.reconnect(ctx, entries(t, c.p))
	if _, connects := c.f.counts(); len(connects) != 2 || c.stops.Load() != 1 {
		t.Errorf("second pass: connects %q, stops %d", connects, c.stops.Load())
	}
	// The person stopped that server, and the one that came reaches it.
	c.f.set(func(f *fakeADB) { f.noRoute = false })
	if _, connected := c.p.wifi.reconnect(ctx, entries(t, c.p)); !connected {
		t.Error("not connected once the server reaches the phone")
	}
	if h := c.p.CheckHost(); h.Missing != "" {
		t.Errorf("CheckHost after the phone connected = %+v", h)
	}
}

// TestReconnectNoRouteForPinkyToo: no route for the server and for Pinky
// is the network's, not the server's: the phone is held back as before,
// and the server is left alone.
func TestReconnectNoRouteForPinkyToo(t *testing.T) {
	c := newCutOffFake(t, true)
	c.p.dial = func(ctx context.Context, addr string) error { return errors.New("connect: no route to host") }
	c.p.wifi.reconnect(context.Background(), entries(t, c.p))
	if c.stops.Load() != 0 || c.starts.Load() != 0 {
		t.Errorf("stops %d, starts %d", c.stops.Load(), c.starts.Load())
	}
	if !c.p.wifi.holdBack(guidPixel, "192.168.1.42:41000") {
		t.Error("the phone is not held back")
	}
	if h := c.p.CheckHost(); h.Missing != "" {
		t.Errorf("CheckHost = %+v", h)
	}
}

// TestPairServerCutOff: pairing through a server that cannot reach the
// network fails with "Unable to start pairing client" while Pinky reaches
// the address: Pinky's own server is restarted and pairs, and another
// program's gives driver.ErrPairNoNetwork, not "nothing at that address".
func TestPairServerCutOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, own := range []bool{true, false} {
		c := newCutOffFake(t, own)
		c.f.set(func(f *fakeADB) {
			f.code = "482913"
			f.guid = guidPixel
			f.autoSerial = guidPixel + connectSuffix
		})
		_, err := c.p.PairWithCode(ctx, "192.168.1.42:37123", "482913")
		switch {
		case own && err != nil:
			t.Errorf("own server: %v", err)
		case !own && (!errors.Is(err, driver.ErrPairNoNetwork) || errors.Is(err, driver.ErrPairNotListening)):
			t.Errorf("another's server: %v", err)
		}
		c.f.mu.Lock()
		pairs := c.f.pairs
		c.f.mu.Unlock()
		if want := map[bool]int{true: 2, false: 1}[own]; pairs != want {
			t.Errorf("own %v: %d pairings, want %d", own, pairs, want)
		}
	}
}

// TestPairRefusedNotCutOff: "Unable to start pairing client" with nothing
// listening at the address for Pinky either is the phone's, as before.
func TestPairRefusedNotCutOff(t *testing.T) {
	c := newCutOffFake(t, true)
	c.p.dial = func(ctx context.Context, addr string) error { return errors.New("connect: connection refused") }
	c.f.set(func(f *fakeADB) { f.code = "482913" })
	_, err := c.p.PairWithCode(context.Background(), "192.168.1.42:37123", "482913")
	if !errors.Is(err, driver.ErrPairNotListening) || c.stops.Load() != 0 {
		t.Errorf("PairWithCode = %v, stops %d", err, c.stops.Load())
	}
}

// TestReconnectFailsAfterRestart: a phone that the new server could not
// connect right after the restart is not held back for it: the next pass
// tries again.
func TestReconnectFailsAfterRestart(t *testing.T) {
	c := newCutOffFake(t, true)
	c.p.cfg.StopServer = func(ctx context.Context) error {
		c.stops.Add(1)
		c.f.set(func(f *fakeADB) { f.noRoute, f.connectOK = false, map[string]bool{} })
		return nil
	}
	ctx := context.Background()
	if _, connected := c.p.wifi.reconnect(ctx, entries(t, c.p)); connected {
		t.Fatal("connected")
	}
	if c.p.wifi.holdBack(guidPixel, "192.168.1.42:41000") {
		t.Error("the phone is held back for the new server's first failure")
	}
	c.f.set(func(f *fakeADB) { f.connectOK = nil })
	if _, connected := c.p.wifi.reconnect(ctx, entries(t, c.p)); !connected {
		t.Error("not connected at the next pass")
	}
	if c.stops.Load() != 1 {
		t.Errorf("stops %d", c.stops.Load())
	}
}

// TestConnectAddrServerCutOff: connecting at an address (Add phone's
// connect step, and the reconnects of phones on adb tcpip's port or
// Tailscale, netLink) through a server cut off from the network restarts
// Pinky's own and connects, and with another program's fails with
// driver.ErrPairNoNetwork.
func TestConnectAddrServerCutOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, own := range []bool{true, false} {
		c := newCutOffFake(t, own)
		_, err := c.p.ConnectWiFi(ctx, "192.168.1.42:41000")
		if own != (err == nil) || (!own && !errors.Is(err, driver.ErrPairNoNetwork)) {
			t.Errorf("own %v: ConnectWiFi = %v", own, err)
		}
		c2 := newCutOffFake(t, own)
		c2.p.link.probe = func(context.Context, string) error { return nil } // something listens there
		err = c2.p.link.connect(ctx, "192.168.1.42:5555")
		if own != (err == nil) || (!own && !errors.Is(err, driver.ErrPairNoNetwork)) {
			t.Errorf("own %v: netLink.connect = %v", own, err)
		}
	}
}

// TestNoHoldBackDuringRestart: a phone whose connection fails while Pinky
// restarts the server (it went away, or answers again only just), as when
// netLink or a pairing set the restart off, is not taken for one that
// crashed the server: it is tried again at the next pass.
func TestNoHoldBackDuringRestart(t *testing.T) {
	c := newCutOffFake(t, true)
	c.f.set(func(f *fakeADB) { f.noRoute, f.quitOnConnect = false, true })
	if c.p.server.tryRestart(time.Now()) != restartNow {
		t.Fatal("no restart")
	}
	c.p.wifi.reconnect(context.Background(), entries(t, c.p))
	if c.p.wifi.holdBack(guidPixel, "192.168.1.42:41000") {
		t.Error("the phone is held back while Pinky restarts the server")
	}
	c.p.server.restarted(time.Now(), true, true, nil)
	if !c.p.server.justRestarted(time.Now()) || c.p.server.justRestarted(time.Now().Add(restartGrace)) {
		t.Error("restartGrace")
	}
}

// TestCutOffOnlyLocalMac: a server on another computer, or a computer
// other than a Mac, is never taken for one cut off from the network, and a
// server on another computer is never Pinky's to stop.
func TestCutOffOnlyLocalMac(t *testing.T) {
	c := newCutOffFake(t, true)
	ctx := context.Background()
	noRoute := errors.New("adb: connect 192.168.1.42:41000: failed to connect to '192.168.1.42:41000': No route to host")
	if !c.p.serverCutOff(ctx, "192.168.1.42:41000", noRoute) {
		t.Fatal("not cut off")
	}
	local := c.p.cfg.Client
	c.p.cfg.Client = &adb.Client{Addr: "10.0.0.2:5037"}
	if c.p.serverCutOff(ctx, "192.168.1.42:41000", noRoute) {
		t.Error("a server on another computer is taken for one cut off")
	}
	if _, ok := c.p.cfg.Client.OwnServer(); ok {
		t.Error("a server on another computer is Pinky's")
	}
	c.p.cfg.Client = local
	cutOffChecks = false
	if c.p.serverCutOff(ctx, "192.168.1.42:41000", noRoute) {
		t.Error("cut off with cutOffChecks off")
	}
	if c.dials.Load() != 1 {
		t.Errorf("Pinky dialed %d times, want once", c.dials.Load())
	}
}

// TestPairWaitsForRestart: a pairing that finds the server cut off while a
// restart is under way (the reconnector set it off) waits for it and pairs
// through the new server, rather than telling the person to stop it.
func TestPairWaitsForRestart(t *testing.T) {
	c := newCutOffFake(t, true)
	c.f.set(func(f *fakeADB) {
		f.code = "482913"
		f.guid = guidPixel
		f.autoSerial = guidPixel + connectSuffix
	})
	entered, release := make(chan struct{}), make(chan struct{})
	c.p.cfg.StopServer = func(ctx context.Context) error {
		c.stops.Add(1)
		close(entered)
		<-release
		c.f.set(func(f *fakeADB) { f.noRoute = false })
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { c.p.wifi.reconnect(ctx, entries(t, c.p)); close(done) }()
	<-entered
	go func() { time.Sleep(200 * time.Millisecond); close(release) }()
	if _, err := c.p.PairWithCode(ctx, "192.168.1.42:37123", "482913"); err != nil {
		t.Errorf("PairWithCode during a restart: %v", err)
	}
	<-done
	if c.stops.Load() != 1 {
		t.Errorf("stops %d", c.stops.Load())
	}
}

// TestConnectWiFiAfterRestart: Add phone's connect step forgives the new
// server's first failed handshake, and reports one that still cannot reach
// the network after the restart.
func TestConnectWiFiAfterRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := newCutOffFake(t, true)
	c.p.cfg.StopServer = func(ctx context.Context) error {
		c.f.set(func(f *fakeADB) { f.noRoute, f.failConnects = false, 1 })
		return nil
	}
	if _, err := c.p.ConnectWiFi(ctx, "192.168.1.42:41000"); err != nil {
		t.Errorf("ConnectWiFi after the first handshake failed: %v", err)
	}
	if _, connects := c.f.counts(); len(connects) != 3 {
		t.Errorf("connects %q, want the cut-off one, the failed one and the one that worked", connects)
	}

	c = newCutOffFake(t, true)
	c.p.cfg.StopServer = func(ctx context.Context) error { return nil } // still cut off
	if _, err := c.p.ConnectWiFi(ctx, "192.168.1.42:41000"); !errors.Is(err, driver.ErrPairNoNetwork) {
		t.Errorf("ConnectWiFi through a new server still cut off: %v", err)
	}
	if h := c.p.CheckHost(); h.Missing != hostADBNoNetwork || !strings.Contains(h.Detail, "after Pinky restarted it") {
		t.Errorf("CheckHost = %+v", h)
	}
}
