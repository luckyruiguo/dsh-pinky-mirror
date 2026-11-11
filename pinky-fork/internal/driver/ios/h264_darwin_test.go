package ios

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPicture(w, h, shift int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x + shift), uint8(y), uint8(x ^ y), 255})
		}
	}
	return img
}

func TestH264Encoder(t *testing.T) {
	if err := h264Available(); err != nil {
		t.Skip(err)
	}
	const w, h = 320, 640
	e, err := newH264Encoder(w, h, 15, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var stream bytes.Buffer
	for i := 0; i < 6; i++ {
		y, uv, _, _ := toNV12(testPicture(w, h, i*8))
		out, err := e.encode(y, uv, time.Duration(i)*66*time.Millisecond, i == 0 || i == 4)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if len(out) != 1 {
			t.Fatalf("frame %d: %d outputs", i, len(out))
		}
		f := out[0]
		wantKey := i == 0 || i == 4
		if f.keyFrame != wantKey || (wantKey && len(f.config) == 0) || len(f.data) == 0 {
			t.Fatalf("frame %d: key %v config %d data %d", i, f.keyFrame, len(f.config), len(f.data))
		}
		if wantKey {
			stream.Write(f.config)
			if f.config[4]&0x1f != 7 { // SPS first
				t.Fatalf("frame %d: config starts with NAL %d", i, f.config[4]&0x1f)
			}
		}
		stream.Write(f.data)
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe to check the stream")
	}
	file := filepath.Join(t.TempDir(), "t.h264")
	os.WriteFile(file, stream.Bytes(), 0o600)
	out, err := exec.Command(probe, "-v", "error", "-count_frames", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,profile,width,height,nb_read_frames", "-of", "csv=p=0", file).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "h264,Main,320,640,6" {
		t.Fatalf("ffprobe: %v %q", err, out)
	}
}

// TestFrameStream runs frames through startFrameStream: packets in order,
// a configuration before the first key frame, unchanged frames skipped.
func TestFrameStream(t *testing.T) {
	if err := h264Available(); err != nil {
		t.Skip(err)
	}
	src := &fakeFrames{}
	for i := 0; i < 5; i++ {
		key := uint64(i / 2) // pairs of equal frames
		pic := testPicture(160, 320, int(key)*16)
		src.frames = append(src.frames, frame{key: key + 1, decode: func() (image.Image, error) { return pic, nil }})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := startFrameStream(ctx, func(context.Context) (frameSource, error) { return src, nil }, 160, 320)
	if err != nil {
		t.Fatal(err)
	}
	if w, h := s.Size(); w != 160 || h != 320 {
		t.Fatalf("size %dx%d", w, h)
	}
	var kinds []string
	timeout := time.After(5 * time.Second)
	want := 4
read:
	for len(kinds) < want+1 {
		if len(kinds) == want {
			timeout = time.After(300 * time.Millisecond) // nothing more should come
		}
		select {
		case p, ok := <-s.Packets():
			if !ok {
				break read
			}
			switch {
			case p.Config:
				kinds = append(kinds, "config")
			case p.KeyFrame:
				kinds = append(kinds, "key")
			default:
				kinds = append(kinds, "delta")
			}
		case <-timeout:
			break read
		}
	}
	// Five frames, three distinct: config, key, delta, delta, and nothing
	// for the repeats.
	if got := strings.Join(kinds, ","); got != "config,key,delta,delta" {
		t.Fatalf("packets %s", got)
	}
	s.Close()
	if err := s.Err(); err != nil {
		t.Fatalf("err %v", err)
	}
	if _, ok := <-s.Packets(); ok {
		t.Fatal("packets after Close")
	}
}

type fakeFrames struct {
	frames []frame
	i      int
}

func (f *fakeFrames) next(ctx context.Context) (frame, error) {
	if f.i >= len(f.frames) {
		<-ctx.Done() // a source that ends ends the stream
		return frame{}, ctx.Err()
	}
	f.i++
	return f.frames[f.i-1], nil
}

func (f *fakeFrames) Close() error { return nil }
