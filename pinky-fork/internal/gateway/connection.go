package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// How Pinky connects to each phone, for drivers that can do it more than
// one way (driver.ConnectionChooser; iPhone: CoreDevice or WebDriverAgent).
// The person chooses in Settings › Phones; the choice is saved per phone and
// applied by reconnecting the phone at once.

// applyConnections hands the saved choices to the providers that take them.
// Ids are unique across platforms, so each provider just keeps the ones it
// will see.
func (g *Gateway) applyConnections(ways map[string]string) {
	g.mu.Lock()
	for id, way := range ways {
		if way != driver.ConnectionAuto {
			g.chosen[id] = way
		}
	}
	g.mu.Unlock()
	for _, p := range g.opt.Providers {
		cc, ok := p.(driver.ConnectionChooser)
		if !ok {
			continue
		}
		for id, way := range ways {
			if err := cc.SetConnection(id, way); err != nil {
				g.log.Warn("saved connection", "device", id, "way", way, "err", err)
			}
		}
	}
}

// chooser returns the provider of a phone that offers a choice of
// connections.
func (g *Gateway) chooser(deviceID string) (*device, driver.ConnectionChooser, error) {
	d, err := g.device(deviceID)
	if err != nil {
		return nil, nil, err
	}
	g.mu.Lock()
	p := d.provider
	g.mu.Unlock()
	cc, ok := p.(driver.ConnectionChooser)
	if !ok {
		return nil, nil, fmt.Errorf("%s connects only one way: %w", d.name(), driver.ErrUnsupported)
	}
	return d, cc, nil
}

// Connections describes how a phone can be connected and how it is now, or
// returns driver.ErrUnsupported for a phone whose driver has one way only.
func (g *Gateway) Connections(ctx context.Context, deviceID string) (driver.Connections, error) {
	d, cc, err := g.chooser(deviceID)
	if err != nil {
		return driver.Connections{}, err
	}
	return cc.Connections(ctx, d.id), nil
}

// reconnectTimeout bounds the discovery pass that reopens a phone after its
// connection changed.
const reconnectTimeout = 60 * time.Second

// SetConnection chooses how Pinky connects to a phone (a Connection ID,
// driver.ConnectionAuto for the driver's own rule), saves it and
// reconnects the phone that way now. A task on the phone survives the
// switch like a short disconnection (Options.ReconnectGrace). A way the
// phone cannot use is still kept: the phone then shows why, and how to fix
// it, until it can, or the person chooses another.
func (g *Gateway) SetConnection(deviceID, way string) error {
	d, cc, err := g.chooser(deviceID)
	if err != nil {
		return err
	}
	// Under g.mu, so that a task's move (switchTo) cannot give the
	// provider another way after this one.
	g.mu.Lock()
	if err := cc.SetConnection(d.id, way); err != nil {
		g.mu.Unlock()
		return err
	}
	if g.store != nil {
		id := d.id
		g.rec.do(func(ctx context.Context) error { return g.store.SetDeviceConnection(ctx, id, way) })
	}
	if way == driver.ConnectionAuto {
		delete(g.chosen, d.id)
	} else {
		g.chosen[d.id] = way
	}
	forgetTaskWay(d)                    // the person's choice beats a task's (taskconn.go)
	d.moving = time.Now().Add(moveWait) // calls wait while it reconnects
	g.mu.Unlock()
	g.setOffline(d, "reconnecting the way you chose")
	go func() {
		ctx, cancel := context.WithTimeout(g.ctx, reconnectTimeout)
		defer cancel()
		g.discover(ctx)
	}()
	g.emit(Event{Kind: EventDevices, DeviceID: d.id})
	return nil
}
