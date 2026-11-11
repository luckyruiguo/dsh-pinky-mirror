package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/recording"
)

// streamer sends packets of the test stream with PTS on the stream's own
// clock and a little time between them, as a phone does.
type streamer struct {
	t           *testing.T
	src         *feedStream
	cfg         driver.Packet
	keys, plain []driver.Packet
	k, p        int
	start       time.Time
}

func newStreamer(t *testing.T, src *feedStream) *streamer {
	st := &streamer{t: t, src: src, start: time.Now()}
	for _, p := range testPackets(t) {
		switch {
		case p.Config:
			st.cfg = p
		case p.KeyFrame:
			st.keys = append(st.keys, p)
		default:
			st.plain = append(st.plain, p)
		}
	}
	return st
}

// config sends the configuration packet.
func (s *streamer) config() { s.src.send(s.cfg) }

// pictures sends n pictures, the first a key frame when key is set, and
// returns once the hub has taken them all.
func (s *streamer) pictures(n int, key bool) {
	s.t.Helper()
	for i := 0; i < n; i++ {
		var p driver.Packet
		if i == 0 && key {
			p = s.keys[s.k%len(s.keys)]
			s.k++
		} else {
			p = s.plain[s.p%len(s.plain)]
			s.p++
		}
		p.PTS = time.Since(s.start)
		s.src.send(p)
		time.Sleep(2 * time.Millisecond)
	}
	// The hub stamps a packet's arrival when it takes it from the stream,
	// and a read vouches only for what arrived before it: a hub that has
	// not run for a while on a busy machine would date the last pictures
	// after the test's next read.
	for deadline := time.Now().Add(5 * time.Second); len(s.src.ch) > 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			s.t.Fatalf("the hub did not take %d pictures", len(s.src.ch))
		}
	}
	time.Sleep(5 * time.Millisecond)
}

func taskID(t *testing.T, g *Gateway, s *Session) string {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, l := range s.leases {
		return l.task.id
	}
	t.Fatal("the session has no task")
	return ""
}

func TestRecordingKeepsOnlyWhatReadsVouchFor(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	g.SetRecording(true)
	s, _ := connect(t, g, "Lunch")
	id := taskID(t, g, s)
	st := newStreamer(t, v.open(t))

	// A second of video, then a read of a plain screen: kept.
	st.config()
	st.pictures(10, true)
	mustStatus(t, s.Observe(ctx, ObserveRequest{}), StatusSettled)

	// A payment hands over to a blocked app: what came since the last
	// read is dropped, and so is what comes while it is in front.
	st.pictures(5, false)
	v.Show("wallet")
	s.Observe(ctx, ObserveRequest{})
	st.pictures(4, false)

	// Home again: writing resumes at a key frame, which the recorder asks
	// for (once at the start, once now).
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusSettled)
	// It asks on a goroutine of its own, through the hub's.
	for deadline := time.Now().Add(5 * time.Second); st.src.keyRequests() < 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d key frame requests", st.src.keyRequests())
		}
	}
	st.pictures(3, false)
	st.pictures(5, true)

	// The task ends: one more read keeps the last part.
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
	g.Flush()
	rec, err := g.Recording(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	info, err := recording.Probe(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Frames != 15 || info.Width != 96 || info.Codec != driver.H264 {
		t.Fatalf("recorded %+v, want the 10 frames before the wallet and the 5 after the key frame", info)
	}
	if rec.Length <= 0 || rec.Start.IsZero() {
		t.Fatalf("recording %+v", rec)
	}
	var home bool
	for _, m := range rec.Marks {
		home = home || m.Text == "Pressed home"
		if m.At < 0 || m.At > rec.Length+time.Second {
			t.Fatalf("mark %+v outside the recording of %v", m, rec.Length)
		}
	}
	if !home || len(rec.Marks) < 3 {
		t.Fatalf("marks %+v", rec.Marks)
	}
}

func TestRecordingDropsTheTakeover(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	g.SetRecording(true)
	s, _ := connect(t, g, "Lunch")
	id := taskID(t, g, s)
	st := newStreamer(t, v.open(t))
	st.config()
	st.pictures(10, true)
	mustStatus(t, s.Observe(ctx, ObserveRequest{}), StatusSettled)
	// The person takes over (to type a password, say): nothing of it is
	// kept, even with a plain screen afterwards.
	st.pictures(4, false)
	if err := g.TakeOver("pixel"); err != nil {
		t.Fatal(err)
	}
	st.pictures(6, false)
	s.Observe(ctx, ObserveRequest{})
	if err := g.HandBack("pixel", ""); err != nil {
		t.Fatal(err)
	}
	// Stopped: no read afterwards, so what is held is not kept.
	st.pictures(5, true)
	if err := g.Stop("pixel"); err != nil {
		t.Fatal(err)
	}
	g.Flush()
	rec, err := g.Recording(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	info, err := recording.Probe(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Frames != 10 {
		t.Fatalf("recorded %d frames, want only the 10 before the takeover", info.Frames)
	}
}

func TestNoRecordingWhenOff(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	s, _ := connect(t, g, "Lunch")
	id := taskID(t, g, s)
	select {
	case <-v.opened:
		t.Fatal("recording off, yet the stream opened")
	case <-time.After(50 * time.Millisecond):
	}
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
	if _, err := g.Recording(ctx, id); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("recording of an unrecorded task: %v", err)
	}
}

// Packets from before the task, which a late member of the hub gets to
// start from, are never written: the person may have used the phone.
func TestRecordingStartsWithTheTask(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	mirror, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()
	st := newStreamer(t, v.open(t))
	st.config()
	st.pictures(8, true) // before the task; the hub keeps them for late viewers
	time.Sleep(10 * time.Millisecond)
	g.SetRecording(true)
	s, _ := connect(t, g, "Lunch")
	id := taskID(t, g, s)
	st.pictures(3, false) // still the old key frame's pictures: not decodable on their own
	st.pictures(4, true)
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
	g.Flush()
	rec, err := g.Recording(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	info, err := recording.Probe(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Frames != 4 {
		t.Fatalf("recorded %d frames, want the 4 from the task's first key frame", info.Frames)
	}
}

// A read that names no app (one app handing over to the next) vouches for
// nothing: what was held is dropped, as for a blocked app.
func TestRecordingDropsAnUnknownApp(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	g.SetRecording(true)
	s, _ := connect(t, g, "Lunch")
	id := taskID(t, g, s)
	st := newStreamer(t, v.open(t))
	st.config()
	st.pictures(10, true)
	mustStatus(t, s.Observe(ctx, ObserveRequest{}), StatusSettled)
	st.pictures(5, false)
	v.setNoApp(true)
	s.Observe(ctx, ObserveRequest{})
	v.setNoApp(false)
	st.pictures(3, false)
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
	g.Flush()
	rec, err := g.Recording(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	info, err := recording.Probe(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Frames != 10 {
		t.Fatalf("recorded %d frames, want the 10 before the unknown app", info.Frames)
	}
}

// Reads can overlap. A read that started earlier and is still running
// may show a blocked app, so a newer clean read writes nothing while it
// runs; when the older read then shows the blocked app, everything held is
// dropped. A drop from outside (a lock, a takeover) makes reads that were
// already running too old to resume the recording.
func TestRecordingOverlappingReads(t *testing.T) {
	t0 := time.Now().Add(-time.Hour) // packets on a clock of their own, in the past
	pkts := testPackets(t)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	newVideo := func() *taskVideo {
		v := &taskVideo{floor: t0, needKey: true}
		v.cond = sync.NewCond(&v.mu)
		return v
	}
	send := func(v *taskVideo, p driver.Packet, ms int) {
		p.PTS = time.Duration(ms) * time.Millisecond
		v.packet(timedPacket{p: p, at: at(ms)}, driver.H264)
	}

	v := newVideo()
	send(v, pkts[0], 1)  // configuration
	send(v, pkts[1], 10) // key frame
	send(v, pkts[2], 20)
	old := v.readStarting(at(25)) // will see the blocked app
	send(v, pkts[3], 30)
	clean := v.readStarting(at(45))
	send(v, pkts[4], 50)
	v.read(clean, at(45), true)
	if len(v.ready) != 0 {
		t.Fatalf("%d packets written while an earlier read still ran", len(v.ready))
	}
	v.read(old, at(25), false)
	if len(v.held) != 0 || !v.paused || len(v.ready) != 0 {
		t.Fatalf("after the older read saw the blocked app: %d held, paused %v, %d written", len(v.held), v.paused, len(v.ready))
	}

	// In order, a clean read writes what came before it.
	v = newVideo()
	send(v, pkts[0], 1)
	send(v, pkts[1], 10)
	send(v, pkts[2], 20)
	r := v.readStarting(at(25))
	send(v, pkts[3], 30)
	v.read(r, at(25), true)
	if n := len(v.ready); n != 3 || len(v.held) != 1 {
		t.Fatalf("%d written, %d held; want the configuration, the key frame and a picture written, one held", n, len(v.held))
	}

	// The phone locks while a read runs: that read cannot resume the
	// recording, and what came before the lock and arrives later (a hub
	// replaying its last key frame) is never written.
	running := v.readStarting(at(40))
	v.pause()
	v.read(running, at(40), true)
	if !v.paused {
		t.Fatal("a read from before the lock resumed the recording")
	}
	now := stamp()
	fresh := v.readStarting(now)
	send(v, pkts[11], 60) // a key frame from before the lock, replayed
	v.read(fresh, now, true)
	send(v, pkts[12], 70)
	if len(v.held) != 0 || len(v.ready) != 3 {
		t.Fatalf("old packets taken after the lock: %d held, %d written", len(v.held), len(v.ready))
	}
}

// Packets that arrive after the task ended are not the task's.
func TestRecordingEndsWithTheTask(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	g.SetRecording(true)
	s, _ := connect(t, g, "Lunch")
	id := taskID(t, g, s)
	st := newStreamer(t, v.open(t))
	st.config()
	st.pictures(10, true)
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
	st.pictures(6, false) // the person's own use, after the task
	g.Flush()
	rec, err := g.Recording(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	info, err := recording.Probe(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Frames != 10 {
		t.Fatalf("recorded %d frames, want the 10 of the task", info.Frames)
	}
}

// On Windows, time.Now moves only with the system's clock tick, so events
// in one tick have the same time. The recorder took a picture of the
// person's own use that arrived in the tick the task ended in for the
// task's, and wrote it when the last read came a tick later; the same at
// the start, with pictures from before the task in the tick it started
// in; and a read that started in the tick the phone locked in, before the
// lock, resumed the recording.
func TestRecordingOnAWindowsClock(t *testing.T) {
	ctx := context.Background()
	t.Run("end", func(t *testing.T) {
		clk := windowsClock(t)
		v := newVideoDev("pixel")
		g := newVideoGateway(t, v)
		g.SetRecording(true)
		s, _ := connect(t, g, "Lunch")
		id := taskID(t, g, s)
		st := newStreamer(t, v.open(t))
		st.config()
		st.pictures(10, true)
		clk.tick()
		// The last read waits for an action in progress: it comes a tick
		// after the task ended.
		d := deviceOf(t, g, "pixel")
		d.act.Lock()
		mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
		st.pictures(6, false) // the person's own use, in the tick the task ended in
		clk.tick()
		d.act.Unlock()
		g.Flush()
		if n := recordedFrames(t, g, id); n != 10 {
			t.Fatalf("recorded %d frames, want the 10 of the task", n)
		}
	})
	t.Run("start", func(t *testing.T) {
		clk := windowsClock(t)
		v := newVideoDev("pixel")
		g := newVideoGateway(t, v)
		mirror, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer mirror.Close()
		st := newStreamer(t, v.open(t))
		st.config()
		st.pictures(8, true) // the person's own use, which the hub keeps for late viewers
		g.SetRecording(true)
		s, _ := connect(t, g, "Lunch") // in the same tick
		id := taskID(t, g, s)
		clk.tick()
		st.pictures(3, false)
		st.pictures(4, true)
		clk.tick()
		mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
		g.Flush()
		if n := recordedFrames(t, g, id); n != 4 {
			t.Fatalf("recorded %d frames, want the 4 from the task's first key frame", n)
		}
	})
	t.Run("lock", func(t *testing.T) {
		windowsClock(t) // every stamp in one tick
		pkts := testPackets(t)
		v := &taskVideo{floor: stamp(), needKey: true}
		v.cond = sync.NewCond(&v.mu)
		send := func(p driver.Packet) { v.packet(timedPacket{p: p, at: stamp()}, driver.H264) }
		send(pkts[0])
		send(pkts[1])
		start := stamp()
		running := v.readStarting(start)
		v.pause() // the phone locks
		send(pkts[2])
		v.read(running, start, true) // the screen it saw before the lock
		if !v.paused || len(v.held) != 0 || len(v.ready) != 0 {
			t.Fatalf("a read from before the lock resumed the recording: paused %v, %d held, %d written", v.paused, len(v.held), len(v.ready))
		}
	})
}

// A read confirmed the recording's first pictures, which the write loop
// has yet to write (creating the file took a while on Windows): the
// recording counts as held, so a release right then asks the person about
// it rather than leave it out.
func TestRecordedBeforeWritten(t *testing.T) {
	pkts := testPackets(t)
	v := &taskVideo{floor: stamp(), needKey: true}
	v.cond = sync.NewCond(&v.mu)
	if v.recorded() {
		t.Fatal("recorded before any picture")
	}
	for _, p := range pkts[:4] {
		v.packet(timedPacket{p: p, at: stamp()}, driver.H264)
	}
	if v.recorded() {
		t.Fatal("recorded with pictures no read confirmed")
	}
	start := stamp()
	v.read(v.readStarting(start), start, true)
	if _, ok := v.view(); ok || len(v.ready) == 0 {
		t.Fatalf("view %v, %d ready", ok, len(v.ready))
	}
	if !v.recorded() {
		t.Fatal("pictures on their way to the file do not count")
	}
}

func deviceOf(t *testing.T, g *Gateway, id string) *device {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	d := g.findDeviceLocked(id)
	if d == nil {
		t.Fatalf("no device %q", id)
	}
	return d
}

func recordedFrames(t *testing.T, g *Gateway, id string) int {
	t.Helper()
	rec, err := g.Recording(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	info, err := recording.Probe(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Frames
}

// closingDev is a phone's device that stops working once closed, as a
// driver's does after the phone dropped; every Open returns a new one.
type closingDev struct {
	*videoDev
	mu     sync.Mutex
	closed bool
}

func (c *closingDev) Stream(ctx context.Context, opt driver.StreamOptions) (driver.Stream, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, errors.New("device closed")
	}
	return c.videoDev.Stream(ctx, opt)
}

func (c *closingDev) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

type closingProvider struct {
	*demo.Provider
	v *videoDev
}

func (p closingProvider) Open(ctx context.Context, id string) (driver.Device, error) {
	if _, err := p.Provider.Open(ctx, id); err != nil {
		return nil, err
	}
	return &closingDev{videoDev: p.v}, nil
}

// After the phone drops and comes back, the recording opens the stream of
// the new connection, not the closed one.
func TestRecordingAfterReconnect(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v, func(o *Options) {
		o.Providers = []driver.Provider{closingProvider{demo.NewProvider(driver.Android, v.Device), v}}
		o.DiscoverEvery = 10 * time.Millisecond
	})
	g.SetRecording(true)
	s, _ := connect(t, g, "Lunch")
	id := taskID(t, g, s)
	first := v.open(t)
	v.SetOffline(true)
	waitStatus(t, g, DeviceOffline)
	first.end(errors.New("unplugged"))
	v.SetOffline(false)
	waitStatus(t, g, DeviceWorking)
	st := newStreamer(t, v.open(t))
	// Nothing is written until a read after the new stream vouches for it.
	mustStatus(t, s.Observe(ctx, ObserveRequest{}), StatusSettled)
	st.config()
	st.pictures(8, true)
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
	g.Flush()
	rec, err := g.Recording(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := recording.Probe(rec.Path); err != nil || info.Frames != 8 {
		t.Fatalf("after the reconnect: %+v, %v", info, err)
	}
}

func waitStatus(t *testing.T, g *Gateway, st DeviceStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if g.Devices()[0].Status == st {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("the phone is %s, not %s", g.Devices()[0].Status, st)
}
