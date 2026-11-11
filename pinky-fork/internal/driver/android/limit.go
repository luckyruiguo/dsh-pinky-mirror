package android

import (
	"context"
	"fmt"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// Limiting the driver to some phones (PINKY_DEVICES, for development while
// other tools use the rest). The driver then touches no other device: it
// does not even ask one what it is (deviceProps), and connects to none of
// the others over the network. It tells a connection's phone without
// asking it from its adb serial alone: a cable's is the phone's id, a
// Wireless debugging connection's name holds the phone's serial, and the
// addresses the driver connected to are kept with their phone. A phone the
// person adds in Pinky (pairing, an address they type) and an emulator
// Pinky starts are used too: the driver knows their connections. Any
// other connection at an address, and an emulator another tool started,
// stays unused unless its adb serial is one of the ids allowed.

// Limit has the driver use only the phones allowed accepts, by id. Call it
// before the provider is used; nil lifts the limit.
func (p *Provider) Limit(allowed func(id string) bool) {
	p.mu.Lock()
	p.only = allowed
	p.mu.Unlock()
}

func (p *Provider) limit() func(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.only
}

// mayUse reports whether the driver may use the device at an adb
// connection (Limit).
func (p *Provider) mayUse(e adb.DeviceEntry) bool {
	p.mu.Lock()
	only, in := p.only, p.letIn[e.Serial]
	p.mu.Unlock()
	if only == nil || in || only(e.Serial) || p.avd.mine(e.Serial) {
		return true
	}
	id := p.knownID(e)
	return id != "" && only(id)
}

// mayUseID reports whether the driver may use the phone with this id.
func (p *Provider) mayUseID(id string) bool {
	only := p.limit()
	return only == nil || only(id)
}

// knownID is the id of the phone at an adb connection as far as the
// driver knows it without asking the phone (phoneID); "" when it does
// not.
func (p *Provider) knownID(e adb.DeviceEntry) string {
	if !e.Wireless() {
		return e.Serial
	}
	if s := connectSerial(e.Serial); s != "" {
		return s
	}
	if p.wifi != nil {
		if s := guidSerial(p.wifi.guidAt(e.Serial)); s != "" {
			return s
		}
	}
	for _, n := range p.net.list() {
		if e.Serial == n.lanSerial() || e.Serial == n.tailSerial() {
			return n.ID
		}
	}
	return ""
}

// allowConn lets the driver use the connection with this adb serial: one
// to the phone the person is adding in Pinky.
func (p *Provider) allowConn(serial string) {
	p.mu.Lock()
	if p.only != nil {
		if p.letIn == nil {
			p.letIn = map[string]bool{}
		}
		p.letIn[serial] = true
	}
	p.mu.Unlock()
}

// devices is the adb server's list of devices, of those the driver may
// use.
func (p *Provider) devices(ctx context.Context) ([]adb.DeviceEntry, error) {
	entries, err := p.cfg.Client.Devices(ctx)
	return p.usable(entries), err
}

// usable returns the entries of the devices the driver may use.
func (p *Provider) usable(entries []adb.DeviceEntry) []adb.DeviceEntry {
	if p.limit() == nil {
		return entries
	}
	out := make([]adb.DeviceEntry, 0, len(entries))
	for _, e := range entries {
		if p.mayUse(e) {
			out = append(out, e)
		}
	}
	return out
}

// errNotUsed is the error for a device the driver may not use (Limit).
func errNotUsed(serial string) error {
	return fmt.Errorf("android: %s is not one of the phones Pinky may use: %w", serial, driver.ErrNotFound)
}
