package fakeapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Installing the iPhone helper from the window, as the gateway does on a
// Mac with Xcode (gateway.InstallHelper): off until SetHelperInstall, so
// the client keeps its links to how to install the helper by hand.

// helperFake is what the fake's helper installs do.
type helperFake struct {
	can    bool
	err    error               // what InstallHelper fails with (SetHelperError)
	teams  []driver.HelperTeam // the teams to choose from (SetHelperTeams)
	unlock bool                // it waits for the phone to be unlocked (SetHelperUnlock)
	hold   chan struct{}       // closed when held installs go on (HoldHelper)
	runs   []HelperRun
}

// HelperRun is an InstallHelper call: the phone ("" for the one Xcode
// finds) and the team it chose ("" for none).
type HelperRun struct {
	DeviceID, Team string
}

// helperValid is how long a helper the fake installs lasts: a free Apple
// ID's 7 days.
const helperValid = 7 * 24 * time.Hour

// CanInstallHelper implements the client's helper install: what
// SetHelperInstall set.
func (a *API) CanInstallHelper() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hlp.can
}

// InstallHelper pretends to install the helper in a second or so at Speed:
// it prepares, builds, waits for the phone to be unlocked when
// SetHelperUnlock says so, and installs, reporting each step. It asks for
// a team when there are several and none was chosen, fails while building
// with what SetHelperError set, and waits while building while HoldHelper
// holds it. Once installed, the phone is online, its helper good for 7
// days.
func (a *API) InstallHelper(ctx context.Context, deviceID, team string, progress func(driver.HelperStep)) error {
	a.mu.Lock()
	h := &a.hlp
	if !h.can {
		a.mu.Unlock()
		return &driver.HelperInstallError{Fix: driver.HelperFixXcode, Err: errors.New("installing the iPhone helper needs a Mac with Xcode")}
	}
	if deviceID != "" {
		d := a.device(deviceID)
		if d == nil {
			a.mu.Unlock()
			return fmt.Errorf("fakeapi: no device %q: %w", deviceID, driver.ErrNotFound)
		}
		if d.info.Platform != driver.IOS || d.info.Virtual {
			a.mu.Unlock()
			return fmt.Errorf("%s needs no helper installed: %w", d.info.Name, driver.ErrUnsupported)
		}
		for _, t := range a.tasks {
			if t.view.DeviceID == deviceID && t.view.Ended.IsZero() {
				a.mu.Unlock()
				return gateway.ErrPhoneInUse
			}
		}
	}
	h.runs = append(h.runs, HelperRun{DeviceID: deviceID, Team: team})
	teams, fail, unlock, hold := h.teams, h.err, h.unlock, h.hold
	a.mu.Unlock()
	step := func(s driver.HelperStep) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(float64(250*time.Millisecond) / a.opt.Speed)):
		}
		if progress != nil {
			progress(s)
		}
		return nil
	}
	if err := step(driver.HelperPreparing); err != nil {
		return err
	}
	if team == "" && len(teams) > 1 {
		return &driver.HelperTeamsError{Teams: teams}
	}
	if err := step(driver.HelperBuilding); err != nil {
		return err
	}
	if hold != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-hold:
		}
	}
	if fail != nil {
		return fail
	}
	if unlock {
		if err := step(driver.HelperUnlock); err != nil {
			return err
		}
	}
	if err := step(driver.HelperInstalling); err != nil {
		return err
	}
	a.mu.Lock()
	var ids []string
	for _, d := range a.devices {
		if d.info.Platform == driver.IOS && !d.info.Virtual && (d.info.ID == deviceID || deviceID == "") {
			ids = append(ids, d.info.ID)
		}
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.SetIPhone(id, driver.Info{State: driver.StateOnline, HelperExpires: time.Now().Add(helperValid)})
	}
	return nil
}

// SetHelperInstall says whether the fake computer is a Mac with Xcode,
// which installs the iPhone helper from the window.
func (a *API) SetHelperInstall(can bool) {
	a.mu.Lock()
	a.hlp.can = can
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices})
}

// SetHelperError makes InstallHelper fail while building with err; nil
// makes it work again.
func (a *API) SetHelperError(err error) {
	a.mu.Lock()
	a.hlp.err = err
	a.mu.Unlock()
}

// SetHelperTeams gives the teams this Mac signs for: with several,
// InstallHelper asks which one, unless one was chosen.
func (a *API) SetHelperTeams(teams []driver.HelperTeam) {
	a.mu.Lock()
	a.hlp.teams = teams
	a.mu.Unlock()
}

// SetHelperUnlock makes InstallHelper wait for the phone to be unlocked
// before it installs.
func (a *API) SetHelperUnlock(unlock bool) {
	a.mu.Lock()
	a.hlp.unlock = unlock
	a.mu.Unlock()
}

// HoldHelper(true) makes InstallHelper wait once it reported building,
// until HoldHelper(false), so a test sees the progress however slowly it
// runs.
func (a *API) HoldHelper(hold bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case hold && a.hlp.hold == nil:
		a.hlp.hold = make(chan struct{})
	case !hold && a.hlp.hold != nil:
		close(a.hlp.hold)
		a.hlp.hold = nil
	}
}

// HelperRuns returns the InstallHelper calls so far.
func (a *API) HelperRuns() []HelperRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]HelperRun(nil), a.hlp.runs...)
}
