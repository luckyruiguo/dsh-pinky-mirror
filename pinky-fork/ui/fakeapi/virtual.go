package fakeapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Virtual phones: the fake computer has two iOS Simulators, an Android
// emulator and one whose system image is gone, as Add phone › Virtual
// phone lists them (gateway's VirtualPhones). StartVirtual boots one into
// a phone a couple of seconds later at Speed, or at BootVirtual with
// Options.HoldSetup; StopVirtual takes it offline.

// virtualPhone is one of the fake computer's virtual phones.
type virtualPhone struct {
	platform driver.Platform
	phone    driver.VirtualPhone // ID, Name, Model, OSVersion, Unavailable
	deviceID string              // its phone's ID once it runs
	failed   string              // why it quit while it started (FailVirtual)
	width    int
	height   int
}

func defaultVirtual() []virtualPhone {
	return []virtualPhone{
		{platform: driver.IOS, phone: driver.VirtualPhone{ID: "5F1E7A2C-SIM-17", Name: "iPhone 17 Pro", Model: "iPhone 17 Pro", OSVersion: "26.5"},
			deviceID: "5F1E7A2C-SIM-17", width: 1206, height: 2622},
		{platform: driver.IOS, phone: driver.VirtualPhone{ID: "9C04B3D1-SIM-AIR", Name: "iPad Air", Model: "iPad Air 13-inch (M3)", OSVersion: "26.5"},
			deviceID: "9C04B3D1-SIM-AIR", width: 2048, height: 2732},
		{platform: driver.Android, phone: driver.VirtualPhone{ID: "Pixel_9_API_36", Name: "Pixel 9 API 36", Model: "Pixel 9", OSVersion: "16"},
			deviceID: "emulator-5554", width: 1080, height: 2424},
		{platform: driver.Android, phone: driver.VirtualPhone{ID: "Tablet_API_35", Name: "Tablet API 35", Model: "Pixel Tablet", OSVersion: "15",
			Unavailable: "Its system image is not installed."}, width: 2560, height: 1600},
	}
}

// virtualState is what the fake's virtual phones look like (API.virt).
type virtualState struct {
	phones   []virtualPhone
	missing  map[driver.Platform]driver.VirtualPhones // SetVirtualMissing
	starting map[string]bool                          // by deviceID: started, not running yet
	err      error                                    // what StartVirtual and StopVirtual fail with
	starts   []string                                 // StartVirtual calls: "ios/<id>"
	stops    []string
}

func (a *API) virtLocked() *virtualState {
	if a.virt == nil {
		a.virt = &virtualState{phones: defaultVirtual(), missing: map[driver.Platform]driver.VirtualPhones{}, starting: map[string]bool{}}
	}
	return a.virt
}

// VirtualPhones implements the client's virtual phones
// (gateway.VirtualPhones): iOS first, then Android, each with what
// SetVirtualMissing set.
func (a *API) VirtualPhones(ctx context.Context) ([]driver.VirtualPhones, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.virtLocked()
	var out []driver.VirtualPhones
	for _, pf := range []driver.Platform{driver.IOS, driver.Android} {
		if m, ok := v.missing[pf]; ok {
			m.Platform = pf
			out = append(out, m)
			continue
		}
		vp := driver.VirtualPhones{Platform: pf}
		for _, p := range v.phones {
			if p.platform != pf {
				continue
			}
			ph := p.phone
			if d := a.device(p.deviceID); d != nil && d.info.State != driver.StateOffline {
				ph.Running, ph.DeviceID = true, p.deviceID
			}
			ph.Starting = v.starting[p.deviceID] && !ph.Running
			if !ph.Running && !ph.Starting {
				ph.Failed = p.failed
			}
			vp.Phones = append(vp.Phones, ph)
		}
		out = append(out, vp)
	}
	return out, nil
}

// SetVirtualMissing says what the fake computer lacks for a platform's
// virtual phones, as VirtualPhones reports it ("xcode", "android_sdk",
// "emulator"); a zero c with only Platform set removes it.
func (a *API) SetVirtualMissing(c driver.VirtualPhones) {
	a.mu.Lock()
	v := a.virtLocked()
	if c.Missing == "" && c.Detail == "" {
		delete(v.missing, c.Platform)
	} else {
		v.missing[c.Platform] = c
	}
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices})
}

// SetVirtualError makes StartVirtual and StopVirtual fail with err; nil
// lets them work again.
func (a *API) SetVirtualError(err error) {
	a.mu.Lock()
	a.virtLocked().err = err
	a.mu.Unlock()
}

// VirtualCalls returns the StartVirtual and StopVirtual calls so far, as
// "<platform>/<id>".
func (a *API) VirtualCalls() (starts, stops []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.virtLocked()
	return append([]string(nil), v.starts...), append([]string(nil), v.stops...)
}

// StartVirtual implements the client's virtual phones
// (gateway.StartVirtual): the phone runs a couple of seconds later at
// Speed, or at BootVirtual with Options.HoldSetup.
func (a *API) StartVirtual(ctx context.Context, platform driver.Platform, id string) error {
	a.mu.Lock()
	v := a.virtLocked()
	v.starts = append(v.starts, string(platform)+"/"+id)
	p := v.find(platform, id)
	switch {
	case v.err != nil:
		a.mu.Unlock()
		return v.err
	case p == nil:
		a.mu.Unlock()
		return fmt.Errorf("no virtual phone %q", id)
	case p.phone.Unavailable != "":
		a.mu.Unlock()
		return errors.New(p.phone.Unavailable)
	}
	if d := a.device(p.deviceID); d != nil && d.info.State != driver.StateOffline {
		a.mu.Unlock()
		return nil // running already: it is in Pinky
	}
	v.starting[p.deviceID] = true
	p.failed = ""
	gen := a.gen
	a.mu.Unlock()
	if !a.opt.HoldSetup {
		go func() {
			if a.sleepGen(gen, 2*time.Second) {
				a.BootVirtual()
			}
		}()
	}
	return nil
}

// StopVirtual implements the client's virtual phones
// (gateway.StopVirtual): its phone goes offline.
func (a *API) StopVirtual(ctx context.Context, platform driver.Platform, id string) error {
	a.mu.Lock()
	v := a.virtLocked()
	v.stops = append(v.stops, string(platform)+"/"+id)
	if v.err != nil {
		a.mu.Unlock()
		return v.err
	}
	p := v.find(platform, id)
	if p == nil {
		a.mu.Unlock()
		return fmt.Errorf("no virtual phone %q", id)
	}
	delete(v.starting, p.deviceID)
	if d := a.device(p.deviceID); d != nil {
		d.info.State, d.info.Setup, d.info.Detail = driver.StateOffline, "", ""
	}
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: p.deviceID})
	return nil
}

// BootVirtual brings every virtual phone StartVirtual started into Pinky,
// online.
func (a *API) BootVirtual() {
	a.mu.Lock()
	v := a.virtLocked()
	var ids []string
	for _, p := range v.phones {
		if !v.starting[p.deviceID] {
			continue
		}
		delete(v.starting, p.deviceID)
		ids = append(ids, p.deviceID)
		if d := a.device(p.deviceID); d != nil {
			d.info.State, d.info.Detail = driver.StateOnline, ""
			continue
		}
		info := driver.Info{ID: p.deviceID, Platform: p.platform, Name: p.phone.Name, Model: p.phone.Model,
			OSVersion: p.phone.OSVersion, Connection: "usb", State: driver.StateOnline,
			Virtual: true, VirtualID: p.phone.ID}
		d := &device{info: info, width: p.width, height: p.height, codec: driver.H264, source: "testsrc2"}
		if p.platform == driver.IOS {
			d.info.Backend, d.codec, d.source = "wda", driver.HEVC, "testsrc"
		} else {
			d.info.Backend = "scrcpy"
		}
		a.devices = append(a.devices, d)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: id})
	}
}

// FailVirtual ends every virtual phone StartVirtual started before it
// runs, as an emulator that crashed while it booted: it is no longer
// starting, and VirtualPhones says why.
func (a *API) FailVirtual(why string) {
	a.mu.Lock()
	v := a.virtLocked()
	for i := range v.phones {
		if v.starting[v.phones[i].deviceID] {
			delete(v.starting, v.phones[i].deviceID)
			v.phones[i].failed = why
		}
	}
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices})
}

func (v *virtualState) find(platform driver.Platform, id string) *virtualPhone {
	for i := range v.phones {
		if v.phones[i].platform == platform && v.phones[i].phone.ID == id {
			return &v.phones[i]
		}
	}
	return nil
}
