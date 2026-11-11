//go:build device

package ios

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestDeviceLockScreenPad goes as far as unlocking does before the first
// key, typing nothing: lock the phone first (notifications on the lock
// screen make it a better test). It wakes the phone, swipes up for the
// passcode pad as Unlock does, and reports what it found; the phone stays
// on the pad, still locked. It also writes what SpringBoard lists on the
// lock screen to /tmp/pinky-ios-probe (delete it afterwards).
func TestDeviceLockScreenPad(t *testing.T) {
	d := testDevice(t)
	ctx := context.Background()
	p := devicePower{d}
	locked, err := p.locked(ctx)
	t.Logf("locked: %v (%v)", locked, err)
	if !locked {
		t.Skip("lock the phone first")
	}
	if rax, err := d.raxReady(ctx); err == nil {
		c, cancel := context.WithTimeout(ctx, raxLimits.Budget+raxRequestTimeout)
		els, _, err := rax.walk(c, raxLimits)
		cancel()
		w := probeOut(t, "lock-screen.txt")
		fmt.Fprintf(w, "walk err %v\n", err)
		dumpRAX(w, "lock screen", els)
	}
	start := time.Now()
	unlocked, err := reachPad(ctx, p)
	t.Logf("reachPad: unlocked %v, err %v (%v)", unlocked, err, time.Since(start).Round(time.Millisecond))
	if err != nil {
		t.Errorf("no passcode pad: %v", err)
	}
	locked, err = p.locked(ctx)
	t.Logf("still locked: %v (%v)", locked, err)
}
