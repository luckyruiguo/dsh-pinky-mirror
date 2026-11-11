package telemetry

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/secret"
)

// Gateway is the gateway as the desktop client uses it, counting what the
// person does there: how they answer approvals, unlocking from the
// computer, taking over, pairing, installing the companion app, background
// displays, agent scopes and exports. It embeds the gateway, so the client
// finds each of the gateway's methods as before, with the same signature
// (TestGatewayKeepsMethods).
type Gateway struct {
	*gateway.Gateway
	c *Client
}

// WrapGateway returns g for the desktop client, counting into c.
func WrapGateway(g *gateway.Gateway, c *Client) *Gateway { return &Gateway{Gateway: g, c: c} }

var _ gateway.API = (*Gateway)(nil)

// Decide answers a need, counting the answer by what the need asked.
func (g *Gateway) Decide(needID string, d gateway.Decision, note string) error {
	kind := "other"
	for _, n := range g.Gateway.Needs() {
		if n.ID == needID {
			kind = needKind(n)
		}
	}
	g.c.markDecided(needID)
	err := g.Gateway.Decide(needID, d, note)
	switch {
	case kind == "unlock" && d != gateway.Deny && d != gateway.TakeOver:
		g.c.Count(Key("unlock", unlockResult(err)))
	case err != nil:
		g.c.Count(Key("needs", kind, "error"))
	default:
		g.c.Count(Key("needs", kind, decisionName(d)))
	}
	return err
}

// Unlock unlocks a phone from the computer, counting how it went.
func (g *Gateway) Unlock(deviceID string) error {
	err := g.Gateway.Unlock(deviceID)
	g.c.Count(Key("unlock", unlockResult(err)))
	return err
}

// TakeOver counts the person taking a phone over.
func (g *Gateway) TakeOver(deviceID string) error {
	g.c.Count("person.take_over")
	return g.Gateway.TakeOver(deviceID)
}

// HandBack counts the person handing a phone back.
func (g *Gateway) HandBack(deviceID, note string) error {
	g.c.Count("person.hand_back")
	return g.Gateway.HandBack(deviceID, note)
}

// Stop counts the person stopping a task.
func (g *Gateway) Stop(deviceID string) error {
	g.c.Count("person.stop")
	return g.Gateway.Stop(deviceID)
}

// StopAll counts the emergency stop.
func (g *Gateway) StopAll() error {
	g.c.Count("person.stop_all")
	return g.Gateway.StopAll()
}

// StartPairing counts pairing a phone over Wi-Fi by QR code, and how it
// ended.
func (g *Gateway) StartPairing(ctx context.Context) (driver.Pairing, error) {
	p, err := g.Gateway.StartPairing(ctx)
	if err != nil {
		g.c.Count(Key("pairing", "qr", errorName(err)))
		return nil, err
	}
	g.c.Count("pairing.qr.started")
	return &countedPairing{Pairing: p, c: g.c}, nil
}

type countedPairing struct {
	driver.Pairing
	c *Client
}

func (p *countedPairing) Wait(ctx context.Context) (driver.Info, error) {
	info, err := p.Pairing.Wait(ctx)
	switch {
	case err == nil:
		p.c.Count("pairing.qr.paired")
	case errors.Is(err, context.Canceled):
		p.c.Count("pairing.qr.cancelled")
	default:
		p.c.Count(Key("pairing", "qr", errorName(err)))
	}
	return info, err
}

// PairWithCode counts pairing a phone over Wi-Fi with a code.
func (g *Gateway) PairWithCode(ctx context.Context, addr, code string) (driver.Info, error) {
	info, err := g.Gateway.PairWithCode(ctx, addr, code)
	if err == nil {
		g.c.Count("pairing.code.paired")
	} else {
		g.c.Count(Key("pairing", "code", errorName(err)))
	}
	return info, err
}

// InstallCompanion counts installing the Android companion app.
func (g *Gateway) InstallCompanion(ctx context.Context, deviceID string) error {
	err := g.Gateway.InstallCompanion(ctx, deviceID)
	if err == nil {
		g.c.Count("companion.installed")
	} else {
		g.c.Count(Key("companion", "install", errorName(err)))
	}
	return err
}

// SetBackground counts turning background displays on and off.
func (g *Gateway) SetBackground(deviceID string, on bool) error {
	err := g.Gateway.SetBackground(deviceID, on)
	if err == nil {
		if on {
			g.c.Count("background.turned_on")
		} else {
			g.c.Count("background.turned_off")
		}
	}
	return err
}

// SetAgentScope counts the scopes the person gives agents.
func (g *Gateway) SetAgentScope(name string, scope gateway.Scope) {
	g.c.Count(Key("agents", "scope", string(scope.Mode)))
	g.Gateway.SetAgentScope(name, scope)
}

// ExportHistory counts exports of the activity.
func (g *Gateway) ExportHistory(ctx context.Context, taskIDs []string, dir string) (string, error) {
	path, err := g.Gateway.ExportHistory(ctx, taskIDs, dir)
	if err == nil {
		g.c.Count("history.exported")
	} else {
		g.c.Count(Key("history", "export", errorName(err)))
	}
	return path, err
}

// WipeHistory counts deleting all activity.
func (g *Gateway) WipeHistory(ctx context.Context) error {
	err := g.Gateway.WipeHistory(ctx)
	if err == nil {
		g.c.Count("history.wiped")
	}
	return err
}

func decisionName(d gateway.Decision) string {
	switch d {
	case gateway.AllowOnce, gateway.AllowTask, gateway.AllowAlways, gateway.Deny, gateway.TakeOver:
		return string(d)
	}
	return "other"
}

// unlockResult names how unlocking from the computer went.
func unlockResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, gateway.ErrWrongPIN):
		return "wrong_pin"
	case errors.Is(err, gateway.ErrNoUnlock):
		return "unavailable"
	case errors.Is(err, secret.ErrCancelled):
		return "cancelled"
	case errors.Is(err, secret.ErrNotFound):
		return "no_pin"
	case errors.Is(err, secret.ErrUnsupported):
		return "no_keychain"
	}
	return errorName(err)
}

// errorName sorts an error into a few fixed kinds, for counting; the
// message itself is never kept.
func errorName(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, driver.ErrUnsupported):
		return "unsupported"
	case errors.Is(err, driver.ErrOffline):
		return "offline"
	case errors.Is(err, driver.ErrLocked):
		return "locked"
	case errors.Is(err, driver.ErrNotFound), errors.Is(err, gateway.ErrUnknownDevice):
		return "not_found"
	case errors.Is(err, gateway.ErrNoPairing):
		return "unavailable"
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return "timeout"
	}
	return "failed"
}

// --- needs answered in the client ---

// markDecided remembers that the person answered a need, so the observer
// does not count it as unanswered when it goes away.
func (c *Client) markDecided(id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.decided == nil {
		c.decided = map[string]time.Time{}
	}
	now := c.now()
	for k, at := range c.decided {
		if now.Sub(at) > time.Hour {
			delete(c.decided, k)
		}
	}
	c.decided[id] = now
}

// takeDecided reports whether the person answered a need, and forgets it.
func (c *Client) takeDecided(id string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.decided[id]
	delete(c.decided, id)
	return ok
}
