package ios

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestVideoFrame: over Wi-Fi a read takes the screen from a frame of the
// video, waits frameWait for it at most, and after a frame that did not
// come uses screenshots for frameRetry; on a cable it never asks.
func TestVideoFrame(t *testing.T) {
	defer func(w time.Duration) { frameWait = w }(frameWait)
	frameWait = 50 * time.Millisecond
	ctx := context.Background()
	d := &Device{info: driver.Info{Connection: "wifi"}, scr: screenInfo{Width: 4, Height: 6}}
	asked := 0
	good := func(context.Context) (image.Image, error) {
		asked++
		return image.NewRGBA(image.Rect(0, 0, 10, 12)), nil
	}
	slow := func(ctx context.Context) (image.Image, error) { asked++; <-ctx.Done(); return nil, ctx.Err() }

	if img, ok := d.videoFrame(ctx, good); !ok || img.Bounds().Dx() != 4 || img.Bounds().Dy() != 6 {
		t.Fatalf("a frame: %v, %v; want the 4x6 screen cropped from it", ok, img)
	}
	if _, ok := d.videoFrame(ctx, nil); ok {
		t.Fatal("no frame offered, yet one was taken")
	}
	usb := &Device{info: driver.Info{Connection: "usb"}, scr: d.scr}
	asked = 0
	if _, ok := usb.videoFrame(ctx, good); ok || asked != 0 {
		t.Fatalf("on a cable: %v, asked %d; want a screenshot", ok, asked)
	}

	start := time.Now()
	if _, ok := d.videoFrame(ctx, slow); ok || time.Since(start) > time.Second {
		t.Fatalf("a frame that does not come: %v after %v", ok, time.Since(start))
	}
	asked = 0
	if _, ok := d.videoFrame(ctx, good); ok || asked != 0 {
		t.Fatalf("right after a frame failed: %v, asked %d; want a screenshot without asking", ok, asked)
	}
	d.mu.Lock()
	d.framesAfter = time.Now().Add(-time.Second) // frameRetry passed
	d.mu.Unlock()
	if _, ok := d.videoFrame(ctx, good); !ok || asked != 1 {
		t.Fatalf("after frameRetry: %v, asked %d", ok, asked)
	}

	// A read that gave up is not the frame's fault.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	d.videoFrame(cctx, slow)
	d.mu.Lock()
	skipping := time.Now().Before(d.framesAfter)
	d.mu.Unlock()
	if skipping {
		t.Fatal("a read that gave up made the phone skip frames")
	}
}

func TestAsleepRead(t *testing.T) {
	ctx := context.Background()
	off := func(context.Context) (bool, error) { return false, nil }
	on := func(context.Context) (bool, error) { return true, nil }
	broken := func(context.Context) (bool, error) { return false, errors.New("no answer") }
	empty := &screen.Screen{}
	one := &screen.Screen{Elements: []screen.Element{{Label: "12:41"}}}
	two := &screen.Screen{Elements: []screen.Element{{Label: "Wi-Fi"}, {Label: "Bluetooth"}}}
	for _, c := range []struct {
		name string
		s    *screen.Screen
		on   func(context.Context) (bool, error)
		want bool
	}{
		{"dark, nothing read", empty, off, true},
		{"dark, one element", one, off, true},
		{"lit, nothing read", empty, on, false},
		{"a screen with elements", two, off, false},
		{"already locked", &screen.Screen{Locked: true}, off, false},
		{"the screen state does not answer", empty, broken, false},
	} {
		if got := asleepRead(ctx, c.s, c.on); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWiFiReadLimits(t *testing.T) {
	wifi := &Device{info: driver.Info{Connection: "wifi"}}
	usb := &Device{info: driver.Info{Connection: "usb"}}
	if wifi.walkLimits().Budget <= usb.walkLimits().Budget {
		t.Errorf("walk budget over Wi-Fi %v, on a cable %v", wifi.walkLimits().Budget, usb.walkLimits().Budget)
	}
	wi, wc := shotTimeouts("wifi")
	ui, uc := shotTimeouts("usb")
	if wi <= 5*time.Second || wc < wi || ui != 5*time.Second || uc != 10*time.Second {
		t.Errorf("screenshot timeouts: Wi-Fi %v/%v, cable %v/%v", wi, wc, ui, uc)
	}
}
