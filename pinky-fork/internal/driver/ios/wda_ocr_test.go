package ios

import (
	"context"
	"fmt"
	"image/color"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// Over Wi-Fi, OCR reads a frame of WDA's MJPEG server instead of the
// full-size screenshot; when no frame comes, the screenshot, and frames are
// not asked for again for frameRetry. Over a cable, always the screenshot.
func TestWDAOCRPicture(t *testing.T) {
	f := &fakeWDA{app: `{"bundleId":"com.apple.Preferences","name":"Settings","pid":8}`}
	srv := httptest.NewServer(f)
	defer srv.Close()
	frame := jpegBytes(t, 10, 20, color.White)
	var frames, mute atomic.Int32
	mj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frames.Add(1)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=BoundaryString")
		if mute.Load() != 0 {
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprintf(w, "--BoundaryString\r\nContent-type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame))
		w.Write(frame)
	}))
	defer mj.Close()
	defer func(w time.Duration) { frameWait = w }(frameWait)
	frameWait = 200 * time.Millisecond

	d := &WDADevice{c: newWDAURL(srv.URL, mj.URL), width: 100, height: 200, scale: 1,
		info: driver.Info{Connection: "wifi"}}
	ctx := context.Background()
	shots := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := f.shots
		f.shots = 0
		return n
	}
	pic, from, err := d.ocrPicture(ctx)
	if err != nil || from != "mjpeg" || len(pic) == 0 || shots() != 0 || frames.Load() != 1 {
		t.Fatalf("wifi: from %q, %d bytes, %v; %d screenshots", from, len(pic), err, shots())
	}
	if _, err := d.Observe(ctx, driver.ObserveOptions{OCR: true}); err != nil || shots() != 0 {
		t.Fatalf("observe over wifi: %v; %d screenshots", err, shots())
	}

	// The MJPEG server sends nothing: the screenshot, then only it.
	mute.Store(1)
	frames.Store(0)
	if _, from, _ := d.ocrPicture(ctx); from != "screenshot" || shots() != 1 || frames.Load() != 1 {
		t.Fatalf("no frame: from %q", from)
	}
	if _, from, _ := d.ocrPicture(ctx); from != "screenshot" || frames.Load() != 1 {
		t.Fatalf("within frameRetry: from %q, %d frames asked", from, frames.Load())
	}
	mute.Store(0)
	d.framesAfter.Store(time.Now().Add(-time.Second).UnixNano())
	if _, from, _ := d.ocrPicture(ctx); from != "mjpeg" {
		t.Fatalf("after frameRetry: from %q", from)
	}

	// Over a cable, the screenshot.
	d.info.Connection = "usb"
	frames.Store(0)
	if _, from, _ := d.ocrPicture(ctx); from != "screenshot" || frames.Load() != 0 {
		t.Fatalf("usb: from %q, %d frames", from, frames.Load())
	}
}
