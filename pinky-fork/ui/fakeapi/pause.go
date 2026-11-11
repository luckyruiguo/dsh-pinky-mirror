package fakeapi

import (
	"fmt"

	"github.com/DIYgod/pinky/internal/gateway"
)

// PauseDevice pauses Pinky's connection to a phone, as the gateway's does:
// its task stops and it shows offline, "Paused", until ResumeDevice.
func (a *API) PauseDevice(deviceID string) error {
	return a.setPaused(deviceID, true)
}

// ResumeDevice undoes PauseDevice.
func (a *API) ResumeDevice(deviceID string) error {
	return a.setPaused(deviceID, false)
}

func (a *API) setPaused(deviceID string, paused bool) error {
	a.mu.Lock()
	d := a.device(deviceID)
	if d == nil {
		a.mu.Unlock()
		return fmt.Errorf("fakeapi: no device %q: %w", deviceID, gateway.ErrUnknownDevice)
	}
	if paused && !d.paused {
		a.stopLocked(d)
	}
	d.paused = paused
	a.mu.Unlock()
	a.emitAll(deviceID)
	return nil
}
