package ui

import (
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Pausing Pinky's connection to a phone: Pause connection in the ⋯ menu
// under its mirror and in its right-click menus. A paused phone stays in
// the list, offline, with a page that resumes it; its right-click menus
// say Resume connection. Plugging it in by cable resumes it too (the
// gateway's pause.go says when).

// phonePauser is the gateway's PauseDevice and ResumeDevice
// (internal/gateway/pause.go).
type phonePauser interface {
	PauseDevice(deviceID string) error
	ResumeDevice(deviceID string) error
}

// canPause reports whether the gateway pauses phones.
func (a *app) canPause() bool {
	_, ok := a.api.(phonePauser)
	return ok
}

// setPaused pauses or resumes Pinky's connection to the phone.
func (a *app) setPaused(d gateway.DeviceView, paused bool) {
	pp, ok := a.api.(phonePauser)
	if !ok {
		return
	}
	id, name := d.Info.ID, d.Info.Name
	go func() {
		var err error
		if paused {
			err = pp.PauseDevice(id)
		} else {
			err = pp.ResumeDevice(id)
		}
		if err != nil {
			a.post(func() { a.toasts = append(a.toasts, locales.PhonePauseFailed(name, err.Error())) })
		}
	}()
}

// hasPhoneMenu reports whether a phone's right-click menu has anything.
func (a *app) hasPhoneMenu() bool { return a.canPause() || a.canRemove() }

// phoneMenu is a phone's right-click menu, in the sidebar and on its tile
// in All phones: pause or resume it, and remove it.
func (a *app) phoneMenu(m *mg.Menu, d gateway.DeviceView) {
	if a.canPause() {
		label := locales.PausePhoneMenu()
		if d.Paused {
			label = locales.ResumePhoneMenu()
		}
		if m.Item(label).Chosen() {
			a.setPaused(d, !d.Paused)
		}
	}
	if a.canRemove() {
		a.removeMenuItem(m, d)
	}
}

// pausedPage is a paused phone's page.
func (a *app) pausedPage(c *mg.Context, d gateway.DeviceView) {
	p := pal(c)
	mg.Column(c).AlignItems(mg.Center).Gap(12).MaxWidth(420).Children(func() {
		mg.Box(c).Size(200, 420).Radius(32).Border(2, p.line2).Background(p.bg2).Center().Children(func() {
			mg.Icon(c, icPause).FontSize(30).TextColor(p.ink3)
		})
		mg.Text(c, locales.PhonePausedTitle(d.Info.Name)).FontSize(fsTitle3).FontWeight(fwSemibold)
		mg.Text(c, locales.PhonePausedDetail()).FontSize(fsSmall).TextColor(p.ink2).TextAlign(mg.Center)
		if a.canPause() && button(c.Key("resume-"+d.Info.ID), locales.PhonePausedResume(), btnOpt{kind: btnPrimary, icon: icPlay}).Clicked() {
			a.setPaused(d, false)
		}
		a.removeLink(c, d)
	})
}

// pauseMenuItem is Pause connection in the ⋯ menu under the mirror, with
// what it does.
func (a *app) pauseMenuItem(c *mg.Context, d gateway.DeviceView, close func()) {
	if !a.canPause() {
		return
	}
	p := pal(c)
	b := mg.ButtonBase(c.Key("pause-"+d.Info.ID)).Label(locales.PausePhoneMenu()).MinHeight(36).Padding(sp2, 10).Radius(rSmall).
		AlignItems(mg.Center).Justify(mg.Start)
	if b.Hovered() || b.Focused() {
		b.Background(p.bg3)
	}
	b.Children(func() { menuText(c, locales.PausePhoneMenu(), locales.PausePhoneMenuDesc(), p.ink) })
	if b.Clicked() {
		close()
		a.setPaused(d, true)
	}
}
