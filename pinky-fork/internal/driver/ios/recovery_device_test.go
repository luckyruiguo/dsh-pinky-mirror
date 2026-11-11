//go:build device

package ios

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// TestDeviceRecovery opens a session to the phone again and again, every
// PINKY_EVERY (20 s by default) for PINKY_FOR (5 minutes), over whatever
// connection usbmuxd offers (USB first), and logs how long each part takes:
// the tunnel and RSD, the screen stream, HID, and one read without OCR. It
// presses nothing. Run it after a session was cut off (a cable pulled in the
// middle of a task, a process killed with its session open, the phone gone
// to sleep with one) to see whether the phone's services answer, and when
// they recover.
func TestDeviceRecovery(t *testing.T) {
	id := os.Getenv("PINKY_DEVICE")
	if id == "" || os.Getenv("PINKY_RECOVERY") == "" {
		t.Skip("set PINKY_DEVICE and PINKY_RECOVERY=1")
	}
	every, total := 20*time.Second, 5*time.Minute
	if v, err := strconv.Atoi(os.Getenv("PINKY_EVERY")); err == nil && v > 0 {
		every = time.Duration(v) * time.Second
	}
	if v, err := strconv.Atoi(os.Getenv("PINKY_FOR")); err == nil && v > 0 {
		total = time.Duration(v) * time.Second
	}
	start := time.Now()
	for time.Since(start) < total {
		round := time.Now()
		e, err := findEntry(id, false)
		if err != nil {
			t.Logf("%5.0fs: not listed: %v", time.Since(start).Seconds(), err)
			time.Sleep(every)
			continue
		}
		conn := "usb"
		if e.Properties.ConnectionType == "Network" {
			conn = "wifi"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		t0 := time.Now()
		d, err := Open(ctx, driver.Info{ID: id, Platform: driver.IOS, Connection: conn})
		open := time.Since(t0)
		if err != nil {
			cancel()
			t.Logf("%5.0fs: %s: open failed after %v: %v", time.Since(start).Seconds(), conn, open.Round(time.Millisecond), err)
			time.Sleep(time.Until(round.Add(every)))
			continue
		}
		t0 = time.Now()
		s, st, err := d.ObserveWithStats(ctx, driver.ObserveOptions{})
		read := time.Since(t0)
		n := 0
		if s != nil {
			n = len(s.Elements)
		}
		locked, _ := d.Locked(ctx)
		t.Logf("%5.0fs: %s: open %v (tunnel, stream, HID), read %v: %d elements, source %s, err %v, locked %v",
			time.Since(start).Seconds(), conn, open.Round(time.Millisecond), read.Round(time.Millisecond), n, st.Source, err, locked)
		d.Close()
		cancel()
		time.Sleep(time.Until(round.Add(every)))
	}
}
