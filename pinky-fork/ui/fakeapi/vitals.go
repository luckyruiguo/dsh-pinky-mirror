package fakeapi

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// vitalsState is the phones' made-up battery and storage.
type vitalsState struct {
	set   map[string]gateway.PhoneVitals // SetVitals, by phone
	reads map[string]int                 // PhoneVitals calls, by phone
}

func (a *API) vitalsLocked() *vitalsState {
	if a.vit == nil {
		a.vit = &vitalsState{set: map[string]gateway.PhoneVitals{}, reads: map[string]int{}}
	}
	return a.vit
}

// PhoneVitals implements the client's vitalsSource, as the gateway's does:
// a phone that is connected reports its battery and storage, made up from
// its id unless SetVitals set them; one that is paused, offline or waits
// for a step reports nothing, a virtual phone no battery, and a Simulator
// no storage either.
func (a *API) PhoneVitals(ctx context.Context, deviceID string) (gateway.PhoneVitals, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	d := a.device(deviceID)
	if d == nil {
		return gateway.PhoneVitals{}, fmt.Errorf("fakeapi: no device %q: %w", deviceID, gateway.ErrUnknownDevice)
	}
	if d.paused || d.info.State != driver.StateOnline || d.connecting != "" {
		return gateway.PhoneVitals{}, nil
	}
	vs := a.vitalsLocked()
	vs.reads[deviceID]++
	if v, ok := vs.set[deviceID]; ok {
		return copyVitals(v), nil
	}
	now := a.opt.Now()
	h := fnv.New32a()
	h.Write([]byte(deviceID))
	n := int(h.Sum32())
	var v gateway.PhoneVitals
	if !d.info.Virtual {
		b := &driver.Battery{Level: 35 + n%61}
		if d.info.Connection == "usb" {
			b.Plugged, b.Charging = true, true
		}
		if d.info.Platform == driver.Android {
			b.TempC = float64(28 + n%6)
		}
		v.Battery, v.BatteryAt = b, now
	}
	if !d.info.Virtual || d.info.Platform == driver.Android {
		total := int64(128_000_000_000) << (n % 2)
		v.Storage = &driver.Storage{Total: total, Free: total / 100 * int64(15+n%50)}
		v.StorageAt = now
	}
	return v, nil
}

// SetVitals sets what PhoneVitals reports of a phone while it is
// connected; an empty value reports nothing.
func (a *API) SetVitals(deviceID string, v gateway.PhoneVitals) {
	a.mu.Lock()
	a.vitalsLocked().set[deviceID] = copyVitals(v)
	a.mu.Unlock()
}

// VitalsReads returns how many times the client asked about a phone's
// battery and storage.
func (a *API) VitalsReads(deviceID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.vitalsLocked().reads[deviceID]
}

func copyVitals(v gateway.PhoneVitals) gateway.PhoneVitals {
	if v.Battery != nil {
		b := *v.Battery
		v.Battery = &b
	}
	if v.Storage != nil {
		s := *v.Storage
		v.Storage = &s
	}
	return v
}
