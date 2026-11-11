package ui

import (
	"context"
	"errors"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// iPhones and iPads over Wi-Fi: Settings › Phones' switch, and the phones
// Pinky found on Wi-Fi but has not used (driver.SetupConfirmWiFi; Android
// phones connected over Wireless debugging too), which wait for the person
// to choose Use or Ignore, on their page, in Settings › Phones and in Add
// phone › iPhone. Until then Pinky does nothing on them and agents do not
// see them.
//
// An Android phone whose connection over the network leads to a device
// that lacks the mark Pinky left on the phone (driver.SetupPhoneChanged)
// waits on its page for the person to trust it, which they do when they
// reset the phone or set it up again, or to plug the phone in.

type (
	// wifiSetter turns using iPhones and iPads over Wi-Fi on or off
	// (gateway.SetWiFi).
	wifiSetter interface {
		SetWiFi(on bool)
	}
	// foundChooser answers a phone found on Wi-Fi (gateway.UseFoundPhone,
	// IgnoreFoundPhone).
	foundChooser interface {
		UseFoundPhone(deviceID string) error
		IgnoreFoundPhone(ctx context.Context, deviceID string) error
	}
	// phoneTruster trusts what answers for a phone over the network,
	// though it lacks the phone's mark (gateway.TrustPhone).
	phoneTruster interface {
		TrustPhone(ctx context.Context, deviceID string) error
	}
	// deviceServiceRestarter restarts the computer's device service, which
	// lost a phone on Wi-Fi (gateway.RestartDeviceService).
	deviceServiceRestarter interface {
		RestartDeviceService(ctx context.Context, deviceID, reason string) error
	}
)

// foundState is the person's choices on their way to the gateway, by phone,
// and the restart of the device service: asked about for which phone, and
// whether it runs.
type foundState struct {
	busy       map[string]bool
	asking     bool
	askFor     string
	restarting bool
	// trustAsking: the dialog that asks before trusting trustFor.
	trustAsking bool
	trustFor    string
}

// foundOnWiFi reports whether a phone waits for the person to say whether
// it is theirs.
func foundOnWiFi(d gateway.DeviceView) bool {
	return d.Status != gateway.DeviceOffline && d.Info.State == driver.StateNeedsSetup && d.Info.Setup == driver.SetupConfirmWiFi
}

// changedOnWiFi reports whether what answers for a phone over the network
// lacks the mark Pinky left on the phone.
func changedOnWiFi(d gateway.DeviceView) bool {
	return d.Status != gateway.DeviceOffline && d.Info.State == driver.StateNeedsSetup && d.Info.Setup == driver.SetupPhoneChanged
}

// lockedOnWiFi reports whether a phone waits to be unlocked on Wi-Fi: Pinky
// connects to it only once it is unlocked, so as not to wake its screen
// (gateway: lockedOnWiFi). Without an agent waiting for it, the gateway
// shows it at rest, offline, "Locked on Wi-Fi".
func lockedOnWiFi(d gateway.DeviceView) bool {
	return d.Info.State == driver.StateNeedsSetup && d.Info.Setup == driver.SetupUnlock && d.Info.Connection == "wifi"
}

// applyWiFi passes Settings › Phones' switch to the gateway.
func (a *app) applyWiFi(on bool) {
	if ws, ok := a.api.(wifiSetter); ok {
		a.setq.push("wifi", func() { ws.SetWiFi(on) })
	}
}

func (a *app) foundBusy(id string) bool { return a.found.busy[id] }

func (a *app) setFoundBusy(id string, on bool) {
	if a.found.busy == nil {
		a.found.busy = map[string]bool{}
	}
	if on {
		a.found.busy[id] = true
	} else {
		delete(a.found.busy, id)
	}
}

// useFound tells the gateway the phone is the person's: Pinky sets it up
// and connects to it.
func (a *app) useFound(d gateway.DeviceView) {
	fc, ok := a.api.(foundChooser)
	id, name := d.Info.ID, d.Info.Name
	if !ok || a.foundBusy(id) {
		return
	}
	a.setFoundBusy(id, true)
	go func() {
		err := fc.UseFoundPhone(id)
		a.post(func() {
			a.setFoundBusy(id, false)
			if err != nil {
				a.toasts = append(a.toasts, locales.FoundUseFailed(name, err.Error()))
			}
		})
	}()
}

// ignoreFound tells the gateway to leave the phone alone: it goes off the
// list until it is plugged in.
func (a *app) ignoreFound(d gateway.DeviceView) {
	fc, ok := a.api.(foundChooser)
	id, name := d.Info.ID, d.Info.Name
	if !ok || a.foundBusy(id) {
		return
	}
	a.setFoundBusy(id, true)
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
		defer cancel()
		err := fc.IgnoreFoundPhone(ctx, id)
		a.post(func() {
			a.setFoundBusy(id, false)
			if err != nil && !errors.Is(err, gateway.ErrUnknownDevice) {
				a.toasts = append(a.toasts, locales.FoundIgnoreFailed(name, err.Error()))
				return
			}
			a.forgetPhone(id, false)
			a.toasts = append(a.toasts, locales.FoundIgnored(name))
		})
	}()
}

// foundButtons draws Use and Ignore for a phone found on Wi-Fi.
func (a *app) foundButtons(c *mg.Context, d gateway.DeviceView, small bool) {
	busy := a.foundBusy(d.Info.ID)
	_, can := a.api.(foundChooser)
	mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
		use := button(c.Key("found-use-"+d.Info.ID), locales.FoundUse(), btnOpt{kind: btnPrimary, small: small}).
			Label(locales.FoundUseLabel(d.Info.Name)).Disabled(busy || !can)
		if use.Clicked() {
			a.useFound(d)
		}
		ignore := button(c.Key("found-ignore-"+d.Info.ID), locales.FoundIgnore(), btnOpt{small: small}).
			Label(locales.FoundIgnoreLabel(d.Info.Name)).Disabled(busy || !can)
		if ignore.Clicked() {
			a.ignoreFound(d)
		}
		if busy {
			spinner(c)
		}
	})
}

// foundPage is the Phone screen of a phone found on Wi-Fi: what it is, why
// Pinky waits, and Use and Ignore.
func (a *app) foundPage(c *mg.Context, d gateway.DeviceView) {
	p := pal(c)
	mg.Column(c).AlignItems(mg.Center).Gap(10).MaxWidth(440).Role(mg.RoleStatus).Children(func() {
		mg.Icon(c, icWifi).FontSize(28).TextColor(p.accentInk)
		mg.Text(c, locales.FoundTitle(d.Info.Name)).FontSize(fsTitle3).FontWeight(fwSemibold).TextAlign(mg.Center)
		if line := foundLine(d); line != "" {
			mg.Text(c, line).FontSize(fsSmall).TextColor(p.ink3)
		}
		mg.Text(c, foundDetail(d)).FontSize(fsSmall).TextColor(p.ink2).TextAlign(mg.Center)
		mg.Box(c).Height(4)
		a.foundButtons(c, d, false)
		mg.Text(c, locales.FoundUseNote()).FontSize(fsCaption).TextColor(p.ink3).TextAlign(mg.Center)
		mg.Text(c, locales.FoundIgnoreNote()).FontSize(fsCaption).TextColor(p.ink3).TextAlign(mg.Center)
	})
}

// foundDetail says why Pinky waits for the person to say whether a phone
// it found is theirs.
func foundDetail(d gateway.DeviceView) string {
	if d.Info.Platform == driver.Android {
		return locales.FoundDetailAndroid()
	}
	return locales.FoundDetail()
}

// foundLine is a found phone's model and system: "iPad Pro 11-inch (3rd
// generation) · iOS 27.0.1", "Pixel 9 · Android 17".
func foundLine(d gateway.DeviceView) string {
	s := d.Info.Model
	if d.Info.OSVersion != "" {
		if s != "" {
			s += " · "
		}
		system := "iOS "
		if d.Info.Platform == driver.Android {
			system = "Android "
		}
		s += system + d.Info.OSVersion
	}
	if s == d.Info.Name {
		return ""
	}
	return s
}

// foundPhones are the phones found on Wi-Fi, in the gateway's order.
func (a *app) foundPhones() []gateway.DeviceView {
	var out []gateway.DeviceView
	for _, d := range a.devices {
		if foundOnWiFi(d) {
			out = append(out, d)
		}
	}
	return out
}

// foundBox lists the phones found on Wi-Fi above Add phone › iPhone's
// steps, each with Use and Ignore.
func (a *app) foundBox(c *mg.Context) {
	found := a.foundPhones()
	if len(found) == 0 {
		return
	}
	p := pal(c)
	mg.Column(c).Padding(14, 16).Radius(12).Background(p.bg2).Gap(10).Label(locales.AddPhoneIPhoneFoundTitle()).Children(func() {
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			mg.Icon(c, icWifi).FontSize(16).TextColor(p.accentInk)
			mg.Text(c, locales.AddPhoneIPhoneFoundTitle()).FontSize(fsHeadline).FontWeight(fwSemibold)
		})
		guideText(c, locales.AddPhoneIPhoneFoundDetail())
		for _, d := range found {
			mg.Row(c.Key("found-"+d.Info.ID)).Gap(12).AlignItems(mg.Center).Padding(4, 0, 0, 0).Children(func() {
				mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					mg.Text(c, d.Info.Name).FontWeight(fwSemibold)
					if line := foundLine(d); line != "" {
						mg.Text(c, line).FontSize(fsSmall).TextColor(p.ink2)
					}
				})
				a.foundButtons(c, d, true)
			})
		}
		guideNote(c, locales.FoundIgnoreNote())
	})
}

// changedPage is the Phone screen of a phone whose connection over the
// network leads to a device that lacks the mark Pinky left on the phone:
// what happened, and Trust this phone, which asks first.
func (a *app) changedPage(c *mg.Context, d gateway.DeviceView) {
	p := pal(c)
	st := &a.found
	busy := a.foundBusy(d.Info.ID)
	_, can := a.api.(phoneTruster)
	mg.Column(c).AlignItems(mg.Center).Gap(10).MaxWidth(440).Role(mg.RoleStatus).Children(func() {
		mg.Icon(c, icAlert).FontSize(28).TextColor(p.amberDot)
		mg.Text(c, locales.ChangedTitle(d.Info.Name)).FontSize(fsTitle3).FontWeight(fwSemibold).TextAlign(mg.Center)
		mg.Text(c, locales.ChangedDetail(d.Info.Name)).FontSize(fsSmall).TextColor(p.ink2).TextAlign(mg.Center)
		mg.Box(c).Height(4)
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			// Not the primary button: leaving it alone is the safe choice.
			b := button(c.Key("trust-"+d.Info.ID), locales.ChangedTrust(), btnOpt{}).
				Label(locales.ChangedTrustLabel(d.Info.Name)).Disabled(busy || !can)
			if b.Clicked() {
				st.trustAsking, st.trustFor = true, d.Info.ID
			}
			if busy {
				spinner(c)
			}
		})
		mg.Text(c, locales.ChangedTrustNote(d.Info.Name)).FontSize(fsCaption).TextColor(p.ink3).TextAlign(mg.Center)
		a.trustDialog(c, d)
	})
}

// trustDialog asks before trusting what answers for a phone over the
// network.
func (a *app) trustDialog(c *mg.Context, d gateway.DeviceView) {
	st := &a.found
	if !st.trustAsking || st.trustFor != d.Info.ID {
		return
	}
	if mg.AlertDialog(c.Key("trust-ask"), &st.trustAsking, locales.ChangedConfirmTitle(), locales.ChangedConfirmMessage(d.Info.Name),
		locales.SettingsCancel(), locales.ChangedConfirmButton()) == 1 {
		st.trustAsking = false
		a.trustPhone(d)
	}
	if !st.trustAsking {
		st.trustFor = ""
	}
}

// trustPhone tells the gateway that what answers for the phone over the
// network is the person's phone: Pinky marks it again and connects to it.
func (a *app) trustPhone(d gateway.DeviceView) {
	t, ok := a.api.(phoneTruster)
	id, name := d.Info.ID, d.Info.Name
	if !ok || a.foundBusy(id) {
		return
	}
	a.setFoundBusy(id, true)
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
		defer cancel()
		err := t.TrustPhone(ctx, id)
		a.post(func() {
			a.setFoundBusy(id, false)
			if err != nil {
				a.toasts = append(a.toasts, locales.ChangedTrustFailed(name, err.Error()))
				return
			}
			a.toasts = append(a.toasts, locales.ChangedTrusted(name))
		})
	}()
}

// connectingPage is the Phone screen while Pinky opens the phone, or opens
// it again over its other connection as it moves between its cable and
// Wi-Fi (gateway.DeviceConnecting).
func (a *app) connectingPage(c *mg.Context, d gateway.DeviceView) {
	p := pal(c)
	title := locales.PhoneConnectingTitle(d.Info.Name)
	switch d.StatusLine {
	case "Moving to Wi-Fi":
		title = locales.PhoneMovingWiFiTitle(d.Info.Name)
	case "Moving to the cable":
		title = locales.PhoneMovingCableTitle(d.Info.Name)
	case "Switching to the helper":
		title = locales.PhoneSwitchHelperTitle(d.Info.Name)
	case "Switching to CoreDevice":
		title = locales.PhoneSwitchCoreDeviceTitle(d.Info.Name)
	}
	mg.Column(c).AlignItems(mg.Center).Gap(12).MaxWidth(420).Role(mg.RoleStatus).Children(func() {
		mg.Box(c).Size(200, 420).Radius(32).Border(2, p.line2).Background(p.bg2).Center().Children(func() {
			spinner(c)
		})
		mg.Text(c, title).FontSize(fsTitle3).FontWeight(fwSemibold).TextAlign(mg.Center)
		if d.TaskID != "" {
			mg.Text(c, locales.PhoneConnectingTask()).FontSize(fsSmall).TextColor(p.ink2).TextAlign(mg.Center)
		}
	})
}

// lostOnWiFi reports whether the computer's device service lost the phone on
// Wi-Fi though it is on the network (driver.SetupDeviceService).
func lostOnWiFi(d gateway.DeviceView) bool {
	return d.Info.State == driver.StateNeedsSetup && d.Info.Setup == driver.SetupDeviceService
}

// lostPage is the Phone screen of a phone the device service lost: what
// happened, and the restart.
func (a *app) lostPage(c *mg.Context, d gateway.DeviceView) {
	p := pal(c)
	mg.Column(c).AlignItems(mg.Center).Gap(10).MaxWidth(440).Role(mg.RoleStatus).Children(func() {
		mg.Icon(c, icWifiOff).FontSize(28).TextColor(p.amberDot)
		mg.Text(c, locales.LostTitle(d.Info.Name)).FontSize(fsTitle3).FontWeight(fwSemibold).TextAlign(mg.Center)
		mg.Text(c, locales.LostDetail(d.Info.Name)).FontSize(fsSmall).TextColor(p.ink2).TextAlign(mg.Center)
		mg.Box(c).Height(4)
		a.restartControls(c, d, true)
	})
}

// restartControls are the restart button, what it does, and how to do it by
// hand; with only the latter where the gateway cannot restart the service.
func (a *app) restartControls(c *mg.Context, d gateway.DeviceView, center bool) {
	p := pal(c)
	note := func(s string) {
		t := mg.Text(c, s).FontSize(fsCaption).TextColor(p.ink3)
		if center {
			t.TextAlign(mg.Center)
		}
	}
	if _, ok := a.api.(deviceServiceRestarter); ok && guideOS == "darwin" {
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			b := button(c.Key("restart-"+d.Info.ID), locales.LostRestart(), btnOpt{kind: btnPrimary}).Disabled(a.found.restarting)
			if b.Clicked() {
				a.found.asking, a.found.askFor = true, d.Info.ID
			}
			if a.found.restarting {
				spinner(c)
			}
		})
		note(locales.LostRestartNote())
	}
	note(locales.LostManual())
	a.restartDialog(c, d)
}

// restartDialog asks before restarting the device service for a phone.
func (a *app) restartDialog(c *mg.Context, d gateway.DeviceView) {
	st := &a.found
	if !st.asking || st.askFor != d.Info.ID {
		return
	}
	if mg.AlertDialog(c.Key("restart-ask"), &st.asking, locales.LostConfirmTitle(), locales.LostConfirmMessage(d.Info.Name),
		locales.SettingsCancel(), locales.LostConfirmButton()) == 1 {
		a.restartDeviceService(d)
	}
	if !st.asking {
		st.askFor = ""
	}
}

// restartDeviceService asks the gateway to restart the device service; macOS
// then asks for an administrator's password.
func (a *app) restartDeviceService(d gateway.DeviceView) {
	r, ok := a.api.(deviceServiceRestarter)
	st := &a.found
	if !ok || st.restarting {
		return
	}
	st.asking, st.askFor, st.restarting = false, "", true
	id, name := d.Info.ID, d.Info.Name
	reason := locales.LostReason()
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute) // the person types a password
		defer cancel()
		err := r.RestartDeviceService(ctx, id, reason)
		a.post(func() {
			st.restarting = false
			switch {
			case err == nil:
				a.toasts = append(a.toasts, locales.LostRestarted(name))
			case errors.Is(err, driver.ErrCanceled):
			default:
				a.toasts = append(a.toasts, locales.LostRestartFailed(err.Error()))
			}
		})
	}()
}
