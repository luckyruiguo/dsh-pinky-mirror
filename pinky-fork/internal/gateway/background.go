package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// Background displays (round three, decision 3): with background on for a
// phone, a lease works on a second display the driver opens
// (driver.Backgrounder), so the phone's own screen stays free for the
// person, or off and locked. The agent's reads, actions, screenshots and
// recording use that display; the phone's lock does not stop the agent,
// and the person's touches on the phone or on the mirror do not pause it
// (the Take over button still does). When the display cannot be opened,
// or an app refuses to run on it (the driver's Launch returns
// driver.ErrUnsupported), the task moves to the phone's own screen for
// good, and the agent's next result says so.

// bgOpenTimeout bounds opening a background display.
const bgOpenTimeout = 20 * time.Second

// personRecent is how recently the person must have used the phone's own
// screen for a task leaving its background display to wait for them.
const personRecent = time.Minute

// SetBackground sets whether agents work on a phone's background display
// instead of its main screen. It applies from the next task (a running
// one keeps the screen it started on) and is saved per phone. A phone whose driver has no background displays returns
// driver.ErrUnsupported while it is connected.
func (g *Gateway) SetBackground(deviceID string, on bool) error {
	d, err := g.device(deviceID)
	if err != nil {
		return err
	}
	g.mu.Lock()
	if dev := d.dev; on && dev != nil {
		if !backgroundWorks(dev) {
			g.mu.Unlock()
			return fmt.Errorf("%s cannot run apps on a background display: %w", d.name(), driver.ErrUnsupported)
		}
	}
	g.background[d.id] = on
	if g.store != nil {
		id := d.id
		g.rec.do(func(ctx context.Context) error { return g.store.SetDeviceBackground(ctx, id, on) })
	}
	g.mu.Unlock()
	g.emit(Event{Kind: EventDevices, DeviceID: d.id})
	return nil
}

// Background reports whether agents work on a phone's background display:
// the person's choice, else the driver's default (driver.BackgroundDefaulter).
func (g *Gateway) Background(deviceID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	d := g.devices[deviceID]
	if d == nil {
		return g.background[deviceID]
	}
	return g.backgroundLocked(d)
}

// backgroundLocked is Background for a known phone.
func (g *Gateway) backgroundLocked(d *device) bool {
	if on, ok := g.background[d.id]; ok {
		return on
	}
	if bd, ok := d.dev.(driver.BackgroundDefaulter); ok {
		return backgroundWorks(d.dev) && bd.BackgroundByDefault()
	}
	return false
}

// AgentStream returns the live video of the background display an agent
// is working on, or driver.ErrNotFound when none is open.
func (g *Gateway) AgentStream(ctx context.Context, deviceID string, opt driver.StreamOptions) (driver.Stream, error) {
	d, err := g.device(deviceID)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	var bg driver.Device
	if l := d.lease; l != nil {
		bg = l.bg
	}
	g.mu.Unlock()
	if bg == nil {
		return nil, fmt.Errorf("no agent works on a background display of %s: %w", d.name(), driver.ErrNotFound)
	}
	return g.watch(ctx, hubKey(d.id, true), bg, opt)
}

// readyScreen gets the screen the agent uses ready before a call reads or
// acts on it: the background display when the person wants one (opening
// it, again after the phone dropped), otherwise the phone's own screen,
// woken when it is off. When the task moves to the phone's screen, the
// agent's next result says so (finish).
func (g *Gateway) readyScreen(ctx context.Context, l *lease) {
	d := l.dev
	g.mu.Lock()
	want := l.bgWanted && !l.bgOff && l.bg == nil && !l.ended
	dev := d.dev
	g.mu.Unlock()
	if want && dev != nil {
		if b, ok := dev.(driver.Backgrounder); ok {
			g.openBackground(ctx, l, b)
		} else {
			g.mu.Lock()
			l.bgOff = true
			g.mu.Unlock()
		}
	}
	g.mu.Lock()
	onBG := l.bg != nil
	g.mu.Unlock()
	if !onBG {
		g.wake(ctx, d)
	}
}

// openBackground opens a background display for the lease, or moves the
// task to the phone's screen when it cannot.
func (g *Gateway) openBackground(ctx context.Context, l *lease, b driver.Backgrounder) {
	d := l.dev
	// One display per lease, whichever call gets here first.
	d.act.Lock()
	defer d.act.Unlock()
	g.mu.Lock()
	again := l.bg != nil || l.bgOff || l.ended
	g.mu.Unlock()
	if again {
		return
	}
	octx, cancel := context.WithTimeout(ctx, bgOpenTimeout)
	defer cancel()
	bg, err := b.OpenBackground(octx, driver.BackgroundOptions{})
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		l.bgOff = true
		l.bgNote = joinNote(l.bgNote, fmt.Sprintf("Pinky could not open a background display on %s (%v), so the task runs on the phone's own screen.", d.name(), err))
		g.log.Warn("background display", "device", d.id, "err", err)
		return
	}
	if l.ended || d.lease != l {
		go bg.Close()
		return
	}
	l.bg = bg
	// Pictures of the phone's screen do not show this display.
	d.kept = nil
	if p := d.shot; p != nil {
		g.dropShotLocked(d, p)
	}
	g.inputLocked(d)
	if g.recordOn {
		if d.video != nil {
			g.moveVideoLocked(d, hubKey(d.id, true))
		} else {
			g.startVideoLocked(l)
		}
	}
	g.emit(Event{Kind: EventDevices, DeviceID: d.id})
}

// leaveBackgroundLocked moves a task from its background display to the
// phone's own screen for the rest of the task. Callers hold g.mu.
func (g *Gateway) leaveBackgroundLocked(l *lease, why string) {
	bg := l.bg
	if bg == nil {
		return
	}
	d := l.dev
	l.bg, l.bgOff = nil, true
	if why != "" {
		l.bgNote = joinNote(l.bgNote, why)
	}
	d.kept = nil
	if p := d.shot; p != nil {
		g.dropShotLocked(d, p)
	}
	g.inputLocked(d)
	g.moveVideoLocked(d, hubKey(d.id, false))
	go func() {
		g.stopHub(hubKey(d.id, true), errors.New("the background display closed"))
		bg.Close()
	}()
	g.emit(Event{Kind: EventDevices, DeviceID: d.id})
}

// closeBackgroundLocked closes a lease's background display when the lease
// ends. Callers hold g.mu.
func (g *Gateway) closeBackgroundLocked(l *lease) {
	bg := l.bg
	if bg == nil {
		return
	}
	l.bg = nil
	d := l.dev
	d.kept = nil
	if p := d.shot; p != nil {
		g.dropShotLocked(d, p)
	}
	go func() {
		g.stopHub(hubKey(d.id, true), errors.New("the background display closed"))
		bg.Close()
	}()
}

func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + " " + b
}

// leaveForLaunch moves the task to the phone's own screen after an app
// refused the background display, and returns the phone's device to
// launch it on, or a result when the phone cannot take it now.
func (g *Gateway) leaveForLaunch(ctx context.Context, l *lease, p *prep, cause error) (driver.Device, *Result) {
	name := p.launch.Name
	if name == "" {
		name = p.launch.ID
	}
	g.log.Info("app refused the background display", "device", l.dev.id, "app", name, "err", cause)
	return g.leaveForPhone(ctx, l, name+" does not run on a background display", "nothing was opened")
}

// leaveForPhone moves the task to the phone's own screen for the rest of
// it, for the reason why ("Dinely does not run on a background display"),
// and returns the phone's device, or a result saying that nothing
// happened ("nothing was opened") when the phone cannot take it now.
func (g *Gateway) leaveForPhone(ctx context.Context, l *lease, why, nothing string) (driver.Device, *Result) {
	d := l.dev
	g.mu.Lock()
	g.leaveBackgroundLocked(l, why+", so the task moved to the phone's own screen.")
	dev := d.dev
	using := time.Since(d.personAt) < personRecent
	g.mu.Unlock()
	if dev == nil {
		return nil, errResult(ErrDeviceOffline, "%s is offline. Nothing was done.", d.name())
	}
	if using {
		// The person has been using the phone's screen, which background
		// work left to them: they get it until they hand it over.
		g.takeOver(d, false)
		g.mu.Lock()
		l.tookOverSeen = true
		g.mu.Unlock()
		return nil, statusResult(StatusTookOver, "%s, and the person is using the phone's own screen, so %s and the agent is paused. Call phone_handoff to wait until they let you use it.", why, nothing)
	}
	g.wake(ctx, d)
	g.checkLock(ctx, d)
	g.mu.Lock()
	locked := d.locked
	g.mu.Unlock()
	if locked {
		return nil, statusResult(StatusLocked, "%s, and the phone's own screen is locked, so %s. Call phone_handoff so the person can unlock it.", why, nothing)
	}
	return dev, nil
}

// ownScreenHint tells an agent on a background display how to reach the
// home screen, which only the phone's own screen has.
const ownScreenHint = "For work on the home screen itself (moving app icons, folders, widgets), " +
	"call phone_connect again with the same task and own_screen: true to work on the phone's own screen."

// useOwnScreen moves a task that would work on a background display to
// the phone's own screen for the rest of it, as the agent asked
// (phone_connect own_screen), and reports whether it moved. As when an
// app refuses the display (leaveForPhone), the person keeps a screen they
// used in the last minute: the task starts paused until they hand it
// over, and a locked screen waits for them to unlock it (firstScreen
// says so). The agent's next result says where the task runs.
func (g *Gateway) useOwnScreen(l *lease) bool {
	d := l.dev
	g.mu.Lock()
	if !l.bgWanted || l.bgOff || l.ended {
		g.mu.Unlock()
		return false
	}
	if l.bg != nil {
		g.leaveBackgroundLocked(l, "")
	} else {
		l.bgOff = true
	}
	l.bgNote = joinNote(l.bgNote, "As asked, this task works on the phone's own screen, not on a background display.")
	using := !d.person && time.Since(d.personAt) < personRecent
	g.mu.Unlock()
	g.log.Info("task moved to the phone's own screen", "device", d.id, "task", l.task.id)
	if using {
		g.takeOver(d, false)
		g.mu.Lock()
		l.tookOverSeen = true
		g.mu.Unlock()
	}
	return true
}

// sameDevice compares two devices without panicking on a type that cannot
// be compared.
func sameDevice(a, b driver.Device) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}
