package ui

import (
	"runtime"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Add phone › Android: the steps for the phone's maker, which the person
// picks and Pinky switches to once it recognizes the phone, then the
// phone's checkup. Before USB debugging is on, adb cannot see the phone at
// all, so the first steps show until it turns up.

// droidState is the Android steps'.
type droidState struct {
	brand string // the maker whose steps show; "" for stock Android's until one is picked
	// auto is the phone whose checkup picked brand, and autoBrand what it
	// picked; the note says so while the person keeps it.
	auto, autoBrand string
	// waiting is when each phone began to wait for the person to allow
	// this computer, to say what to try when no prompt shows.
	waiting map[string]time.Time
	// host is what the computer lacks for Android phones (the bundled
	// adb's HostCheck), asked every 2 s while the steps show.
	host     driver.HostCheck
	hostAt   time.Time
	hostBusy bool
}

// hostCheckLister is a gateway that says what the computer lacks for each
// platform's phones (gateway.HostChecks).
type hostCheckLister interface {
	HostChecks() map[driver.Platform]driver.HostCheck
}

// androidHost returns what the computer lacks for Android phones, asking
// the gateway off the UI thread every 2 s.
func (a *app) androidHost(c *mg.Context) driver.HostCheck {
	st := &a.droid
	hc, ok := a.api.(hostCheckLister)
	if !ok {
		return driver.HostCheck{}
	}
	if !st.hostBusy && time.Since(st.hostAt) >= 2*time.Second {
		st.hostBusy = true
		go func() {
			h := hc.HostChecks()[driver.Android]
			a.post(func() { st.host, st.hostAt, st.hostBusy = h, time.Now(), false })
		}()
	}
	c.After(2 * time.Second)
	return st.host
}

// noPromptAfter is how long a phone waits for Allow before the steps say
// what to do when the phone shows no prompt.
const noPromptAfter = 12 * time.Second

// androidBeingAdded returns the Android phone the steps follow: one that
// turned up since the step began, else one still waiting to be allowed.
func (a *app) androidBeingAdded() *gateway.DeviceView {
	if d := a.add.fresh(a, driver.Android); d != nil {
		return d
	}
	for i := range a.devices {
		d := &a.devices[i]
		if d.Info.Platform == driver.Android && (d.Info.State == driver.StateUnauthorized || d.Info.State == driver.StateNeedsSetup) {
			return d
		}
	}
	return nil
}

// recognize switches the steps to the maker of the phone, once its
// checkup read it.
func (a *app) recognize(d gateway.DeviceView, run *checkupRun) {
	st := &a.droid
	if run == nil || !run.have || st.auto == d.Info.ID || run.view.Family+run.view.System == "" {
		return
	}
	st.brand = brandOfFamily(run.view.Family)
	st.auto, st.autoBrand = d.Info.ID, st.brand
}

func (a *app) addAndroid(c *mg.Context) {
	s := &a.add
	st := &a.droid
	heading(c, locales.AddAndroidTitle(), locales.AddAndroidDetail())
	d := a.androidBeingAdded()
	online := d != nil && d.Info.State == driver.StateOnline
	var run *checkupRun
	if online {
		run = a.keepChecked(*d)
		a.recognize(*d, run)
	}
	a.brandPicker(c, d)
	// adb that does not work keeps any phone from showing up: say so first.
	if h := a.androidHost(c); h.Missing != "" {
		if words := hostWords(h.Missing); words != "" {
			guideProblem(c, words, h.Detail)
		}
	}
	b := brandByID(nonEmpty(st.brand, "other"))
	seen, before := d != nil, guideNow
	if seen {
		before = guideDone
	}
	plug, check := guideNow, guideLater
	if online {
		plug, check = guideDone, guideNow
	}
	mg.Column(c).Gap(14).Role(mg.RoleList).Label(locales.AddAndroidStepsLabel()).Children(func() {
		guideRow(c, 1, locales.AddAndroidStepDev(), before, func() {
			menuPath(c, b.about(), b.tap())
			guideText(c, locales.AddAndroidStepDevTap(b.tap()))
			if n := b.aboutNote(); n != "" {
				guideNote(c, n)
			}
		})
		guideRow(c, 2, locales.AddAndroidStepUSB(), before, func() {
			menuPath(c, b.dev(), "")
			switchList(c, b.switches())
			if n := b.note(); n != "" {
				guideNote(c, n)
			}
		})
		guideRow(c, 3, locales.AddAndroidStepPlug(), plug, func() {
			always, allow := b.prompt()
			guideText(c, locales.AddAndroidStepPlugDetail(allow))
			allowSketch(c, always, allow)
			a.plugStatus(c, d)
		})
		guideRow(c, 4, locales.AddAndroidStepCheck(), check, func() {
			guideText(c, locales.AddAndroidStepCheckDetail())
			// Done comes first: the items can be long, and none of them
			// keeps the phone from being added.
			mg.Row(c).Gap(10).AlignItems(mg.Center).Children(func() {
				if button(c, locales.AddPhoneDone(), btnOpt{kind: btnPrimary}).Clicked() {
					a.selectPhone(d.Info.ID)
					s.step = ""
				}
				again := button(c, locales.CheckupAgain(), btnOpt{}).Disabled(run != nil && run.running)
				if again.Clicked() {
					a.runCheckup(*d)
				}
			})
			a.checkupList(c, *d, run, true)
			a.addAndroidWiFi(c, *d)
		})
	})
	if online {
		return
	}
	mg.Row(c).Gap(18).Wrap().Children(func() {
		if linkButton(c, locales.AddPhoneOrWiFi()).Clicked() {
			s.step = "qr"
			s.snapshot(a)
		}
		if _, ok := a.api.(tailnetter); ok && linkButton(c, locales.AddAndroidTailnet()).Clicked() {
			s.step = "tailnet"
			s.snapshot(a)
		}
	})
}

// brandPicker asks which phone it is: the makers, as a row of choices.
func (a *app) brandPicker(c *mg.Context, d *gateway.DeviceView) {
	p := pal(c)
	st := &a.droid
	mg.Column(c).Gap(8).Children(func() {
		mg.Text(c, locales.AddAndroidBrandQuestion()).FontSize(fsSmall).FontWeight(fwSemibold)
		mg.Row(c).Gap(6).Wrap().Role(mg.RoleRadioGroup).Label(locales.AddAndroidBrandQuestion()).Children(func() {
			for _, b := range brands() {
				opt := mg.RadioBase(c.Key(b.id), &st.brand, b.id).Padding(5, 11).Radius(14).AlignItems(mg.Center).Label(b.name())
				on := st.brand == b.id || (st.brand == "" && b.id == "other")
				border, bg, ink := p.line2, p.panel, p.ink
				switch {
				case on:
					border, bg, ink = p.accent, p.accentTint, p.accentInk
				case opt.Hovered():
					border = p.ink3
				}
				opt.Border(1, border).Background(bg).Children(func() {
					mg.Text(c, b.name()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(ink)
				})
			}
		})
		if st.auto != "" && st.brand == st.autoBrand {
			if rd := a.device(st.auto); rd != nil {
				msg := locales.AddAndroidRecognizedPhone(rd.Info.Name)
				if run := a.checkupOf(st.auto); run != nil && run.view.System != "" {
					msg = locales.AddAndroidRecognized(rd.Info.Name, run.view.System)
				}
				mg.Row(c).Gap(6).AlignItems(mg.Start).Children(func() {
					mg.Icon(c, icCheck).FontSize(13).TextColor(p.green).Margin(2, 0, 0, 0)
					mg.Text(c, msg).FontSize(fsSmall).TextColor(p.ink2).Grow(1).MinWidth(0)
				})
			}
		}
		mg.Text(c, locales.AddAndroidNamesDiffer()).FontSize(fsCaption).TextColor(p.ink3)
	})
}

// switchList is what to turn on in Developer options, one switch a line.
func switchList(c *mg.Context, sw []brandSwitch) {
	p := pal(c)
	mg.Column(c).Gap(6).Role(mg.RoleList).Children(func() {
		for i, w := range sw {
			mg.Row(c.Key(i)).Gap(8).AlignItems(mg.Start).Role(mg.RoleListItem).Children(func() {
				// A small drawn switch, on or off as the step wants it.
				track := mg.Row(c).Size(26, 15).Radius(8).Padding(2).Margin(2, 0, 0, 0).Shrink(0).Background(p.accent).Justify(mg.End)
				if w.off {
					track.Background(p.line2).Justify(mg.Start)
				}
				track.Children(func() { mg.Box(c).Size(11, 11).Radius(6).Background(p.white) })
				mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					name := locales.AddAndroidTurnOn(w.name)
					if w.off {
						name = locales.AddAndroidTurnOff(w.name)
					}
					mg.Text(c, name).FontSize(fsBody).FontWeight(fwSemibold)
					if w.path != "" {
						menuPath(c, w.path, "")
					}
					if w.why != "" {
						mg.Text(c, w.why).FontSize(fsSmall).TextColor(p.ink2).LineHeight(1.4)
					}
				})
			})
		}
	})
}

// plugStatus says what step 3 waits for, and what is wrong when the phone
// turned up but cannot be used.
func (a *app) plugStatus(c *mg.Context, d *gateway.DeviceView) {
	st := &a.droid
	if st.waiting == nil {
		st.waiting = map[string]time.Time{}
	}
	switch {
	case d == nil:
		guideWaiting(c, locales.AddPhoneLooking())
		guideNote(c, locales.AddAndroidNotSeen())
	case d.Info.State == driver.StateUnauthorized:
		since, ok := st.waiting[d.Info.ID]
		if !ok {
			since = time.Now()
			st.waiting[d.Info.ID] = since
		}
		guideWaiting(c, locales.AddAndroidFoundAllow(d.Info.Name))
		if time.Since(since) > noPromptAfter {
			guideProblem(c, locales.AddAndroidNoPrompt(), "")
		} else {
			c.After(time.Second)
		}
	case d.Info.State == driver.StateNeedsSetup &&
		(d.Info.Setup == driver.SetupUSBPermission || (d.Info.Setup == "" && runtime.GOOS == "linux")):
		guideProblem(c, locales.CheckUSBPermission(), d.Info.Detail)
		guideNote(c, locales.CheckUSBPermissionNote())
	case d.Info.State == driver.StateNeedsSetup:
		// Windows lists a phone whose USB debugging has no driver
		// (SetupUSBDriver); the driver says which to install.
		guideProblem(c, locales.CheckUSBNotReady(), d.Info.Detail)
	case d.Info.State == driver.StateOffline:
		guideProblem(c, locales.AddAndroidOffline(d.Info.Name), d.Info.Detail)
	}
}

// prompt returns the check box and the button of the brand's "Allow USB
// debugging?" prompt.
func (b brand) prompt() (always, allow string) {
	always, allow = locales.AddAndroidSketchAlways(), locales.AddAndroidSketchAllow()
	if b.promptAlways != nil {
		always = b.promptAlways()
	}
	if b.promptAllow != nil {
		allow = b.promptAllow()
	}
	return always, allow
}

// allowSketch draws the prompt the phone shows, so the person knows it
// when it comes: a drawing, not a screenshot.
func allowSketch(c *mg.Context, always, allow string) {
	p := pal(c)
	mg.Column(c).Width(268).Padding(14, 16, 10, 16).Radius(16).Background(p.panel).Border(1, p.line2).Gap(8).Margin(2, 0, 2, 0).
		Role(mg.RoleImage).Label(locales.AddAndroidSketchLabel()).Children(func() {
		mg.Text(c, locales.AddAndroidSketchTitle()).FontSize(fsBody).FontWeight(fwSemibold)
		mg.Column(c).Gap(4).Children(func() {
			mg.Box(c).Height(6).Width(212).Radius(3).Background(p.bg3)
			mg.Box(c).Height(6).Width(150).Radius(3).Background(p.bg3)
		})
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			mg.Box(c).Size(15, 15).Radius(4).Background(p.accent).Center().Shrink(0).Children(func() {
				mg.Icon(c, icCheck).FontSize(11).TextColor(p.white)
			})
			mg.Text(c, always).FontSize(fsSmall).Grow(1).MinWidth(0)
		})
		mg.Row(c).Gap(18).Justify(mg.End).AlignItems(mg.Center).Children(func() {
			mg.Text(c, locales.AddAndroidSketchCancel()).FontSize(fsSmall).TextColor(p.ink3)
			mg.Box(c).Padding(3, 10).Radius(8).Border(2, p.brand).Children(func() {
				mg.Text(c, allow).FontSize(fsSmall).FontWeight(fwBold).TextColor(p.accentInk)
			})
		})
	})
}

// wirelessPath shows where Wireless debugging is on the brand of phone the
// Android steps show, for pairing over Wi-Fi.
func (a *app) wirelessPath(c *mg.Context) {
	b := brandByID(nonEmpty(a.droid.brand, "other"))
	menuPath(c, b.dev()+" › "+locales.GuideWirelessDebugging(), "")
}
