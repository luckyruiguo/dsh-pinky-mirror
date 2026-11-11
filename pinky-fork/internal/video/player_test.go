package video

import (
	"context"
	"image"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// gatedStream hands over packets recorded beforehand only when the test
// sends them, so it can pause a stream as a still phone screen does.
type gatedStream struct {
	codec   driver.Codec
	w, h    int
	packets chan driver.Packet
}

func (s *gatedStream) Codec() driver.Codec           { return s.codec }
func (s *gatedStream) Size() (int, int)              { return s.w, s.h }
func (s *gatedStream) Packets() <-chan driver.Packet { return s.packets }
func (s *gatedStream) Err() error                    { return nil }
func (s *gatedStream) Close() error                  { return nil }

// A mirror of a still screen stops taking pictures until OnFrame wakes it.
// When the stream comes back after a pause longer than the player's idle
// second, the pictures after the first must still be converted: the mirror
// takes them, just not before the next has come.
func TestPlayerAfterPause(t *testing.T) {
	needFFmpeg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	src, err := StartTestStream(ctx, TestStreamOptions{Width: 320, Height: 640, FPS: 30})
	if err != nil {
		t.Fatal(err)
	}
	var recorded []driver.Packet
	for p := range src.Packets() {
		recorded = append(recorded, p)
		if len(recorded) == 8 {
			break
		}
	}
	src.Close()
	if len(recorded) < 8 {
		t.Fatalf("recorded %d packets: %v", len(recorded), src.Err())
	}

	s := &gatedStream{codec: driver.H264, w: 320, h: 640, packets: make(chan driver.Packet, 16)}
	frames := make(chan struct{}, 16)
	p, err := NewPlayer(s, PlayerOptions{YUV: true, SkipRepeats: true, OnFrame: func() { frames <- struct{}{} }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	defer close(s.packets)
	next := 0
	send := func() {
		// The configuration rides with the first key frame.
		for next < len(recorded) {
			pkt := recorded[next]
			next++
			s.packets <- pkt
			if !pkt.Config {
				return
			}
		}
	}
	wait := func(what string) {
		select {
		case <-frames:
		case <-ctx.Done():
			t.Fatalf("%s: no picture: %+v", what, p.Stats())
		}
	}

	send()
	wait("first")
	if !p.Pending() || p.Take() == nil || p.Pending() {
		t.Fatal("the first picture was not pending, or still is after Take")
	}
	// The screen stays still for longer than the idle second.
	time.Sleep(1200 * time.Millisecond)
	// Two pictures come before the woken mirror takes any.
	send()
	wait("second")
	send()
	wait("third")
	f := p.Take()
	st := p.Stats()
	if f == nil || f.Seq != 3 || st.Idle != 0 || st.Dropped != 1 {
		t.Fatalf("after the pause took %+v; stats %+v; want the third picture, none idle, one dropped", f, st)
	}
}

// A phone goes on sending its still screen for a while: pictures that
// decode to the same samples as the one before. The player hands over
// none of them and calls no OnFrame for them, so a sleeping mirror stays
// asleep. An encoder's key frame and the picture that refines it after are
// new pictures, as they differ a little.
func TestPlayerSkipsRepeats(t *testing.T) {
	needFFmpeg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := StartTestStream(ctx, TestStreamOptions{Width: 320, Height: 640, FPS: 30, Source: "smptehdbars"})
	if err != nil {
		t.Fatal(err)
	}
	var woke atomic.Int64
	p, err := NewPlayer(s, PlayerOptions{YUV: true, SkipRepeats: true, OnFrame: func() { woke.Add(1) }})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	for st := p.Stats(); st.Decoded+st.Repeats < 30; st = p.Stats() {
		select {
		case <-ctx.Done():
			t.Fatalf("too few pictures: %+v", st)
		case <-p.Done():
			t.Fatalf("the stream ended: %v", p.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	f := p.Take()
	p.Close()
	st := p.Stats()
	t.Logf("handed over %d pictures, skipped %d repeats", st.Decoded, st.Repeats)
	// One key frame a second, each followed by a picture refining it.
	if st.Repeats < 20 || st.Decoded < 1 || st.Decoded > 6 {
		t.Fatalf("stats %+v: want most of a still stream's pictures skipped as repeats", st)
	}
	// OnFrame came for each picture handed over, and once at the end.
	if n := woke.Load(); n != int64(st.Decoded)+1 {
		t.Fatalf("OnFrame called %d times for %d pictures handed over", n, st.Decoded)
	}
	if f == nil || f.Seq < 1 || f.Seq > st.Decoded {
		t.Fatalf("took %+v; stats %+v", f, st)
	}
}

func TestSamePicture(t *testing.T) {
	pic := func() *YUV {
		y := &YUV{Width: 4, Height: 2}
		y.layout(false, 4, 2)
		for i := range y.Planes {
			for j := range y.Planes[i] {
				y.Planes[i][j] = byte(16*i + j)
			}
		}
		return y
	}
	a, b := &Frame{YUV: pic(), Width: 4, Height: 2}, &Frame{YUV: pic(), Width: 4, Height: 2}
	if !samePicture(a, b) {
		t.Fatal("the same samples are not the same picture")
	}
	for i := range 3 {
		b.YUV.Planes[i][1]++
		if samePicture(a, b) {
			t.Errorf("a sample of plane %d changed, and the picture is the same", i)
		}
		b.YUV.Planes[i][1]--
	}
	b.YUV.Matrix = BT709
	if samePicture(a, b) {
		t.Error("other colors are the same picture")
	}
	b.YUV.Matrix = a.YUV.Matrix
	b.Turn = 1
	if samePicture(a, b) {
		t.Error("a turned picture is the same")
	}
	b.Turn = 0
	if samePicture(a, nil) || samePicture(nil, nil) {
		t.Error("no picture is the same as one, or as none")
	}
	r1 := &Frame{Image: image.NewRGBA(image.Rect(0, 0, 2, 2)), Width: 2, Height: 2}
	r2 := &Frame{Image: image.NewRGBA(image.Rect(0, 0, 2, 2)), Width: 2, Height: 2}
	if !samePicture(r1, r2) {
		t.Error("the same RGBA pictures differ")
	}
	r2.Image.Pix[5] = 1
	if samePicture(r1, r2) {
		t.Error("RGBA pictures that differ are the same")
	}
}

// The cost of telling a repeat from a new picture at an iPhone 13 mini's
// size: the whole of two equal pictures compared.
func BenchmarkYUVEqual(b *testing.B) {
	pic := func() *YUV {
		y := &YUV{NV12: true}
		y.layout(true, 1136, 2464)
		return y
	}
	x, y := pic(), pic()
	b.SetBytes(int64(len(x.Planes[0]) + len(x.Planes[1])))
	for b.Loop() {
		if !x.Equal(y) {
			b.Fatal("not equal")
		}
	}
}
