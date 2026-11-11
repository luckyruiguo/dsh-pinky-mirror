package fakeapi

import (
	"context"
	"fmt"
	"slices"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Removal is one RemoveDevice call the fake took.
type Removal struct {
	Device  string
	History bool
}

// RemoveDevice removes a phone, as the gateway's does: its task stops, its
// approvals go, and with History its finished tasks leave Activity. The
// fake's phones do not come back by themselves.
func (a *API) RemoveDevice(ctx context.Context, deviceID string, opt gateway.RemoveOptions) error {
	a.mu.Lock()
	d := a.device(deviceID)
	if d == nil {
		a.mu.Unlock()
		return fmt.Errorf("fakeapi: no device %q: %w", deviceID, gateway.ErrUnknownDevice)
	}
	a.stopLocked(d)
	a.devices = slices.DeleteFunc(a.devices, func(x *device) bool { return x == d })
	delete(a.readOnly, deviceID)
	if opt.History {
		a.history = slices.DeleteFunc(a.history, func(t gateway.TaskView) bool { return t.DeviceID == deviceID })
		for _, t := range a.tasks {
			if t.view.DeviceID == deviceID && isFinished(t.view.State) {
				t.removed = true
			}
		}
	}
	a.removals = append(a.removals, Removal{Device: deviceID, History: opt.History})
	a.mu.Unlock()
	a.emitAll(deviceID)
	return nil
}

// Removals returns the RemoveDevice calls so far.
func (a *API) Removals() []Removal {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.removals)
}

// SetTransport changes how a phone is connected ("usb" or "wifi") and
// whether it is online, for the removal dialog's notes.
func (a *API) SetTransport(deviceID, connection string, online bool) {
	a.mu.Lock()
	if d := a.device(deviceID); d != nil {
		d.info.Connection = connection
		d.info.State, d.info.Detail = driver.StateOnline, ""
		if !online {
			d.info.State, d.info.Detail = driver.StateOffline, "Unplugged"
		}
	}
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: deviceID})
}
