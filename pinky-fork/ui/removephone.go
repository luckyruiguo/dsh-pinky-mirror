package ui

import (
	"context"
	"errors"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/secret"
)

// Removing a phone: Remove this phone… in the ⋯ menu under its mirror, on
// its page while it is offline or needs setup, on its tile in All phones,
// in the sidebar's right-click menu and in Settings › Phones. A dialog
// says what goes, what stays and whether the phone will come back; the
// client then asks the gateway to remove it (its record, settings and,
// when the person checks it, history), and deletes its saved PIN, which
// the client keeps in the keychain.

// phoneRemover is the gateway's RemoveDevice (internal/gateway/remove.go).
type phoneRemover interface {
	RemoveDevice(ctx context.Context, deviceID string, opt gateway.RemoveOptions) error
}

// removeState is the remove dialog's: which phone it asks about, as it was
// when the dialog opened, should it go meanwhile.
type removeState struct {
	open     bool
	id       string
	name     string
	platform driver.Platform
	history  bool // delete its history too
	busy     bool
	notes    []string // the notes while busy, as they were when Remove was clicked
	err      string
}

// canRemove reports whether the gateway removes phones.
func (a *app) canRemove() bool {
	_, ok := a.api.(phoneRemover)
	return ok
}

// askRemove opens the dialog about a phone.
func (a *app) askRemove(d gateway.DeviceView) {
	if !a.canRemove() || a.rm.busy {
		return
	}
	a.rm = removeState{open: true, id: d.Info.ID, name: d.Info.Name, platform: d.Info.Platform}
}

// removeMenuItem adds Remove this phone… to a phone's context menu, which
// is given only when canRemove.
func (a *app) removeMenuItem(m *mg.Menu, d gateway.DeviceView) {
	if m.Item(locales.RemovePhoneMenu()).Chosen() {
		a.askRemove(d)
	}
}

// removeLink is Remove this phone on the page of a phone that is offline or
// needs setup, where there is no ⋯ menu.
func (a *app) removeLink(c *mg.Context, d gateway.DeviceView) {
	if !a.canRemove() {
		return
	}
	if quietButton(c.Key("remove-link-"+d.Info.ID), locales.RemovePhoneLink()).Clicked() {
		a.askRemove(d)
	}
}

// removeNotes say what removing the phone does that depends on how it is
// connected now: whether it comes back by itself, and how to add it back.
func removeNotes(d *gateway.DeviceView, platform driver.Platform) []string {
	if d != nil && d.Info.Virtual {
		// It runs on this computer: nothing to trust or unplug.
		return []string{locales.RemovePhoneVirtual()}
	}
	listed := d != nil && d.Status != gateway.DeviceOffline && d.Info.State != driver.StateOffline
	var notes []string
	switch {
	case listed && d.Info.Connection != "wifi":
		notes = append(notes, locales.RemovePhoneCable())
	case listed && platform == driver.IOS:
		notes = append(notes, locales.RemovePhoneWiFiIPhone())
	case listed:
		notes = append(notes, locales.RemovePhoneWiFi())
	case platform == driver.IOS:
		notes = append(notes, locales.RemovePhoneAddBackIPhone())
	default:
		notes = append(notes, locales.RemovePhoneAddBackAndroid())
	}
	return append(notes, locales.RemovePhoneTrust())
}

// removeDialog asks before removing a phone.
func (a *app) removeDialog(c *mg.Context) {
	st := &a.rm
	if !st.open {
		return
	}
	d := a.device(st.id)
	if d == nil && !st.busy {
		*st = removeState{} // it went meanwhile
		return
	}
	p := pal(c)
	title := locales.RemovePhoneTitle(st.name)
	sheet(c.Key("remove-phone"), &st.open, func() {
		mg.Column(c).Width(480).Gap(14).Label(title).Children(func() {
			mg.Text(c, title).FontSize(fsTitle3).FontWeight(fwSemibold).Role(mg.RoleHeading)
			mg.Column(c).Gap(6).Children(func() {
				bullet := func(text string) {
					mg.Row(c).Gap(8).AlignItems(mg.Start).Children(func() {
						mg.Text(c, "•").FontSize(fsSmall).TextColor(p.ink3)
						mg.Text(c, text).FontSize(fsSmall).TextColor(p.ink2).Grow(1).MinWidth(0)
					})
				}
				bullet(locales.RemovePhoneGoesList())
				bullet(locales.RemovePhoneGoesSettings())
				if t := a.taskOf(st.id); t != nil && running(t.State) {
					bullet(locales.RemovePhoneGoesTask(agentOrTheAgent(t), t.Title))
				}
			})
			mg.Column(c).Gap(4).Children(func() {
				mg.Checkbox(c.Key("remove-history"), &st.history, locales.RemovePhoneHistory()).Disabled(st.busy)
				if !st.history {
					mg.Text(c, locales.RemovePhoneHistoryStays()).FontSize(fsCaption).TextColor(p.ink3).Padding(0, 0, 0, 26)
				}
			})
			notes := st.notes
			if !st.busy {
				notes = removeNotes(d, st.platform)
			}
			mg.Column(c).Gap(6).Padding(10, 12).Radius(8).Background(p.bg2).Border(1, p.line).Children(func() {
				for _, note := range notes {
					mg.Text(c, note).FontSize(fsSmall).TextColor(p.ink2)
				}
			})
			if st.err != "" {
				mg.Text(c, st.err).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus)
			}
			mg.Row(c).Gap(8).Justify(mg.End).AlignItems(mg.Center).Children(func() {
				if st.busy {
					spinner(c)
				}
				if button(c, locales.RemovePhoneCancel(), btnOpt{}).Disabled(st.busy).Clicked() && !st.busy {
					st.open = false
				}
				label := locales.RemovePhoneConfirm()
				if st.busy {
					label = locales.RemovePhoneBusy()
				}
				if button(c, label, btnOpt{kind: btnRed}).Disabled(st.busy).Clicked() && !st.busy {
					a.removePhone()
				}
			})
		})
	})
	if st.busy {
		st.open = true // it closes when the gateway answers
	}
}

// removePhone removes the phone the dialog asks about: the gateway forgets
// it, then the keychain its PIN.
func (a *app) removePhone() {
	st := &a.rm
	r, ok := a.api.(phoneRemover)
	if !ok || st.busy {
		return
	}
	st.busy, st.err = true, ""
	st.notes = removeNotes(a.device(st.id), st.platform) // the phone leaves the list before the gateway answers
	id, name, history := st.id, st.name, st.history
	store := a.secrets()
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
		defer cancel()
		err := r.RemoveDevice(ctx, id, gateway.RemoveOptions{History: history})
		removed := err == nil || errors.Is(err, gateway.ErrUnknownDevice) || errors.Is(err, gateway.ErrHistoryNotDeleted)
		var pinErr error
		if removed {
			if perr := store.Delete(secret.PINAccount(id)); perr != nil && !errors.Is(perr, secret.ErrNotFound) && !errors.Is(perr, secret.ErrUnsupported) {
				pinErr = perr
			}
		}
		a.post(func() {
			st.busy = false
			if !removed {
				st.err = locales.RemovePhoneFailed(name, err.Error())
				return
			}
			*st = removeState{}
			a.forgetPhone(id, history)
			switch {
			case errors.Is(err, gateway.ErrHistoryNotDeleted):
				a.toasts = append(a.toasts, locales.RemovePhoneHistoryFailed(name, err.Error()))
			case pinErr != nil:
				a.toasts = append(a.toasts, locales.RemovePhonePINFailed(name, pinErr.Error()))
			default:
				a.toasts = append(a.toasts, locales.RemovePhoneDone(name))
			}
		})
	}()
}

// forgetPhone drops what the client keeps about a removed phone, so it
// starts afresh should it come back.
func (a *app) forgetPhone(id string, history bool) {
	pin := &a.pin
	for _, m := range []map[string]bool{pin.saved, pin.asked, pin.unlocking, pin.later, pin.wrong} {
		delete(m, id)
	}
	if pin.editing == id {
		pin.editing, pin.value, pin.err = "", "", ""
		pin.gen++
	}
	delete(a.bg.on, id)
	delete(a.bg.busy, id)
	delete(a.bg.phone, id)
	comp := &a.comp
	delete(comp.state, id)
	delete(comp.checked, id)
	delete(comp.loading, id)
	delete(comp.installing, id)
	delete(comp.err, id)
	delete(comp.failure, id)
	delete(comp.later, id)
	a.forgetRefused(id)
	conn := &a.conn // Settings › Phones' connection choice, read again should it come back
	delete(conn.got, id)
	delete(conn.at, id)
	delete(conn.loading, id)
	delete(conn.busy, id)
	delete(conn.chosen, id)
	delete(a.chk.runs, id) // its checkup, which shows in place of the mirror
	delete(a.chk.family, id)
	if a.chk.showing == id {
		a.chk.showing = ""
	}
	delete(a.droid.waiting, id) // Add phone's wait for the phone to allow this computer
	if a.droid.auto == id {
		a.droid.auto, a.droid.autoBrand = "", ""
	}
	delete(a.more, id)
	delete(a.more, "tile:"+id)
	delete(a.control, id) // the banner above its mirror
	if history && a.act.loaded {
		a.act.load(a)
	}
}
