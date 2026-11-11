package gateway

import (
	"context"
	"errors"
	"fmt"

	"github.com/DIYgod/pinky/internal/driver"
)

// ErrPhoneInUse means an agent holds the phone, so an automatic update of
// its companion app waits.
var ErrPhoneInUse = errors.New("an agent is using the phone")

// UpdateCompanion updates a phone's companion app without asking the
// person, for the client's automatic updates (driver.CompanionUpdater). It
// refuses while an agent holds the phone, and holds the phone's action lock
// while it installs, so an agent that connects meanwhile waits for the
// update rather than reading the screen through an app being replaced. The
// person's own Update button uses InstallCompanion.
func (g *Gateway) UpdateCompanion(ctx context.Context, deviceID string) (driver.CompanionUpdate, error) {
	d, err := g.device(deviceID)
	if err != nil {
		return driver.CompanionUpdate{}, err
	}
	d.act.Lock()
	defer d.act.Unlock()
	g.mu.Lock()
	dev, leased := d.dev, d.lease != nil
	g.mu.Unlock()
	if dev == nil {
		return driver.CompanionUpdate{}, driver.ErrOffline
	}
	if leased {
		return driver.CompanionUpdate{}, ErrPhoneInUse
	}
	cu, ok := dev.(driver.CompanionUpdater)
	if !ok {
		return driver.CompanionUpdate{}, fmt.Errorf("%s cannot update its companion app by itself: %w", d.name(), driver.ErrUnsupported)
	}
	res, err := cu.UpdateCompanion(ctx)
	if res.Updated {
		g.emit(Event{Kind: EventDevices, DeviceID: deviceID})
	}
	return res, err
}
