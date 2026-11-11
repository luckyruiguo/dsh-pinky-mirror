package gateway

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// A phone's battery and storage (driver.VitalsReader) are read only when
// asked: by the client while it shows them, and by an agent's
// phone_device read. Each answer reuses what is fresh enough, so however
// often anyone asks, Pinky reads the battery at most once a minute and the
// storage every ten minutes. It never reads a phone that is paused, offline
// or not open, and a read that fails waits vitalsRetry before the next.
const (
	batteryEvery = time.Minute
	storageEvery = 10 * time.Minute
	// vitalsSlack lets a client that asks every batteryEvery get a fresh
	// battery each time, though its timer and the last read do not line
	// up to the millisecond.
	vitalsSlack   = 5 * time.Second
	vitalsRetry   = 2 * time.Minute
	vitalsTimeout = 10 * time.Second
	// A value older than this many of its periods is not shown: the reads
	// since failed.
	vitalsKeep = 3
)

// PhoneVitals is what Pinky last read of a phone's battery and storage.
// Battery or Storage is nil when the phone does not report it, could not
// be read lately, or is not online.
type PhoneVitals struct {
	Battery   *driver.Battery
	BatteryAt time.Time
	Storage   *driver.Storage
	StorageAt time.Time
}

// Empty reports whether it holds nothing to show.
func (v PhoneVitals) Empty() bool { return v.Battery == nil && v.Storage == nil }

// vitalsState is a device's vitals and the reads of them.
type vitalsState struct {
	PhoneVitals
	reading bool
	failAt  time.Time
	// unsupported is the open device that answered ErrUnsupported (an
	// iOS Simulator): Pinky does not ask it again.
	unsupported driver.Device
}

// PhoneVitals returns a phone's battery and storage, reading the ones that
// are due first. A phone that is paused, offline or not open returns
// nothing and is not read. Errors are quiet: the parts that could not be
// read are left out.
func (g *Gateway) PhoneVitals(ctx context.Context, deviceID string) (PhoneVitals, error) {
	d, err := g.device(deviceID)
	if err != nil {
		return PhoneVitals{}, err
	}
	return g.vitals(ctx, d), nil
}

// vitals is PhoneVitals for d.
func (g *Gateway) vitals(ctx context.Context, d *device) PhoneVitals {
	now := time.Now()
	g.mu.Lock()
	v := &d.vitals
	dev := d.dev
	if dev == nil || !d.online || g.paused[d.id] || d.removed || g.closed {
		g.mu.Unlock()
		return PhoneVitals{}
	}
	vr, ok := dev.(driver.VitalsReader)
	if !ok || v.unsupported == dev {
		g.mu.Unlock()
		return PhoneVitals{}
	}
	var parts driver.VitalParts
	if !v.reading && now.Sub(v.failAt) >= vitalsRetry {
		if now.Sub(v.BatteryAt) >= batteryEvery-vitalsSlack {
			parts |= driver.VitalBattery
		}
		if now.Sub(v.StorageAt) >= storageEvery-vitalsSlack {
			parts |= driver.VitalStorage
		}
	}
	if parts == 0 {
		out := v.fresh(now)
		g.mu.Unlock()
		return out
	}
	v.reading = true
	g.mu.Unlock()

	rctx, cancel := context.WithTimeout(ctx, vitalsTimeout)
	got, err := vr.Vitals(rctx, parts)
	cancel()

	g.mu.Lock()
	defer g.mu.Unlock()
	v.reading = false
	now = time.Now()
	switch {
	case errors.Is(err, driver.ErrUnsupported):
		v.unsupported = dev
		return PhoneVitals{}
	case err != nil:
		if ctx.Err() == nil {
			// Not the caller giving up: the phone did not answer.
			v.failAt = now
		}
		return v.fresh(now)
	}
	if parts&driver.VitalBattery != 0 {
		v.Battery, v.BatteryAt = got.Battery, now
	}
	if parts&driver.VitalStorage != 0 {
		v.Storage, v.StorageAt = got.Storage, now
	}
	return v.fresh(now)
}

// fresh is what of v is recent enough to show.
func (v *vitalsState) fresh(now time.Time) PhoneVitals {
	out := v.PhoneVitals
	if now.Sub(v.BatteryAt) > vitalsKeep*batteryEvery {
		out.Battery, out.BatteryAt = nil, time.Time{}
	}
	if now.Sub(v.StorageAt) > vitalsKeep*storageEvery {
		out.Storage, out.StorageAt = nil, time.Time{}
	}
	if out.Battery != nil {
		b := *out.Battery
		out.Battery = &b
	}
	if out.Storage != nil {
		s := *out.Storage
		out.Storage = &s
	}
	return out
}

// PhoneAbout is the phone in a phone_device read.
type PhoneAbout struct {
	Model string // "iPhone 13 mini", "Pixel 8"
	OS    string // "iOS 27.0.1", "Android 16"
	PhoneVitals
}

// about is d's PhoneAbout, nil when Pinky knows none of it.
func (g *Gateway) about(ctx context.Context, d *device) *PhoneAbout {
	vit := g.vitals(ctx, d)
	g.mu.Lock()
	info := d.info
	g.mu.Unlock()
	a := &PhoneAbout{Model: info.Model, OS: OSName(info), PhoneVitals: vit}
	if a.Model == "" && a.OS == "" && vit.Empty() {
		return nil
	}
	return a
}

// OSName is the phone's system and its version: "iOS 27.0.1",
// "iPadOS 26.1", "Android 16"; "" when the version is unknown.
func OSName(info driver.Info) string {
	if info.OSVersion == "" {
		return ""
	}
	switch {
	case info.Platform == driver.IOS && strings.HasPrefix(info.Model, "iPad"):
		return "iPadOS " + info.OSVersion
	case info.Platform == driver.IOS:
		return "iOS " + info.OSVersion
	}
	return "Android " + info.OSVersion
}

// StorageText is a size of storage as phones show it, in decimal units:
// "87.7 GB", "128 GB", "1 TB".
func StorageText(n int64) string {
	const gb, tb = 1e9, 1e12
	trim := func(f float64) string { return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0") }
	switch {
	case n >= tb:
		return trim(float64(n)/tb) + " TB"
	case n >= 100*gb:
		return strconv.FormatInt((n+gb/2)/gb, 10) + " GB"
	case n >= gb/10:
		return trim(float64(n)/gb) + " GB"
	}
	return SizeText(n)
}
