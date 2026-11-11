package fakeapi

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os/exec"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/video"
)

// startStream starts an ffmpeg test source for a device.
func startStream(ctx context.Context, ffmpeg string, codec driver.Codec, source string, w, h, fps int) (driver.Stream, error) {
	return video.StartTestStream(ctx, video.TestStreamOptions{
		Codec: codec, Width: w, Height: h, FPS: fps, Source: source, FFmpeg: ffmpeg,
	})
}

// screenshot returns a still of the device's test source, as the gateway
// would attach the screen to an approval. It is made once per device.
func (a *API) screenshot(d *device) image.Image {
	a.mu.Lock()
	id, w, h, src := d.info.ID, d.width, d.height, d.source
	if img, ok := a.shotCache[id]; ok {
		a.mu.Unlock()
		return img
	}
	a.mu.Unlock()

	img, err := grabFrame(a.opt.FFmpeg, src, w, h)
	if err != nil {
		img = placeholder(w, h)
	}
	a.mu.Lock()
	a.shotCache[id] = img
	a.mu.Unlock()
	return img
}

func grabFrame(ffmpeg, src string, w, h int) (image.Image, error) {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// A moment into the source, so testsrc2's moving parts have moved.
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", fmt.Sprintf("%s=size=%dx%d:rate=30", src, w, h),
		"-ss", "1.5", "-frames:v", "1", "-f", "image2pipe", "-c:v", "png", "-")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return png.Decode(bytes.NewReader(out))
}

// placeholder is a plain picture for when ffmpeg is missing.
func placeholder(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		c := color.RGBA{R: uint8(40 + 60*y/h), G: uint8(44 + 50*y/h), B: uint8(60 + 80*y/h), A: 255}
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}
