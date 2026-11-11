package video

import (
	"bytes"
	"context"
	"errors"
	"image"
	"os/exec"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

func TestSplitNALs(t *testing.T) {
	b := []byte{0, 0, 0, 1, 0x67, 1, 2, 0, 0, 1, 0x68, 3, 0, 0, 0, 1, 0x65, 4, 5, 0}
	nals := SplitNALs(b)
	want := [][]byte{{0x67, 1, 2}, {0x68, 3}, {0x65, 4, 5, 0}}
	if len(nals) != len(want) {
		t.Fatalf("got %d NALs, want %d: %x", len(nals), len(want), nals)
	}
	for i := range want {
		if !bytes.Equal(nals[i], want[i]) {
			t.Errorf("NAL %d = %x, want %x", i, nals[i], want[i])
		}
	}
}

func TestAccessUnits(t *testing.T) {
	aud := []byte{0, 0, 0, 1, 0x09, 0xf0}
	sps := []byte{0, 0, 0, 1, 0x67, 0x42}
	pps := []byte{0, 0, 0, 1, 0x68, 0xce}
	idr := []byte{0, 0, 0, 1, 0x65, 0x88, 0x84}
	p1 := []byte{0, 0, 1, 0x41, 0x9a}
	stream := bytes.Join([][]byte{aud, sps, pps, idr, aud, p1, aud, p1}, nil)

	// Feed it a byte at a time: units must not depend on read sizes.
	s := auSplitter{codec: driver.H264}
	var units [][]byte
	for i := range stream {
		units = append(units, s.push(stream[i:i+1])...)
	}
	if len(units) != 2 {
		t.Fatalf("got %d complete units, want 2 (the last stays buffered)", len(units))
	}
	pk := packetizer{codec: driver.H264}
	got := pk.packets(units[0])
	if len(got) != 2 || !got[0].Config || got[1].Config || !got[1].KeyFrame {
		t.Fatalf("first unit: got %+v, want a config packet and a key frame", got)
	}
	if !bytes.Equal(got[0].Data, append(append([]byte{}, sps...), pps...)) {
		t.Errorf("config = %x", got[0].Data)
	}
	if !bytes.Equal(got[1].Data, idr) {
		t.Errorf("key frame = %x, want %x", got[1].Data, idr)
	}
	got = pk.packets(units[1])
	if len(got) != 1 || got[0].KeyFrame || got[0].Config {
		t.Fatalf("second unit: got %+v, want one picture", got)
	}
}

func TestFitSize(t *testing.T) {
	for _, c := range []struct{ w, h, mw, mh, ow, oh int }{
		{1080, 2400, 0, 0, 1080, 2400},
		{1080, 2400, 2000, 3000, 1080, 2400},
		{1080, 2400, 540, 2400, 540, 1200},
		{1080, 2400, 600, 1264, 568, 1264},
	} {
		if w, h := fitSize(c.w, c.h, c.mw, c.mh); w != c.ow || h != c.oh {
			t.Errorf("fitSize(%d, %d, %d, %d) = %d, %d; want %d, %d", c.w, c.h, c.mw, c.mh, w, h, c.ow, c.oh)
		}
	}
}

// needFFmpeg skips tests that decode when FFmpeg is missing.
func needFFmpeg(t *testing.T) {
	t.Helper()
	if err := Load(); err != nil {
		if errors.Is(err, ErrNotFound) {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg command to make a test stream")
	}
}

func TestDecodeTestStream(t *testing.T) {
	needFFmpeg(t)
	// Which FFmpeg decodes, for TestPackagedFFmpeg.
	t.Logf("FFmpeg: %s", Version())
	for _, c := range []struct {
		name     string
		codec    driver.Codec
		software bool
	}{
		{"h264", driver.H264, false},
		{"h264-software", driver.H264, true},
		{"hevc", driver.HEVC, false},
		{"hevc-software", driver.HEVC, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			s, err := StartTestStream(ctx, TestStreamOptions{Codec: c.codec, Width: 360, Height: 800, FPS: 30})
			if err != nil {
				t.Fatal(err)
			}
			frames := make(chan struct{}, 64)
			p, err := NewPlayer(s, PlayerOptions{
				Decoder: DecoderOptions{Software: c.software},
				OnFrame: func() {
					select {
					case frames <- struct{}{}:
					default:
					}
				},
			})
			if err != nil {
				s.Close()
				t.Fatal(err)
			}
			defer p.Close()
			p.SetSize(180, 400)
			var got *Frame
			for got == nil || got.Seq < 10 {
				select {
				case <-frames:
					if f := p.Take(); f != nil {
						got = f
					}
				case <-p.Done():
					t.Fatalf("stream ended: %v", p.Err())
				case <-ctx.Done():
					t.Fatalf("no pictures: %+v", p.Stats())
				}
			}
			if got.Width != 360 || got.Height != 800 {
				t.Errorf("picture is %dx%d, want 360x800", got.Width, got.Height)
			}
			if b := got.Image.Bounds(); b != image.Rect(0, 0, 180, 400) {
				t.Errorf("converted to %v, want 180x400", b)
			}
			// testsrc2 is colorful: the picture must not be one flat color.
			colors := map[[3]byte]bool{}
			pix := got.Image.Pix
			for i := 0; i+3 < len(pix); i += 4 * 97 {
				colors[[3]byte{pix[i], pix[i+1], pix[i+2]}] = true
				if pix[i+3] != 255 {
					t.Fatalf("alpha %d at %d, want opaque", pix[i+3], i)
				}
			}
			if len(colors) < 8 {
				t.Errorf("only %d colors in the picture", len(colors))
			}
			st := p.Stats()
			if c.software && st.Hardware != "" {
				t.Errorf("software decoding used %q", st.Hardware)
			}
			t.Logf("%s: hardware=%q decoded=%d decode=%v convert=%v latency=%v", c.name, st.Hardware, st.Decoded, st.Decode, st.Convert, st.Latency)
		})
	}
}

func TestFrameStream(t *testing.T) {
	needFFmpeg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// A picture whose left half is red and right half blue.
	src := image.NewRGBA(image.Rect(0, 0, 320, 640))
	for y := 0; y < 640; y++ {
		for x := 0; x < 320; x++ {
			i := src.PixOffset(x, y)
			if x < 160 {
				src.Pix[i], src.Pix[i+3] = 255, 255
			} else {
				src.Pix[i+2], src.Pix[i+3] = 255, 255
			}
		}
	}
	s, err := FrameStream(ctx, 320, 640, 15, func() image.Image { return src })
	if err != nil {
		t.Fatal(err)
	}
	frames := make(chan struct{}, 8)
	p, err := NewPlayer(s, PlayerOptions{OnFrame: func() {
		select {
		case frames <- struct{}{}:
		default:
		}
	}})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	defer p.Close()
	var f *Frame
	for f == nil {
		select {
		case <-frames:
			f = p.Take()
		case <-ctx.Done():
			t.Fatalf("no picture: %+v", p.Stats())
		}
	}
	if f.Width != 320 || f.Height != 640 {
		t.Fatalf("picture %dx%d, want 320x640", f.Width, f.Height)
	}
	at := func(x, y int) (r, b byte) {
		i := f.Image.PixOffset(x, y)
		return f.Image.Pix[i], f.Image.Pix[i+2]
	}
	if r, b := at(40, 320); r < 200 || b > 60 {
		t.Errorf("left half is r=%d b=%d, want red", r, b)
	}
	if r, b := at(280, 320); b < 200 || r > 60 {
		t.Errorf("right half is r=%d b=%d, want blue", r, b)
	}
}

// Some phones tag their stream with a matrix swscale cannot convert, such
// as the YCgCo matrix of the Mi 10 Pro's encoder; the picture must still
// show.
func TestDecodeYCgCoStream(t *testing.T) {
	needFFmpeg(t)
	for _, software := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		s, err := StartTestStream(ctx, TestStreamOptions{Width: 360, Height: 800, FPS: 30, Matrix: 8})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		frames := make(chan struct{}, 64)
		p, err := NewPlayer(s, PlayerOptions{
			Decoder: DecoderOptions{Software: software},
			OnFrame: func() {
				select {
				case frames <- struct{}{}:
				default:
				}
			},
		})
		if err != nil {
			s.Close()
			cancel()
			t.Fatal(err)
		}
		p.SetSize(180, 400)
		var got *Frame
		for got == nil || got.Seq < 3 {
			select {
			case <-frames:
				if f := p.Take(); f != nil {
					got = f
				}
			case <-p.Done():
				t.Fatalf("software=%v: stream ended: %v", software, p.Err())
			case <-ctx.Done():
				t.Fatalf("software=%v: no pictures: %+v", software, p.Stats())
			}
		}
		p.Close()
		cancel()
	}
}
