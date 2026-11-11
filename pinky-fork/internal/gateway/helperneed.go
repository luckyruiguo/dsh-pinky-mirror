package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// The helper on demand. An iPhone or iPad on iOS 27 or later left on
// Automatic (Settings › Phones) runs over CoreDevice, which is quicker and
// streams a smoother picture, but has no switches (phone_device, the
// person's Rotate) and no second finger (phone_pinch). When the phone has
// the WebDriverAgent helper installed, what needs one of these moves the
// phone to the helper first, as an app that refuses screen sharing does
// (taskconn.go):
//
//   - phone_device changing a switch moves the agent's task, which then
//     goes on; reading the switches moves nothing;
//   - phone_pinch moves the task and returns stale with the screen read
//     there, since the refs and points the agent pinched by were read over
//     CoreDevice;
//   - the person's Rotate moves the phone for them, even while they are in
//     control, and the phone goes back to CoreDevice switchBackAfter after
//     it, once no task uses it. A turn made through the helper stays when
//     the helper goes (iPhone Air, iOS 27.3: Calculator stayed landscape);
//   - the person's pinch on the mirror moves it too, in the background: the
//     gesture that starts it is lost, the next ones reach the phone;
//   - while the phone waits to go back, each of the person's inputs on the
//     mirror starts switchBackAfter again (personInput), so it does not go
//     back while they use it.
//
// A task that changed a switch through the helper stays there until it
// ends, so the switch is put back (useConnection's keep).

// helperCheckTimeout bounds asking a phone whether the helper is installed.
const helperCheckTimeout = 10 * time.Second

// hasSwitchChangesLocked reports whether the lease's task changed a switch
// that is put back when it ends. The caller holds g.mu.
func hasSwitchChangesLocked(l *lease) bool {
	if l == nil {
		return false
	}
	for _, c := range l.changes {
		if c.name != changeLanguage {
			return true
		}
	}
	return false
}

// helperFor moves the task to the helper for what (a sentence's words:
// "turning the screen") when the phone has no other way to do it. It
// returns what useConnection does, and the device the task runs on now.
func (g *Gateway) helperFor(ctx context.Context, l *lease, what string) (*Result, string, driver.Device) {
	res, note := g.useConnection(ctx, l, wayHelper, helperNeed{what: what})
	return res, note, g.driverOf(l.dev)
}

// switchNeed is what a phone_device call does, as helperFor names it.
func switchNeed(want []switchSet) string {
	var words []string
	for _, w := range want {
		switch w.name {
		case driver.SwitchRotation:
			words = append(words, "turning the screen")
		case driver.SwitchAppearance:
			words = append(words, "switching the appearance")
		case driver.SwitchLocation:
			words = append(words, "simulating a location")
		}
	}
	switch len(words) {
	case 0:
		return "phone_device"
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// checkHelper asks an iPhone or iPad that came online whether it has the
// helper installed, for the person's Rotate on a phone that runs CoreDevice
// (helperReadyLocked).
func (g *Gateway) checkHelper(d *device) {
	g.mu.Lock()
	cc, ok := d.provider.(driver.ConnectionChooser)
	ios := d.info.Platform == driver.IOS
	g.mu.Unlock()
	if !ok || !ios {
		return
	}
	ctx, cancel := context.WithTimeout(g.ctx, helperCheckTimeout)
	defer cancel()
	c, found := connectionWay(cc.Connections(ctx, d.id), wayHelper)
	installed := found && c.Available
	g.mu.Lock()
	changed := d.helper != installed
	d.helper = installed
	g.mu.Unlock()
	if changed {
		g.emit(Event{Kind: EventDevices, DeviceID: d.id})
	}
}

// helperReadyLocked reports whether the phone may move to the helper for
// the person: it has it installed and the person left the connection on
// Automatic. The caller holds g.mu.
func (g *Gateway) helperReadyLocked(d *device) bool {
	_, ok := d.provider.(driver.ConnectionChooser)
	return ok && d.helper && g.chosen[d.id] == "" && d.info.Platform == driver.IOS
}

// personHelper moves the phone to the helper for the person's Rotate and
// returns the device it runs on then. Without a task, it goes back to the
// person's choice switchBackAfter later; with one, when the task ends.
func (g *Gateway) personHelper(ctx context.Context, d *device) (driver.Device, error) {
	g.mu.Lock()
	cc, ok := d.provider.(driver.ConnectionChooser)
	g.mu.Unlock()
	if !ok {
		return nil, driver.ErrUnsupported
	}
	g.log.Info("person: moving to the helper", "device", d.id)
	if res := g.switchTo(ctx, d, cc, wayHelper); res != nil {
		// The phone waits for a step first, the passcode iOS asks before
		// the helper starts: it goes back on its own after switchBackAfter
		// like any move for the person, and they are told in their words
		// (res.Message is the agent's).
		g.mu.Lock()
		if d.lease == nil {
			g.switchBackLater(d)
		}
		setup := d.info.Setup
		g.mu.Unlock()
		g.log.Info("person: the helper waits for a step", "device", d.id, "setup", setup)
		return nil, &driver.SetupError{Setup: setup, Detail: personSetupWords(d.name(), setup)}
	}
	g.mu.Lock()
	dev, there := d.dev, d.online && d.info.Backend == wayHelper && d.dev != nil
	detail := "the helper did not start"
	if d.openErr != nil {
		detail = strings.TrimSuffix(d.openErr.Error(), ".")
	}
	if there && d.lease == nil {
		g.switchBackLater(d)
	}
	g.mu.Unlock()
	if !there {
		// It did not come up on the helper: back to the person's choice.
		g.log.Warn("person: the helper did not start", "device", d.id, "detail", detail)
		g.switchTo(ctx, d, cc, "")
		return nil, fmt.Errorf("Pinky could not move %s to the WebDriverAgent helper: %s", d.name(), detail)
	}
	return dev, nil
}

// pinchHelper moves a task whose phone cannot pinch to the helper before
// phone_pinch, under the device's action lock. Once there, the call does
// nothing more: the refs and points it pinches by were read over CoreDevice,
// so it returns stale with the screen read through the helper, to pinch
// again by. It returns nil to go on as before.
func (g *Gateway) pinchHelper(ctx context.Context, l *lease) *Result {
	d := l.dev
	dev, _ := g.screenOf(d)
	if dev == nil {
		return nil
	}
	if _, ok := dev.(driver.Pincher); ok {
		return nil
	}
	res, note, now := g.helperFor(ctx, l, "pinching")
	if res != nil {
		return res
	}
	if _, ok := now.(driver.Pincher); !ok {
		if note == "" {
			return nil // prepare says it cannot pinch
		}
		return errResult(ErrUnsupported, "This phone cannot pinch. Nothing was done. %s", note)
	}
	var cur *screen.Screen
	if s, err := g.observe(ctx, d, false); err == nil {
		cur = s
	}
	res = statusResult(StatusStale, "%s Nothing was pinched yet: pinch again with the refs and points of the screen below, read through the helper.", note)
	if cur == nil {
		return res
	}
	return g.attachScreen(l, res, cur)
}

// personPinchHelper starts moving the phone to the helper for the person's
// pinch on the mirror, when it may: the gesture that starts it is lost, as
// are the touches until the phone is back, while its status reads
// "Switching to the helper". It reports whether the touch is taken care
// of so.
func (g *Gateway) personPinchHelper(d *device, a driver.TouchAction) bool {
	g.mu.Lock()
	ready, switching := g.helperReadyLocked(d), d.switching != ""
	g.mu.Unlock()
	switch {
	case switching:
		return true // the move under way
	case !ready:
		return false
	case a != driver.TouchDown:
		return true // the rest of a gesture begun before
	}
	go func() {
		ctx, cancel := context.WithTimeout(g.ctx, switchWait+helperCheckTimeout)
		defer cancel()
		if _, err := g.personHelper(ctx, d); err != nil {
			g.log.Warn("person: pinch", "device", d.id, "err", err)
		}
	}()
	return true
}

// personSetupWords says to the person what the phone waits for before the
// helper starts, in English for the log; the client says it in their
// language (driver.SetupError's Setup).
func personSetupWords(name, setup string) string {
	if setup == driver.SetupPasscode {
		return fmt.Sprintf("Enter the passcode on %s: iOS asks for it before the WebDriverAgent helper can work. Then try again.", name)
	}
	return fmt.Sprintf("%s needs a step before the WebDriverAgent helper can work: see its page. Then try again.", name)
}
