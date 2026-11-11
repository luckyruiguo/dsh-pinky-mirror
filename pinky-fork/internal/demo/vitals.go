package demo

import (
	"context"

	"github.com/DIYgod/pinky/internal/driver"
)

// vitals is a demo phone's battery and storage. It starts at 82 % on
// battery with 41.3 GB of 128 GB free.
type vitals struct {
	set   bool
	v     driver.Vitals
	err   error
	reads []driver.VitalParts
}

var _ driver.VitalsReader = (*Device)(nil)

// Vitals implements driver.VitalsReader.
func (d *Device) Vitals(ctx context.Context, parts driver.VitalParts) (driver.Vitals, error) {
	if err := d.check(ctx); err != nil {
		return driver.Vitals{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.vit.reads = append(d.vit.reads, parts)
	if d.vit.err != nil {
		return driver.Vitals{}, d.vit.err
	}
	v := d.vit.v
	if !d.vit.set {
		v = driver.Vitals{Battery: &driver.Battery{Level: 82, TempC: 29.5},
			Storage: &driver.Storage{Total: 128_000_000_000, Free: 41_300_000_000}}
		if d.info.Platform == driver.IOS {
			v.Battery.TempC = 0
		}
	}
	var out driver.Vitals
	if parts&driver.VitalBattery != 0 && v.Battery != nil {
		b := *v.Battery
		out.Battery = &b
	}
	if parts&driver.VitalStorage != 0 && v.Storage != nil {
		s := *v.Storage
		out.Storage = &s
	}
	return out, nil
}

// SetVitals sets the battery and storage the phone reports, and err, when
// not nil, the error its reads fail with.
func (d *Device) SetVitals(v driver.Vitals, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.vit.set, d.vit.v, d.vit.err = true, v, err
}

// VitalReads returns the parts of each read of the battery and storage.
func (d *Device) VitalReads() []driver.VitalParts {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]driver.VitalParts(nil), d.vit.reads...)
}
