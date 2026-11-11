package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// Virtual phones: the iOS Simulators and Android emulators on this
// computer, which the person starts from Add phone (driver.VirtualLauncher).
// Agents get no tool for this.

// virtualWatch is how long StartVirtual waits for a virtual phone it
// started to be listed, and how often it looks.
const (
	virtualWatch      = 3 * time.Minute
	virtualWatchEvery = 2 * time.Second
)

// VirtualPhones lists the virtual phones of each platform whose provider
// starts them, for Add phone. A provider that fails to list them gives its
// platform with the error as Detail.
func (g *Gateway) VirtualPhones(ctx context.Context) ([]driver.VirtualPhones, error) {
	var out []driver.VirtualPhones
	for _, p := range g.opt.Providers {
		l, ok := p.(driver.VirtualLauncher)
		if !ok {
			continue
		}
		vp, err := l.VirtualPhones(ctx)
		if errors.Is(err, driver.ErrUnsupported) {
			continue
		}
		if err != nil {
			g.log.Warn("virtual phones", "platform", p.Platform(), "err", err)
			vp = driver.VirtualPhones{Platform: p.Platform(), Detail: err.Error()}
		}
		if vp.Platform == "" {
			vp.Platform = p.Platform()
		}
		out = append(out, vp)
	}
	return out, nil
}

// StartVirtual starts the platform's virtual phone with the id
// VirtualPhones gave it, or brings one that runs back into Pinky. The
// person asked for it, so a phone they removed is back once it runs.
func (g *Gateway) StartVirtual(ctx context.Context, platform driver.Platform, id string) error {
	return g.withLauncher(platform, func(l driver.VirtualLauncher) error {
		if err := l.StartVirtual(ctx, id); err != nil {
			return err
		}
		g.log.Info("virtual phone started", "platform", platform, "id", id)
		go g.watchVirtual(l, id)
		return nil
	})
}

// StopVirtual shuts the platform's virtual phone down. Its phone goes
// offline, as one unplugged does.
func (g *Gateway) StopVirtual(ctx context.Context, platform driver.Platform, id string) error {
	return g.withLauncher(platform, func(l driver.VirtualLauncher) error {
		if err := l.StopVirtual(ctx, id); err != nil {
			return err
		}
		g.log.Info("virtual phone stopped", "platform", platform, "id", id)
		g.discoverSoon()
		return nil
	})
}

// withLauncher runs fn with the platform's providers that start virtual
// phones, until one does not answer driver.ErrUnsupported.
func (g *Gateway) withLauncher(platform driver.Platform, fn func(driver.VirtualLauncher) error) error {
	for _, p := range g.opt.Providers {
		l, ok := p.(driver.VirtualLauncher)
		if !ok || p.Platform() != platform {
			continue
		}
		if err := fn(l); !errors.Is(err, driver.ErrUnsupported) {
			return err
		}
	}
	return fmt.Errorf("%s virtual phones: %w", platform, driver.ErrUnsupported)
}

// watchVirtual waits for a virtual phone the person started to run, as an
// emulator gets its adb serial only then, lets it back on the list if they
// had removed it, and looks for it at once.
func (g *Gateway) watchVirtual(l driver.VirtualLauncher, id string) {
	deadline := time.Now().Add(virtualWatch)
	for {
		ctx, cancel := context.WithTimeout(g.ctx, reconnectTimeout)
		vp, err := l.VirtualPhones(ctx)
		cancel()
		if err == nil {
			for _, v := range vp.Phones {
				if v.ID == id && v.DeviceID != "" {
					g.unremove(v.DeviceID)
					g.discoverSoon()
					return
				}
			}
		}
		if time.Now().After(deadline) {
			g.log.Warn("virtual phone did not start", "id", id, "err", err)
			return
		}
		select {
		case <-g.ctx.Done():
			return
		case <-time.After(virtualWatchEvery):
		}
	}
}
