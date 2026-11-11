//go:build device

package ios

import (
	"os"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestDeviceCaptureWait times what waiting for MayCapture costs a read with
// OCR: the same screen read three times with it (allowing any unlocked,
// named app, as the gateway does without blocked apps) and three times
// without, over PINKY_CONN ("usb" or "wifi"). It only reads, and logs
// timings, not the text. Run it with the phone unlocked on any screen.
func TestDeviceCaptureWait(t *testing.T) {
	conn := os.Getenv("PINKY_CONN")
	if conn == "" {
		t.Skip("PINKY_CONN not set")
	}
	ctx := ctxFor(t, 3*time.Minute)
	d, err := Open(ctx, driver.Info{ID: os.Getenv("PINKY_DEVICE"), Platform: driver.IOS, Connection: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	may := func(s *screen.Screen) bool { return !s.Locked && (s.App.ID != "" || s.App.Name != "") }
	for i := 0; i < 6; i++ {
		opt := driver.ObserveOptions{OCR: true}
		if i%2 == 0 {
			opt.MayCapture = may
		}
		s, st, err := d.ObserveWithStats(ctx, opt)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("MayCapture %-5v read %v: picture %q after %v (%s in %v), walk %v, OCR %v, %d lines, %s, app %s",
			opt.MayCapture != nil, st.Total.Round(time.Millisecond), st.Capture, st.CaptureWait.Round(time.Millisecond),
			st.ShotFrom, st.Shot.Round(time.Millisecond), st.Walk.Round(time.Millisecond), st.OCR.Round(time.Millisecond),
			st.OCRLines, st.Source, s.App.ID)
		time.Sleep(time.Second)
	}
}
