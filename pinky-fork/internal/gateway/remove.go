package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/driver"
)

// Removing a phone from Pinky (the client's Remove this phone). The phone
// leaves the list with its record and its settings and, when the person
// asks, its history; its saved PIN is the client's to delete, as the client
// keeps it in the keychain. The gateway remembers that the person removed
// it, so a phone that a driver keeps listing over the network by itself
// (an Android phone the adb server reconnects) stays off the list. It
// comes back when the person adds it again: pairs it (StartPairing,
// PairWithCode) or connects it by address (ConnectWiFi), or plugs it in,
// since a phone listed with a cable is the person's doing. A phone still
// plugged in comes back at once, as a new phone.

// RemoveOptions say what removing a phone deletes besides the phone itself.
type RemoveOptions struct {
	// History also deletes the phone's tasks, with their steps,
	// screenshots and recordings. Without it they stay in Activity.
	History bool
}

// ErrHistoryNotDeleted is wrapped by RemoveDevice when the phone was
// removed but deleting its history failed.
var ErrHistoryNotDeleted = errors.New("the phone was removed, but its history could not be deleted")

// removeTimeout bounds what removing a phone waits for: the driver
// dropping its connections, and deleting its history.
const removeTimeout = time.Minute

// RemoveDevice removes a phone from Pinky. Its task, if one runs, stops as
// with Stop; the phone's connection closes, and its connections over the
// network that the driver can drop go too (driver.Forgetter); its record
// and settings (background displays, its read-only switch, how Pinky
// connects to it) are deleted. A phone the providers list over the network
// stays off the list until the person pairs it again or plugs it in. It
// returns ErrUnknownDevice for a phone the gateway does not list, and an
// error wrapping ErrHistoryNotDeleted when the phone went but its history
// did not.
func (g *Gateway) RemoveDevice(ctx context.Context, deviceID string, opt RemoveOptions) error {
	at := time.Now()
	g.mu.Lock()
	d := g.devices[deviceID]
	if d == nil {
		g.mu.Unlock()
		return ErrUnknownDevice
	}
	if l := d.lease; l != nil {
		g.endLeaseLocked(l, OutcomeStopped, "")
	}
	d.removed = true
	delete(g.devices, d.id)
	delete(g.background, d.id)
	delete(g.known, d.id)
	delete(g.chosen, d.id)
	forgetTaskWay(d)
	g.removed[d.id] = true
	if g.paused[d.id] {
		// Added back, it is not paused (its row goes with ForgetDevice).
		delete(g.paused, d.id)
		g.tellPausedLocked(d.id, false)
	}
	dev := d.dev
	d.dev, d.online, d.opening = nil, false, false
	d.kept = nil
	g.inputLocked(d) // drops a history screenshot being taken
	if d.inflight != nil {
		d.inflight()
	}
	g.wakeLocked(d)
	id, p, name := d.id, d.provider, d.name()
	if g.store != nil {
		g.rec.do(func(ctx context.Context) error {
			if err := g.store.ForgetDevice(ctx, id); err != nil {
				return err
			}
			return g.store.SetDeviceRemoved(ctx, id, true)
		})
	}
	// Under g.mu, so a discovery that adds the phone back (on a cable)
	// tells the provider to Remember it after this.
	f, forgets := p.(driver.Forgetter)
	if forgets {
		f.Forget(id)
	}
	g.mu.Unlock()
	g.log.Info("device removed", "device", id, "name", name, "history", opt.History)

	// Approvals about the phone that no task holds, such as an agent's
	// first connection to it.
	gone := g.approvals.WithdrawWhere(func(i approval.Info) bool { return i.Binding.Device == id }, OutcomeStopped)
	g.closePendingSteps(gone, OutcomeStopped)
	g.stopHubs(id)
	if dev != nil {
		go dev.Close()
	}
	if err := g.policy.ForgetDevice(id); err != nil {
		g.log.Warn("remove device: read-only switch", "device", id, "err", err)
	}
	// How the person chose to connect it (connection.go): the provider
	// drops the choice and what it started for it, such as an iPhone's
	// WebDriverAgent helper waiting for the passcode. Not SetConnection,
	// which would take the phone offline and discover it again.
	if cc, ok := p.(driver.ConnectionChooser); ok {
		if err := cc.SetConnection(id, driver.ConnectionAuto); err != nil {
			g.log.Warn("remove device: connection", "device", id, "err", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, removeTimeout)
	defer cancel()
	if forgets {
		if err := f.Disconnect(ctx, id); err != nil {
			g.log.Warn("remove device: disconnect", "device", id, "err", err)
		}
	}
	var err error
	if opt.History {
		if herr := g.deleteDeviceHistory(ctx, id, at); herr != nil {
			err = fmt.Errorf("%w: %v", ErrHistoryNotDeleted, herr)
		}
	}
	g.emit(Event{Kind: EventDevices, DeviceID: id})
	g.emit(Event{Kind: EventTasks, DeviceID: id})
	g.emit(Event{Kind: EventNeeds, DeviceID: id})
	return err
}

// deleteDeviceHistory deletes the tasks of a removed phone that started
// before it was removed, once what was being written about them is.
func (g *Gateway) deleteDeviceHistory(ctx context.Context, id string, before time.Time) error {
	if g.store != nil {
		g.Flush()
		n, err := g.store.DeleteDeviceHistory(ctx, id, before)
		if err != nil {
			return err
		}
		g.log.Info("remove device: history", "device", id, "tasks", n.Tasks, "steps", n.Steps, "screenshots", n.Media,
			"recordings", n.Recordings, "sessions", n.Sessions)
	}
	g.mu.Lock()
	kept := g.ended[:0]
	for _, t := range g.ended {
		if t.deviceID != id || !t.started.Before(before) {
			kept = append(kept, t)
		}
	}
	g.ended = kept
	g.mu.Unlock()
	return nil
}

// cabled reports whether a provider lists the phone over a cable (or on
// this computer, as an emulator), rather than over the network by itself.
func cabled(info driver.Info) bool { return info.Connection != "wifi" }

// unremove lets a phone the person removed back on the list: they paired
// it again or connected it by address.
func (g *Gateway) unremove(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.removed[id] {
		g.addedBackLocked(id)
	}
	// Paired or connected by address: the person wants Pinky to use it.
	if g.resumeLocked(id) {
		g.log.Info("paused device connected again: resuming", "device", id)
	}
}

// addedBackLocked takes a removed phone off the removed list, saves that,
// and lets the providers connect to it by themselves again. Under g.mu,
// so it keeps its order with RemoveDevice's Forget.
func (g *Gateway) addedBackLocked(id string) {
	delete(g.removed, id)
	g.log.Info("removed device added back", "device", id)
	if g.store != nil {
		g.rec.do(func(ctx context.Context) error { return g.store.SetDeviceRemoved(ctx, id, false) })
	}
	for _, p := range g.opt.Providers {
		if f, ok := p.(driver.Forgetter); ok {
			f.Remember(id)
		}
	}
}

// loadRemoved reads the phones the person removed, and tells the providers
// that connect to phones by themselves to leave them alone, before the
// first discovery.
func (g *Gateway) loadRemoved() {
	ids, err := g.store.RemovedDevices(context.Background())
	if err != nil {
		g.log.Warn("history: removed devices", "err", err)
		return
	}
	for _, id := range ids {
		g.removed[id] = true
		for _, p := range g.opt.Providers {
			if f, ok := p.(driver.Forgetter); ok {
				f.Forget(id)
			}
		}
	}
}
