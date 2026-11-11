package video

import (
	"bytes"
	"context"
	"image"
	"math/rand/v2"
	"os/exec"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// flatYUV is a w×h picture of one color.
func flatYUV(nv12 bool, w, h int, y, cb, cr byte, m Matrix, full bool) *YUV {
	p := &YUV{Matrix: m, FullRange: full}
	p.layout(nv12, w, h)
	fill := func(b []byte, v byte) {
		for i := range b {
			b[i] = v
		}
	}
	fill(p.Planes[0], y)
	if nv12 {
		for i := 0; i < len(p.Planes[1]); i += 2 {
			p.Planes[1][i], p.Planes[1][i+1] = cb, cr
		}
	} else {
		fill(p.Planes[1], cb)
		fill(p.Planes[2], cr)
	}
	return p
}

func TestYUVRGBA(t *testing.T) {
	for _, c := range []struct {
		name      string
		y, cb, cr byte
		m         Matrix
		full      bool
		want      [3]byte
	}{
		{"BT.601 red", 81, 90, 240, BT601, false, [3]byte{255, 0, 0}},
		{"BT.709 red", 63, 102, 240, BT709, false, [3]byte{255, 0, 0}},
		{"BT.709 green", 173, 42, 26, BT709, false, [3]byte{0, 255, 0}},
		{"BT.2020 red", 74, 97, 240, BT2020, false, [3]byte{255, 0, 0}},
		{"video black", 16, 128, 128, BT709, false, [3]byte{0, 0, 0}},
		{"video white", 235, 128, 128, BT709, false, [3]byte{255, 255, 255}},
		{"full-range gray", 128, 128, 128, BT601, true, [3]byte{128, 128, 128}},
		{"full-range blue", 29, 255, 107, BT601, true, [3]byte{0, 0, 255}},
	} {
		for _, nv12 := range []bool{true, false} {
			img := flatYUV(nv12, 5, 3, c.y, c.cb, c.cr, c.m, c.full).RGBA()
			if b := img.Bounds(); b != image.Rect(0, 0, 5, 3) {
				t.Fatalf("%s: picture of %v", c.name, b)
			}
			for i := 0; i < len(img.Pix); i += 4 {
				for k := range 3 {
					if d := int(img.Pix[i+k]) - int(c.want[k]); d < -2 || d > 2 {
						t.Fatalf("%s (NV12 %v): pixel %d is %v, want %v", c.name, nv12, i/4, img.Pix[i:i+4], c.want)
					}
				}
				if img.Pix[i+3] != 255 {
					t.Fatalf("%s: alpha %d", c.name, img.Pix[i+3])
				}
			}
		}
	}
}

// Each chroma sample sits in the middle of the pixels it covers, as MyGo
// draws the planes: pixels between two samples take some of each.
func TestYUVRGBAChroma(t *testing.T) {
	p := flatYUV(false, 4, 2, 81, 128, 128, BT601, false)
	p.Planes[1][1], p.Planes[2][1] = 90, 240 // the right half's chroma is red's
	img := p.RGBA()
	var red [4]int
	for x := range 4 {
		red[x] = int(img.Pix[4*x])
	}
	if !(red[0] < red[1] && red[1] < red[2] && red[2] < red[3]) || red[3] < 250 {
		t.Errorf("red across the row = %v, want it to rise from gray to full", red)
	}
}

func TestYUVLayouts(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	i420 := &YUV{Matrix: BT709}
	i420.layout(false, 7, 5)
	for _, pl := range i420.Planes {
		for i := range pl {
			pl[i] = byte(rng.IntN(256))
		}
	}
	nv12 := &YUV{Matrix: BT709}
	nv12.layout(true, 7, 5)
	copy(nv12.Planes[0], i420.Planes[0])
	for i := range i420.Planes[1] {
		nv12.Planes[1][2*i], nv12.Planes[1][2*i+1] = i420.Planes[1][i], i420.Planes[2][i]
	}
	if nv12.Strides != [3]int{7, 8, 0} || i420.Strides != [3]int{7, 4, 4} || len(i420.Planes[1]) != 12 {
		t.Fatalf("strides NV12 %v, I420 %v, I420 chroma of %d bytes", nv12.Strides, i420.Strides, len(i420.Planes[1]))
	}
	if !bytes.Equal(nv12.RGBA().Pix, i420.RGBA().Pix) {
		t.Error("the same samples convert differently in NV12 and in I420")
	}
	c := i420.Clone()
	c.Planes[0][0]++
	if c.Planes[0][0] == i420.Planes[0][0] {
		t.Error("Clone shares the planes")
	}
}

// decodeOne decodes the stream to its n-th picture and returns the decoder
// holding it.
func decodeOne(t *testing.T, s driver.Stream, software bool, n int) *Decoder {
	t.Helper()
	dec, err := NewDecoder(s.Codec(), DecoderOptions{Software: software})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dec.Close)
	got := 0
	for pkt := range s.Packets() {
		if err := dec.Send(pkt); err != nil {
			t.Fatal(err)
		}
		for {
			_, ok, err := dec.Receive()
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			if got++; got == n {
				return dec
			}
		}
	}
	t.Fatalf("the stream ended after %d pictures: %v", got, s.Err())
	return nil
}

// medianDiff is the median of the pixels' largest difference in R, G or B
// between two pictures, in levels: pixels at the edges between colors,
// where chroma is interpolated in different ways, don't count.
func medianDiff(a, b *image.RGBA) int {
	var counts [256]int
	for i := 0; i < len(a.Pix); i += 4 {
		d := 0
		for k := range 3 {
			v := int(a.Pix[i+k]) - int(b.Pix[i+k])
			d = max(d, v, -v)
		}
		counts[d]++
	}
	half := len(a.Pix) / 8
	for d, n := range counts {
		if half -= n; half < 0 {
			return d
		}
	}
	return 255
}

// decodeYUV decodes the n-th picture of a stream into planes.
func decodeYUV(t *testing.T, s driver.Stream, software bool, n int) (*Decoder, *YUV) {
	t.Helper()
	dec := decodeOne(t, s, software, n)
	var y YUV
	if err := dec.ConvertYUV(&y); err != nil {
		t.Fatal(err)
	}
	return dec, &y
}

// TestConvertYUV checks that the planes ConvertYUV hands over show as
// Convert's RGBA shows the picture: in the matrix swscale reads from the
// stream's tag, and in the layout the decoder gives.
func TestConvertYUV(t *testing.T) {
	needFFmpeg(t)
	for _, c := range []struct {
		name     string
		codec    driver.Codec
		matrix   int // the stream's tag (H.273); 0 names none
		software bool
		want     Matrix
	}{
		{"untagged", driver.H264, 0, false, BT601},
		{"untagged-software", driver.H264, 0, true, BT601},
		{"bt709", driver.H264, 1, false, BT709},
		{"bt709-software", driver.H264, 1, true, BT709},
		{"bt2020-software", driver.H264, 9, true, BT2020},
		{"ycgco", driver.H264, 8, false, BT709},
		{"ycgco-software", driver.H264, 8, true, BT709},
		{"hevc", driver.HEVC, 0, false, BT601},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			// Bars of full colors, which every matrix shows in others.
			s, err := StartTestStream(ctx, TestStreamOptions{Codec: c.codec, Width: 360, Height: 800, FPS: 30, Source: "pal100bars", Matrix: c.matrix})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			dec, y := decodeYUV(t, s, c.software, 5)
			rgba := image.NewRGBA(image.Rect(0, 0, y.Width, y.Height))
			if err := dec.Convert(rgba); err != nil {
				t.Fatal(err)
			}
			hw := dec.Hardware() != ""
			if y.Width != 360 || y.Height != 800 || y.Matrix != c.want || y.FullRange {
				t.Errorf("%dx%d in matrix %d (full range %v), want 360x800 in %d", y.Width, y.Height, y.Matrix, y.FullRange, c.want)
			}
			if y.NV12 != hw {
				t.Errorf("NV12 = %v from the %q decoder", y.NV12, dec.Hardware())
			}
			got := medianDiff(y.RGBA(), rgba)
			for m := BT601; m <= BT2020; m++ {
				if m == y.Matrix {
					continue
				}
				other := *y
				other.Matrix = m
				wrong := medianDiff(other.RGBA(), rgba)
				t.Logf("decoder %q: %d levels from Convert's RGBA; %d in matrix %d", dec.Hardware(), got, wrong, m)
				if got > 4 || wrong < got+5 {
					t.Errorf("the planes show %d levels from Convert's RGBA, and %d in matrix %d", got, wrong, m)
				}
			}
		})
	}
}

// hevcBars starts a stream of ffmpeg's full color bars in HEVC, in the
// pixel format given ("yuv420p10le" for 10 bits), or skips the test when
// ffmpeg has no libx265 to make it.
func hevcBars(t *testing.T, ctx context.Context, pixFmt string) *TestStream {
	t.Helper()
	if out, _ := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output(); !bytes.Contains(out, []byte("libx265")) {
		t.Skip("no libx265 to make a 10-bit stream")
	}
	ffmpeg, _ := exec.LookPath("ffmpeg")
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-re", "-f", "lavfi", "-i", "pal100bars=size=360x800:rate=30",
		"-pix_fmt", pixFmt, "-c:v", "libx265", "-preset", "ultrafast", "-tune", "zerolatency", "-x265-params", "log-level=error",
		"-bsf:v", "hevc_metadata=aud=insert", "-f", "hevc", "-"}
	s, err := startFFmpeg(ctx, ffmpeg, args, TestStreamOptions{Codec: driver.HEVC, Width: 360, Height: 800, FPS: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// A picture in another layout, as 10-bit HEVC's, is converted to I420: it
// shows as the same picture in 8 bits.
func TestConvertYUVTenBit(t *testing.T) {
	needFFmpeg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, want := decodeYUV(t, hevcBars(t, ctx, "yuv420p"), true, 5)
	for _, software := range []bool{false, true} {
		dec, y := decodeYUV(t, hevcBars(t, ctx, "yuv420p10le"), software, 5)
		if y.NV12 || y.Width != 360 || y.Height != 800 || y.Matrix != BT601 {
			t.Errorf("software=%v: decoder %q gave %dx%d, NV12 %v, matrix %d; want I420 360x800 in BT.601", software, dec.Hardware(), y.Width, y.Height, y.NV12, y.Matrix)
		}
		if d := medianDiff(y.RGBA(), want.RGBA()); d > 3 {
			t.Errorf("software=%v: the 10-bit picture shows %d levels from the 8-bit one", software, d)
		}
	}
}

// Convert turns a 10-bit picture into RGBA, as the gateway's screenshots
// from a phone's video do (frameshot.go): the same picture as in 8 bits.
// It still does after ConvertYUV converted the picture to I420, which
// used to leave reserved colors on the frame Convert describes its RGBA
// with, so that swscale refused it.
func TestConvertTenBit(t *testing.T) {
	needFFmpeg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dec8 := decodeOne(t, hevcBars(t, ctx, "yuv420p"), true, 5)
	want := image.NewRGBA(image.Rect(0, 0, 360, 800))
	if err := dec8.Convert(want); err != nil {
		t.Fatal(err)
	}
	for _, software := range []bool{false, true} {
		dec := decodeOne(t, hevcBars(t, ctx, "yuv420p10le"), software, 5)
		got := image.NewRGBA(image.Rect(0, 0, 360, 800))
		if err := dec.Convert(got); err != nil {
			t.Fatalf("software=%v: decoder %q: %v", software, dec.Hardware(), err)
		}
		if d := medianDiff(got, want); d > 3 {
			t.Errorf("software=%v: the 10-bit picture shows %d levels from the 8-bit one", software, d)
		}
		var y YUV
		if err := dec.ConvertYUV(&y); err != nil {
			t.Fatal(err)
		}
		again := image.NewRGBA(image.Rect(0, 0, 360, 800))
		if err := dec.Convert(again); err != nil {
			t.Fatalf("software=%v: after ConvertYUV: %v", software, err)
		}
		if !bytes.Equal(again.Pix, got.Pix) {
			t.Errorf("software=%v: after ConvertYUV, Convert shows %d levels from before", software, medianDiff(again, got))
		}
	}
}

func TestPlayerYUV(t *testing.T) {
	needFFmpeg(t)
	for _, software := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		s, err := StartTestStream(ctx, TestStreamOptions{Width: 360, Height: 800, FPS: 30})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		frames := make(chan struct{}, 64)
		p, err := NewPlayer(s, PlayerOptions{
			YUV:     true,
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
		p.SetSize(180, 400) // planes keep the stream's size
		var got *Frame
		for got == nil || got.Seq < 5 {
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
		st := p.Stats()
		p.Close()
		cancel()
		y := got.YUV
		if got.Image != nil || y == nil || y.Width != 360 || y.Height != 800 || st.OutW != 360 || st.OutH != 800 {
			t.Fatalf("software=%v: frame %+v, planes %+v, stats %+v", software, got, y, st)
		}
		if y.NV12 != (st.Hardware != "") {
			t.Errorf("software=%v: NV12 = %v from the %q decoder", software, y.NV12, st.Hardware)
		}
		if len(y.Planes[0]) != 360*800 {
			t.Errorf("software=%v: luma of %d bytes", software, len(y.Planes[0]))
		}
	}
}
