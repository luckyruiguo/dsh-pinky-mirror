//go:build device

package android

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// TestDeviceCheckup runs the checkup on a real phone or an emulator and
// checks that every item ran, that input and the screen work, and that
// the checkup left the screen on or off as it found it. It unlocks
// nothing and changes no setting:
//
//	scripts/devlock.sh <id> env PINKY_DEVICE=<id> go test -tags device -run TestDeviceCheckup -v ./internal/driver/android/
func TestDeviceCheckup(t *testing.T) {
	serial := os.Getenv("PINKY_DEVICE")
	if serial == "" {
		t.Skip("PINKY_DEVICE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := NewProvider(Config{Logf: t.Logf})
	d, err := p.OpenDevice(ctx, serial)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	before, err := d.ScreenOn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	cu, err := d.Checkup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("family %q, system %q, in %v", cu.Family, cu.System, time.Since(start).Round(time.Millisecond))
	got := map[string]driver.Check{}
	for _, c := range cu.Checks {
		t.Logf("%-5s %-14s %s [%s]", c.Status, c.ID, c.Detail, c.Fix)
		got[c.ID] = c
	}
	want := []string{driver.CheckADB, driver.CheckCompanion, driver.CheckInput, driver.CheckScreen, driver.CheckBackground, driver.CheckWiFi}
	if runtime.GOOS == "darwin" {
		want = append(want, driver.CheckLocalNetwork)
	}
	for _, id := range want {
		if _, ok := got[id]; !ok {
			t.Errorf("no %s item", id)
		}
	}
	for _, id := range []string{driver.CheckADB, driver.CheckInput} {
		if c := got[id]; c.Status != driver.CheckPass {
			t.Errorf("%s: %+v", id, c)
		}
	}
	// The Android 10 emulator's video encoder fails: screenshots still work.
	if c := got[driver.CheckScreen]; c.Status == driver.CheckFail || (c.Status == driver.CheckWarn && c.Fix != driver.FixVideoEncoder) {
		t.Errorf("screen: %+v", c)
	}
	after, err := d.ScreenOn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("the screen was on=%v before the checkup and on=%v after it", before, after)
	}
}
