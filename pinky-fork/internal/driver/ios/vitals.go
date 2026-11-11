package ios

import (
	"context"

	goios "github.com/danielpaulus/go-ios/ios"

	"github.com/DIYgod/pinky/internal/driver"
)

// The lockdown domains and keys Vitals reads. Each key is read on its own:
// reading a whole domain is slow (com.apple.disk_usage holds a NAND blob
// that took 20 s over Wi-Fi).
const (
	batteryDomain = "com.apple.mobile.battery"
	diskDomain    = "com.apple.disk_usage"
)

var (
	batteryKeys = []string{"BatteryCurrentCapacity", "BatteryIsCharging", "ExternalConnected", "FullyCharged"}
	// TotalDiskCapacity is the capacity the phone is sold with (128 GB).
	// TotalDataAvailable counts what iOS can purge, as Settings › General
	// › iPhone Storage does; AmountDataAvailable leaves that out and reads
	// a few GB on a phone Settings shows with tens of GB free.
	diskKeys = []string{"TotalDiskCapacity", "TotalDataAvailable"}
)

// Vitals implements driver.VitalsReader through lockdown, which answers
// while the phone is locked and starts nothing the person sees: no display
// stream, no screen, no HID.
func (d *Device) Vitals(ctx context.Context, parts driver.VitalParts) (driver.Vitals, error) {
	v, err := readVitals(ctx, d.l.base, parts)
	return v, d.gone(err)
}

// Vitals implements driver.VitalsReader.
func (a *AXDevice) Vitals(ctx context.Context, parts driver.VitalParts) (driver.Vitals, error) {
	return a.d.Vitals(ctx, parts)
}

// Vitals implements driver.VitalsReader through lockdown, beside the
// runner. A Simulator has no lockdown and reports nothing.
func (d *WDADevice) Vitals(ctx context.Context, parts driver.VitalParts) (driver.Vitals, error) {
	if d.entry.Properties.SerialNumber == "" {
		return driver.Vitals{}, driver.ErrUnsupported
	}
	return readVitals(ctx, d.entry, parts)
}

// readVitals reads the keys of parts in one lockdown session, within
// lockdownTimeout.
func readVitals(ctx context.Context, e goios.DeviceEntry, parts driver.VitalParts) (driver.Vitals, error) {
	return bounded(ctx, lockdownTimeout, func() (driver.Vitals, error) {
		conn, err := goios.ConnectLockdownWithSession(e)
		if err != nil {
			return driver.Vitals{}, err
		}
		defer conn.Close()
		var first error
		read := func(domain string, keys []string) map[string]any {
			m := map[string]any{}
			for _, k := range keys {
				v, err := conn.GetValueForDomain(k, domain)
				if err != nil {
					if first == nil {
						first = err
					}
					continue
				}
				m[k] = v
			}
			return m
		}
		var v driver.Vitals
		if parts&driver.VitalBattery != 0 {
			v.Battery = batteryOf(read(batteryDomain, batteryKeys))
		}
		if parts&driver.VitalStorage != 0 {
			v.Storage = storageOf(read(diskDomain, diskKeys))
		}
		if v.Battery == nil && v.Storage == nil && first != nil {
			return v, first
		}
		return v, nil
	})
}

// batteryOf makes a Battery of com.apple.mobile.battery's values; nil
// without a level.
func batteryOf(m map[string]any) *driver.Battery {
	level, ok := plistInt(m["BatteryCurrentCapacity"])
	if !ok || level < 0 || level > 100 {
		return nil
	}
	b := &driver.Battery{Level: int(level)}
	b.Charging, _ = m["BatteryIsCharging"].(bool)
	b.Plugged, _ = m["ExternalConnected"].(bool)
	b.Full, _ = m["FullyCharged"].(bool)
	return b
}

// storageOf makes a Storage of com.apple.disk_usage's values; nil without
// both.
func storageOf(m map[string]any) *driver.Storage {
	total, ok1 := plistInt(m["TotalDiskCapacity"])
	free, ok2 := plistInt(m["TotalDataAvailable"])
	if !ok1 || !ok2 || total <= 0 || free < 0 {
		return nil
	}
	return &driver.Storage{Total: total, Free: min(free, total)}
}

// plistInt reads a plist integer, which go-ios decodes as one of Go's
// integer types.
func plistInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case int32:
		return int64(n), true
	case uint64:
		if n > 1<<62 {
			return 0, false
		}
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return 0, false
}

var (
	_ driver.VitalsReader = (*Device)(nil)
	_ driver.VitalsReader = (*AXDevice)(nil)
	_ driver.VitalsReader = (*WDADevice)(nil)
)
