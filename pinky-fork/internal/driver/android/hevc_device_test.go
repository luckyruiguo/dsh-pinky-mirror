//go:build device

package android

import (
	"context"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/recording"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/video"
)

// TestDeviceHEVCOnComputer sends a phone's H.265 stream (its default over
// Wi-Fi; PINKY_VIDEO_CODEC=h265 forces it elsewhere) through what the
// computer does with it: the mirror's decoder, a key frame asked for
// midway (recordings after a pause, screenshots from the video), and a
// recording written and played back. It works on a background display, so
// the phone's screen must be off and the phone locked.
//
// PINKY_FFMPEG_DIR=build/ffmpeg/lib tests the FFmpeg that Pinky.app ships.
func TestDeviceHEVCOnComputer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	d := openOff(t, ctx)
	if c := os.Getenv("PINKY_VIDEO_CODEC"); c != "" {
		d.cfg.VideoCodec = c
	}
	if err := video.Load(); err != nil {
		t.Skipf("no FFmpeg: %v", err)
	}
	t.Logf("FFmpeg %s", video.Version())
	b, err := d.OpenDisplay(ctx, DisplayOptions{App: "com.android.settings"})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	st, err := b.Stream(ctx, driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Codec() != driver.HEVC {
		t.Fatalf("the stream is %s, want H.265", st.Codec())
	}
	dec, err := video.NewDecoder(st.Codec(), video.DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	path := filepath.Join(t.TempDir(), "hevc.mp4")
	rec, err := recording.Create(path, st.Codec())
	if err != nil {
		t.Fatal(err)
	}
	var img *image.RGBA
	pictures, keys, configs := 0, 0, 0
	keyAfterAsk := time.Duration(-1)
	var asked time.Time
	begin := time.Now()
	swiped := 0
	for time.Since(begin) < 8*time.Second {
		if el := time.Since(begin); int(el/time.Second) > swiped {
			swiped++
			from, to := screen.Point{X: 540, Y: 1700}, screen.Point{X: 540, Y: 700}
			if swiped%2 == 0 {
				from, to = to, from
			}
			go b.Swipe(ctx, from, to, 300*time.Millisecond)
			if swiped == 4 {
				asked = time.Now()
				st.(interface{ RequestKeyFrame() }).RequestKeyFrame()
			}
		}
		var p driver.Packet
		select {
		case p = <-st.Packets():
		case <-time.After(200 * time.Millisecond):
			continue
		}
		switch {
		case p.Config:
			configs++
		case p.KeyFrame:
			keys++
			if !asked.IsZero() && keyAfterAsk < 0 {
				keyAfterAsk = time.Since(asked)
			}
		}
		if err := rec.Write(p, time.Since(begin)); err != nil {
			t.Fatalf("recording: %v", err)
		}
		if err := dec.Send(p); err != nil {
			t.Errorf("decoder refused a packet: %v", err)
			continue
		}
		for {
			pic, ok, err := dec.Receive()
			if err != nil || !ok {
				break
			}
			if img == nil || img.Rect.Dx() != pic.Width || img.Rect.Dy() != pic.Height {
				img = image.NewRGBA(image.Rect(0, 0, pic.Width, pic.Height))
			}
			if err := dec.Convert(img); err != nil {
				t.Fatalf("convert: %v", err)
			}
			pictures++
		}
	}
	w, h := st.Size()
	hw, sw, _ := dec.Stats()
	t.Logf("live: %dx%d, %d pictures decoded (%d in hardware, %d in software, %q), %d key frames, %d configurations; a key frame %v after asking",
		w, h, pictures, hw, sw, dec.Hardware(), keys, configs, keyAfterAsk.Round(time.Millisecond))
	if pictures < 30 || img == nil || img.Rect.Dx() != w || img.Rect.Dy() != h {
		t.Errorf("decoded %d pictures, last %v, want many of %dx%d", pictures, img, w, h)
	}
	if keyAfterAsk < 0 || keyAfterAsk > 2*time.Second || configs < 2 {
		t.Errorf("a key frame %v after asking, %d configurations: the encoder did not start over", keyAfterAsk, configs)
	}
	if err := rec.Close(time.Since(begin)); err != nil {
		t.Fatal(err)
	}
	info, err := recording.Probe(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("recording: %+v", info)
	if info.Width != w || info.Height != h || info.Frames < 30 {
		t.Errorf("recording %+v, want %dx%d with frames", info, w, h)
	}
	play, err := recording.Open(ctx, path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer play.Close()
	pdec, err := video.NewDecoder(play.Codec(), video.DecoderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer pdec.Close()
	played := 0
	for p := range play.Packets() {
		if err := pdec.Send(p); err != nil {
			continue
		}
		for {
			_, ok, err := pdec.Receive()
			if err != nil || !ok {
				break
			}
			played++
		}
	}
	t.Logf("played back: %d pictures (%v)", played, play.Err())
	if played < info.Frames*9/10 {
		t.Errorf("played back %d pictures of %d", played, info.Frames)
	}
	if out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,codec_tag_string,width,height",
		"-of", "csv=p=0", path).Output(); err == nil {
		t.Logf("ffprobe: %s", strings.TrimSpace(string(out)))
		if !strings.Contains(string(out), "hevc,hvc1") {
			t.Errorf("ffprobe: %s, want hevc in hvc1", out)
		}
	}
}
