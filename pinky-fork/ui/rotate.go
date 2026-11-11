package ui

import (
	"context"
	"errors"
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Rotate in the ⋯ menu under the mirror: a button per way the screen can
// turn, the current one marked, turns the phone's screen through the
// gateway's SetDeviceSwitch, the switch agents set with phone_device. It is
// the person's input: an agent working on the phone's own screen pauses,
// one on a background display goes on. An iPhone or iPad over CoreDevice
// moves to the WebDriverAgent helper first when it has it; one without the
// helper shows Rotate disabled.

// deviceSwitcher is a gateway that reads and sets a phone's switches for
// the person.
type deviceSwitcher interface {
	DeviceSwitch(ctx context.Context, deviceID, name string) (string, error)
	SetDeviceSwitch(ctx context.Context, deviceID, name, value string) error
}

// rotateState is the client's part of it, by phone.
type rotateState struct {
	got     map[string]string // the rotation the gateway last read, or the one the person chose since
	at      map[string]time.Time
	loading map[string]bool
	// busy counts the turns on their way: a phone read meanwhile may not
	// have turned yet (an iPhone takes seconds), so it is not read, and
	// turns counts every turn asked for, so a read begun before one does
	// not undo its mark.
	busy  map[string]int
	turns map[string]int
}

func (s *rotateState) init() {
	if s.got == nil {
		s.got, s.at, s.loading = map[string]string{}, map[string]time.Time{}, map[string]bool{}
		s.busy, s.turns = map[string]int{}, map[string]int{}
	}
}

// rotateEvery is how old a read rotation may be while the menu is open:
// the person may turn a phone that turns by itself.
const rotateEvery = 2 * time.Second

// rotation is one button of the row.
type rotation struct {
	value string
	icon  *mg.SVG
	name  func() string
}

var rotations = []rotation{
	{driver.RotationPortrait, icRotPortrait, locales.PhoneRotatePortrait},
	{driver.RotationLandscape, icRotLandscape, locales.PhoneRotateLandscape},
	{driver.RotationReverseLandscape, icRotReverse, locales.PhoneRotateReverseLandscape},
	{driver.RotationUpsideDown, icRotUpsideDown, locales.PhoneRotateUpsideDown},
	{driver.RotationAuto, icRotAuto, locales.PhoneRotateAuto},
}

// rotationsOf lists the buttons a phone gets. An iPhone's or iPad's
// rotation lock is the person's alone, so they have no Auto-rotate, and an
// iPhone's screen never turns upside down: WebDriverAgent turns the device
// so, but the screen stays as it was (iPhone 12 Pro Max, iOS 18).
func rotationsOf(d gateway.DeviceView) []rotation {
	switch {
	case d.Info.Platform == driver.Android:
		return rotations
	case strings.HasPrefix(d.Info.Model, "iPad"):
		return rotations[:4]
	}
	return rotations[:3]
}

// canRotate reports whether the person turns this phone's screen from the
// menu.
func (a *app) canRotate(d gateway.DeviceView) bool {
	_, ok := a.api.(deviceSwitcher)
	return ok && d.Features.Rotate
}

// rotationOf returns the phone's rotation as last read, and reads it again
// off the main thread when it is old.
func (a *app) rotationOf(c *mg.Context, d gateway.DeviceView) string {
	sw, ok := a.api.(deviceSwitcher)
	if !ok {
		return ""
	}
	s := &a.rotate
	s.init()
	id := d.Info.ID
	if !s.loading[id] && s.busy[id] == 0 && time.Since(s.at[id]) >= rotateEvery {
		s.loading[id] = true
		turns := s.turns[id]
		go func() {
			ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
			defer cancel()
			v, err := sw.DeviceSwitch(ctx, id, driver.SwitchRotation)
			a.post(func() {
				s.loading[id], s.at[id] = false, time.Now()
				if s.turns[id] != turns {
					return // the person turned it meanwhile
				}
				if err == nil {
					s.got[id] = v
				} else {
					delete(s.got, id)
				}
			})
		}()
	}
	c.After(rotateEvery)
	return s.got[id]
}

// setRotation turns the phone's screen.
func (a *app) setRotation(d gateway.DeviceView, value string) {
	sw, ok := a.api.(deviceSwitcher)
	if !ok {
		return
	}
	s := &a.rotate
	s.init()
	id := d.Info.ID
	s.got[id], s.at[id] = value, time.Now()
	s.busy[id]++
	s.turns[id]++
	a.call(id, locales.PhoneRotateCall(), func() error {
		// An iPhone over CoreDevice moves to the helper first, which may
		// take up to a minute (the gateway's switchWait).
		ctx, cancel := context.WithTimeout(a.ctx, 90*time.Second)
		defer cancel()
		err := sw.SetDeviceSwitch(ctx, id, driver.SwitchRotation, value)
		var se *driver.SetupError
		passcode := errors.As(err, &se) && se.Setup == driver.SetupPasscode
		a.post(func() {
			s.busy[id]--
			s.at[id] = time.Now()
			if err != nil {
				s.at[id] = time.Time{} // read what it is
			}
			if passcode {
				// Moving to the helper, the phone asks for its passcode.
				a.toasts = append(a.toasts, locales.PhoneHelperPasscode(d.Info.Name))
			}
		})
		if passcode {
			return nil // said above, in the person's words
		}
		return err
	})
}

// rotateMenu is the Rotate row of the ⋯ menu; close closes the menu.
func (a *app) rotateMenu(c *mg.Context, d gateway.DeviceView, close func()) {
	p := pal(c)
	if !a.canRotate(d) {
		if d.Info.Platform == driver.IOS && d.Status != gateway.DeviceOffline {
			menuItem(c, locales.PhoneRotate(), p.ink3).Disabled(true).Tooltip(locales.PhoneRotateTooltip())
		}
		return
	}
	cur := a.rotationOf(c, d)
	id := d.Info.ID
	mg.Column(c).Gap(6).Padding(6, 10).Children(func() {
		mg.Column(c).Children(func() {
			mg.Text(c, locales.PhoneRotate()).FontSize(14).TextColor(p.ink)
			mg.Text(c, locales.PhoneRotateDesc()).FontSize(12.5).TextColor(p.ink2)
		})
		mg.Row(c).Gap(4).Children(func() {
			for _, r := range rotationsOf(d) {
				on := r.value == cur
				b := mg.ButtonBase(c.Key("rotate-"+id+"-"+r.value)).Size(40, 34).Radius(8).Center().
					Label(r.name()).Tooltip(r.name()).Selected(on)
				col := p.ink2
				switch {
				case on:
					b.Background(p.accentTint)
					col = p.accentInk
				case b.Hovered() || b.Focused():
					b.Background(p.bg3)
					col = p.ink
				}
				if b.Clicked() && !on {
					close()
					a.setRotation(d, r.value)
				}
				b.TextColor(col).Children(func() { mg.Icon(c, r.icon).FontSize(20) })
			}
		})
	})
}

// The row's icons: a phone turned each way, its top marked by a filled
// notch, and the turning arrow of Auto-rotate.
var (
	icRotPortrait   = icon(`<rect x="6.5" y="2.5" width="11" height="19" rx="2.5"/><rect x="9.5" y="2.5" width="5" height="2.6" rx="1" fill="currentColor"/>`)
	icRotLandscape  = icon(`<rect x="2.5" y="6.5" width="19" height="11" rx="2.5"/><rect x="2.5" y="9.5" width="2.6" height="5" rx="1" fill="currentColor"/>`)
	icRotReverse    = icon(`<rect x="2.5" y="6.5" width="19" height="11" rx="2.5"/><rect x="18.9" y="9.5" width="2.6" height="5" rx="1" fill="currentColor"/>`)
	icRotUpsideDown = icon(`<rect x="6.5" y="2.5" width="11" height="19" rx="2.5"/><rect x="9.5" y="18.9" width="5" height="2.6" rx="1" fill="currentColor"/>`)
	icRotAuto       = icon(`<path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/>`)
)
