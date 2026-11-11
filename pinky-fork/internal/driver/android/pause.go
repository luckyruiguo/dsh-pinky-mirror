package android

import "github.com/DIYgod/pinky/internal/driver"

// Phones the person paused in Pinky (driver.Pauser). The gateway does not
// open them, so no scrcpy session or companion connection runs; the driver
// does not reconnect them over Wi-Fi either, as for a phone the person
// removed (forget.go). Unlike removing, pausing drops nothing: the phone's
// addresses and pairing stay, and resuming reconnects it as before.

// SetPaused implements driver.Pauser.
func (p *Provider) SetPaused(id string, paused bool) {
	if id == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !paused {
		delete(p.paused, id)
		return
	}
	if p.paused == nil {
		p.paused = map[string]bool{}
	}
	p.paused[id] = true
}

var _ driver.Pauser = (*Provider)(nil)

// Capturing implements driver.CaptureReporter: the scrcpy session with
// video runs, so the phone encodes its screen for Pinky.
func (d *Device) Capturing() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	l := d.main
	return !d.closed && l != nil && l.video && !l.ended()
}

var _ driver.CaptureReporter = (*Device)(nil)
