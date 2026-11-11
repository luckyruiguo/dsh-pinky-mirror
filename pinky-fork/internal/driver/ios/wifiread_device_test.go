//go:build device

package ios

import (
	"os"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// TestDeviceWiFiReads checks a new session over PINKY_CONN ("wifi" most
// useful): whether its first two reads are complete and how long their
// walks take (raxLimitsWiFi), how long two screenshots take and how big
// they are (instShot's wait), and the screen's state. It only reads, and
// logs numbers, not the text. Run it with the phone unlocked on a busy
// screen (the home screen).
func TestDeviceWiFiReads(t *testing.T) {
	conn := os.Getenv("PINKY_CONN")
	if conn == "" {
		t.Skip("PINKY_CONN not set")
	}
	ctx := ctxFor(t, 3*time.Minute)
	t0 := time.Now()
	d, err := Open(ctx, driver.Info{ID: os.Getenv("PINKY_DEVICE"), Platform: driver.IOS, Connection: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	t.Logf("open %v", time.Since(t0).Round(time.Millisecond))
	for i := 0; i < 2; i++ {
		s, st, err := d.ObserveWithStats(ctx, driver.ObserveOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("read %d: %v, walk %v, complete %v, %d elements, %s, app %s, locked %v",
			i+1, st.Total.Round(time.Millisecond), st.Walk.Round(time.Millisecond), st.Complete, len(s.Elements), st.Source, s.App.ID, s.Locked)
	}
	for i := 0; i < 2; i++ {
		t0 := time.Now()
		b, err := d.ScreenshotPNG(ctx)
		d.svcMu.Lock()
		capture := d.preferCapture
		d.svcMu.Unlock()
		t.Logf("screenshot %d: %v, %d KB, err %v, capture service first now %v", i+1, time.Since(t0).Round(time.Millisecond), len(b)>>10, err, capture)
	}
	on, err := d.ScreenOn(ctx)
	t.Logf("screen on %v, err %v", on, err)
}
