package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/history"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/recording"
	"github.com/DIYgod/pinky/internal/screen"
)

// Screen recordings: one MP4 per task, remuxed without re-encoding from
// the stream of the screen the agent uses (the phone's, or its background
// display), shared with the mirror through the device's hub.
//
// Nothing of a blocked app, a locked screen or the person's own use of the
// phone may be stored, and the gateway knows what is on the screen only
// from reads. So the recorder holds packets back until a read vouches for
// them: the packets that arrived before a read started are written when
// that read shows a recordable screen (unlocked, an app that is not
// blocked, the person not in control), and dropped, with everything held
// since the last read that did, when it does not. Until a read shows a
// recordable screen again, packets are dropped, and writing resumes at a
// key frame. Taking over, a lock reported by the driver, an unlock from
// the computer and the phone going offline drop what is held too, and so
// does a new stream. Packets that arrived before the task started are
// never written, and nor are packets that arrived after it ended, however
// late its last read comes. When packets have waited for videoConfirm and
// no action is running, the recorder reads the screen itself, so it never
// holds much; when the task ends, it reads once more, except after Stop,
// which leaves the phone alone.
//
// These decisions compare times: when packets arrived, when reads started,
// when the task started and ended, when what was held was dropped. They
// all come from stamp (clock.go), so no two are equal and the order of
// any two is the order of the events.
//
// A blocked app that comes and goes between two reads, neither of which
// sees it, is not caught. Payment hand-offs stay in the bank's app until
// the person is done, so the next read sees those.

// Recorder limits.
const (
	videoConfirm  = 10 * time.Second // held this long, the recorder reads the screen
	maxHeldBytes  = 48 << 20         // held beyond this, packets are dropped
	videoRetry    = 2 * time.Second  // between attempts to open the stream, at first
	videoRetryMax = 30 * time.Second // and at most, while it keeps failing
	videoStopRead = 15 * time.Second // the last read, when a task ends
)

// vpacket is a packet on its way to the file. A key frame carries the
// configuration it was encoded with, written before it when it starts the
// file again after a gap.
type vpacket struct {
	p       driver.Packet
	arrived time.Time // when the hub got it, which reads are compared with
	at      time.Time // where it goes, on the wall clock
	cfg     *driver.Packet
}

// taskVideo records one task.
type taskVideo struct {
	g *Gateway
	d *device
	t *task

	ctx    context.Context
	cancel context.CancelFunc
	ran    chan struct{} // closed when run returns
	wrote  chan struct{} // closed when writeLoop returns

	mu    sync.Mutex
	cond  *sync.Cond    // ready or stopping changed
	key   string        // the hub the recording taps
	tap   *streamTap    // the current tap, nil between streams
	moved chan struct{} // closed when the recording moves to another screen
	// floor: packets that arrived before it are never written (the task's
	// start, then the last drop: a member that joins a hub again gets old
	// packets first). endAt: the task ended; later packets are not taken.
	floor    time.Time
	endAt    time.Time
	held     []vpacket // waiting for a read
	heldSize int
	paused   bool // dropping until a recordable read
	needKey  bool // the next picture moved to ready must be a key frame
	cfg      *driver.Packet
	ready    []vpacket
	queued   int // pictures a read moved to ready that the write loop has not written yet
	reading  bool
	stopping bool
	reads    map[uint64]time.Time // reads in progress, by token, and when they started
	readNext uint64
	newest   time.Time // the start of the newest read that has decided
	codec    driver.Codec
	base     time.Time // arrival minus PTS of the fastest picture so far
	lastPTS  time.Duration

	// What the write loop has written so far, for Recording.
	file    string
	origin  time.Time // wall time of the file's start
	length  time.Duration
	started bool

	// Owned by writeLoop, then by stop.
	w      *recording.Writer
	saved  bool
	failed bool
}

// startVideoLocked starts recording a lease's task. Callers hold g.mu.
func (g *Gateway) startVideoLocked(l *lease) {
	d := l.dev
	if !g.recordOn || g.store == nil || d.video != nil || l.ended || l.recorded || g.closed {
		return
	}
	key := hubKey(d.id, false)
	if l.bg != nil {
		key = hubKey(d.id, true)
	}
	v := &taskVideo{g: g, d: d, t: l.task, floor: stamp(), key: key, moved: make(chan struct{}),
		needKey: true, ran: make(chan struct{}), wrote: make(chan struct{})}
	v.cond = sync.NewCond(&v.mu)
	v.ctx, v.cancel = context.WithCancel(g.ctx)
	d.video = v
	// One file per task: a task whose recording was turned off is not
	// recorded again, which would overwrite it.
	l.recorded = true
	g.videos.add()
	go v.run()
	go v.writeLoop()
}

// stopVideoLocked ends the recording of a device's task; with read, the
// recorder reads the screen once more to keep what it holds. Callers hold
// g.mu; the rest happens in the background.
func (g *Gateway) stopVideoLocked(d *device, read bool) {
	v := d.video
	if v == nil {
		return
	}
	d.video = nil
	v.mu.Lock()
	v.endAt = stamp()
	v.mu.Unlock()
	g.videoStops.add()
	go v.stop(read)
}

// moveVideoLocked moves the recording to another screen of the device, as
// when a task leaves its background display, or to a new display under the
// same key (one opened again after the phone dropped). Callers hold g.mu.
func (g *Gateway) moveVideoLocked(d *device, key string) {
	v := d.video
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.key = key
	close(v.moved)
	v.moved = make(chan struct{})
	v.dropLocked()
}

// source returns the hub key the recording taps and that screen's device
// now: the phone's (a new one after it reconnected), or the lease's
// background display; nil while there is none.
func (v *taskVideo) source() (string, driver.Device, chan struct{}) {
	g, d := v.g, v.d
	g.mu.Lock()
	defer g.mu.Unlock()
	v.mu.Lock()
	key, moved := v.key, v.moved
	v.mu.Unlock()
	if key == hubKey(d.id, true) {
		if l := d.lease; l != nil && l.task == v.t && l.bg != nil {
			return key, l.bg, moved
		}
		return key, nil, moved
	}
	return key, d.dev, moved
}

// run taps the stream until the recording stops, tapping it again when it
// ends (the phone dropped) or the recording moves to another screen.
func (v *taskVideo) run() {
	defer close(v.ran)
	g := v.g
	wait := videoRetry
	backoff := func() bool {
		ok := sleepCtx(v.ctx, wait, nil)
		if wait *= 2; wait > videoRetryMax {
			wait = videoRetryMax
		}
		return ok
	}
	for v.ctx.Err() == nil {
		key, dev, moved := v.source()
		if dev == nil {
			// Offline, or the display is gone: wait for it.
			if !backoff() {
				return
			}
			continue
		}
		opened := time.Now()
		t, err := g.tapStream(v.ctx, key, dev, v.packet)
		if err != nil {
			if errors.Is(err, driver.ErrUnsupported) {
				g.log.Info("no screen recording: the phone sends no video", "device", v.d.id, "err", err)
				return
			}
			g.log.Debug("screen recording: no stream", "device", v.d.id, "err", err)
			if !backoff() {
				return
			}
			continue
		}
		v.mu.Lock()
		v.tap = t
		v.mu.Unlock()
		// The file starts at a key frame: ask for one rather than wait.
		t.h.requestKeyFrame()
		move := false
		select {
		case <-t.Done():
		case <-moved:
			move = true
		case <-v.ctx.Done():
		}
		t.Close()
		v.mu.Lock()
		v.tap = nil
		// A new stream starts over: nothing held belongs to it.
		v.dropLocked()
		v.base, v.lastPTS = time.Time{}, 0
		v.mu.Unlock()
		if move {
			wait = videoRetry
			continue // the other screen's stream, at once
		}
		if time.Since(opened) > videoRetryMax {
			wait = videoRetry // it ran for a while: try again soon
		}
		if !backoff() {
			return
		}
	}
}

// packet holds a packet from the hub, on the hub's goroutine.
func (v *taskVideo) packet(tp timedPacket, codec driver.Codec) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stopping {
		return
	}
	if v.codec == "" {
		v.codec = codec
	}
	if codec != v.codec {
		return // another codec on a new stream: the file cannot take it
	}
	if !v.endAt.IsZero() && tp.at.After(v.endAt) {
		return // after the task
	}
	p := tp.p
	if p.Config {
		cp := p
		v.cfg = &cp
		if !v.paused && !tp.at.Before(v.floor) {
			v.held = append(v.held, vpacket{p: p, arrived: tp.at, at: tp.at})
		}
		return
	}
	at := v.timeOf(tp)
	if v.paused || tp.at.Before(v.floor) {
		v.needKey = true
		return
	}
	vp := vpacket{p: p, arrived: tp.at, at: at}
	if p.KeyFrame {
		vp.cfg = v.cfg
	}
	v.held = append(v.held, vp)
	v.heldSize += len(p.Data)
	if v.heldSize > maxHeldBytes {
		v.dropLocked() // no read came in time: this part is lost
		return
	}
	if !v.reading && tp.at.Sub(v.held[0].arrived) >= videoConfirm {
		v.reading = true
		go v.confirm()
	}
}

// timeOf places a picture on the wall clock by its PTS, which is smoother
// than arrival times: at its PTS plus the shortest delay any picture of
// the stream took to arrive. So a picture is never placed after it
// arrived, and a late one keeps its place. A PTS that goes back (a new
// stream, an encoder restart) starts over.
func (v *taskVideo) timeOf(tp timedPacket) time.Time {
	p := tp.p
	base := tp.at.Add(-p.PTS)
	if v.base.IsZero() || p.PTS < v.lastPTS || base.Before(v.base) {
		v.base = base
	}
	v.lastPTS = p.PTS
	return v.base.Add(p.PTS)
}

// dropLocked drops what is held and waits for a recordable read that
// started after now: a read already running may have looked before what
// caused the drop, and packets from before now that come later (a member
// joining a hub gets the last key frame's) are never written.
func (v *taskVideo) dropLocked() {
	v.held, v.heldSize = nil, 0
	v.paused, v.needKey = true, true
	now := stamp()
	if now.After(v.floor) {
		v.floor = now
	}
	if now.After(v.newest) {
		v.newest = now
	}
}

// readStarting registers a read of the screen the agent uses, before it
// runs, and returns its token for read or readFailed.
func (v *taskVideo) readStarting(start time.Time) uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.reads == nil {
		v.reads = map[uint64]time.Time{}
	}
	v.readNext++
	v.reads[v.readNext] = start
	return v.readNext
}

// readFailed forgets a read that returned no screen.
func (v *taskVideo) readFailed(tok uint64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.reads, tok)
}

// read takes a read of the screen the agent uses, which started at start:
// what arrived before it goes to the file when the screen is recordable,
// and is dropped otherwise. Reads can overlap (phone_observe during an
// action, the recorder's own): a read that started earlier and is still
// running may yet show a blocked app, so a recordable read writes only
// what arrived before every read still running started. A read that shows
// what must not be recorded drops what is held whenever it ends; a
// recordable read older than one that already decided changes nothing.
func (v *taskVideo) read(tok uint64, start time.Time, recordable bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.reads, tok)
	if !recordable {
		v.dropLocked()
		return
	}
	if start.Before(v.newest) {
		return
	}
	v.newest = start
	for _, s := range v.reads {
		if s.Before(start) {
			// An earlier read is still running and may yet show what must
			// not be recorded: in order, it would have dropped what came
			// before this one. Write nothing now; the next read does.
			v.paused = false
			return
		}
	}
	n := 0
	for n < len(v.held) && v.held[n].arrived.Before(start) {
		n++
	}
	cfgQueued := false
	for _, vp := range v.held[:n] {
		switch {
		case vp.p.Config:
			v.ready = append(v.ready, vp)
			cfgQueued = true
		case v.needKey && !vp.p.KeyFrame:
			// Cannot be decoded without the frames dropped before it.
		default:
			if vp.p.KeyFrame && v.needKey && vp.cfg != nil && !cfgQueued {
				// The configuration it was encoded with, which a gap may
				// have dropped.
				v.ready = append(v.ready, vpacket{p: *vp.cfg, arrived: vp.arrived, at: vp.at})
			}
			v.needKey, cfgQueued = false, false
			v.ready = append(v.ready, vp)
			v.queued++
		}
		v.heldSize -= len(vp.p.Data)
	}
	v.held = append([]vpacket(nil), v.held[n:]...)
	if v.paused && v.needKey && v.tap != nil {
		// Writing resumes at a key frame: ask for one rather than wait. On
		// a goroutine of its own: the hub calls packet with its lock held,
		// and asking takes that lock.
		go v.tap.h.requestKeyFrame()
	}
	v.paused = false
	v.cond.Broadcast()
}

// pause drops what is held: the screen shows, or may show, what must not
// be recorded.
func (v *taskVideo) pause() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.dropLocked()
}

// confirm reads the screen for the packets held, unless an action runs,
// which reads it soon anyway.
func (v *taskVideo) confirm() {
	defer func() {
		v.mu.Lock()
		v.reading = false
		v.mu.Unlock()
	}()
	d := v.d
	if !d.act.TryLock() {
		return
	}
	defer d.act.Unlock()
	ctx, cancel := context.WithTimeout(v.ctx, observeTimeout)
	defer cancel()
	v.g.observe(ctx, d, false)
}

// writeLoop writes what reads confirmed.
func (v *taskVideo) writeLoop() {
	defer close(v.wrote)
	for {
		v.mu.Lock()
		for len(v.ready) == 0 && !v.stopping {
			v.cond.Wait()
		}
		batch := v.ready
		v.ready = nil
		stopping := v.stopping
		codec := v.codec
		v.mu.Unlock()
		pictures := 0
		for _, vp := range batch {
			v.write(vp, codec)
			if !vp.p.Config {
				pictures++
			}
		}
		if pictures > 0 {
			v.mu.Lock()
			v.queued -= pictures
			v.mu.Unlock()
		}
		if stopping && len(batch) == 0 {
			return
		}
	}
}

// write adds one confirmed packet to the file; the file starts with the
// first key frame.
func (v *taskVideo) write(vp vpacket, codec driver.Codec) {
	if v.failed {
		return
	}
	g := v.g
	if v.w == nil {
		path, err := g.store.RecordingPath(v.t.id, v.t.started)
		if err == nil {
			v.w, err = recording.Create(path, codec)
		}
		if err != nil {
			v.failed = true
			g.log.Warn("screen recording", "task", v.t.id, "err", err)
			return
		}
	}
	var at time.Duration
	if !vp.p.Config {
		if !v.w.Started() {
			v.mu.Lock()
			v.origin = vp.at
			v.mu.Unlock()
		}
		at = vp.at.Sub(v.origin)
	}
	if err := v.w.Write(vp.p, at); err != nil {
		v.failed = true
		g.log.Warn("screen recording", "task", v.t.id, "err", err)
		return
	}
	if v.w.Started() {
		v.mu.Lock()
		v.file, v.length, v.started = v.w.Path(), v.w.Length(), true
		v.mu.Unlock()
		if !v.saved {
			v.saved = true
			v.save()
		}
	}
}

// save records the recording in history.
func (v *taskVideo) save() {
	g := v.g
	w := v.w
	width, height := w.Size()
	v.mu.Lock()
	origin := v.origin
	v.mu.Unlock()
	rec := history.Recording{TaskID: v.t.id, Path: w.Path(), Started: origin, Length: w.Length(),
		Codec: string(v.codec), Width: width, Height: height}
	if fi, err := os.Stat(w.Path()); err == nil {
		rec.Size = fi.Size()
	}
	g.rec.do(func(ctx context.Context) error { return g.store.SaveRecording(ctx, rec) })
}

// stop ends the recording: with read, one more read of the recording's own
// screen keeps what arrived before the task ended, when that screen is
// still recordable; then the file is closed. A background display closes
// with its task, so there is no last read of it.
func (v *taskVideo) stop(read bool) {
	g := v.g
	defer g.videos.done()
	defer g.videoStops.done()
	v.mu.Lock()
	holds := len(v.held) > 0
	v.mu.Unlock()
	if key, dev, _ := v.source(); read && holds && v.ctx.Err() == nil && dev != nil && key == hubKey(v.d.id, false) {
		v.lastRead(dev)
	}
	v.mu.Lock()
	v.stopping = true
	v.held, v.heldSize = nil, 0
	v.cond.Broadcast()
	v.mu.Unlock()
	v.cancel()
	<-v.ran
	<-v.wrote
	if v.w == nil {
		return
	}
	v.mu.Lock()
	origin, end := v.origin, v.endAt
	v.mu.Unlock()
	if err := v.w.Close(end.Sub(origin)); err != nil {
		g.log.Warn("screen recording", "task", v.t.id, "err", err)
	}
	if v.w.Started() {
		v.mu.Lock()
		v.length = v.w.Length()
		v.mu.Unlock()
		v.save()
	}
}

// lastRead reads the phone's screen once more for what the recording
// holds, waiting at most videoStopRead for actions in progress.
func (v *taskVideo) lastRead(dev driver.Device) {
	d := v.d
	if !lockWithin(v.ctx, &d.act, videoStopRead) {
		return
	}
	defer d.act.Unlock()
	ctx, cancel := context.WithTimeout(v.ctx, videoStopRead)
	defer cancel()
	start := stamp()
	tok := v.readStarting(start)
	s, err := dev.Observe(ctx, driver.ObserveOptions{})
	if err != nil || s == nil {
		v.readFailed(tok)
		return
	}
	g := v.g
	g.mu.Lock()
	person := d.person
	g.mu.Unlock()
	v.read(tok, start, recordableScreen(g.policy, s) && !person)
}

// lockWithin locks mu unless that takes longer than max or ctx ends.
func lockWithin(ctx context.Context, mu *sync.Mutex, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for !mu.TryLock() {
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		if !sleepCtx(ctx, 20*time.Millisecond, nil) {
			return false
		}
	}
	return true
}

// recorded reports whether the recording holds pictures, or will hold the
// ones reads confirmed that the write loop is still writing: its first
// picture can take a moment to reach the file (creating the file is slow
// on Windows), and a release right then must still ask about it.
func (v *taskVideo) recorded() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.started || v.queued > 0
}

// view returns what the recording holds so far.
func (v *taskVideo) view() (RecordingView, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.started {
		return RecordingView{}, false
	}
	return RecordingView{TaskID: v.t.id, Path: v.file, Start: v.origin, Length: v.length}, true
}

// recordableScreen reports whether a read shows a screen whose pictures
// may be kept: unlocked, and an app that is known and not blocked. A read
// that names no app (taken while one app hands over to the next) vouches
// for nothing: a blocked app may be on its way in.
func recordableScreen(pol *policy.Engine, s *screen.Screen) bool {
	if s == nil || s.Locked || (s.App.ID == "" && s.App.Name == "") {
		return false
	}
	_, blocked := pol.Blocked(s.App)
	return !blocked
}

// pauseVideoLocked drops what a device's recording holds. Callers hold
// g.mu.
func (g *Gateway) pauseVideoLocked(d *device) {
	if v := d.video; v != nil {
		v.pause()
	}
}

// SetRecording turns screen recording of agent tasks on or off. Turning it
// on starts recording running tasks; turning it off finishes their files.
// It needs a history folder (Options.DataDir). Recording pauses while a
// blocked app is in front.
func (g *Gateway) SetRecording(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.recordOn == on {
		return
	}
	g.recordOn = on
	for _, d := range g.devices {
		switch {
		case on && d.lease != nil:
			g.startVideoLocked(d.lease)
		case !on:
			g.stopVideoLocked(d, true)
		}
	}
}

// Recording returns a task's screen recording, or driver.ErrNotFound when
// it has none (recording was off, the phone never showed a recordable
// screen, or the retention limit removed it). A running task's recording
// grows; Length is what is written so far.
func (g *Gateway) Recording(ctx context.Context, taskID string) (RecordingView, error) {
	notFound := fmt.Errorf("task %s has no recording: %w", taskID, driver.ErrNotFound)
	g.mu.Lock()
	var running *taskVideo
	for _, d := range g.devices {
		if v := d.video; v != nil && v.t.id == taskID {
			running = v
		}
	}
	var steps []StepView
	if running != nil {
		steps = append(steps, running.t.steps...)
	}
	g.mu.Unlock()
	var view RecordingView
	if running != nil {
		v, ok := running.view()
		if !ok {
			return RecordingView{}, notFound
		}
		view = v
	} else {
		if g.store == nil {
			return RecordingView{}, notFound
		}
		g.Flush()
		r, err := g.store.Recording(ctx, taskID)
		if errors.Is(err, history.ErrNoRecording) {
			return RecordingView{}, notFound
		}
		if err != nil {
			return RecordingView{}, err
		}
		view = RecordingView{TaskID: taskID, Path: r.Path, Start: r.Started, Length: r.Length}
		// The file is the truth: a crash leaves the row's length behind.
		if info, err := recording.Probe(r.Path); err == nil {
			view.Length = info.Length
		}
		recs, err := g.store.Steps(ctx, taskID)
		if err != nil {
			return RecordingView{}, err
		}
		for _, st := range recs {
			steps = append(steps, StepView{Index: st.Index, Text: st.Text, At: st.Started})
		}
	}
	view.Marks = marksFor(view, steps)
	return view, nil
}

// marksFor places steps on a recording: where each started, from the
// recording's start, named by their first line. Steps from before it start
// at 0; steps after its end are left out.
func marksFor(view RecordingView, steps []StepView) []RecordingMark {
	var out []RecordingMark
	for _, st := range steps {
		if st.At.IsZero() {
			continue
		}
		at := st.At.Sub(view.Start)
		if at < 0 {
			at = 0
		}
		if at > view.Length+time.Second {
			continue
		}
		text, _, _ := strings.Cut(st.Text, "\n")
		out = append(out, RecordingMark{At: at, Step: st.Index, Text: text})
	}
	return out
}
