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

// Installing the Pinky helper (WebDriverAgent) on an iPhone from the
// window, on a Mac with Xcode: the same install as pinkyctl helper
// install, through the gateway (helperInstaller). Add phone's older
// iPhones, the checkup, and the Phone screen when the helper is missing
// or its signature runs out offer it; where Pinky cannot install it (not a
// Mac, no Xcode), they keep their link to how to install it by hand.

// helperTimeout is how long an install from the window may take: Xcode
// builds for a minute or two, and may wait for the phone to be unlocked
// and for its passcode.
const helperTimeout = 20 * time.Minute

// helperState is the installs from the window, by phone: the one under
// way, and how the last one ended when it did not work. "" is a phone
// Add phone found no entry for, which Xcode finds by itself.
type helperState struct {
	runs map[string]*helperRun
}

type helperRun struct {
	running bool
	cancel  context.CancelFunc
	step    driver.HelperStep
	name    string // the phone's name in the messages
	// After a failure: why, in the person's words; Xcode's or the
	// driver's own words, in small print; the install's log.
	err, detail, log string
	// teams are the teams this Mac can sign for, when it can for several
	// and the person picks one.
	teams []driver.HelperTeam
}

// helperOpt says how helperInstall draws.
type helperOpt struct {
	again  bool   // Install again: the helper's signature runs out or ran out
	small  bool   // small buttons, as in the checkup's items
	center bool   // centered, as on the Phone screen
	note   bool   // the note on what the install does, under the button
	done   func() // after the helper is installed
}

// canInstallHelper reports whether Pinky can install the iPhone helper
// from the window: on a Mac with Xcode.
func (a *app) canInstallHelper() bool {
	hi, ok := a.api.(helperInstaller)
	return ok && hi.CanInstallHelper()
}

// helperRunning reports whether Pinky installs the helper on the phone id.
func (a *app) helperRunning(id string) bool {
	r := a.helper.runs[id]
	return r != nil && r.running
}

// helperBusy reports whether Pinky installs the helper on any phone:
// Xcode installs on one at a time.
func (a *app) helperBusy() bool {
	for _, r := range a.helper.runs {
		if r.running {
			return true
		}
	}
	return false
}

// startHelperInstall installs the helper on the phone id, named name in
// the messages, signed with team ("" lets Pinky choose), in the
// background; done runs once it is installed.
func (a *app) startHelperInstall(id, name, team string, done func()) {
	hi, ok := a.api.(helperInstaller)
	if !ok || a.helperRunning(id) {
		return
	}
	if a.helper.runs == nil {
		a.helper.runs = map[string]*helperRun{}
	}
	ctx, cancel := context.WithTimeout(a.ctx, helperTimeout)
	r := &helperRun{running: true, cancel: cancel, step: driver.HelperPreparing, name: name}
	a.helper.runs[id] = r
	go func() {
		defer cancel()
		err := hi.InstallHelper(ctx, id, team, func(s driver.HelperStep) {
			a.post(func() { r.step = s })
		})
		stopped := errors.Is(err, context.Canceled)
		a.post(func() {
			r.running, r.cancel = false, nil
			if a.helper.runs[id] != r {
				return
			}
			switch {
			case err == nil:
				delete(a.helper.runs, id)
				a.toasts = append(a.toasts, locales.HelperInstalledToast(name))
				if done != nil {
					done()
				}
			case stopped:
				delete(a.helper.runs, id)
			default:
				var teams *driver.HelperTeamsError
				if errors.As(err, &teams) && len(teams.Teams) > 0 {
					r.teams = teams.Teams
					return
				}
				r.err, r.detail, r.log = helperErrorWords(err, name)
			}
		})
	}()
}

// helperErrorWords says why installing the helper on the phone named name
// failed and what to do, with the details in English and the install's
// log, when there is one.
func helperErrorWords(err error, name string) (text, detail, log string) {
	var hie *driver.HelperInstallError
	if errors.Is(err, gateway.ErrPhoneInUse) {
		return locales.HelperErrInUse(name), "", ""
	}
	if !errors.As(err, &hie) {
		return locales.HelperErrOther(name), err.Error(), ""
	}
	if hie.Err != nil {
		// Without Hint, which says what to do on the command line.
		detail = hie.Err.Error()
	}
	switch hie.Fix {
	case driver.HelperFixXcode:
		text = locales.HelperErrXcode()
	case driver.HelperFixPhone:
		text = locales.HelperErrPhone(name)
	case driver.HelperFixDeveloperMode:
		text = locales.HelperErrDeveloperMode(name)
	case driver.HelperFixAppleID:
		text = locales.HelperErrAppleID()
	case driver.HelperFixOtherTeam:
		text = locales.HelperErrOtherTeam(name)
	case driver.HelperFixSigning:
		text = locales.HelperErrSigning(name)
	case driver.HelperFixTrust:
		text = locales.HelperErrTrust(name)
	case driver.HelperFixPasscode:
		text = locales.HelperErrPasscode(name)
	case driver.HelperFixUnlock:
		text = locales.HelperErrUnlock(name)
	case driver.HelperFixDownload:
		text = locales.HelperErrDownload()
	default:
		text = locales.HelperErrOther(name)
	}
	return text, detail, hie.Log
}

// helperStepWords says what the install is doing, and whether it waits
// for the person.
func helperStepWords(step driver.HelperStep, name string) (text string, waits bool) {
	switch step {
	case driver.HelperBuilding:
		return locales.HelperStepBuilding(), false
	case driver.HelperInstalling:
		return locales.HelperStepInstalling(name), false
	case driver.HelperUnlock:
		return locales.HelperStepUnlock(name), true
	}
	return locales.HelperStepPreparing(), false
}

// helperTeamLabel is a team's button: its name and ID, or the ID alone.
func helperTeamLabel(t driver.HelperTeam) string {
	if t.Name == "" {
		return t.ID
	}
	return locales.HelperTeam(t.Name, t.ID)
}

// helperInstall draws the button that installs the helper on the phone
// id (name in the messages), or the install under way with its step, the
// teams to choose from, and why the last install failed. It draws nothing
// and returns false where Pinky cannot install it from the window, so the
// caller says how to install it by hand.
func (a *app) helperInstall(c *mg.Context, id, name string, o helperOpt) bool {
	r := a.helper.runs[id]
	running := r != nil && r.running
	if !running && !a.canInstallHelper() {
		return false
	}
	p := pal(c)
	align := mg.Start
	if o.center {
		align = mg.Center
	}
	textAlign := func(t mg.Element) mg.Element {
		if o.center {
			t.TextAlign(mg.Center)
		}
		return t
	}
	mg.Column(c.Key("helper-install-" + id)).Gap(8).AlignItems(align).FillWidth().Children(func() {
		switch {
		case running:
			text, waits := helperStepWords(r.step, name)
			mg.Progress(c, -1).FillWidth().Label(text)
			t := mg.Text(c, text).LineHeight(1.4).Role(mg.RoleStatus)
			if waits {
				t.FontSize(fsBody).FontWeight(fwSemibold)
			} else {
				t.FontSize(fsSmall).TextColor(p.ink2)
			}
			textAlign(t)
			if r.cancel != nil && quietButton(c, locales.HelperCancel()).Clicked() {
				r.cancel()
			}
		case r != nil && len(r.teams) > 0:
			textAlign(mg.Text(c, locales.HelperChooseTeam()).FontSize(fsSmall).LineHeight(1.4))
			mg.Row(c).Gap(8).Wrap().AlignItems(mg.Center).Justify(align).Children(func() {
				for _, t := range r.teams {
					if button(c, helperTeamLabel(t), btnOpt{small: o.small}).Clicked() {
						a.startHelperInstall(id, name, t.ID, o.done)
					}
				}
				if quietButton(c, locales.HelperCancel()).Clicked() {
					delete(a.helper.runs, id)
				}
			})
		default:
			if r != nil && r.err != "" {
				textAlign(mg.Text(c, r.err).FontSize(fsSmall).LineHeight(1.4).TextColor(p.red).Role(mg.RoleStatus))
				if r.detail != "" {
					textAlign(mg.Text(c, clipDetail(r.detail)).FontSize(fsCaption).LineHeight(1.4).TextColor(p.ink3))
				}
			}
			mg.Row(c).Gap(14).Wrap().AlignItems(mg.Center).Justify(align).Children(func() {
				label := locales.HelperInstall()
				if o.again {
					label = locales.HelperInstallAgain()
				}
				kind := btnPrimary
				if o.small {
					kind = btnDefault
				}
				if button(c, label, btnOpt{kind: kind, small: o.small}).Disabled(a.helperBusy()).Clicked() && !a.helperBusy() {
					a.startHelperInstall(id, name, "", o.done)
				}
				if r != nil && r.log != "" && linkButton(c, locales.HelperShowLog()).Tooltip(tildePath(r.log)).Clicked() {
					a.reveal(r.log)
				}
			})
			if o.note {
				textAlign(mg.Text(c, locales.HelperInstallNote()).FontSize(fsCaption).LineHeight(1.4).TextColor(p.ink3))
			}
		}
	})
	return true
}

// helperInstallingPage is the Phone screen's middle while Pinky installs
// the helper on the phone: the phone is let go of meanwhile, and looked
// for again after.
func (a *app) helperInstallingPage(c *mg.Context, d gateway.DeviceView) {
	p := pal(c)
	mg.Column(c).AlignItems(mg.Center).Gap(sp2).Width(420).MaxWidth(420).Children(func() {
		mg.Icon(c, icIphone).FontSize(28).TextColor(p.ink2)
		stateTitle(c, locales.CheckHelperTitle())
		a.helperInstall(c, d.Info.ID, d.Info.Name, helperOpt{center: true})
	})
}

// clipDetail keeps the first 400 characters of Xcode's words: the log
// has the rest.
func clipDetail(s string) string {
	if r := []rune(s); len(r) > 400 {
		return string(r[:400]) + "…"
	}
	return s
}
