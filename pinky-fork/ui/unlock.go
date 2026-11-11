package ui

import (
	"errors"
	"fmt"
	"runtime"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/secret"
)

// Unlocking from this computer. The client saves and removes a phone's PIN
// in the system keychain and never reads it back: when the person clicks
// Unlock, the gateway asks the keychain for it, which asks for Touch ID,
// and enters it on the phone. The PIN being typed lives in pinState.value
// until Save, and its input is rebuilt afresh after each save so no editor
// state keeps it.

// pinState is the PINs' part of the client.
type pinState struct {
	ready     bool            // the keychain was probed
	supported bool            // it can keep PINs
	probing   bool            // a probe is under way
	saved     map[string]bool // phones with a saved PIN, by id
	asked     map[string]bool // phones whose PIN was looked up
	editing   string          // the phone whose PIN form is open
	where     string          // which form: "settings" or "card"
	value     string          // the PIN being typed
	gen       int             // bumped on each save, to start the input afresh
	busy      bool
	err       string
	unlocking map[string]bool // phones being unlocked
	later     map[string]bool // phones whose lock card the person put off
	// wrong holds phones whose saved PIN the phone refused. Pinky does not
	// try it again until the phone is seen unlocked or the PIN changes:
	// phones lock out, or erase themselves, after repeated wrong PINs.
	wrong map[string]bool
}

// notAvailableHere is what the client says where the system has no
// keychain it can use, in English; locales.UnlockNotAvailable says it in
// the app's language.
const notAvailableHere = "Unlocking from this computer is not available on this system."

func (a *app) secrets() secret.Store {
	if a.opt.Secrets != nil {
		return a.opt.Secrets
	}
	return secret.System()
}

// keychainSupported reports whether a store can keep PINs, without asking
// the person anything.
func keychainSupported(s secret.Store) bool { return secret.Supported(s) }

// loadPINs probes the keychain once and looks up whether the phones it has
// not seen yet have a saved PIN, off the main thread: a keychain call can
// take a moment.
func (a *app) loadPINs() {
	st := &a.pin
	var ids []string
	for _, d := range a.devices {
		if !st.asked[d.Info.ID] {
			st.asked[d.Info.ID] = true
			ids = append(ids, d.Info.ID)
		}
	}
	probe := !st.ready && !st.probing
	if len(ids) == 0 && !probe {
		return
	}
	st.probing = st.probing || probe
	store := a.secrets()
	go func() {
		supported := true
		if probe {
			supported = keychainSupported(store)
		}
		has := map[string]bool{}
		for _, id := range ids {
			has[id] = store.Has(secret.PINAccount(id))
		}
		a.post(func() {
			if probe {
				st.ready, st.supported, st.probing = true, supported, false
			}
			for id, ok := range has {
				st.saved[id] = ok
			}
		})
	}()
}

// canUnlockHere reports whether the phone can be unlocked from this
// computer: its driver can, a PIN is saved and the setting is on.
func (a *app) canUnlockHere(id string) bool {
	d := a.device(id)
	st := &a.pin
	return d != nil && d.Features.Unlock && a.settings.UnlockFromComputer && st.ready && st.supported && st.saved[id] && !st.wrong[id]
}

// unlock enters the phone's saved PIN, after Touch ID, and says in plain
// words what went wrong.
func (a *app) unlock(id string) {
	if a.pin.unlocking[id] {
		return
	}
	a.pin.unlocking[id] = true
	name := id
	if d := a.device(id); d != nil {
		name = d.Info.Name
	}
	a.call(id, locales.UnlockCallWhat(), func() error {
		err := a.api.Unlock(id)
		a.post(func() {
			delete(a.pin.unlocking, id)
			if errors.Is(err, driver.ErrLocked) {
				a.pin.wrong[id] = true
			}
			if msg := unlockError(err, name); msg != "" {
				a.toasts = append(a.toasts, msg)
			}
		})
		return nil
	})
}

func unlockError(err error, name string) string {
	switch {
	case err == nil, errors.Is(err, secret.ErrCancelled):
		return "" // unlocked, or the person cancelled Touch ID
	case errors.Is(err, driver.ErrLocked):
		return locales.UnlockErrWrongPIN(name)
	case errors.Is(err, driver.ErrPINNotEntered):
		return locales.UnlockErrNoPad(name)
	case errors.Is(err, secret.ErrNotFound):
		return locales.UnlockErrNoPIN(name)
	case errors.Is(err, secret.ErrMicrosoftAccount):
		return locales.UnlockErrMicrosoftAccount(name)
	case errors.Is(err, secret.ErrAccountRestricted):
		return locales.UnlockErrAccountRestricted(name)
	case errors.Is(err, secret.ErrUnsupported):
		return locales.UnlockNotAvailable()
	case errors.Is(err, gateway.ErrNoUnlock):
		return locales.UnlockErrNoUnlock(name)
	case errors.Is(err, driver.ErrUnsupported):
		return locales.UnlockErrUnsupported(name)
	case errors.Is(err, driver.ErrOffline):
		return locales.UnlockErrOffline(name)
	}
	return locales.UnlockErrOther(name, err.Error())
}

// unlockButton is the Unlock with Touch ID button, which says when it is
// at work.
func (a *app) unlockButton(c *mg.Context, id string) mg.Element {
	label, _ := unlockWords()
	busy := a.pin.unlocking[id]
	if busy {
		label = locales.UnlockBusy()
	}
	b := button(c, label, btnOpt{kind: btnPrimary, icon: icTouchID}).Disabled(busy)
	if b.Clicked() {
		a.unlock(id)
	}
	return b
}

// unlockControls are the unlock card's: the Unlock button when the phone
// can be unlocked from here, the PIN form when only its PIN is missing,
// else why it cannot.
func (a *app) unlockControls(c *mg.Context, n gateway.NeedView) {
	p := pal(c)
	if a.canUnlockHere(n.DeviceID) {
		a.unlockButton(c, n.DeviceID).Margin(12, 0, 0, 0).FillWidth()
		_, keep := unlockWords()
		mg.Text(c, locales.UnlockOrOnPhone()).FontSize(fsSmall).TextColor(p.ink2).TextAlign(mg.Center).Margin(8, 0, 0, 0)
		mg.Text(c, keep).FontSize(fsCaption).TextColor(p.ink3).TextAlign(mg.Center).Margin(6, 0, 0, 0)
		return
	}
	mg.Text(c, a.unlockElsewhere(n.DeviceID, "")).FontSize(fsSmall).TextColor(p.ink2).Margin(10, 0, 0, 0)
	if d := a.device(n.DeviceID); d != nil && a.pinMissing(*d) {
		mg.Column(c).Margin(10, 0, 0, 0).Gap(8).Children(func() { a.pinForm(c, *d, "card") })
	}
}

// pinMissing reports whether saving the phone's PIN is all that keeps it
// from being unlocked from here.
func (a *app) pinMissing(d gateway.DeviceView) bool {
	st := &a.pin
	return d.Features.Unlock && a.settings.UnlockFromComputer && st.ready && st.supported && !st.saved[d.Info.ID]
}

// unlockElsewhere says how to unlock a phone that this computer cannot
// unlock, ending with then (", then click Done.", in the app's language)
// when given.
func (a *app) unlockElsewhere(id, then string) string {
	d := a.device(id)
	st := &a.pin
	end := locales.UnlockEnd()
	if then != "" {
		end = then
	}
	switch {
	case d == nil || !d.Features.Unlock:
		return locales.UnlockOnPhone(end)
	case !a.settings.UnlockFromComputer:
		return locales.UnlockOnPhoneOff(end)
	case st.ready && !st.supported:
		return locales.UnlockOnPhoneNotAvailable(end)
	case st.wrong[id]:
		return locales.UnlockOnPhoneWrongPIN(end)
	case st.ready && !st.saved[id]:
		return locales.UnlockOnPhoneSavePIN(end)
	}
	return locales.UnlockOnPhone(end)
}

// pinForm saves or removes a phone's PIN: a secure input while the person
// types it, "PIN saved" with Change and Remove once it is saved.
func (a *app) pinForm(c *mg.Context, d gateway.DeviceView, where string) {
	p := pal(c)
	st := &a.pin
	id, name := d.Info.ID, d.Info.Name
	if !st.ready {
		return
	}
	if !st.supported {
		mg.Text(c, locales.UnlockNotAvailable()).FontSize(fsSmall).TextColor(p.ink2)
		return
	}
	open := st.editing == id && st.where == where
	_, keep := unlockWords()
	switch {
	case open:
		in := mg.TextInput(c.Key(fmt.Sprintf("pin-%s-%s-%d", where, id, st.gen)), &st.value).Password().
			Placeholder(locales.PinPlaceholder()).Label(locales.PinFieldLabel(name)).AutoFocus().MaxWidth(260)
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			save := button(c, locales.PinSave(), btnOpt{kind: btnPrimary}).Disabled(st.busy || st.value == "")
			if (save.Clicked() || in.Submitted()) && !st.busy && st.value != "" {
				a.savePIN(id, name)
			}
			if button(c, locales.PinCancel(), btnOpt{}).Clicked() {
				st.editing, st.value, st.err = "", "", ""
				st.gen++
			}
			if st.busy {
				spinner(c)
			}
		})
		if st.err != "" {
			mg.Text(c, st.err).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus)
		}
		mg.Text(c, keep).FontSize(fsCaption).TextColor(p.ink3)
	case st.saved[id]:
		mg.Row(c).Gap(12).AlignItems(mg.Center).Wrap().Children(func() {
			mg.Row(c).Gap(6).AlignItems(mg.Center).Children(func() {
				mg.Icon(c, icLock).FontSize(14).TextColor(p.ink2)
				mg.Text(c, locales.PinSaved()).FontSize(fsSmall).TextColor(p.ink2)
			})
			if linkButton(c, locales.PinChange()).Label(locales.PinChangeLabel(name)).Clicked() {
				st.editing, st.where, st.value, st.err = id, where, "", ""
			}
			if linkButton(c, locales.PinRemove()).Label(locales.PinRemoveLabel(name)).Clicked() {
				a.removePIN(id, name)
			}
		})
	default:
		if button(c, locales.PinSaveOpen(), btnOpt{small: true}).Label(locales.PinSaveLabel(name)).Clicked() {
			st.editing, st.where, st.value, st.err = id, where, "", ""
		}
	}
}

// savePIN puts the typed PIN in the keychain and forgets it.
func (a *app) savePIN(id, name string) {
	st := &a.pin
	pin := st.value
	if len([]rune(pin)) < 4 {
		st.err = locales.PinErrShort(name)
		return
	}
	st.value, st.err, st.busy = "", "", true
	st.gen++
	store := a.secrets()
	go func() {
		err := store.Save(secret.PINAccount(id), pin)
		a.post(func() {
			st.busy = false
			switch {
			case errors.Is(err, secret.ErrUnsupported):
				st.supported, st.editing = false, ""
			case err != nil:
				st.err = locales.PinErrSave(err.Error())
			default:
				st.saved[id], st.editing = true, ""
				delete(st.wrong, id)
				a.toasts = append(a.toasts, locales.PinSavedToast(name))
			}
		})
	}()
}

func (a *app) removePIN(id, name string) {
	store := a.secrets()
	go func() {
		err := store.Delete(secret.PINAccount(id))
		a.post(func() {
			if err != nil && !errors.Is(err, secret.ErrNotFound) {
				a.toasts = append(a.toasts, locales.PinErrRemove(err.Error()))
				return
			}
			a.pin.saved[id] = false
			delete(a.pin.wrong, id)
			if a.pin.editing == id {
				a.pin.editing = ""
			}
			a.toasts = append(a.toasts, locales.PinRemovedToast(name))
		})
	}()
}

// lockCard is shown on a locked phone's page while nothing needs the
// person: it tells them the phone can be unlocked from this computer, and
// offers to save its PIN when none is saved, or to unlock it when one is.
// Without it nobody would know the feature exists until an agent hits the
// lock screen.
func (a *app) lockCard(c *mg.Context, d gateway.DeviceView) {
	id := d.Info.ID
	canUnlock, missing, wrong := a.canUnlockHere(id), a.pinMissing(d), a.pin.wrong[id]
	if !canUnlock && !missing && !wrong {
		return // this computer cannot unlock it: the mirror's lock says enough
	}
	if missing && a.pin.later[id] && a.pin.editing != id {
		return
	}
	p := pal(c)
	_, keep := unlockWords()
	mg.Column(c.Key("lock-"+id)).Padding(14).Radius(12).Background(p.bg2).Border(1, p.line).Gap(8).
		Label(locales.UnlockLocked(d.Info.Name)).Children(func() {
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			mg.Icon(c, icLock).FontSize(16).TextColor(p.ink2)
			mg.Text(c, locales.UnlockLocked(d.Info.Name)).FontSize(fsHeadline).FontWeight(fwSemibold).Grow(1).MinWidth(0)
		})
		if wrong {
			mg.Text(c, a.unlockElsewhere(id, "")).FontSize(fsSmall).TextColor(p.ink2).LineHeight(1.4)
			return
		}
		if canUnlock {
			mg.Text(c, locales.UnlockFromHere()).FontSize(fsSmall).TextColor(p.ink2).LineHeight(1.4)
			a.unlockButton(c, id).Margin(4, 0, 0, 0).FillWidth()
			mg.Text(c, keep).FontSize(fsCaption).TextColor(p.ink3)
			return
		}
		mg.Text(c, unlockPitch()).
			FontSize(fsSmall).TextColor(p.ink2).LineHeight(1.4)
		mg.Column(c).Gap(8).Margin(4, 0, 0, 0).Children(func() { a.pinForm(c, d, "lock") })
		if a.pin.editing != id {
			if quietButton(c, locales.UnlockNotNow()).Label(locales.UnlockNotNowLabel(d.Info.Name)).Clicked() {
				a.pin.later[id] = true
			}
		}
	})
}

// unlockPitch offers to save a phone's PIN, naming how the person
// confirms an unlock here.
func unlockPitch() string {
	switch runtime.GOOS {
	case "darwin":
		return locales.UnlockPitchMac()
	case "windows":
		return locales.UnlockPitchWindows()
	}
	return locales.UnlockPitchOther()
}
