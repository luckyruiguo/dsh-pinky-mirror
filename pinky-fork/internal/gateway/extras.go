package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// Client features beyond API: pairing phones over Wi-Fi, the Android
// companion app, background displays, screen recordings, and history
// retention and export. The client finds them with type assertions, so
// fakes and older gateways may leave them out.

// ErrNoPairing is returned when no provider can pair phones over Wi-Fi.
var ErrNoPairing = errors.New("no connected platform can pair phones over Wi-Fi")

// StartPairing begins pairing a phone over Wi-Fi by QR code, with the first
// provider that can (driver.Pairer). The phone shows up in Devices once
// Wait returns.
func (g *Gateway) StartPairing(ctx context.Context) (driver.Pairing, error) {
	for _, p := range g.opt.Providers {
		if pr, ok := p.(driver.Pairer); ok {
			pg, err := pr.StartPairing(ctx)
			if err != nil {
				return nil, err
			}
			return &discoverOnPair{Pairing: pg, g: g}, nil
		}
	}
	return nil, ErrNoPairing
}

// PairWithCode pairs a phone with the address and six-digit code it shows
// under Pair device with pairing code. A phone the person removed is back
// once it pairs.
func (g *Gateway) PairWithCode(ctx context.Context, addr, code string) (driver.Info, error) {
	for _, p := range g.opt.Providers {
		if pr, ok := p.(driver.Pairer); ok {
			info, err := pr.PairWithCode(ctx, addr, code)
			if err == nil {
				g.unremove(info.ID)
				g.discover(ctx)
			}
			g.logPairing(ctx, "code", err)
			return info, err
		}
	}
	return driver.Info{}, ErrNoPairing
}

// ConnectWiFi connects a phone paired over Wi-Fi at the address it shows
// under Wireless debugging (IP address & Port), for a network on which the
// driver cannot find it by itself (driver.ErrPairedNotFound from a
// pairing). A phone the person removed is back once it connects.
func (g *Gateway) ConnectWiFi(ctx context.Context, addr string) (driver.Info, error) {
	for _, p := range g.opt.Providers {
		if wc, ok := p.(driver.WiFiConnector); ok {
			info, err := wc.ConnectWiFi(ctx, addr)
			if err == nil {
				g.unremove(info.ID)
				g.discover(ctx)
			}
			g.logPairing(ctx, "connect", err)
			return info, err
		}
	}
	return driver.Info{}, ErrNoPairing
}

// discoverOnPair looks for devices as soon as a pairing finishes, rather
// than at the next discovery tick, and lets a phone the person removed
// back on the list.
type discoverOnPair struct {
	driver.Pairing
	g *Gateway
}

func (p *discoverOnPair) Wait(ctx context.Context) (driver.Info, error) {
	info, err := p.Pairing.Wait(ctx)
	if err == nil {
		p.g.unremove(info.ID)
		p.g.discover(ctx)
	}
	p.g.logPairing(ctx, "qr", err)
	return info, err
}

// logPairing logs why pairing over Wi-Fi failed, in the driver's and the
// pairing tool's own words, which the client words for the person: the
// diagnostics then show them. Not when the person walked away.
func (g *Gateway) logPairing(ctx context.Context, how string, err error) {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return
	}
	g.log.Info("pairing over Wi-Fi failed", "how", how, "err", err)
}

// Progress passes the pairing's progress on (driver.PairingReporter).
func (p *discoverOnPair) Progress() driver.PairingProgress {
	if r, ok := p.Pairing.(driver.PairingReporter); ok {
		return r.Progress()
	}
	return driver.PairingProgress{}
}

// CompanionState reports whether a phone's companion app is ready, or
// driver.ErrUnsupported for phones without one (iPhones).
func (g *Gateway) CompanionState(ctx context.Context, deviceID string) (driver.CompanionState, error) {
	ci, err := g.companionInstaller(deviceID)
	if err != nil {
		return "", err
	}
	return ci.CompanionState(ctx)
}

// InstallCompanion installs or updates a phone's companion app from the APK
// bundled with Pinky. Only the person asks for it, from the client.
func (g *Gateway) InstallCompanion(ctx context.Context, deviceID string) error {
	ci, err := g.companionInstaller(deviceID)
	if err != nil {
		return err
	}
	if err := ci.InstallCompanion(ctx); err != nil {
		return err
	}
	g.emit(Event{Kind: EventDevices, DeviceID: deviceID})
	return nil
}

func (g *Gateway) companionInstaller(deviceID string) (driver.CompanionInstaller, error) {
	d, err := g.device(deviceID)
	if err != nil {
		return nil, err
	}
	dev := g.driverOf(d)
	if dev == nil {
		return nil, driver.ErrOffline
	}
	ci, ok := dev.(driver.CompanionInstaller)
	if !ok {
		return nil, fmt.Errorf("%s has no companion app: %w", d.name(), driver.ErrUnsupported)
	}
	return ci, nil
}

// RecordingView is a task's screen recording.
type RecordingView struct {
	TaskID string
	Path   string // an MP4 file, in the codec the phone sent (H.264 or HEVC)
	Start  time.Time
	Length time.Duration
	Marks  []RecordingMark
}

// RecordingMark is a step's place in a recording.
type RecordingMark struct {
	At   time.Duration // from Start
	Step int           // StepView.Index
	Text string        // the step's first line
}
