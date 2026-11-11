package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// A phone gives one video stream at a time, and the mirror (one or more
// viewers) and the recorder all want it. A hub opens the device's stream
// for its first member, sends every packet to each member, and closes the
// stream when the last one leaves. A member that joins late first gets the
// configuration and the packets since the last key frame, so it shows the
// current picture at once instead of waiting for the next key frame
// (scrcpy sends one every few seconds, iPhones none while the screen is
// still). Members are viewers, which are driver.Streams for the mirror,
// and taps, which the recorder uses: a tap gets each packet with the time
// the hub received it, which tells the recorder which read confirms it.

// Limits on what a hub and a viewer hold.
const (
	maxGOPBytes   = 64 << 20 // packets kept since the last key frame
	maxQueueBytes = 64 << 20 // packets a slow viewer may fall behind by
	maxQueueLen   = 2048
)

// errStreamEnded is a viewer's error when the phone's stream ended
// without one of its own.
var errStreamEnded = errors.New("the phone's video stream ended")

// keyFrameRequester is a driver.Stream that can ask the phone for a key
// frame now (iOS can; scrcpy sends one every few seconds by itself). It is
// optional: members that need a key frame (a recording after a pause, a
// viewer that fell behind) ask through the hub, and wait for the next one
// otherwise.
type keyFrameRequester interface {
	RequestKeyFrame()
}

// timedPacket is a packet and when the hub received it.
type timedPacket struct {
	p  driver.Packet
	at time.Time
}

// member is a hub's viewer or tap. push must not block. release leaves
// the hub and frees the member's goroutine, for a member nobody will read.
type member interface {
	push(tp timedPacket)
	finish()
	release()
}

// hub shares one device stream.
type hub struct {
	g     *Gateway
	key   string
	ready chan struct{} // closed once the stream is open, or failed to

	// Set before ready closes.
	src    driver.Stream
	cancel context.CancelFunc
	codec  driver.Codec
	w, h   int

	srcOnce sync.Once

	mu      sync.Mutex
	members map[member]struct{}
	config  *timedPacket
	gop     []timedPacket
	gopSize int
	err     error
	ended   bool
}

// hubKey names the stream of a device's main screen, or of the background
// display an agent works on.
func hubKey(deviceID string, agent bool) string {
	if agent {
		return deviceID + "#agent"
	}
	return deviceID
}

// join adds m to the hub under key, opening dev's stream when the hub is
// new, and waits until the stream is open.
func (g *Gateway) join(ctx context.Context, key string, dev driver.Device, opt driver.StreamOptions, m func(*hub) member) (*hub, member, error) {
	g.hubMu.Lock()
	if g.hubsClosed {
		g.hubMu.Unlock()
		return nil, nil, errors.New("the gateway is stopping")
	}
	h := g.hubs[key]
	opening := false
	if h == nil {
		h = &hub{g: g, key: key, ready: make(chan struct{}), members: map[member]struct{}{}}
		g.hubs[key] = h
		opening = true
	}
	mem := m(h)
	h.add(mem)
	g.hubMu.Unlock()
	if opening {
		go h.open(dev, opt)
	}
	select {
	case <-h.ready:
	case <-ctx.Done():
		mem.release()
		return nil, nil, ctx.Err()
	}
	if h.src == nil {
		mem.release()
		return nil, nil, h.failure()
	}
	return h, mem, nil
}

// watchOpen returns a viewer of the stream under key when one is open, and
// errNoStream when none is.
func (g *Gateway) watchOpen(ctx context.Context, key string) (driver.Stream, error) {
	g.hubMu.Lock()
	h := g.hubs[key]
	if h == nil || g.hubsClosed {
		g.hubMu.Unlock()
		return nil, errNoStream
	}
	select {
	case <-h.ready:
	default:
		g.hubMu.Unlock()
		return nil, errNoStream
	}
	if h.src == nil {
		g.hubMu.Unlock()
		return nil, errNoStream
	}
	v := newViewer(h)
	h.add(v)
	g.hubMu.Unlock()
	return v, nil
}

// errNoStream is watchOpen's error when no stream is open.
var errNoStream = errors.New("no video stream is open")

// watch returns a viewer of dev's stream under key.
func (g *Gateway) watch(ctx context.Context, key string, dev driver.Device, opt driver.StreamOptions) (driver.Stream, error) {
	_, m, err := g.join(ctx, key, dev, opt, func(h *hub) member { return newViewer(h) })
	if err != nil {
		return nil, err
	}
	return m.(*viewer), nil
}

// open starts the device's stream and fans it out until it ends.
func (h *hub) open(dev driver.Device, opt driver.StreamOptions) {
	ctx, cancel := context.WithCancel(h.g.ctx)
	src, err := dev.Stream(ctx, opt)
	if err != nil {
		cancel()
		h.end(fmt.Errorf("open the video stream: %w", err))
		close(h.ready)
		return
	}
	h.mu.Lock()
	h.src, h.cancel = src, cancel
	h.codec = src.Codec()
	h.w, h.h = src.Size()
	left := h.ended // everyone left while it opened
	h.mu.Unlock()
	close(h.ready)
	if left {
		h.closeSource()
		return
	}
	for p := range src.Packets() {
		h.fanOut(timedPacket{p: p, at: stamp()}) // ordered against reads (clock.go)
	}
	err = src.Err()
	if err == nil {
		err = errStreamEnded
	}
	h.end(err)
}

// add adds a member; a late one starts from the last key frame. When none
// is kept (the pictures since it outgrew maxGOPBytes), the phone is asked
// for one: an iPhone sends key frames only when asked, so a mirror opened
// late in a long session would otherwise wait for a picture forever.
func (h *hub) add(m member) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.members[m] = struct{}{}
	if h.config != nil {
		m.push(*h.config)
	}
	for _, tp := range h.gop {
		m.push(tp)
	}
	if len(h.gop) == 0 && h.src != nil {
		if kr, ok := h.src.(keyFrameRequester); ok {
			go kr.RequestKeyFrame()
		}
	}
}

func (h *hub) fanOut(tp timedPacket) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch p := tp.p; {
	case p.Config:
		cp := tp
		h.config = &cp
	case p.KeyFrame:
		h.gop, h.gopSize = append(h.gop[:0], tp), len(p.Data)
	case len(h.gop) > 0:
		if h.gopSize+len(p.Data) > maxGOPBytes {
			// Late members wait for the next key frame instead.
			h.gop, h.gopSize = nil, 0
		} else {
			h.gop = append(h.gop, tp)
			h.gopSize += len(p.Data)
		}
	}
	for m := range h.members {
		m.push(tp)
	}
}

// configLocked returns the last configuration packet. Callers hold h.mu.
func (h *hub) configLocked() *driver.Packet {
	if h.config == nil {
		return nil
	}
	return &h.config.p
}

// leave removes a member and closes the stream after the last one.
func (h *hub) leave(m member) {
	g := h.g
	g.hubMu.Lock()
	h.mu.Lock()
	if _, ok := h.members[m]; !ok {
		h.mu.Unlock()
		g.hubMu.Unlock()
		return
	}
	delete(h.members, m)
	last := len(h.members) == 0 && !h.ended
	if last {
		h.ended = true
		if g.hubs[h.key] == h {
			delete(g.hubs, h.key)
		}
	}
	h.mu.Unlock()
	g.hubMu.Unlock()
	if last {
		h.closeSource()
	}
}

// closeSource closes the device's stream, once it is open.
func (h *hub) closeSource() {
	h.mu.Lock()
	src, cancel := h.src, h.cancel
	h.mu.Unlock()
	if src == nil {
		return
	}
	h.srcOnce.Do(func() {
		cancel()
		src.Close()
	})
}

// end closes the hub after its stream ended: every member gets what is
// queued for it, then the end.
func (h *hub) end(err error) {
	g := h.g
	g.hubMu.Lock()
	h.mu.Lock()
	if g.hubs[h.key] == h {
		delete(g.hubs, h.key)
	}
	if h.err == nil {
		h.err = err
	}
	h.ended = true
	ms := make([]member, 0, len(h.members))
	for m := range h.members {
		ms = append(ms, m)
	}
	h.members = map[member]struct{}{}
	h.mu.Unlock()
	g.hubMu.Unlock()
	for _, m := range ms {
		m.finish()
	}
	h.closeSource()
}

// requestKeyFrame asks the phone for a key frame, when its stream can.
func (h *hub) requestKeyFrame() {
	h.mu.Lock()
	src := h.src
	h.mu.Unlock()
	if kr, ok := src.(keyFrameRequester); ok {
		go kr.RequestKeyFrame()
	}
}

// canRequestKeyFrame reports whether the hub's source can be asked for a
// key frame.
func (h *hub) canRequestKeyFrame() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.src.(keyFrameRequester)
	return ok
}

func (h *hub) failure() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return h.err
	}
	return errStreamEnded
}

func (h *hub) endErr() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.ended {
		return nil
	}
	return h.err
}

// stopHubs ends the streams of a device, as when it goes offline: their
// viewers see the stream end and may open it again.
func (g *Gateway) stopHubs(deviceID string) {
	g.stopHub(hubKey(deviceID, false), fmt.Errorf("the phone went offline: %w", driver.ErrOffline))
	g.stopHub(hubKey(deviceID, true), fmt.Errorf("the phone went offline: %w", driver.ErrOffline))
}

func (g *Gateway) stopHub(key string, err error) {
	g.hubMu.Lock()
	h := g.hubs[key]
	g.hubMu.Unlock()
	if h != nil {
		h.end(err)
	}
}

// stopAllHubs ends every stream, when the gateway closes.
func (g *Gateway) stopAllHubs() {
	g.hubMu.Lock()
	g.hubsClosed = true
	hs := make([]*hub, 0, len(g.hubs))
	for _, h := range g.hubs {
		hs = append(hs, h)
	}
	g.hubMu.Unlock()
	for _, h := range hs {
		h.end(errors.New("the gateway stopped"))
	}
}

// viewer is one watcher of a hub, a driver.Stream of its own. Packets wait
// in a queue of the viewer's own and go out through a short channel as
// the viewer reads them, so a burst (the packets since the last key frame)
// never looks like a backlog to the mirror's player. A viewer that falls
// too far behind skips to the next key frame.
type viewer struct {
	h    *hub
	out  chan driver.Packet
	stop chan struct{}

	mu       sync.Mutex
	cond     *sync.Cond
	queue    []driver.Packet
	size     int
	skip     bool // dropped packets: wait for a key frame
	finished bool // no more packets will come
	closed   bool
	once     sync.Once
}

func newViewer(h *hub) *viewer {
	v := &viewer{h: h, out: make(chan driver.Packet, 4), stop: make(chan struct{})}
	v.cond = sync.NewCond(&v.mu)
	go v.run()
	return v
}

// push queues a packet. The hub calls it with h.mu held.
func (v *viewer) push(tp timedPacket) {
	p := tp.p
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.finished || v.closed {
		return
	}
	if v.skip {
		if !p.KeyFrame {
			return
		}
		v.skip = false
		v.queueConfigLocked()
	}
	if len(v.queue) >= maxQueueLen || v.size+len(p.Data) > maxQueueBytes {
		v.queue, v.size = nil, 0
		if !p.KeyFrame {
			v.skip = true
			if kr, ok := v.h.src.(keyFrameRequester); ok {
				go kr.RequestKeyFrame()
			}
			return
		}
		v.queueConfigLocked()
	}
	v.queue = append(v.queue, p)
	v.size += len(p.Data)
	v.cond.Signal()
}

func (v *viewer) queueConfigLocked() {
	if cfg := v.h.configLocked(); cfg != nil {
		v.queue = append(v.queue, *cfg)
		v.size += len(cfg.Data)
	}
}

func (v *viewer) run() {
	defer close(v.out)
	for {
		v.mu.Lock()
		for len(v.queue) == 0 && !v.finished && !v.closed {
			v.cond.Wait()
		}
		if v.closed || len(v.queue) == 0 {
			v.mu.Unlock()
			return
		}
		p := v.queue[0]
		v.queue[0] = driver.Packet{}
		v.queue = v.queue[1:]
		v.size -= len(p.Data)
		v.mu.Unlock()
		select {
		case v.out <- p:
		case <-v.stop:
			return
		}
	}
}

// finish ends the viewer once its queue is sent.
func (v *viewer) finish() {
	v.mu.Lock()
	v.finished = true
	v.cond.Signal()
	v.mu.Unlock()
}

func (v *viewer) Codec() driver.Codec           { <-v.h.ready; return v.h.codec }
func (v *viewer) Size() (int, int)              { <-v.h.ready; return v.h.w, v.h.h }
func (v *viewer) Packets() <-chan driver.Packet { return v.out }

// Err says why the stream ended: the device stream's error, or nil after
// Close.
func (v *viewer) Err() error {
	v.mu.Lock()
	closed := v.closed
	v.mu.Unlock()
	if closed {
		return nil
	}
	return v.h.endErr()
}

func (v *viewer) release() { v.Close() }

// Close leaves the hub; the device's stream closes after its last member.
func (v *viewer) Close() error {
	v.once.Do(func() {
		v.mu.Lock()
		v.closed = true
		v.queue, v.size = nil, 0
		v.cond.Signal()
		v.mu.Unlock()
		close(v.stop)
		v.h.leave(v)
	})
	return nil
}

// streamTap is the recorder's member of a hub: fn gets each packet, with the
// time the hub received it, on the hub's goroutine, and must not block.
type streamTap struct {
	h    *hub
	fn   func(timedPacket, driver.Codec)
	done chan struct{}
	once sync.Once
}

// tapStream adds fn to the hub under key and returns the tap once the
// stream is open.
func (g *Gateway) tapStream(ctx context.Context, key string, dev driver.Device, fn func(timedPacket, driver.Codec)) (*streamTap, error) {
	_, m, err := g.join(ctx, key, dev, driver.StreamOptions{}, func(h *hub) member {
		return &streamTap{h: h, fn: fn, done: make(chan struct{})}
	})
	if err != nil {
		return nil, err
	}
	return m.(*streamTap), nil
}

func (t *streamTap) push(tp timedPacket) { t.fn(tp, t.h.codec) }
func (t *streamTap) finish()             { t.once.Do(func() { close(t.done) }) }

// Done is closed when the stream ends.
func (t *streamTap) Done() <-chan struct{} { return t.done }

// Close leaves the hub.
func (t *streamTap) Close() {
	t.h.leave(t)
	t.finish()
}

func (t *streamTap) release() { t.Close() }
