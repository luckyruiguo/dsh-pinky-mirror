package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// age makes the phone's vitals older by d, as if read that long ago.
func age(g *Gateway, id string, d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	v := &g.devices[id].vitals
	v.BatteryAt = v.BatteryAt.Add(-d)
	v.StorageAt = v.StorageAt.Add(-d)
	v.failAt = v.failAt.Add(-d)
}

// TestPhoneVitalsReadsRarely: the first ask reads both parts, asks within
// a minute read nothing, then the battery alone is read each minute and
// the storage every ten.
func TestPhoneVitalsReadsRarely(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t)
	waitOpen(t, g, "pixel")

	v, err := g.PhoneVitals(ctx, "pixel")
	if err != nil {
		t.Fatal(err)
	}
	if v.Battery == nil || v.Battery.Level != 82 || v.Storage == nil || v.Storage.Total != 128_000_000_000 {
		t.Fatalf("vitals %+v", v)
	}
	for range 5 {
		if v, _ := g.PhoneVitals(ctx, "pixel"); v.Battery == nil || v.Storage == nil {
			t.Fatalf("cached vitals %+v", v)
		}
	}
	if r := dev.VitalReads(); len(r) != 1 || r[0] != driver.VitalBattery|driver.VitalStorage {
		t.Fatalf("reads %v, want one of both", r)
	}

	age(g, "pixel", batteryEvery)
	g.PhoneVitals(ctx, "pixel")
	if r := dev.VitalReads(); len(r) != 2 || r[1] != driver.VitalBattery {
		t.Fatalf("reads %v, want the battery alone after a minute", r)
	}
	age(g, "pixel", storageEvery)
	g.PhoneVitals(ctx, "pixel")
	if r := dev.VitalReads(); len(r) != 3 || r[2] != driver.VitalBattery|driver.VitalStorage {
		t.Fatalf("reads %v, want both after ten minutes", r)
	}
}

// TestPhoneVitalsQuietFailure: a failed read returns what is still fresh,
// waits before reading again, and values too old are left out.
func TestPhoneVitalsQuietFailure(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t)
	waitOpen(t, g, "pixel")
	g.PhoneVitals(ctx, "pixel")

	dev.SetVitals(driver.Vitals{}, errors.New("no answer"))
	age(g, "pixel", batteryEvery)
	v, err := g.PhoneVitals(ctx, "pixel")
	if err != nil || v.Battery == nil || v.Storage == nil {
		t.Fatalf("after a failed read: %+v, %v; want the last values", v, err)
	}
	g.PhoneVitals(ctx, "pixel")
	if r := dev.VitalReads(); len(r) != 2 {
		t.Fatalf("reads %v: a failed read was retried at once", r)
	}
	age(g, "pixel", vitalsRetry)
	g.PhoneVitals(ctx, "pixel")
	if r := dev.VitalReads(); len(r) != 3 {
		t.Fatalf("reads %v: no read after the retry wait", r)
	}
	age(g, "pixel", vitalsKeep*storageEvery)
	if v, _ := g.PhoneVitals(ctx, "pixel"); !v.Empty() {
		t.Fatalf("stale vitals shown: %+v", v)
	}
}

// TestPhoneVitalsNotWhilePaused: a paused phone is never read, nor does it
// show what was read before.
func TestPhoneVitalsNotWhilePaused(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t)
	waitOpen(t, g, "pixel")
	g.PhoneVitals(ctx, "pixel")
	if err := g.PauseDevice("pixel"); err != nil {
		t.Fatal(err)
	}
	age(g, "pixel", storageEvery)
	if v, _ := g.PhoneVitals(ctx, "pixel"); !v.Empty() {
		t.Fatalf("paused phone's vitals: %+v", v)
	}
	if r := dev.VitalReads(); len(r) != 1 {
		t.Fatalf("reads %v: a paused phone was read", r)
	}
	if _, err := g.PhoneVitals(ctx, "nope"); !errors.Is(err, ErrUnknownDevice) {
		t.Fatalf("unknown phone: %v", err)
	}
}

// TestPhoneVitalsLeavesScreenOff: reading neither wakes the phone nor
// starts its stream.
func TestPhoneVitalsLeavesScreenOff(t *testing.T) {
	x := newExtraDev("pixel", "Pixel 9")
	x.setScreen(false)
	g := newExtraGateway(t, x)
	waitOpen(t, g, "pixel")
	if v, _ := g.PhoneVitals(context.Background(), "pixel"); v.Battery == nil {
		t.Fatalf("vitals %+v", v)
	}
	x.mu.Lock()
	wakes, on := x.wakes, x.screenOn
	x.mu.Unlock()
	if wakes != 0 || on {
		t.Fatalf("the read woke the phone: %d wakes, screen on %v", wakes, on)
	}
	if v, _ := viewOf(g, "pixel"); v.Streaming {
		t.Fatal("the read started the stream")
	}
}

// TestPhoneDeviceReadsAbout: phone_device's read starts with the phone.
func TestPhoneDeviceReadsAbout(t *testing.T) {
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Check the phone")
	res := s.Device(context.Background(), DeviceRequest{})
	mustStatus(t, res, StatusSettled)
	a := res.About
	if a == nil || a.Model != "Pixel 9" || a.OS != "Android 16" || a.Battery == nil || a.Battery.Level != 82 ||
		a.Storage == nil || a.Storage.Free != 41_300_000_000 {
		t.Fatalf("about %+v", a)
	}
	if len(res.Switches) != 3 {
		t.Fatalf("switches %+v", res.Switches)
	}
}

func TestStorageText(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{
		{128_000_000_000, "128 GB"},
		{87_700_000_000, "87.7 GB"},
		{41_349_000_000, "41.3 GB"},
		{4_000_000_000, "4 GB"},
		{1_000_000_000_000, "1 TB"},
		{2_000_000_000_000, "2 TB"},
		{512_000_000, "0.5 GB"},
		{2_000_000, "2 MB"},
	} {
		if got := StorageText(c.n); got != c.want {
			t.Errorf("StorageText(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestOSName(t *testing.T) {
	for _, c := range []struct {
		info driver.Info
		want string
	}{
		{driver.Info{Platform: driver.IOS, Model: "iPhone 13 mini", OSVersion: "27.0.1"}, "iOS 27.0.1"},
		{driver.Info{Platform: driver.IOS, Model: "iPad Air (M2)", OSVersion: "26.1"}, "iPadOS 26.1"},
		{driver.Info{Platform: driver.Android, Model: "Pixel 8", OSVersion: "16"}, "Android 16"},
		{driver.Info{Platform: driver.Android, Model: "Pixel 8"}, ""},
	} {
		if got := OSName(c.info); got != c.want {
			t.Errorf("OSName(%+v) = %q, want %q", c.info, got, c.want)
		}
	}
}
