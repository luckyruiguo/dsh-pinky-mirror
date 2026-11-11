//go:build device

package ios

import (
	"context"
	"errors"
	"image"
	"os"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/video"
)

// TestDeviceOCRFromVideo compares the two pictures OCR can read over one
// connection (PINKY_CONN, "wifi" most useful): screenshots, and frames of
// the live video as the gateway offers them over Wi-Fi. It only reads the
// screen, presses nothing, and logs timings and line counts, not the text.
// Run it with the phone unlocked.
func TestDeviceOCRFromVideo(t *testing.T) {
	conn := os.Getenv("PINKY_CONN")
	if conn == "" {
		t.Skip("PINKY_CONN not set")
	}
	if err := video.Load(); err != nil {
		t.Skip(err)
	}
	ctx := ctxFor(t, 3*time.Minute)
	d, err := Open(ctx, driver.Info{ID: os.Getenv("PINKY_DEVICE"), Platform: driver.IOS, Connection: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if locked, err := d.Locked(ctx); err != nil || locked {
		t.Fatalf("the phone is locked (%v, %v): unlock it first", locked, err)
	}
	frame := func(ctx context.Context) (image.Image, error) { return videoFrame(ctx, d) }
	for _, c := range []struct {
		name string
		opt  driver.ObserveOptions
	}{
		{"screenshot", driver.ObserveOptions{OCR: true}},
		{"video", driver.ObserveOptions{OCR: true, Frame: frame}},
		{"screenshot", driver.ObserveOptions{OCR: true}},
		{"video", driver.ObserveOptions{OCR: true, Frame: frame}},
	} {
		t0 := time.Now()
		s, st, err := d.ObserveWithStats(ctx, c.opt)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		t.Logf("%-10s read %v: picture from %s in %v, OCR %v, %d lines, %d elements (%s)",
			c.name, time.Since(t0).Round(time.Millisecond), st.ShotFrom, st.Shot.Round(time.Millisecond), st.OCR.Round(time.Millisecond),
			st.OCRLines, len(s.Elements), st.Source)
		time.Sleep(time.Second)
	}
}

// videoFrame decodes the newest picture of the device's live video, as the
// gateway's frameShot does: a key frame, then the picture at or after it.
func videoFrame(ctx context.Context, d *Device) (image.Image, error) {
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
	if k, ok := st.(interface{ RequestKeyFrame() }); ok {
		k.RequestKeyFrame()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var img *image.RGBA
	for {
		select {
		case p, ok := <-st.Packets():
			if !ok {
				if img != nil {
					return img, nil
				}
				return nil, errors.New("the video ended")
			}
			if err := dec.Send(p); err != nil {
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
				return img, nil
			}
		case <-ctx.Done():
			if img != nil {
				return img, nil
			}
			return nil, ctx.Err()
		}
	}
}
