package gateway

import (
	"context"
	"fmt"
	"sync"

	"github.com/DIYgod/pinky/internal/driver"
)

// CanInstallHelper says whether a provider can install the iPhone helper
// from the client: on a Mac with Xcode (driver.HelperInstaller).
func (g *Gateway) CanInstallHelper() bool {
	for _, p := range g.opt.Providers {
		if hi, ok := p.(driver.HelperInstaller); ok && hi.CanInstallHelper() {
			return true
		}
	}
	return false
}

// InstallHelper installs the WebDriverAgent helper on an iPhone, when the
// person asks from the client: on the phone deviceID, or with "" on the
// only one Xcode reaches (a phone being added, not listed yet). It lets go
// of the phone once the install begins, and looks for it again once it is
// done; progress gets each step. It refuses while an agent holds the phone.
func (g *Gateway) InstallHelper(ctx context.Context, deviceID, team string, progress func(driver.HelperStep)) error {
	var d *device
	var hi driver.HelperInstaller
	if deviceID != "" {
		var err error
		if d, err = g.device(deviceID); err != nil {
			return err
		}
		g.mu.Lock()
		p, virtual, leased := d.provider, d.info.Virtual, d.lease != nil
		g.mu.Unlock()
		var ok bool
		if hi, ok = p.(driver.HelperInstaller); !ok || virtual {
			return fmt.Errorf("%s needs no helper installed: %w", d.name(), driver.ErrUnsupported)
		}
		if leased {
			return ErrPhoneInUse
		}
	} else {
		for _, p := range g.opt.Providers {
			if h, ok := p.(driver.HelperInstaller); ok && h.CanInstallHelper() {
				hi = h
				break
			}
		}
		if hi == nil {
			return fmt.Errorf("installing the iPhone helper: %w", driver.ErrUnsupported)
		}
	}
	var once sync.Once
	step := func(s driver.HelperStep) {
		// The provider lists the phone as being set up from now on: let
		// go of it now rather than at the next discovery.
		once.Do(func() {
			if d != nil {
				g.setOffline(d, "installing the Pinky helper")
				g.emit(Event{Kind: EventDevices, DeviceID: d.id})
			}
		})
		if progress != nil {
			progress(s)
		}
	}
	err := hi.InstallHelper(ctx, deviceID, team, step)
	go func() {
		ctx, cancel := context.WithTimeout(g.ctx, reconnectTimeout)
		defer cancel()
		g.discover(ctx)
	}()
	g.emit(Event{Kind: EventDevices, DeviceID: deviceID})
	return err
}
