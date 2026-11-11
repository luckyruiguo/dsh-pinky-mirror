package android

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// An adb server cut off from the local network. On macOS (15 and later) a
// process may use the local network only while the app responsible for it
// may (Privacy & Security › Local Network), and the adb server Pinky
// starts counts as Pinky. It keeps the Pinky that started it as its app
// after that one quit, and once an update replaced it, the server lost the
// network: on 2026-11-08 a server that Pinky 2026.1109.8 had started got
// "No route to host" at once for every phone on the network after Pinky
// updated itself to 2026.1109.10, while Pinky, a shell and a server
// started anew reached them. Its mDNS discovery listed nothing, pairing
// failed with "Unable to start pairing client", and the phones on Wi-Fi
// never came back; the ones on a cable worked.
//
// So when the server gets no route to an address that Pinky's own process
// reaches, the driver stops the server, if Pinky started it (in this run
// or an earlier one: adb.Client.OwnServer), and starts another one, which
// counts as the Pinky that runs. A server another program started it
// leaves running, as always, and CheckHost says what to do
// (adb_no_network).

// cutOffChecks: only macOS keeps a process off the local network
// (elsewhere "No route to host" is what a phone that left gives), so only
// there does the driver look for a server cut off from it. Tests turn it
// on.
var cutOffChecks = runtime.GOOS == "darwin"

const (
	// offNetRetry is how long Pinky leaves a server cut off from the
	// network that it could not restart (another program's) before it
	// looks again. One it restarted it does not restart again in this run
	// (serverWatch.tryRestart).
	offNetRetry = 10 * time.Minute
	// restartGrace is how long after a restart a failed connection is not
	// held against the phone (reconnector.refuse).
	restartGrace = 30 * time.Second
	// After a restart, the request that found the server cut off is made
	// again up to restartTries times, restartTryWait apart: a new server's
	// first TLS handshake with a phone failed once (platform-tools 37.0.1,
	// "Handshake failed in SSL_accept/SSL_connect"), and the server
	// connected it by itself six seconds later.
	restartTries = 3
)

// restartTryWait is the wait between those tries; tests shorten it.
var restartTryWait = 3 * time.Second

// dialWait is how long Pinky's own connection to an address may take.
const dialWait = 3 * time.Second

// dialOwn connects to addr from Pinky's own process, and closes the
// connection at once.
func dialOwn(ctx context.Context, addr string) error {
	d := net.Dialer{Timeout: dialWait}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}

// serverCutOff reports whether the adb server failed to reach addr because
// it cannot use the local network: on macOS, with a server on this
// computer, its words say that there was no route to addr (connecting) or
// that it could not start its pairing client (pairing; adb says that for
// no route and for a refused connection alike), while Pinky's own
// connection to addr goes through. A server on another computer
// (ANDROID_ADB_SERVER_ADDRESS, ADB_SERVER_SOCKET) reaches another network,
// which Pinky's connection tells nothing about.
func (p *Provider) serverCutOff(ctx context.Context, addr string, err error) bool {
	if !cutOffChecks || err == nil || addr == "" || !p.cfg.Client.Local() {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "no route to host") && !strings.Contains(msg, "unable to start pairing client") {
		return false
	}
	return p.dial(ctx, addr) == nil
}

// serverOffNetwork deals with an adb server that could not reach addr
// while Pinky could (serverCutOff): it stops one Pinky started and starts
// another (Config.StopServer, Config.StartServer), once per run, and
// leaves any other running for CheckHost to report; while a start or
// restart is under way, it waits for that one. It reports whether a new
// server runs.
func (p *Provider) serverOffNetwork(ctx context.Context, addr string) bool {
	switch p.server.tryRestart(time.Now()) {
	case restartUnderway:
		return p.server.waitRestart(ctx)
	case restartNo:
		if p.server.justRestarted(time.Now()) {
			return true // another request restarted it just now: ask the new one
		}
		p.server.cutOff(addr)
		return false
	}
	if p.cfg.StartServer == nil {
		p.server.restarted(time.Now(), false, false, nil)
		p.server.cutOff(addr)
		return false
	}
	err := p.cfg.StopServer(ctx)
	if err != nil {
		own := !errors.Is(err, adb.ErrNotOwnServer)
		p.server.restarted(time.Now(), own, false, nil)
		p.server.cutOff(addr)
		if own {
			p.cfg.infof("android: the adb server Pinky started cannot reach %s on the local network, which Pinky reaches, and stopping it failed: %v", addr, err)
		} else {
			p.cfg.infof("android: the adb server cannot reach %s on the local network, which Pinky reaches; another program started it, so Pinky leaves it running", addr)
		}
		return false
	}
	p.cfg.infof("android: the adb server Pinky started could not reach %s on the local network, which Pinky reaches (it lost Local Network access); Pinky stopped it (SIGTERM) and starts another", addr)
	serr := p.cfg.StartServer(ctx)
	p.server.restarted(time.Now(), true, true, serr)
	if serr != nil {
		p.cfg.infof("android: starting an adb server after stopping the one cut off from the network: %v", serr)
		return false
	}
	return true
}

// afterRestart makes the request that found the server cut off (try) again
// through the server that came after the restart, up to restartTries
// times: again for any failure when retry says so, and for a server that
// still cannot reach addr. One that still cannot is recorded for CheckHost
// and gives an error wrapping driver.ErrPairNoNetwork.
func (p *Provider) afterRestart(ctx context.Context, addr string, try func() error, retry func(error) bool) error {
	var err error
	cut := false
	for i := range restartTries {
		if i > 0 && sleepCtx(ctx, restartTryWait) != nil {
			break
		}
		if err = try(); err == nil {
			p.server.reached()
			return nil
		}
		if cut = p.serverCutOff(ctx, addr, err); !cut && !retry(err) {
			return err
		}
	}
	if cut {
		p.server.cutOff(addr)
		p.cfg.infof("android: the adb server cannot reach %s on the local network either after Pinky restarted it", addr)
		return noNetwork(addr, err)
	}
	return err
}

// noNetwork is the error for a request to addr that a server cut off from
// the network could not make.
func noNetwork(addr string, err error) error {
	return fmt.Errorf("android: reaching %s failed: %w (%w)", addr, driver.ErrPairNoNetwork, err)
}

// connectAddr has the adb server connect at addr, as Client.Connect. A
// server that cannot reach the network while Pinky can is restarted when
// Pinky started it, and asked again (afterRestart); otherwise the error
// wraps driver.ErrPairNoNetwork.
func (p *Provider) connectAddr(ctx context.Context, addr string) error {
	connect := func() error { return p.cfg.Client.Connect(ctx, addr) }
	err := connect()
	if err == nil {
		p.server.reached()
		return nil
	}
	if !p.serverCutOff(ctx, addr, err) {
		return err
	}
	if !p.serverOffNetwork(ctx, addr) {
		return noNetwork(addr, err)
	}
	return p.afterRestart(ctx, addr, connect, func(error) bool { return true })
}
