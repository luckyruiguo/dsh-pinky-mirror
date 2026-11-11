package android

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// When List starts an adb server (Config.StartServer).
const (
	// serverAway is how long a server that ran may stay away before List
	// starts one: an adb client that finds a server of another version
	// kills it and starts its own at once, and a server Android Studio
	// restarts comes back within a second or two.
	serverAway = 3 * time.Second
	// serverRetry is the wait after a start failed, doubling up to
	// serverRetryMax.
	serverRetry    = 5 * time.Second
	serverRetryMax = 2 * time.Minute

	// A server that went away restartsMin times within restartWindow is
	// being replaced by another program (CheckHost's adb_restarts).
	restartWindow = 10 * time.Minute
	restartsMin   = 3
)

// What CheckHost finds missing (driver.HostCheck.Missing).
const (
	// hostNoADB: no adb to start a server with, in a build of Pinky
	// without its own on a computer without one.
	hostNoADB = "adb"
	// hostADBServer: no adb server runs, and starting one failed; Detail
	// says why.
	hostADBServer = "adb_server"
	// hostADBOld: the server that runs is older than protocol 41
	// (platform-tools before 29, 2019), which lacks pairing over Wi-Fi and
	// more.
	hostADBOld = "adb_old"
	// hostADBRestarts: the server keeps going away, as when another
	// program restarts it with an adb of another version.
	hostADBRestarts = "adb_restarts"
	// hostADBNoNetwork: the server cannot reach phones on the local
	// network while Pinky can, and Pinky could not restart it: another
	// program started it, or one Pinky started again cannot either
	// (offnet.go).
	hostADBNoNetwork = "adb_no_network"
)

// serverWatch decides when List starts an adb server, and keeps what
// CheckHost reports from the Lists so far.
type serverWatch struct {
	mu        sync.Mutex
	seen      bool      // a server answered once
	downSince time.Time // when List found none, zero while one answers
	starting  bool      // a start is under way
	next      time.Time // no start before then
	wait      time.Duration

	lastErr error       // why the last start failed; nil once a server answers
	version int         // the protocol version of the server that answers, 0 when unknown
	asked   bool        // its version was asked for since it came
	aways   []time.Time // when a server that ran went away, within restartWindow

	stopping    bool      // Pinky stops the server it started, to start another (offnet.go)
	restartedAt time.Time // when it last did; once it has, it never does again in this run
	triedAt     time.Time // when Pinky last dealt with a server cut off from the network
	offNet      string    // an address the server that answers could not reach while Pinky could, "" for none
	offNetOwn   bool      // that server is one Pinky started (again)
	lastOwn     bool      // the server Pinky dealt with at triedAt was its own
}

// claim records that List found no server, and reports whether List
// should start one now; the caller then reports the outcome with done.
func (w *serverWatch) claim(now time.Time, logf func(string, ...any)) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.downSince.IsZero() {
		w.downSince = now
		w.version, w.asked = 0, false
		w.offNet, w.offNetOwn = "", false
		if w.seen && !w.stopping {
			w.aways = append(recent(w.aways, now), now)
			logf("android: the adb server went away; Pinky starts one if none is back within %v", serverAway)
		}
	}
	if w.starting || now.Before(w.next) || (w.seen && now.Sub(w.downSince) < serverAway) {
		return false
	}
	w.starting = true
	return true
}

// recent keeps the times within restartWindow of now.
func recent(ts []time.Time, now time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < restartWindow {
			out = append(out, t)
		}
	}
	return out
}

// done records how a start that claim allowed went.
func (w *serverWatch) done(now time.Time, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.starting = false
	w.lastErr = err
	if err == nil {
		w.wait = 0
		w.next = time.Time{}
		return
	}
	w.wait = min(max(2*w.wait, serverRetry), serverRetryMax)
	w.next = now.Add(w.wait)
}

// noADB reports whether the last start found no adb at all.
func (w *serverWatch) noADB() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.downSince.IsZero() && errors.Is(w.lastErr, adb.ErrNoADB)
}

// answered records a server that answers, and reports whether its version
// is still to be asked for (once after it came).
func (w *serverWatch) answered() (askVersion bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen = true
	w.downSince = time.Time{}
	w.lastErr = nil
	askVersion = !w.asked
	w.asked = true
	return askVersion
}

// setVersion records the protocol version of the server that answers.
func (w *serverWatch) setVersion(v int) {
	w.mu.Lock()
	w.version = v
	w.mu.Unlock()
}

// check is CheckHost's answer, from what the Lists found.
func (w *serverWatch) check(now time.Time) driver.HostCheck {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case !w.downSince.IsZero() && errors.Is(w.lastErr, adb.ErrNoADB):
		return driver.HostCheck{Missing: hostNoADB,
			Detail: "Pinky found no adb to reach Android phones with: this build of Pinky does not carry its own. Install Android's platform tools, or set " + adb.EnvADB + " to an adb."}
	case !w.downSince.IsZero() && w.lastErr != nil:
		return driver.HostCheck{Missing: hostADBServer,
			Detail: "Android's adb server is not running, and Pinky could not start one: " + w.lastErr.Error()}
	case w.downSince.IsZero() && w.version > 0 && w.version < 41:
		return driver.HostCheck{Missing: hostADBOld,
			Detail: fmt.Sprintf("The adb server that runs is an old one (protocol %d, from Android platform tools older than 29), which cannot pair phones over Wi-Fi. Quit the program that started it, or stop it with `adb kill-server`: Pinky then starts its own.", w.version)}
	case w.downSince.IsZero() && w.offNet != "" && w.offNetOwn:
		return driver.HostCheck{Missing: hostADBNoNetwork,
			Detail: "Android's adb server cannot reach phones on the local network (it got no route to " + w.offNet + ", which Pinky reaches), also after Pinky restarted it. On a Mac, check that Pinky is on in System Settings › Privacy & Security › Local Network."}
	case w.downSince.IsZero() && w.offNet != "":
		return driver.HostCheck{Missing: hostADBNoNetwork,
			Detail: "Android's adb server cannot reach phones on the local network: it got no route to " + w.offNet + ", which Pinky reaches. On a Mac, a server loses Local Network access when the app that started it quits or is updated. Pinky did not start this one, so it leaves it running: quit the program that started it, or stop it with `adb kill-server`, and Pinky starts its own."}
	}
	w.aways = recent(w.aways, now)
	if n := len(w.aways); n >= restartsMin {
		return driver.HostCheck{Missing: hostADBRestarts,
			Detail: fmt.Sprintf("Android's adb server stopped %d times in the last %v, and its phones disconnected each time: another program restarts it, most often with an adb of another version (an old one on PATH, or one that comes with another tool). Keep one adb, or quit that program.", n, restartWindow)}
	}
	return driver.HostCheck{}
}

// What tryRestart decides.
const (
	restartNow      = iota // stop the server and start another
	restartUnderway        // a start or restart is under way: wait for it (waitRestart)
	restartNo              // not now, or not again in this run
)

// tryRestart decides what to do about a server cut off from the network
// (offnet.go). Pinky restarts a server once per run: one it started again
// that cannot reach the network either will not after another restart,
// and every restart drops every connection on the server, so CheckHost
// reports it instead. A server Pinky could not restart (another
// program's) is looked at again after offNetRetry. On restartNow it holds
// off List's starts until restarted, and the server's going away does not
// count as a restart by another program.
func (w *serverWatch) tryRestart(now time.Time) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.starting:
		return restartUnderway
	case !w.restartedAt.IsZero():
		return restartNo
	case !w.triedAt.IsZero() && now.Sub(w.triedAt) < offNetRetry:
		return restartNo
	}
	w.starting, w.stopping, w.triedAt = true, true, now
	return restartNow
}

// waitRestart waits for the start or restart under way to end, and
// reports whether a server answers then, as far as Pinky knows.
func (w *serverWatch) waitRestart(ctx context.Context) bool {
	for {
		w.mu.Lock()
		starting, err := w.starting, w.lastErr
		w.mu.Unlock()
		if !starting {
			return err == nil
		}
		if sleepCtx(ctx, 100*time.Millisecond) != nil {
			return false
		}
	}
}

// justRestarted reports whether Pinky is restarting the server, or did
// within restartGrace: a phone that failed to connect then is not to
// blame.
func (w *serverWatch) justRestarted(now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopping || (!w.restartedAt.IsZero() && now.Sub(w.restartedAt) < restartGrace)
}

// restarted records how a restart that tryRestart allowed went: own
// reports whether the server was Pinky's, stopped whether it went, err how
// starting another one did.
func (w *serverWatch) restarted(now time.Time, own, stopped bool, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.starting, w.stopping = false, false
	w.lastOwn = own
	if own {
		w.restartedAt = now // once Pinky signalled its server, never again in this run
	}
	if !stopped {
		return
	}
	w.offNet, w.offNetOwn = "", false
	w.lastErr = err
	if err != nil {
		w.wait = serverRetry
		w.next = now.Add(w.wait)
	} else {
		w.wait = 0
		w.next = time.Time{}
	}
}

// cutOff records that the server that answers could not reach addr while
// Pinky could, for CheckHost: one Pinky started again when the last one
// it dealt with was its own. Nothing while a restart is under way.
func (w *serverWatch) cutOff(addr string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopping {
		return
	}
	w.offNet, w.offNetOwn = addr, w.lastOwn
}

// reached records that the server reached a phone on the network.
func (w *serverWatch) reached() {
	w.mu.Lock()
	w.offNet, w.offNetOwn = "", false
	w.mu.Unlock()
}
