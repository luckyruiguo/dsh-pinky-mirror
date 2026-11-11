package gateway

import (
	"context"
	"errors"
	"fmt"
	"image"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/video"
)

// Screenshots from the video. An Android background display cannot be
// captured with screencap, which reaches physical displays only, so its
// driver returns ErrUnsupported; the gateway then decodes a picture of the
// display's live video, which it shares with the mirror and the recorder.
// Approval cards, history and phone_observe's screenshot all need one.

const (
	// frameShotTimeout bounds getting a picture from the video.
	frameShotTimeout = 5 * time.Second
	// A late joiner first gets the packets since the last key frame, which
	// may be seconds old, in a burst, then the live ones. frameShot asks
	// for a fresh key frame and returns the first picture decoded from it
	// or later; a still screen sends nothing more, so a pause of
	// frameShotGap after a picture also ends it.
	frameShotGap = 150 * time.Millisecond
	// frameShotMore bounds the wait for a stream that cannot be asked for
	// a key frame: it returns the newest picture this long after the first.
	frameShotMore = 400 * time.Millisecond
)

// shotOf takes a screenshot of the screen dev shows (the phone's own, or
// the lease's background display when background is set): from the video
// first when the driver prefers it (driver.FrameShooter: an iPhone over
// Wi-Fi), and when the driver cannot take one.
func (g *Gateway) shotOf(ctx context.Context, d *device, dev driver.Device, background bool) (image.Image, error) {
	if fs, ok := dev.(driver.FrameShooter); ok {
		key := hubKey(d.id, background)
		if img, ok := fs.ScreenshotFromFrame(ctx, func(ctx context.Context) (image.Image, error) { return g.frameShot(ctx, key, dev) }); ok {
			return img, nil
		}
	}
	img, err := dev.Screenshot(ctx)
	if err == nil || !errors.Is(err, driver.ErrUnsupported) {
		return img, err
	}
	if img, ferr := g.frameShot(ctx, hubKey(d.id, background), dev); ferr == nil {
		return img, nil
	}
	return nil, err
}

// frameShot decodes the newest picture of a screen's video: it joins the
// screen's shared stream, which starts a late joiner at the last key frame
// (or opens the stream, which starts with one), and keeps the last picture
// that arrives until the stream goes quiet.
func (g *Gateway) frameShot(ctx context.Context, key string, dev driver.Device) (image.Image, error) {
	if err := video.Load(); err != nil {
		return nil, fmt.Errorf("no screenshot: FFmpeg is missing (%v): %w", err, driver.ErrUnsupported)
	}
	ctx, cancel := context.WithTimeout(ctx, frameShotTimeout)
	defer cancel()
	st, err := g.watch(ctx, key, dev, driver.StreamOptions{})
	if err != nil {
		return nil, err
	}
	return newestFrame(ctx, st)
}

// frameShotOpen is frameShot from a stream that is open already: it never
// opens one, which waits for the phone's input (dragfollow.go).
func (g *Gateway) frameShotOpen(ctx context.Context, key string) (image.Image, error) {
	if err := video.Load(); err != nil {
		return nil, fmt.Errorf("no screenshot: FFmpeg is missing (%v): %w", err, driver.ErrUnsupported)
	}
	ctx, cancel := context.WithTimeout(ctx, frameShotTimeout)
	defer cancel()
	st, err := g.watchOpen(ctx, key)
	if err != nil {
		return nil, err
	}
	return newestFrame(ctx, st)
}

// newestFrame decodes the newest picture of st and closes it.
func newestFrame(ctx context.Context, st driver.Stream) (image.Image, error) {
	defer st.Close()
	dec, err := video.NewDecoder(st.Codec(), video.DecoderOptions{})
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	var img *image.RGBA
	var gap, enough <-chan time.Time
	asked, canAsk, keys := false, false, 0
	var fresh time.Duration // the PTS of the key frame asked for
	v, _ := st.(*viewer)
	for {
		select {
		case p, ok := <-st.Packets():
			if !ok {
				if img != nil {
					return img, nil
				}
				if err := st.Err(); err != nil {
					return nil, err
				}
				return nil, errors.New("the video ended before a picture")
			}
			if !asked && v != nil {
				// The stream is open now: ask for a key frame of the screen
				// as it is.
				asked = true
				canAsk = v.h.canRequestKeyFrame()
				v.h.requestKeyFrame()
			}
			if p.KeyFrame {
				if keys++; keys == 2 {
					fresh = p.PTS
				}
			}
			if err := dec.Send(p); err != nil {
				continue // a packet the decoder refused; the next key frame recovers
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
				if keys >= 2 && pic.PTS >= fresh {
					return img, nil // the screen as it is now
				}
			}
			if img != nil {
				gap = time.After(frameShotGap)
				if !canAsk && enough == nil {
					enough = time.After(frameShotMore)
				}
			}
		case <-gap:
			return img, nil
		case <-enough:
			return img, nil
		case <-ctx.Done():
			if img != nil {
				return img, nil
			}
			return nil, fmt.Errorf("no picture from the video: %w", ctx.Err())
		}
	}
}
