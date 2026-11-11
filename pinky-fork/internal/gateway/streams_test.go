package gateway

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/recording/recordingtest"
	"github.com/DIYgod/pinky/internal/screen"
)

// testPackets is a real H.264 stream (ffmpeg's test pattern, 96x64, a key
// frame every 10 pictures) cut into packets as drivers send them.
func testPackets(t *testing.T) []driver.Packet {
	t.Helper()
	return recordingtest.Packets(driver.H264, recordingtest.H264())
}

// feedStream is a driver.Stream the test feeds.
type feedStream struct {
	ch       chan driver.Packet
	once     sync.Once
	mu       sync.Mutex
	closed   bool
	err      error
	keyAsked int
}

// RequestKeyFrame counts requests, as an iPhone's stream would answer them.
func (s *feedStream) RequestKeyFrame() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keyAsked++
}

func (s *feedStream) keyRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keyAsked
}

func (s *feedStream) Codec() driver.Codec           { return driver.H264 }
func (s *feedStream) Size() (int, int)              { return 96, 64 }
func (s *feedStream) Packets() <-chan driver.Packet { return s.ch }
func (s *feedStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
func (s *feedStream) Close() error {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.ch)
	})
	return nil
}
func (s *feedStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// end ends the stream with an error, as a dropped phone does.
func (s *feedStream) end(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	s.Close()
}

// send delivers a packet unless the stream closed.
func (s *feedStream) send(p driver.Packet) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.ch <- p
	return true
}

// videoDev is a demo phone whose Stream the test feeds: one stream at a
// time, as phones give.
type videoDev struct {
	*demo.Device
	mu      sync.Mutex
	streams []*feedStream
	opened  chan *feedStream
	noApp   bool // reads name no app, as during a hand-over between apps
}

func (v *videoDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, err := v.Device.Observe(ctx, opt)
	v.mu.Lock()
	defer v.mu.Unlock()
	if err == nil && v.noApp {
		s.App = screen.App{}
	}
	return s, err
}

func (v *videoDev) setNoApp(on bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.noApp = on
}

func newVideoDev(id string) *videoDev {
	return &videoDev{Device: demo.NewDevice(id, "Pixel 9", driver.Android), opened: make(chan *feedStream, 16)}
}

func (v *videoDev) Stream(context.Context, driver.StreamOptions) (driver.Stream, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, s := range v.streams {
		if !s.isClosed() {
			return nil, errors.New("a stream is already open")
		}
	}
	s := &feedStream{ch: make(chan driver.Packet, 1024)}
	v.streams = append(v.streams, s)
	v.opened <- s
	return s, nil
}

func (v *videoDev) open(t *testing.T) *feedStream {
	t.Helper()
	select {
	case s := <-v.opened:
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("the phone's stream was not opened")
		return nil
	}
}

type videoProvider struct {
	*demo.Provider
	d driver.Device
}

func (p videoProvider) Open(context.Context, string) (driver.Device, error) { return p.d, nil }

func newVideoGateway(t *testing.T, v *videoDev, opts ...func(*Options)) *Gateway {
	t.Helper()
	o := Options{Providers: []driver.Provider{videoProvider{demo.NewProvider(driver.Android, v.Device), v}},
		TrustNewAgents: true, DataDir: t.TempDir(), DiscoverEvery: time.Hour, SettleMax: 100 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, NoScreenshots: true}
	for _, f := range opts {
		f(&o)
	}
	g, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

func recv(t *testing.T, s driver.Stream) driver.Packet {
	t.Helper()
	select {
	case p, ok := <-s.Packets():
		if !ok {
			t.Fatalf("stream ended: %v", s.Err())
		}
		return p
	case <-time.After(3 * time.Second):
		t.Fatal("no packet")
		return driver.Packet{}
	}
}

func TestOneStreamForEveryViewer(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	pkts := testPackets(t)

	a, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	src := v.open(t)
	if c := a.Codec(); c != driver.H264 {
		t.Fatalf("codec %s", c)
	}
	// Config, a key frame and three pictures.
	for _, p := range pkts[:5] {
		src.send(p)
	}
	for i := 0; i < 5; i++ {
		if p := recv(t, a); string(p.Data) != string(pkts[i].Data) {
			t.Fatalf("packet %d differs", i)
		}
	}
	// A second viewer shares the stream and starts from the key frame.
	b, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-v.opened:
		t.Fatal("a second stream was opened")
	default:
	}
	if p := recv(t, b); !p.Config {
		t.Fatal("a late viewer's first packet is not the configuration")
	}
	if p := recv(t, b); !p.KeyFrame {
		t.Fatal("a late viewer's second packet is not the key frame")
	}
	for i := 0; i < 3; i++ {
		recv(t, b)
	}
	src.send(pkts[5])
	if p := recv(t, a); string(p.Data) != string(pkts[5].Data) {
		t.Fatal("the first viewer missed a live packet")
	}
	if p := recv(t, b); string(p.Data) != string(pkts[5].Data) {
		t.Fatal("the second viewer missed a live packet")
	}
	// The stream stays while one viewer is left, and closes after the last.
	a.Close()
	time.Sleep(20 * time.Millisecond)
	if src.isClosed() {
		t.Fatal("closed with a viewer left")
	}
	b.Close()
	deadline := time.Now().Add(2 * time.Second)
	for !src.isClosed() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !src.isClosed() {
		t.Fatal("the phone's stream stayed open after the last viewer left")
	}
	// The next viewer opens it again.
	c, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	v.open(t)
}

func TestStreamEndReachesViewers(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	a, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	src := v.open(t)
	src.end(errors.New("scrcpy died"))
	select {
	case _, ok := <-a.Packets():
		if ok {
			t.Fatal("a packet after the end")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the viewer did not see the end")
	}
	if err := a.Err(); err == nil || err.Error() != "scrcpy died" {
		t.Fatalf("viewer error %v", err)
	}
	a.Close()
}

// A viewer that stops reading falls behind and skips to the next key
// frame; the other viewer gets everything.
func TestSlowViewerSkipsToAKeyFrame(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	pkts := testPackets(t)
	slow, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	src := v.open(t)
	fast, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer fast.Close()
	var got atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range fast.Packets() {
			if got.Add(1) == maxQueueLen+51 {
				return
			}
		}
	}()
	// eventually waits up to 5s for cond, which the viewers' goroutines
	// make true.
	eventually := func(cond func() bool) bool {
		for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				return false
			}
		}
		return true
	}
	src.send(pkts[0]) // the configuration
	if !eventually(func() bool { return got.Load() == 1 && len(slow.Packets()) > 0 }) {
		t.Fatal("the viewers did not get the configuration")
	}
	// Far more than the slow viewer may queue: pictures, then a key frame
	// near the end. The fast viewer reads each batch before the next goes
	// out: on a busy machine its goroutines may not run for a while, and it
	// must not fall behind as far as the slow one.
	for i := 0; i < maxQueueLen+50; i++ {
		p := pkts[2+i%9]
		if i == maxQueueLen+40 {
			p = pkts[1]
		}
		src.send(p)
		if sent := int64(i + 2); i%256 == 255 && !eventually(func() bool { return got.Load() >= sent }) {
			t.Fatalf("the fast viewer got %d of %d packets", got.Load(), sent)
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("the fast viewer got %d packets", got.Load())
	}
	// The slow viewer got the configuration and the few packets already on
	// their way, then the configuration again and the key frame it resumed
	// at, and the pictures after it.
	if p := recv(t, slow); !p.Config {
		t.Fatal("the slow viewer's first packet is not the configuration")
	}
	n := 0
	for {
		p := recv(t, slow)
		if p.Config {
			break
		}
		if n++; n > 8 {
			t.Fatalf("the slow viewer did not skip ahead: %d pictures before the key frame", n)
		}
	}
	if p := recv(t, slow); !p.KeyFrame {
		t.Fatal("the slow viewer did not resume at the key frame")
	}
	for i := 0; i < 9; i++ {
		recv(t, slow)
	}
}

// A viewer that joins while no key frame is kept (an iPhone sends one only
// when asked, and the pictures since the last one may have outgrown
// maxGOPBytes) asks the phone for one instead of waiting forever.
func TestLateViewerAsksForAKeyFrame(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	pkts := testPackets(t)
	a, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	src := v.open(t)
	// The configuration, then pictures without their key frame.
	src.send(pkts[0])
	for _, p := range pkts[2:5] {
		src.send(p)
	}
	for i := 0; i < 4; i++ {
		recv(t, a)
	}
	before := src.keyRequests()
	b, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for deadline := time.Now().Add(3 * time.Second); src.keyRequests() == before; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a late viewer with no key frame to start from did not ask the phone for one")
		}
	}
	if p := recv(t, b); !p.Config {
		t.Fatal("the late viewer's first packet is not the configuration")
	}
}

// A late viewer that gets the pictures since the last key frame does not
// ask for another.
func TestLateViewerWithAKeyFrameDoesNotAsk(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	pkts := testPackets(t)
	a, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	src := v.open(t)
	for _, p := range pkts[:5] {
		src.send(p)
	}
	for i := 0; i < 5; i++ {
		recv(t, a)
	}
	before := src.keyRequests()
	b, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	recv(t, b)
	if p := recv(t, b); !p.KeyFrame {
		t.Fatal("the late viewer did not start from the kept key frame")
	}
	time.Sleep(20 * time.Millisecond)
	if n := src.keyRequests(); n != before {
		t.Fatalf("%d key frame requests for a viewer that had one", n-before)
	}
}
