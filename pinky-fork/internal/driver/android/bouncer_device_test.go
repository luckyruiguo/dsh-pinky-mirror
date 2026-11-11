//go:build device

package android

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestDeviceShowBouncer brings up the PIN pad on a locked phone and says
// what it found, without entering anything: lock the phone first.
func TestDeviceShowBouncer(t *testing.T) {
	// Not openTestDevice, which leaves locked phones alone: this needs one,
	// and enters nothing.
	id := os.Getenv("PINKY_DEVICE")
	if id == "" {
		t.Skip("PINKY_DEVICE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d, err := NewProvider(Config{Logf: t.Logf}).OpenDevice(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	locked, err := d.Locked(ctx)
	if err != nil || !locked {
		t.Skipf("lock the phone first (locked %v, %v)", locked, err)
	}
	if err := d.Wake(ctx); err != nil {
		t.Fatal(err)
	}
	l, err := d.session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	b, err := d.showBouncer(ctx, l)
	t.Logf("showBouncer: %v, fingerprint %v, enabled %v, err %v, in %v", b.kind, b.fingerprint, b.enabled, err, time.Since(start).Round(time.Millisecond))
	if err != nil || b.kind != bouncerPIN {
		t.Errorf("no PIN pad")
	}
}
