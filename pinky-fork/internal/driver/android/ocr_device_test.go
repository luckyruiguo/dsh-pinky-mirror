//go:build device

package android

import (
	"context"
	"errors"
	"image"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/video"
)

// TestDeviceOCR reads the screen in front with OCR from a screenshot, from
// a frame of the video and with all the text, and logs what each added and
// how long it took. It presses nothing: put the page to read on the
// screen first. On an emulator, PINKY_OCR_URL opens a page in Chrome
// first (an emulator is Pinky's own).
//
//	scripts/devlock.sh <id> env PINKY_DEVICE=<id> go test -tags device -run TestDeviceOCR -v ./internal/driver/android/
func TestDeviceOCR(t *testing.T) {
	if err := ocrSupported(); err != nil {
		t.Skip(err)
	}
	d, ctx := openTestDevice(t)
	if url := os.Getenv("PINKY_OCR_URL"); url != "" {
		if !strings.HasPrefix(d.Serial(), "emulator-") {
			t.Fatal("PINKY_OCR_URL opens pages on emulators only")
		}
		if _, err := d.client.Shell(ctx, d.Serial(), "am start -a android.intent.action.VIEW -n com.android.chrome/com.google.android.apps.chrome.Main -d '"+url+"'"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
	}
	frame := func(ctx context.Context) (image.Image, error) {
		start := time.Now()
		img, err := videoFrame(ctx, d)
		t.Logf("frame: %v, %v", time.Since(start).Round(time.Millisecond), err)
		return img, err
	}
	for _, c := range []struct {
		name string
		opt  driver.ObserveOptions
	}{
		{"tree only", driver.ObserveOptions{}},
		{"OCR from a screenshot", driver.ObserveOptions{OCR: true}},
		{"OCR from the video", driver.ObserveOptions{OCR: true, Frame: frame}},
		{"OCR from the video, again", driver.ObserveOptions{OCR: true, Frame: frame}},
		{"all the text, from the video", driver.ObserveOptions{OCR: true, OCRAll: true, Frame: frame}},
	} {
		start := time.Now()
		s, st, err := d.ObserveWithText(ctx, c.opt)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var lines []string
		for _, e := range s.Elements {
			if e.Source == screen.SourceOCR || e.Has(stateNoOCR) {
				lines = append(lines, e.Line())
			}
		}
		t.Logf("%s: %v in all; %s %s; %d elements; %+v\n  %s", c.name, time.Since(start).Round(time.Millisecond),
			s.App.ID, s.Header(), len(s.Elements), st, strings.Join(lines, "\n  "))
		time.Sleep(300 * time.Millisecond)
	}
}

// videoFrame decodes the newest picture of the phone's video, as the
// gateway's frameShot does: it asks for a key frame and decodes until a
// picture from it, or until the stream pauses.
func videoFrame(ctx context.Context, d *Device) (image.Image, error) {
	if err := video.Load(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := d.Stream(ctx, driver.StreamOptions{})
	if err != nil {
		return nil, err
	}
	defer st.Close()
	dec, err := video.NewDecoder(st.Codec(), video.DecoderOptions{})
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	var img *image.RGBA
	keys := 0
	var gap <-chan time.Time
	for {
		select {
		case p, ok := <-st.Packets():
			if !ok {
				if img != nil {
					return img, nil
				}
				return nil, errors.New("the video ended")
			}
			if p.KeyFrame {
				keys++
			}
			if dec.Send(p) != nil {
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
					return nil, err
				}
				if keys >= 1 {
					gap = time.After(150 * time.Millisecond)
				}
			}
		case <-gap:
			return img, nil
		case <-ctx.Done():
			if img != nil {
				return img, nil
			}
			return nil, ctx.Err()
		}
	}
}
