package ui

import (
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// The checkup's words for iPhones: what the computer lacks (usbmuxd), the
// step a phone that is not online waits for (Info.Setup, which the
// gateway gives the "authorized" item as its fix), and the items of the
// iOS driver's checkup. Fixes that are steps of Add phone › iPhone get a
// link to those steps.

// helperDocs is how to install the WebDriverAgent helper, and
// helperAgainDocs how to install it again when its signature runs out.
const (
	helperDocs      = "https://github.com/DIYgod/pinky/blob/main/docs/notes/ios.md#installing-the-webdriveragent-helper"
	helperAgainDocs = "https://github.com/DIYgod/pinky/blob/main/docs/notes/ios.md#when-the-helpers-signature-runs-out"
)

// helperLink is how to install an iPhone's helper: again, when Pinky knows
// when its signature runs out.
func helperLink(d gateway.DeviceView) string {
	if !d.Info.HelperExpires.IsZero() {
		return helperAgainDocs
	}
	return helperDocs
}

// iphoneCheckTitle names the items only iPhones have; "" for others.
func iphoneCheckTitle(id string) string {
	switch id {
	case driver.CheckDeveloperMode:
		return locales.CheckDeveloperModeTitle()
	case driver.CheckDiskImage:
		return locales.CheckDiskImageTitle()
	case driver.CheckHelper:
		return locales.CheckHelperTitle()
	}
	return ""
}

// iphoneCheckWords puts an iPhone's item into words; ok is false for
// items the shared words say well enough.
func (a *app) iphoneCheckWords(d gateway.DeviceView, ck driver.Check) (w checkWords, ok bool) {
	pass := ck.Status == driver.CheckPass
	w = checkWords{title: checkTitle(ck.ID), detail: true}
	switch ck.ID {
	case driver.CheckHost:
		switch ck.Fix {
		case "apple_devices":
			w.text = locales.AddPhoneIPhoneStepDriverDetail()
		case "usbmuxd":
			w.text = locales.AddPhoneIPhoneStepUsbmuxdDetail()
		case "usbmuxd_stopped":
			w.text = locales.AddPhoneIPhoneUsbmuxdStopped()
		default:
			return w, false
		}
		return w, true
	case driver.CheckAuthorized:
		if pass && d.Info.Virtual {
			// A Simulator trusts every app on the Mac; what it waits for
			// is the helper Pinky builds for it.
			w.title, w.text, w.detail = locales.CheckSimHelperTitle(), locales.CheckSimHelperPass(), false
			return w, true
		}
		if pass {
			// An iPhone trusts the computer; it has no USB debugging.
			w.title, w.text, w.detail = locales.AddPhoneIPhoneStepTrust(), locales.CheckTrustedPass(), false
			return w, true
		}
		if ck.Status == driver.CheckSkip {
			return w, false
		}
		w.title, w.text, w.note = setupWords(d, ck.Fix, a.canInstallHelper())
		if w.text == "" {
			return w, false
		}
		// Steps in the person's words say it all, except why a mount
		// failed or the Simulator helper's build did.
		w.detail = ck.Fix == driver.SetupDiskImage || ck.Fix == driver.SetupSimulatorHelper
		if ck.Fix == driver.SetupDiskImageRefused && refusedOffersMirror(d.Info.DiskImage) {
			w.note = locales.CheckDiskImageRefusedNote()
		}
		return w, true
	case driver.CheckDeveloperMode:
		if pass {
			w.text, w.detail = locales.CheckDeveloperModePass(), false
		} else {
			w.text, w.note = locales.AddPhoneIPhoneStepDevModeDetail(), locales.AddPhoneIPhoneStepDevModeNote()
		}
		return w, true
	case driver.CheckDiskImage:
		switch {
		case pass:
			w.text = locales.CheckDiskImagePass()
			w.detail = false
		case ck.Fix == driver.SetupNoDiskImage:
			w.text = locales.CheckDiskImageNoCopy()
			w.detail = false
		default:
			w.text = locales.CheckDiskImageMissing()
		}
		return w, true
	case driver.CheckHelper:
		w.detail = false
		now := time.Now()
		text, ranOut := helperWords(d, now, a.canInstallHelper())
		exp := d.Info.HelperExpires
		switch {
		case pass:
			w.text = locales.CheckHelperPass()
			if exp.After(now) {
				w.note = locales.CheckHelperUntil(exp.Local())
			}
		case text != "" && (ck.Status == driver.CheckWarn || ranOut):
			// It stops soon, or its signature ran out: iOS won't start it
			// again, though it may still run.
			w.text = text
		case ck.Status == driver.CheckWarn:
			w.text, w.detail = locales.CheckHelperPass(), true
		default:
			w.text = locales.CheckHelperMissing()
		}
		return w, true
	case driver.CheckScreen:
		if pass || ck.Fix != driver.FixRestart {
			return w, false
		}
		// Pinky could not open the phone: it sent no live picture. The
		// driver's details say what came instead (ios.noVideoError).
		w.text = locales.CheckScreenNoLivePicture()
		return w, true
	case driver.CheckInput:
		switch {
		case pass:
			w.text, w.detail = locales.CheckInputIPhonePass(), false
		case ck.Fix == driver.FixInstallHelper:
			w.text, w.detail = locales.CheckInputIPhoneNoHelper(), false
		default:
			return w, false
		}
		return w, true
	}
	return w, false
}

// setupWords says an iPhone's setup step (Info.Setup): a title, what to do
// and a note. here: Pinky installs the helper from the window, so the
// words about it leave out the command.
func setupWords(d gateway.DeviceView, step string, here bool) (title, text, note string) {
	if d.Info.Virtual {
		switch step {
		case driver.SetupPreparing:
			return locales.CheckSimHelperTitle(), locales.CheckSimHelperBuilding(), ""
		case driver.SetupSimulatorHelper:
			return locales.CheckSimHelperTitle(), locales.CheckSimHelperFailed(), ""
		}
	}
	switch step {
	case driver.SetupTrustRecord:
		// The phone trusts the computer; the record here is the problem, and
		// Detail says how an administrator lets Apple's device service
		// replace it.
		return locales.AddPhoneIPhoneTrustRecordTitle(), d.Info.Detail, ""
	case driver.SetupTrust:
		if d.Info.Connection == "wifi" {
			// The phone asks to trust a computer only on a cable.
			return locales.AddPhoneIPhoneStepTrust(), locales.AddPhoneIPhoneTrustWiFi(d.Info.Name), ""
		}
		return locales.AddPhoneIPhoneStepTrust(), locales.AddPhoneIPhoneStepTrustDetail(), ""
	case driver.SetupConfirmWiFi:
		return locales.AddPhoneIPhoneFoundTitle(), locales.FoundDetail(), ""
	case driver.SetupDeviceService:
		return locales.LostTitle(d.Info.Name), locales.LostDetail(d.Info.Name), locales.LostManual()
	case driver.SetupDeveloperMode:
		if d.Info.Connection == "wifi" {
			return locales.CheckDeveloperModeTitle(), locales.AddPhoneIPhoneStepDevModeDetail(), locales.AddPhoneIPhoneDevModeWiFiNote(d.Info.Name)
		}
		return locales.CheckDeveloperModeTitle(), locales.AddPhoneIPhoneStepDevModeDetail(), locales.AddPhoneIPhoneStepDevModeNote()
	case driver.SetupUnlock:
		return locales.CheckDiskImageTitle(), locales.AddPhoneIPhoneStepUnlock(), ""
	case driver.SetupPreparing:
		return locales.CheckDiskImageTitle(), locales.AddPhoneIPhoneStepPreparing(), ""
	case driver.SetupNoDiskImage:
		text := locales.AddPhoneIPhoneNoImageMac()
		if guideOS != "darwin" {
			text = locales.AddPhoneIPhoneNoImageCopy()
		}
		return locales.AddPhoneIPhoneNoImageTitle(), text, locales.CheckDiskImageDownloadNote()
	case driver.SetupDiskImage:
		return locales.CheckDiskImageTitle(), locales.AddPhoneIPhoneStuck(), locales.AddPhoneIPhoneRetrying()
	case driver.SetupDiskImageRefused:
		if d.Info.DiskImage == nil {
			text := d.Info.Detail
			if text == "" {
				text = locales.AddPhoneIPhoneStuck()
			}
			return locales.AddPhoneIPhoneRefusedTitle(), text, locales.AddPhoneIPhoneRefusedRetry()
		}
		return locales.AddPhoneIPhoneRefusedTitle(), refusedWords(*d.Info.DiskImage), locales.AddPhoneIPhoneRefusedRetry()
	case driver.SetupUpdate:
		return locales.AddPhoneIPhoneTooOldTitle(d.Info.Name), locales.AddPhoneIPhoneTooOld(d.Info.OSVersion), ""
	case driver.SetupMediaInUse:
		// iOS does not mirror the screen while an app uses the camera or
		// microphone; Pinky tries again by itself (ios/mediawait.go).
		return locales.MediaInUseTitle(), locales.MediaInUseDetail(d.Info.Name), locales.MediaInUseNote()
	case driver.SetupPasscode:
		// iOS asks now and then before the helper may control the phone.
		return locales.AddPhoneIPhoneStepPasscode(), locales.AddPhoneIPhoneStepPasscodeDetail(), ""
	case driver.SetupConnection:
		// Set to CoreDevice below iOS 27 (Settings › Phones).
		return locales.ConnectionTitle(), locales.ConnectionChosenNeedsIOS27(d.Info.OSVersion), ""
	case driver.SetupInstallHelper:
		// Its signature ran out (Info.HelperExpires), or it is missing.
		if text, ranOut := helperWords(d, time.Now(), here); ranOut {
			return locales.CheckHelperTitle(), text, ""
		}
		return locales.CheckHelperTitle(), locales.CheckHelperMissing(), ""
	}
	return "", "", ""
}

// refusedWords says which developer disk image Apple's signing server
// would not sign (driver.SetupDiskImageRefused) and how to get one it
// signs, by where the image came from.
func refusedWords(img driver.DiskImage) string {
	build, version := img.Build, img.Version
	if build == "" {
		build = "–"
	}
	if version == "" {
		version = "–"
	}
	switch img.Source {
	case driver.DiskImageFromXcode:
		return locales.AddPhoneIPhoneRefusedXcode(build, version)
	case driver.DiskImageFromMirror:
		return locales.AddPhoneIPhoneRefusedMirror(build, version)
	}
	return locales.AddPhoneIPhoneRefusedFolder(img.Dir, build, version)
}

// refusedNeedsXcode reports whether the way to an image Apple signs, after
// it refused img, is installing or updating Xcode from the App Store: for
// Xcode's own image and the mirror's, on a Mac.
func refusedNeedsXcode(img *driver.DiskImage) bool {
	return guideOS == "darwin" && img != nil && (img.Source == driver.DiskImageFromXcode || img.Source == driver.DiskImageFromMirror)
}

// refusedOffersMirror reports whether the steps offer to download an image
// from the third-party mirror after Apple refused img: when img came with
// Xcode or was put in Pinky's folder. Not for the mirror's own download,
// which says to update Xcode or Pinky, nor for PINKY_DDI's image, the only
// one Pinky uses while it is set.
func refusedOffersMirror(img *driver.DiskImage) bool {
	return img != nil && (img.Source == driver.DiskImageFromXcode || img.Source == driver.DiskImageFromFolder)
}

// chooseConnection opens Settings › Phones, where an iPhone's connection
// is chosen (connectionRows), for a phone whose chosen connection does
// not work with it (driver.SetupConnection).
func (a *app) chooseConnection() {
	a.page, a.set.sec = pageSettings, secPhones
}

// needsHelper reports whether an iPhone waits for its helper to be
// installed, as it is missing or its signature ran out.
func needsHelper(d gateway.DeviceView) bool {
	return d.Info.Platform == driver.IOS && d.Info.State == driver.StateNeedsSetup && d.Info.Setup == driver.SetupInstallHelper
}

// helperWords says when the helper an iPhone is driven through stops
// working (Info.HelperExpires): within driver.HelperReminder, or that it
// did, which ranOut reports. "" when it lasts longer or is not known.
// here: Pinky installs it again from the window (Install again), so the
// words leave out how.
func helperWords(d gateway.DeviceView, now time.Time, here bool) (text string, ranOut bool) {
	exp := d.Info.HelperExpires
	switch {
	case exp.IsZero():
		return "", false
	case !now.Before(exp) && here:
		return locales.CheckHelperExpiredHere(d.Info.Name, exp.Local()), true
	case !now.Before(exp):
		return locales.CheckHelperExpired(d.Info.Name, exp.Local()), true
	case exp.Sub(now) > driver.HelperReminder:
		return "", false
	case here:
		return locales.CheckHelperSoonHere(d.Info.Name, exp.Local()), false
	}
	return locales.CheckHelperSoon(d.Info.Name, exp.Local()), false
}

// helperReminder draws the Phone screen's reminder above the mirror that
// the helper an iPhone is driven through stops working soon, width wide,
// and returns the room it takes, with its gap: 0 when it shows none. On a
// Mac with Xcode it installs the helper again (helper.go), and says why
// the last try failed or which teams to choose from; elsewhere it links
// to how to install it again. Once the helper has stopped, the phone
// needs setup and says so in its place.
func (a *app) helperReminder(c *mg.Context, d gateway.DeviceView, width float32) float32 {
	if d.Info.Platform != driver.IOS || d.Info.HelperExpires.IsZero() {
		return 0
	}
	now := c.Now()
	here := a.canInstallHelper()
	text, _ := helperWords(d, now, here)
	if text == "" {
		// Draw it when the time comes.
		c.After(min(d.Info.HelperExpires.Add(-driver.HelperReminder).Sub(now), time.Hour))
		return 0
	}
	p := pal(c)
	id, name := d.Info.ID, d.Info.Name
	r := a.helper.runs[id]
	// The buttons, and what each does.
	var labels []string
	var acts []func()
	switch {
	case r != nil && r.running:
		// Gone in a moment: Pinky lets go of the phone while it installs.
		text, _ = helperStepWords(r.step, name)
	case !here:
		labels, acts = []string{locales.CheckHelperHow()}, []func(){func() { openURL(c, helperAgainDocs, func(error) {}) }}
	case r != nil && len(r.teams) > 0:
		text = locales.HelperChooseTeam()
		for _, t := range r.teams {
			labels = append(labels, helperTeamLabel(t))
			acts = append(acts, func() { a.startHelperInstall(id, name, t.ID, nil) })
		}
	default:
		if r != nil && r.err != "" {
			text = r.err
		}
		labels, acts = []string{locales.HelperInstallAgain()}, []func(){func() { a.startHelperInstall(id, name, "", nil) }}
	}
	height := helperReminderHeight(c, text, labels, width)
	mg.Row(c).Width(width).Height(height).Shrink(0).Padding(8, 8, 8, 14).Radius(12).Background(p.amberTint).Border(1, p.amberLine).
		Gap(10).AlignItems(mg.Center).Role(mg.RoleStatus).Label(text).Children(func() {
		mg.Icon(c, icAlert).FontSize(16).TextColor(p.amberDot)
		mg.Text(c, text).FontSize(fsSmall).Grow(1).MinWidth(0)
		for i, label := range labels {
			if button(c, label, btnOpt{small: true}).Disabled(here && a.helperBusy()).Clicked() {
				acts[i]()
			}
		}
	})
	return height + 14
}

// helperReminderHeight works out the reminder's height as its row lays it
// out: the words wrap in what the width leaves after the padding, the
// border, the icon, the gaps and the buttons (their labels, padding and
// border), and the reminder is no shorter than a button.
func helperReminderHeight(c *mg.Context, text string, labels []string, width float32) float32 {
	tw := width - (14 + 8 + 2) - 16 - 10
	for _, label := range labels {
		lw, _ := c.MeasureText(0, mg.Span{Text: label, Size: 13, Weight: 500})
		tw -= 10 + lw + 2*10 + 2
	}
	_, th := c.MeasureText(max(tw, 1), mg.Span{Text: text, Size: 13})
	return max(th, 28) + 2*8 + 2
}

// iphoneCheckAction draws what an iPhone's item offers to do, and reports
// whether it did: the steps in Add phone for a setup step, how to install
// the helper.
func (a *app) iphoneCheckAction(c *mg.Context, d gateway.DeviceView, ck driver.Check) bool {
	if d.Info.Virtual {
		// A Simulator has none of an iPhone's steps; its helper's build
		// can be tried again.
		if ck.Fix == driver.SetupSimulatorHelper {
			mg.Row(c).Margin(4, 0, 0, 0).Children(func() {
				if button(c, locales.CheckSimHelperRetry(), btnOpt{small: true}).Clicked() {
					a.startVirtualPhone(d)
				}
			})
			return true
		}
		return ck.Fix == driver.SetupPreparing
	}
	switch ck.Fix {
	case driver.SetupTrust, driver.SetupTrustRecord, driver.SetupDeveloperMode, driver.SetupUnlock, driver.SetupPreparing,
		driver.SetupNoDiskImage, driver.SetupDiskImage, driver.SetupDiskImageRefused:
		mg.Row(c).Margin(4, 0, 0, 0).Children(func() {
			if button(c, locales.PhoneSetUpIPhone(), btnOpt{small: true}).Clicked() {
				a.page = pageAddPhone
				a.add.reset(a)
				a.add.step, a.add.focus = "iphone", d.Info.ID
			}
		})
		return true
	case driver.FixInstallHelper:
		// On a Mac with Xcode, Pinky installs it from here: from the
		// helper's item, not again from the input's, which needs it too.
		if ck.ID == driver.CheckInput && a.canInstallHelper() {
			return true
		}
		mg.Column(c).Margin(4, 0, 0, 0).Children(func() {
			again := !d.Info.HelperExpires.IsZero()
			if !a.helperInstall(c, d.Info.ID, d.Info.Name, helperOpt{again: again, small: true}) &&
				button(c, locales.CheckHelperHow(), btnOpt{small: true}).Clicked() {
				openURL(c, helperLink(d), func(error) {})
			}
		})
		return true
	case driver.SetupConnection:
		mg.Row(c).Margin(4, 0, 0, 0).Children(func() {
			if button(c, locales.ConnectionChoose(), btnOpt{small: true}).Clicked() {
				a.chooseConnection()
			}
		})
		return true
	}
	return false
}
