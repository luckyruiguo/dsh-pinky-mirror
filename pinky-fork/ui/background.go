package ui

import (
	"errors"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Work in the background: on phones that offer it (Android), agents use a
// second, hidden display while the phone's own screen stays the person's,
// or stays off and locked. While such a task runs the mirror shows the
// agent's display, and a switch shows the phone's own screen instead.

// bgState is the background displays' part of the client.
type bgState struct {
	on    map[string]bool   // Background(id), read with the devices
	busy  map[string]bool   // a SetBackground is under way
	phone map[string]string // the task during which the person chose the phone's own screen, by phone
}

// readBackground asks the gateway which phones work in the background, off
// the main thread, as Devices is read.
func (a *app) readBackground(devs []gateway.DeviceView) map[string]bool {
	bs, ok := a.api.(backgroundSetter)
	if !ok {
		return nil
	}
	out := map[string]bool{}
	for _, d := range devs {
		if d.Features.Background {
			out[d.Info.ID] = bs.Background(d.Info.ID)
		}
	}
	return out
}

// setBackground turns background work on or off for a phone; it applies
// from the next task.
func (a *app) setBackground(d gateway.DeviceView, on bool) {
	bs, ok := a.api.(backgroundSetter)
	id, name := d.Info.ID, d.Info.Name
	if !ok || a.bg.busy[id] {
		return
	}
	a.bg.on[id], a.bg.busy[id] = on, true
	go func() {
		err := bs.SetBackground(id, on)
		now := bs.Background(id) // what the gateway kept, after an error too
		a.post(func() {
			a.bg.busy[id], a.bg.on[id] = false, now
			if err != nil {
				msg := locales.BackgroundChangeFailed(name, err.Error())
				if errors.Is(err, driver.ErrUnsupported) {
					msg = locales.BackgroundUnsupported(name)
				}
				a.toasts = append(a.toasts, msg)
			}
		})
	}()
}

// backgroundRow is the "Work in the background" switch with its words.
func (a *app) backgroundRow(c *mg.Context, d gateway.DeviceView, last bool) {
	on := a.bg.on[d.Info.ID]
	if a.toggleRowFor(c, "bg-"+d.Info.ID, locales.BackgroundTitle(), locales.BackgroundDesc(), last, &on) {
		a.setBackground(d, on)
	}
}

// backgroundTask reports whether an agent works on the phone's background
// display now: a task runs, the phone works in the background, and the
// gateway can show that display.
func (a *app) backgroundTask(d gateway.DeviceView, t *gateway.TaskView) bool {
	_, ok := a.api.(agentStreamer)
	return ok && d.Features.Background && a.bg.on[d.Info.ID] && t != nil && running(t.State)
}

// phoneChosen reports whether the person chose the phone's own screen
// during this task.
func (a *app) phoneChosen(id string, t *gateway.TaskView) bool {
	return t != nil && a.bg.phone[id] == t.ID
}

// agentMirrorFor returns the mirror of the display an agent works on.
func (a *app) agentMirrorFor(id string) *mirror {
	key := "agent:" + id
	m := a.mirrors[key]
	if m == nil {
		m = &mirror{a: a, id: id, agent: true}
		a.mirrors[key] = m
		m.open()
	}
	m.used = a.buildSeq
	if m.state == "failed" && !m.opening && time.Now().After(m.retry) && a.videoErr == nil {
		m.open()
	}
	return m
}

// screenFor picks the mirror a phone's page or tile shows: the agent's
// display while it works in the background, unless the person chose the
// phone's own screen for this task, holds the phone (took over, or the
// agent handed it to them, which the person does on the phone's screen),
// or the gateway has no such display open.
func (a *app) screenFor(d gateway.DeviceView, t *gateway.TaskView, tile bool) (m *mirror, bg bool) {
	if !a.backgroundTask(d, t) {
		return a.mirrorFor(d.Info.ID), false
	}
	_, handoff := a.handoffFor(d.Info.ID)
	holds := d.Status == gateway.DeviceInControl || handoff || t.State == gateway.TaskPaused
	if !holds && (!a.phoneChosen(d.Info.ID, t) || tile) {
		if am := a.agentMirrorFor(d.Info.ID); !am.missing() {
			return am, true
		}
	}
	return a.mirrorFor(d.Info.ID), true
}
