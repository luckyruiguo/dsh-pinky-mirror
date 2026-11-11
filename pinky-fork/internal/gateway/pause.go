package gateway

import (
	"context"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/driver"
)

// Pausing Pinky's connection to a phone (the client's Pause connection).
// The phone stays on the list with its record, pairing and settings, but
// Pinky closes its session (the display stream, an iPhone's tunnel and
// WebDriverAgent helper, an Android phone's scrcpy and companion
// connection) and does not open it again, over its cable or Wi-Fi, until
// the person resumes it. A task on it ends, and agents get device_paused
// until then. The state is saved, so the phone stays paused when Pinky
// starts again.
//
// Plugging the phone in resumes it: a cable is the person's doing, as when
// they add back a phone they removed (remove.go). Only a new plug counts,
// though: a phone paused while on its cable (most likely charging) stays
// paused until it is unplugged, or Pinky stops seeing it, and plugged in
// again. Otherwise pausing a phone that charges would undo itself at the
// next listing. The first listing after Pinky starts only notes where the
// phone is, for the same reason.

// pausedWhy is the offline reason of a paused phone (setOffline).
const pausedWhy = "paused"

// PauseDevice pauses Pinky's connection to a phone. Its task, if one runs,
// ends with OutcomePaused; the phone stays listed, offline, until
// ResumeDevice or until it is plugged in again. It returns
// ErrUnknownDevice for a phone the gateway does not list.
func (g *Gateway) PauseDevice(deviceID string) error {
	g.mu.Lock()
	d := g.devices[deviceID]
	if d == nil {
		g.mu.Unlock()
		return ErrUnknownDevice
	}
	if g.paused[d.id] {
		g.mu.Unlock()
		return nil
	}
	g.paused[d.id] = true
	// Where the phone is now: on its cable, the next plug resumes it, not
	// this one.
	d.pauseAway = !d.unlistedAt.IsZero() || !cabled(d.info) || d.info.Virtual
	if l := d.lease; l != nil {
		g.endLeaseLocked(l, OutcomePaused, "")
	}
	d.person, d.personInput = false, false
	id, name := d.id, d.name()
	if g.store != nil {
		g.rec.do(func(ctx context.Context) error { return g.store.SetDevicePaused(ctx, id, true) })
	}
	// Under g.mu, so the order with ResumeDevice holds.
	g.tellPausedLocked(id, true)
	g.mu.Unlock()
	g.log.Info("device paused", "device", id, "name", name)

	// Approvals about the phone that no task holds, such as an agent's
	// first connection to it.
	gone := g.approvals.WithdrawWhere(func(i approval.Info) bool { return i.Binding.Device == id }, OutcomePaused)
	g.closePendingSteps(gone, OutcomePaused)
	g.setOffline(d, pausedWhy)
	g.stopHubs(id)
	g.emit(Event{Kind: EventDevices, DeviceID: id})
	g.emit(Event{Kind: EventTasks, DeviceID: id})
	g.emit(Event{Kind: EventNeeds, DeviceID: id})
	return nil
}

// ResumeDevice undoes PauseDevice: Pinky connects to the phone again at
// once, over whatever it is listed on. Resuming a phone that is not paused
// does nothing. It returns ErrUnknownDevice for a phone the gateway does
// not list.
func (g *Gateway) ResumeDevice(deviceID string) error {
	g.mu.Lock()
	d := g.devices[deviceID]
	if d == nil {
		g.mu.Unlock()
		return ErrUnknownDevice
	}
	resumed := g.resumeLocked(d.id)
	g.mu.Unlock()
	if resumed {
		g.log.Info("device resumed", "device", d.id)
		g.emit(Event{Kind: EventDevices, DeviceID: d.id})
		g.discoverSoon()
	}
	return nil
}

// DevicePaused reports whether the person paused Pinky's connection to the
// phone.
func (g *Gateway) DevicePaused(deviceID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused[deviceID]
}

// resumeLocked takes the phone off the paused list, saves that and lets
// the providers connect to it again. It reports whether it was paused.
func (g *Gateway) resumeLocked(id string) bool {
	if !g.paused[id] {
		return false
	}
	delete(g.paused, id)
	if d := g.devices[id]; d != nil {
		d.pauseAway = false
		if d.offlineWhy == pausedWhy {
			d.offlineWhy = ""
		}
	}
	if g.store != nil {
		g.rec.do(func(ctx context.Context) error { return g.store.SetDevicePaused(ctx, id, false) })
	}
	g.tellPausedLocked(id, false)
	return true
}

// tellPausedLocked tells the providers that leave paused phones alone.
func (g *Gateway) tellPausedLocked(id string, paused bool) {
	for _, p := range g.opt.Providers {
		if ps, ok := p.(driver.Pauser); ok {
			ps.SetPaused(id, paused)
		}
	}
}

// pausedListedLocked applies a listing of a paused phone: it notes whether
// the phone is away from its cable, and resumes it when it is plugged in
// after it was. It reports whether the phone was resumed.
func (g *Gateway) pausedListedLocked(d *device, info driver.Info) bool {
	if !cabled(info) || info.Virtual {
		d.pauseAway = true
		return false
	}
	if !d.pauseAway {
		return false // still on the cable it was paused on
	}
	g.log.Info("paused device plugged in: resuming", "device", d.id)
	return g.resumeLocked(d.id)
}

// loadPaused reads the phones the person paused, before the first
// discovery: it tells the providers to leave them alone and lists each one
// Pinky has a record of, offline, so a paused phone that no provider
// lists (one on Wi-Fi) can still be resumed.
func (g *Gateway) loadPaused() {
	ids, err := g.store.PausedDevices(context.Background())
	if err != nil {
		g.log.Warn("history: paused devices", "err", err)
		return
	}
	for _, id := range ids {
		if g.removed[id] {
			continue
		}
		g.paused[id] = true
		g.tellPausedLocked(id, true)
		rec, ok := g.names[id]
		if !ok {
			continue
		}
		var p driver.Provider
		for _, x := range g.opt.Providers {
			if string(x.Platform()) == rec.Platform {
				p = x
				break
			}
		}
		if p == nil {
			continue
		}
		d := newDevice(p, driver.Info{ID: id, Platform: driver.Platform(rec.Platform), Name: rec.Name,
			Model: rec.Model, OSVersion: rec.OSVersion, Backend: rec.Backend, State: driver.StateOffline})
		d.offlineWhy = pausedWhy
		g.devices[id] = d
	}
}

// pausedLeaseLocked returns a phone of s whose lease ended as the person
// paused it and that is still paused, or nil.
func (g *Gateway) pausedLeaseLocked(s *Session) *device {
	for id := range s.pausedOn {
		if d := g.devices[id]; d != nil && g.paused[id] {
			return d
		}
	}
	return nil
}

// capturingLocked reports whether the phone sends its screen to Pinky
// now: its driver says it captures it (driver.CaptureReporter), or a
// stream of it is open (streams.go).
func (g *Gateway) capturingLocked(d *device) bool {
	if !d.online || d.dev == nil {
		return false
	}
	if c, ok := d.dev.(driver.CaptureReporter); ok && c.Capturing() {
		return true
	}
	g.hubMu.Lock()
	defer g.hubMu.Unlock()
	return g.hubs[hubKey(d.id, false)] != nil || g.hubs[hubKey(d.id, true)] != nil
}

// pollCapture tells the client when a phone starts or stops sending its
// screen: a driver stops capturing by itself (an iPhone's display stream
// when nobody looks), with no other change to say so.
func (g *Gateway) pollCapture() {
	g.mu.Lock()
	var changed []string
	for _, d := range g.devices {
		if c := g.capturingLocked(d); c != d.capturing {
			d.capturing = c
			changed = append(changed, d.id)
		}
	}
	g.mu.Unlock()
	for _, id := range changed {
		g.emit(Event{Kind: EventDevices, DeviceID: id})
	}
}

// pausedResult is what an agent gets for a phone the person paused.
func pausedResult(name string) *Result {
	return errResult(ErrDevicePaused, "The person paused Pinky's connection to %s in Pinky, so Pinky cannot see or use it. "+
		"Nothing was done. Ask the person to resume it (or plug it in by cable), or use another phone; then call phone_connect again.", name)
}
