package ui

import (
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// addState is the Add phone wizard's. Over USB it finds the new phone by
// watching Devices for one it did not know when the step began; over
// Wi-Fi the gateway's pairing says which phone paired.
type addState struct {
	step     string // "choose", "android", "qr", "tailnet", "iphone", "virtual"
	known    map[string]bool
	old      bool   // the older-iPhone path
	oldPick  string // "helper" or "mirror"
	tryAgain bool
	pair     pairState // pairing an Android phone over Wi-Fi

	// The iPhone steps (addiphone.go): when they began, and what the
	// computer lacks for iPhones, asked every 2 s.
	since    time.Time
	focus    string // the iPhone the Phone screen sent the person here for
	host     driver.HostCheck
	hostAt   time.Time
	hostBusy bool
	dl       diskDownload // the developer disk image from the mirror
}

func (s *addState) reset(a *app) {
	a.stopPairing()
	seq := s.pair.seq // stays counting, so a stale pairing's answer is dropped
	*s = addState{step: "choose"}
	s.pair.seq = seq
	s.snapshot(a)
}

func (s *addState) snapshot(a *app) {
	s.known = map[string]bool{}
	for _, d := range a.devices {
		s.known[d.Info.ID] = true
	}
}

// fresh returns the newest device of the platform that was not there
// when the step began.
func (s *addState) fresh(a *app, pf driver.Platform) *gateway.DeviceView {
	var out *gateway.DeviceView
	for i := range a.devices {
		d := &a.devices[i]
		if !s.known[d.Info.ID] && d.Info.Platform == pf {
			out = d
		}
	}
	return out
}

func (a *app) addPhonePage(c *mg.Context) {
	p := pal(c)
	s := &a.add
	if s.step == "" {
		s.reset(a)
	}
	// As in the prototype, the wizard has no header: a strip at the top
	// drags the window.
	mg.Row(c).Height(32).Shrink(0).Background(p.bg2).DragWindow()
	// Steps taller than the window scroll inside the card.
	_, h := c.Size()
	mg.Column(c).Grow(1).Center().Padding(0, sp6, sp8, sp6).Background(p.bg2).Children(func() {
		card := mg.Scroll(c).FillWidth().MaxWidth(480).MaxHeight(max(h-64, 240)).Padding(sp8).Radius(rSheet).Background(p.panel).Border(1, p.line).Gap(sp5).Label(locales.AddPhoneLabel())
		shadow(c, card).Children(func() {
			mg.Row(c).MinHeight(22).AlignItems(mg.Center).Margin(0, 0, -6, 0).Children(func() {
				if s.step != "choose" && linkButton(c, locales.AddPhoneBack()).Clicked() {
					switch s.step {
					case "qr", "tailnet":
						s.step = "android"
					default:
						s.step, s.old, s.oldPick, s.since = "choose", false, "", time.Time{}
					}
					s.snapshot(a)
				}
				mg.Spacer(c)
				if len(a.devices) > 0 && quietButton(c, locales.AddPhoneCancel()).Clicked() {
					a.page = pagePhones
				}
			})
			switch s.step {
			case "android":
				a.addAndroid(c)
			case "qr":
				a.addQR(c)
			case "tailnet":
				a.addTailnet(c)
			case "iphone":
				a.addIPhone(c)
			case "virtual":
				a.addVirtual(c)
			default:
				a.addChoose(c)
			}
		})
	})
}

func heading(c *mg.Context, title, desc string) {
	p := pal(c)
	mg.Column(c).Children(func() {
		mg.Text(c, title).FontSize(fsTitle2).FontWeight(fwSemibold).Role(mg.RoleHeading)
		mg.Text(c, desc).FontSize(fsBody).LineHeight(1.5).TextColor(p.ink2).Margin(6, 0, 0, 0)
	})
}

// statusBox is the prototype's role=status line.
func statusBox(c *mg.Context, fn func()) {
	p := pal(c)
	mg.Row(c).Gap(10).Padding(sp3, 14).Radius(rControl).Background(p.bg2).AlignItems(mg.Center).Role(mg.RoleStatus).Children(fn)
}

func (a *app) addChoose(c *mg.Context) {
	p := pal(c)
	s := &a.add
	heading(c, locales.AddPhoneChooseTitle(), locales.AddPhoneChooseDetail())
	mg.Grid(c).Columns(2).Gap(sp3).Children(func() {
		choice := func(ic *mg.SVG, title, desc, step string) {
			b := mg.ButtonBase(c.Key(step)).Padding(sp4).Radius(rCard).Gap(sp1).AlignItems(mg.Start).Justify(mg.Start).Column().Label(title)
			border := p.line2
			if b.Hovered() {
				border = p.accent
				b.Shadow(0, 0, 0, 1, p.accent)
			}
			if b.Clicked() {
				s.step = step
				s.snapshot(a)
			}
			b.Border(1, border).Background(p.panel).Children(func() {
				tileIcon(c, ic).Margin(0, 0, sp2, 0)
				mg.Text(c, title).FontSize(fsHeadline).FontWeight(fwSemibold)
				mg.Text(c, desc).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
			})
		}
		choice(icAndroid, "Android", locales.AddPhoneChooseAndroid(), "android")
		choice(icIphone, "iPhone", locales.AddPhoneChooseIPhone(), "iphone")
	})
	if a.launcher() == nil {
		return
	}
	// Virtual phones: a row under the two, as fewer people want one.
	title, desc := locales.AddPhoneChooseVirtual(), locales.AddPhoneChooseVirtualPC()
	if guideOS == "darwin" {
		desc = locales.AddPhoneChooseVirtualMac()
	}
	b := mg.ButtonBase(c.Key("virtual")).Padding(sp3, sp4).Radius(rCard).Gap(sp3).AlignItems(mg.Center).Label(title)
	border := p.line2
	if b.Hovered() {
		border = p.accent
		b.Shadow(0, 0, 0, 1, p.accent)
	}
	if b.Clicked() {
		s.step = "virtual"
		s.snapshot(a)
	}
	b.Border(1, border).Background(p.panel).Children(func() {
		tileIcon(c, icMonitor)
		mg.Column(c).Gap(2).Grow(1).MinWidth(0).AlignItems(mg.Start).Children(func() {
			mg.Text(c, title).FontSize(fsHeadline).FontWeight(fwSemibold)
			mg.Text(c, desc).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
		})
	})
}

// tileIcon is the icon of a choice of Add phone, on a square of its own
// so that the choices' titles line up whatever the icon's shape.
func tileIcon(c *mg.Context, ic *mg.SVG) mg.Element {
	p := pal(c)
	b := mg.Box(c).Size(36, 36).Radius(rControl).Background(p.bg3).Center().Shrink(0)
	b.Children(func() { mg.Icon(c, ic).FontSize(20).TextColor(p.ink2) })
	return b
}

func (a *app) addQR(c *mg.Context) {
	p := pal(c)
	s := &a.add
	st := &s.pair
	_, canPair := a.api.(pairer)
	switch {
	case !canPair:
		heading(c, locales.PairingTitle(), locales.PairingUnsupported())
	case st.form && st.connect:
		heading(c, locales.PairingConnectTitle(), locales.PairingConnectDetail())
		a.pairConnectForm(c)
	case st.form:
		heading(c, locales.PairingCodeTitle(), locales.PairingCodeDetail())
		a.wirelessPath(c)
		a.pairCodeForm(c)
	default:
		if st.state == "" {
			a.startPairing()
		}
		heading(c, locales.PairingTitle(), locales.PairingQRDetail())
		a.wirelessPath(c)
		mg.Box(c).AlignSelf(mg.Center).Padding(6).Radius(16).Background(hex("#ffffff")).Border(1, p.line).Children(func() {
			if st.code != nil {
				qrView(c, st.code, 208)
				return
			}
			mg.Column(c).Size(208, 208).Center().Gap(8).Padding(16).Children(func() {
				if st.state == "failed" {
					mg.Icon(c, icAlert).FontSize(26).TextColor(hex("#975800"))
					mg.Text(c, locales.PairingNoCode()).FontSize(fsSmall).TextColor(hex("#5b5b64"))
				} else {
					spinner(c)
				}
			})
		})
	}
	if canPair && !st.form && st.state == "failed" {
		// Why it failed, with what to do next under it: the code where it
		// may help, and a new QR code.
		mg.Column(c).Gap(10).Padding(sp3, 14).Radius(rControl).Background(p.bg2).Role(mg.RoleStatus).Children(func() {
			mg.Row(c).Gap(10).AlignItems(mg.Start).Children(func() {
				mg.Icon(c, icAlert).FontSize(16).TextColor(p.amberDot).Shrink(0)
				mg.Text(c, st.err).Grow(1).MinWidth(0)
			})
			mg.Row(c).Gap(10).Justify(mg.End).Wrap().Children(func() {
				if st.errCode && button(c, locales.PairingUseCode(), btnOpt{small: true}).Clicked() {
					a.stopPairing()
					st.form, st.formErr = true, ""
					return
				}
				if button(c, locales.AddPhoneTryAgain(), btnOpt{small: true}).Clicked() {
					a.startPairing()
				}
			})
		})
	} else if canPair && !st.form {
		statusBox(c, func() {
			switch st.state {
			case "waiting":
				spinner(c)
				c.After(time.Second) // the progress has no events
				msg := locales.PairingWaiting()
				switch pr := st.prog(); {
				case pr.Paired:
					msg = locales.PairingConnecting()
				case pr.Found:
					msg = locales.PairingFound()
				}
				mg.Text(c, msg).Grow(1).MinWidth(0)
			default:
				spinner(c)
				mg.Text(c, locales.PairingGettingCode())
			}
		})
	}
	stuck := canPair && !st.form && a.pairStuck(c)
	if d := s.fresh(a, driver.Android); d != nil && d.Info.State == driver.StateOnline {
		statusBox(c, func() {
			dot(c, p.green, false)
			mg.Text(c, locales.AddPhoneFound(d.Info.Name)).FontWeight(fwSemibold)
		})
		mg.Row(c).Children(func() {
			if button(c, locales.AddPhoneDone(), btnOpt{kind: btnPrimary}).Clicked() {
				a.selectPhone(d.Info.ID)
				s.step = ""
			}
		})
		return
	}
	mg.Row(c).Gap(16).Wrap().Children(func() {
		// A stuck pairing offers the pairing code in its note, a failed one
		// next to Try again.
		if canPair && (st.form || !stuck && !(st.state == "failed" && st.errCode)) {
			other := locales.PairingUseCode()
			if st.form {
				other = locales.PairingUseQR()
			}
			if linkButton(c, other).Clicked() {
				st.form, st.formErr, st.connect = !st.form, "", false
				if st.form {
					a.stopPairing()
				}
			}
		}
		if linkButton(c, locales.PairingUseUSB()).Clicked() {
			s.step = "android"
			s.snapshot(a)
		}
	})
}

// pairCodeForm is Pair device with pairing code: the address and port,
// and the six-digit code.
func (a *app) pairCodeForm(c *mg.Context) {
	p := pal(c)
	st := &a.add.pair
	mg.Column(c).Gap(12).Children(func() {
		field := func(label string) {
			mg.Text(c, label).FontSize(fsSmall).FontWeight(fwSemibold)
		}
		var addr, code mg.Element
		mg.Column(c).Gap(6).Children(func() {
			field(locales.PairingAddrField())
			addr = mg.TextInput(c.Key("pair-addr"), &st.addr).Placeholder("192.168.1.20:37123").Label(locales.PairingAddrField()).AutoFocus()
		})
		mg.Column(c).Gap(6).Children(func() {
			field(locales.PairingCodeField())
			code = mg.TextInput(c.Key("pair-code"), &st.pin).Placeholder(locales.PairingCodePlaceholder()).Label(locales.PairingCodeField()).MaxWidth(160)
		})
		if code.Changed() {
			st.pin = keepDigits(st.pin, 6)
		}
		mg.Row(c).Gap(10).AlignItems(mg.Center).Children(func() {
			pair := button(c, locales.PairingPair(), btnOpt{kind: btnPrimary}).Disabled(st.formBusy || len(st.pin) < 6 || st.addr == "")
			if (pair.Clicked() || code.Submitted() || addr.Submitted()) && !st.formBusy && len(st.pin) == 6 {
				a.pairWithCode()
			}
			if st.formBusy {
				spinner(c)
				mg.Text(c, locales.PairingBusy()).FontSize(fsSmall).TextColor(p.ink2)
			}
		})
		if st.formErr != "" {
			mg.Text(c, st.formErr).FontSize(fsBody).TextColor(p.red).Role(mg.RoleStatus)
		}
	})
}

// pairConnectForm is the connect step: the address and port at the top of
// Wireless debugging, for a phone that paired but that Pinky could not find
// on the network.
func (a *app) pairConnectForm(c *mg.Context) {
	p := pal(c)
	st := &a.add.pair
	mg.Column(c).Gap(12).Children(func() {
		var addr mg.Element
		mg.Column(c).Gap(6).Children(func() {
			mg.Text(c, locales.PairingAddrField()).FontSize(fsSmall).FontWeight(fwSemibold)
			addr = mg.TextInput(c.Key("connect-addr"), &st.connAddr).Placeholder("192.168.1.20:41234").Label(locales.PairingAddrField()).AutoFocus()
		})
		mg.Row(c).Gap(10).AlignItems(mg.Center).Children(func() {
			connect := button(c, locales.PairingConnect(), btnOpt{kind: btnPrimary}).Disabled(st.formBusy || strings.TrimSpace(st.connAddr) == "")
			if (connect.Clicked() || addr.Submitted()) && !st.formBusy && strings.TrimSpace(st.connAddr) != "" {
				a.connectWiFi()
			}
			if st.formBusy {
				spinner(c)
				mg.Text(c, locales.PairingConnectBusy()).FontSize(fsSmall).TextColor(p.ink2)
			}
		})
		if st.formErr != "" {
			mg.Text(c, st.formErr).FontSize(fsBody).TextColor(p.red).Role(mg.RoleStatus)
		}
	})
}

func (a *app) addIPhone(c *mg.Context) {
	p := pal(c)
	s := &a.add
	d := s.fresh(a, driver.IOS)
	if !s.old {
		a.addIPhoneSteps(c)
		return
	}
	heading(c, locales.AddPhoneIPhoneTitle(), locales.AddPhoneOlderDetail())
	if d != nil {
		statusBox(c, func() {
			dot(c, p.amberDot, false)
			mg.Text(c, strings.TrimSpace(locales.AddPhoneFoundIOS(d.Info.Name, d.Info.OSVersion)))
		})
	}
	mg.Column(c).Padding(sp4, 18).Radius(rCard).Background(p.amberTint).Border(1, p.amberLine).Gap(10).Children(func() {
		mg.Text(c, locales.AddPhoneOlderTitle()).FontSize(fsHeadline).FontWeight(fwSemibold)
		opt := func(key, title, desc string) {
			b := mg.ButtonBase(c.Key(key)).Column().Gap(3).AlignItems(mg.Start).Padding(sp3, 14).Radius(rControl).Background(p.panel).Label(title)
			border := p.line2
			if b.Hovered() || s.oldPick == key {
				border = p.accent
				b.Shadow(0, 0, 0, 1, p.accent)
			}
			if b.Clicked() {
				s.oldPick = key
			}
			b.Border(1, border).Children(func() {
				mg.Text(c, title).FontWeight(fwSemibold)
				mg.Text(c, desc).FontSize(fsSmall).TextColor(p.ink2)
			})
		}
		opt("helper", locales.AddPhoneOlderHelper(), locales.AddPhoneOlderHelperDetail())
		opt("mirror", locales.AddPhoneOlderMirroring(), locales.AddPhoneOlderMirroringDetail())
	})
	switch s.oldPick {
	case "":
		mg.Text(c, locales.AddPhoneOlderUpdate()).FontSize(fsSmall).TextColor(p.ink2).Margin(-6, 0, 0, 0)
	case "mirror":
		mg.Text(c, locales.AddPhoneOlderMirrorNotYet()).FontSize(fsSmall).TextColor(p.ink2)
	default:
		// The helper, installed from here on a Mac with Xcode: on the
		// iPhone Pinky found, else on the one Xcode finds; once it is
		// installed, the steps follow the phone. An install begun before
		// Pinky found the phone keeps its place.
		id, name := "", locales.HelperThisIPhone()
		if d != nil && a.helper.runs[""] == nil {
			id, name = d.Info.ID, d.Info.Name
		}
		done := func() { s.old, s.oldPick = false, "" }
		if !a.helperInstall(c, id, name, helperOpt{note: true, done: done}) {
			mg.Text(c, locales.AddPhoneOlderHelperHow()).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
			mg.Row(c).Children(func() {
				if linkButton(c, locales.CheckHelperHow()).Clicked() {
					openURL(c, helperDocs, func(error) {})
				}
			})
		}
	}
}

func keepDigits(s string, n int) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' && b.Len() < n {
			b.WriteRune(r)
		}
	}
	return b.String()
}
