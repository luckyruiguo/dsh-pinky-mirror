package ios

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// An app on the phone that uses the camera or microphone (a call or
// FaceTime, Camera, Voice Memos, a screen recording with the microphone,
// dictation, a Mac using it as Continuity Camera) keeps iOS from mirroring
// the screen: the display service refuses the stream with errMediaInUse
// until the app lets go. Open keeps such a phone's session then, its tunnel
// without a stream (a mediaWait), and starts the stream again on it every
// mediaRetry, which costs two connections inside the tunnel and nothing
// more. Meanwhile List says the phone needs the SetupMediaInUse step, so
// the gateway shows the person what to do and does not open the phone again
// and again. Once a start succeeds, List says online, and the next Open
// takes the session over, its stream running.

// mediaRetry is how often a mediaWait starts the stream again.
const mediaRetry = 2 * time.Second

// mediaTake is how long a mediaWait whose stream started keeps it for the
// next Open; then it closes the session (the gateway did not open the phone:
// the person paused or removed it meanwhile).
const mediaTake = 30 * time.Second

// mediaInUseDetail says what the phone waits for, in English for agents
// and the log; the client says it in the person's language.
const mediaInUseDetail = "An app on the iPhone is using the camera or microphone (a call or FaceTime, Camera, Voice Memos, " +
	"a screen recording with the microphone, dictation, or a Mac using it as Continuity Camera), and iOS does not allow " +
	"mirroring the screen then. Ask the person to end the call or close that app: the green or orange dot at the top " +
	"of the screen shows it is in use, and Control Center shows which app. Pinky connects by itself once the camera " +
	"and microphone are free."

// mediaInUse reports whether err is the phone refusing the stream because
// an app uses the camera or microphone.
func mediaInUse(err error) bool {
	var ce *CoreDeviceError
	return errors.As(err, &ce) && ce.Code == errMediaInUse
}

// coreSession is a CoreDevice session from its tunnel on: *Device, or a
// fake in tests.
type coreSession interface {
	// start starts the first stream.
	start(ctx context.Context) error
	// retryMedia starts the stream again after the phone refused it.
	retryMedia(ctx context.Context) error
	// opened starts what runs beside the stream and returns the device.
	opened() driver.Device
	Close() error
}

// The states of a mediaWait.
const (
	mediaWaiting = iota // the phone refuses the stream
	mediaStarted        // the stream runs, for the next Open to take over
	mediaTaken          // Open took the session over
	mediaEnded          // stopped, not taken in time, or the stream failed otherwise
)

// mediaWait is a session whose stream the phone refused because an app
// uses the camera or microphone, and the loop that starts it again.
type mediaWait struct {
	key   entryKey // the attach it was opened over
	s     coreSession
	retry time.Duration
	take  time.Duration
	log   *slog.Logger
	id    string

	mu    sync.Mutex
	state int
	end   chan struct{} // closed when the state leaves waiting and started
	done  chan struct{} // closed when the loop returns
}

func newMediaWait(id string, key entryKey, s coreSession, retry time.Duration, log *slog.Logger) *mediaWait {
	if retry <= 0 {
		retry = mediaRetry
	}
	return &mediaWait{key: key, s: s, retry: retry, take: mediaTake, log: logTo(log), id: id,
		end: make(chan struct{}), done: make(chan struct{})}
}

// current is the wait's state.
func (w *mediaWait) current() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

// finish moves a wait that is waiting or started to state, and reports
// whether it did; the caller then closes the session, unless Open took it.
func (w *mediaWait) finish(state int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != mediaWaiting && w.state != mediaStarted {
		return false
	}
	w.state = state
	close(w.end)
	return true
}

// run starts the stream again every retry until it starts, then keeps the
// session for take.
func (w *mediaWait) run() {
	defer close(w.done)
	t := time.NewTimer(w.retry)
	defer t.Stop()
	for {
		select {
		case <-w.end:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), restartTimeout)
		err := w.s.retryMedia(ctx)
		cancel()
		if mediaInUse(err) {
			t.Reset(w.retry)
			continue
		}
		if err != nil {
			if w.finish(mediaEnded) {
				w.log.Warn("iphone: the stream did not start after the camera or microphone was in use", "device", w.id, "err", err)
				w.s.Close()
			}
			return
		}
		w.mu.Lock()
		started := w.state == mediaWaiting
		if started {
			w.state = mediaStarted
		}
		w.mu.Unlock()
		if !started {
			return // stopped while it started; stop closes the session
		}
		w.log.Info("iphone: the camera and microphone are free again, the stream started", "device", w.id)
		break
	}
	t.Reset(w.take)
	select {
	case <-w.end:
	case <-t.C:
		if w.finish(mediaEnded) {
			w.log.Info("iphone: closing a session nobody opened after the camera and microphone were free", "device", w.id)
			w.s.Close()
		}
	}
}

// stop ends the wait and closes its session, unless Open took it.
func (w *mediaWait) stop() {
	if w.finish(mediaEnded) {
		w.s.Close()
	}
}

// takeOver hands Open the session whose stream started, and reports
// whether it did.
func (w *mediaWait) takeOver() (coreSession, bool) {
	w.mu.Lock()
	ok := w.state == mediaStarted
	w.mu.Unlock()
	if !ok || !w.finish(mediaTaken) {
		return nil, false
	}
	return w.s, true
}

// mediaState is how a phone is while its session waits for the camera and
// microphone: needing the SetupMediaInUse step, so the gateway shows it and
// does not open the phone. Once the stream started, the phone is as List
// found it, online, for the next Open to take the session over. A wait
// whose phone reconnected, restarted or went another way, or that ended,
// is dropped.
func (p *Provider) mediaState(key entryKey, id string, info driver.Info) driver.Info {
	p.mu.Lock()
	w := p.mediaWaits[id]
	if w == nil {
		p.mu.Unlock()
		return info
	}
	state := w.current()
	drop := w.key != key || info.State != driver.StateOnline || info.Backend != backendCoreDevice ||
		state == mediaEnded || state == mediaTaken
	if drop {
		delete(p.mediaWaits, id)
	}
	p.mu.Unlock()
	if drop {
		go w.stop()
		return info
	}
	if state == mediaWaiting {
		info.State, info.Setup, info.Detail = driver.StateNeedsSetup, driver.SetupMediaInUse, mediaInUseDetail
	}
	return info
}

// openCoreDevice opens a phone over CoreDevice. It takes over the session
// of a mediaWait whose stream started; when the phone refuses the stream
// because an app uses the camera or microphone, it keeps the session for
// mediaState and returns the SetupMediaInUse step.
func (p *Provider) openCoreDevice(ctx context.Context, info driver.Info) (driver.Device, error) {
	p.mu.Lock()
	w := p.mediaWaits[info.ID]
	if w != nil {
		delete(p.mediaWaits, info.ID)
	}
	p.mu.Unlock()
	if w != nil {
		if s, ok := w.takeOver(); ok {
			p.logger().Info("iphone: opening the session that waited for the camera and microphone", "device", info.ID)
			return s.opened(), nil
		}
		// Still waiting (Open came before List said so) or ended: start
		// afresh.
		w.stop()
	}
	dial := p.dialFn
	if dial == nil {
		dial = func(ctx context.Context, info driver.Info) (coreSession, error) {
			return dialDevice(ctx, info, p.logger())
		}
	}
	s, err := dial(ctx, info)
	if err != nil {
		return nil, err
	}
	err = s.start(ctx)
	if err == nil {
		return s.opened(), nil
	}
	if !mediaInUse(err) {
		s.Close()
		return nil, err
	}
	w = newMediaWait(info.ID, p.mediaKey(info.ID), s, p.mediaEvery, p.logger())
	p.mu.Lock()
	closed := p.closed
	if !closed {
		if old := p.mediaWaits[info.ID]; old != nil {
			go old.stop()
		}
		if p.mediaWaits == nil {
			p.mediaWaits = map[string]*mediaWait{}
		}
		p.mediaWaits[info.ID] = w
	}
	p.mu.Unlock()
	if closed {
		s.Close()
		return nil, err
	}
	p.logger().Info("iphone: an app uses the camera or microphone; trying the stream again until it is free", "device", info.ID, "every", w.retry)
	go w.run()
	// Until the next List says so (mediaState), the error tells.
	return nil, &driver.SetupError{Setup: driver.SetupMediaInUse, Detail: mediaInUseDetail, Err: err}
}

// mediaKey is the attach List last saw the phone through, which a
// mediaWait belongs to.
func (p *Provider) mediaKey(id string) entryKey {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.known[id].key
}

// stopMediaWaitLocked ends the phone's mediaWait, if any. The caller holds p.mu;
// the session closes in the background.
func (p *Provider) stopMediaWaitLocked(id string) {
	if w := p.mediaWaits[id]; w != nil {
		delete(p.mediaWaits, id)
		go w.stop()
	}
}
