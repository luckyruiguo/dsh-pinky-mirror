package ui

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// phonesPage shows the selected phone, or all of them, with the Needs you
// and Tasks panel on the right.
func (a *app) phonesPage(c *mg.Context) {
	p := pal(c)
	if len(a.devices) == 0 {
		pageHeader(c, func() { pageTitle(c, locales.PhonePageTitle()) })
		mg.Column(c).Grow(1).Center().Gap(sp3).Children(func() {
			if !a.loaded {
				spinner(c)
				return
			}
			stateTitle(c, locales.PhoneNoPhones())
			if b := button(c, locales.PhoneAddPhone(), btnOpt{kind: btnPrimary, icon: icPlus}); b.Clicked() {
				a.page = pageAddPhone
				a.add.reset(a)
			}
		})
		return
	}
	name, line := locales.PhoneAllPhones(), ""
	if a.sel == allPhones {
		online, offline := 0, 0
		for _, d := range a.devices {
			if d.Status == gateway.DeviceOffline {
				offline++
			} else {
				online++
			}
		}
		line = locales.PhoneConnected(online)
		if offline > 0 {
			line += " · " + locales.PhoneOfflineCount(offline)
		}
	} else if d := a.device(a.sel); d != nil {
		name, line = d.Info.Name, connectionLine(*d)
		if d.Streaming {
			line += " · " + locales.PhoneStreaming()
		}
		if t := a.taskOf(d.Info.ID); a.backgroundTask(*d, t) {
			line += " · " + locales.PhoneWorksInBackground(agentOrTheAgent(t))
		}
	}
	pageHeader(c, func() {
		mg.Row(c).AlignItems(mg.Baseline).Gap(10).Children(func() {
			pageTitle(c, name)
			mg.Text(c, line).FontSize(fsSmall).TextColor(p.ink2)
		})
		if d := a.device(a.sel); d != nil && a.sel != allPhones {
			mg.Spacer(c)
			a.phoneAbout(c, *d)
		}
	})
	mg.Row(c).Grow(1).MinHeight(0).AlignItems(mg.Stretch).Children(func() {
		if a.sel == allPhones {
			a.allPhonesGrid(c)
		} else {
			mg.Column(c).Grow(1).MinWidth(0).Padding(stagePad).Gap(stageGap).AlignItems(mg.Center).Justify(mg.Center).Children(func() {
				if d := a.device(a.sel); d != nil {
					a.phoneCenter(c, *d)
				}
			})
		}
		a.rightPanel(c)
	})
}

// phoneCenter is the middle of the Phone screen: the banner, the mirror
// and the keys below it.
func (a *app) phoneCenter(c *mg.Context, d gateway.DeviceView) {
	p := pal(c)
	if a.chk.showing == d.Info.ID {
		a.checkupPanel(c, d)
		return
	}
	if a.helperRunning(d.Info.ID) {
		// Pinky lets go of the phone while it installs the helper, which
		// would show it offline meanwhile (helper.go).
		a.helperInstallingPage(c, d)
		return
	}
	if d.Paused {
		a.pausedPage(c, d)
		return
	}
	if d.Status == gateway.DeviceConnecting {
		a.connectingPage(c, d)
		return
	}
	if lockedOnWiFi(d) {
		// Locked on Wi-Fi: Pinky waits until it is unlocked (wifi.go).
		mg.Column(c).AlignItems(mg.Center).Gap(sp2).MaxWidth(420).Children(func() {
			mg.Box(c).Size(200, 420).Radius(32).Border(2, p.line2).Background(p.bg2).Center().Children(func() {
				mg.Icon(c, icLock).FontSize(30).TextColor(p.ink3)
			})
			stateTitle(c, locales.PhoneLockedWiFiTitle(d.Info.Name))
			stateDetail(c, locales.PhoneLockedWiFiDetail())
			a.removeLink(c, d)
		})
		return
	}
	if d.Status == gateway.DeviceOffline || d.Info.State == driver.StateOffline {
		mg.Column(c).AlignItems(mg.Center).Gap(sp2).Children(func() {
			ic := icWifiOff
			if d.Info.Virtual {
				ic = icMonitor
			}
			mg.Box(c).Size(200, 420).Radius(32).Border(2, p.line2).Background(p.bg2).Center().Children(func() {
				mg.Icon(c, ic).FontSize(30).TextColor(p.ink3)
			})
			stateTitle(c, locales.PhoneOffline(d.Info.Name))
			detail := d.Info.Detail
			switch {
			case networkOfflineText(d) != "":
				detail = networkOfflineText(d)
			case detail == "" && d.Info.Virtual:
				detail = locales.PhoneVirtualNotRunning()
			case detail == "":
				detail = locales.PhoneNotConnected()
			default:
				detail += "."
			}
			stateDetail(c, detail)
			if a.canStartVirtual(d) {
				a.startVirtualButton(c, d)
			}
			// Reconnect and Checkup side by side, then Remove, quieter.
			mg.Row(c).Gap(sp2).Margin(sp1, 0, 0, 0).Children(func() {
				if !a.canStartVirtual(d) && button(c, locales.PhoneReconnect(), btnOpt{kind: btnPrimary}).Label(locales.PhoneReconnect()).Clicked() {
					a.page = pageAddPhone
					a.add.reset(a)
					if d.Info.Virtual && a.launcher() != nil {
						a.add.step = "virtual"
					}
				}
				if button(c, locales.PhoneCheckup(), btnOpt{}).Label(locales.PhoneCheckup()).Clicked() {
					a.openCheckup(d)
				}
			})
			a.removeLink(c, d)
		})
		return
	}
	if foundOnWiFi(d) {
		// Found on Wi-Fi, and maybe not the person's (wifi.go).
		a.foundPage(c, d)
		return
	}
	if lostOnWiFi(d) {
		// The device service lost it on Wi-Fi (wifi.go).
		a.lostPage(c, d)
		return
	}
	if changedOnWiFi(d) {
		// What answers for it over the network lacks its mark (wifi.go).
		a.changedPage(c, d)
		return
	}
	if d.Info.State != driver.StateOnline {
		// Connected, and waiting for a step (gateway.DeviceNeedsSetup),
		// which the page says as the phone's checkup does.
		w := a.setupStep(d)
		// An app on the iPhone holds the camera or microphone: nothing to
		// set up or fix in Pinky, which connects once it lets go.
		mediaInUse := d.Info.Setup == driver.SetupMediaInUse && w.title != ""
		mg.Column(c).AlignItems(mg.Center).Gap(sp2).MaxWidth(420).Children(func() {
			mg.Icon(c, icAlert).FontSize(28).TextColor(p.amberDot)
			if mediaInUse {
				stateTitle(c, w.title)
			} else {
				stateTitle(c, locales.PhoneNeedsSetup(d.Info.Name))
			}
			if w.text != "" {
				stateDetail(c, w.text)
			}
			if w.detail && d.Info.Detail != "" && d.Info.Detail != w.text {
				// What the words leave to the driver: which USB driver
				// to install, why the disk image did not mount.
				mg.Text(c, d.Info.Detail).FontSize(fsCaption).LineHeight(1.4).TextColor(p.ink3).TextAlign(mg.Center)
			}
			// On a Mac with Xcode, Pinky installs a missing helper from
			// here (helper.go), and the checkup is a link below.
			installs := needsHelper(d) && a.helperInstall(c, d.Info.ID, d.Info.Name,
				helperOpt{again: !d.Info.HelperExpires.IsZero(), center: true, note: true})
			switch {
			case installs:
				if linkButton(c, locales.PhoneCheckup()).Clicked() {
					a.openCheckup(d)
				}
			case d.Info.Virtual && d.Info.Setup == driver.SetupPreparing:
				spinner(c) // the Simulator helper builds: nothing to fix
			case mediaInUse:
				guideWaiting(c, w.note)
			default:
				if button(c, locales.PhoneCheckupFix(), btnOpt{kind: btnPrimary}).Clicked() {
					a.openCheckup(d)
				}
			}
			switch {
			case installs:
			case mediaInUse:
				// No steps to show: the page says what to do.
			case d.Info.Virtual:
				if d.Info.Setup == driver.SetupSimulatorHelper && linkButton(c, locales.CheckSimHelperRetry()).Clicked() {
					a.startVirtualPhone(d)
				}
			case needsHelper(d):
				if linkButton(c, locales.CheckHelperHow()).Clicked() {
					openURL(c, helperLink(d), func(error) {})
				}
			case d.Info.Setup == driver.SetupConnection:
				// The fix is another connection, in Settings › Phones.
				if linkButton(c, locales.ConnectionChoose()).Clicked() {
					a.chooseConnection()
				}
			case d.Info.Platform == driver.IOS:
				// An iPhone's steps are in Add phone, which follows a
				// phone that waits for one (addiphone.go).
				if linkButton(c, locales.PhoneSetUpIPhone()).Clicked() {
					a.page = pageAddPhone
					a.add.reset(a)
					a.add.step, a.add.focus = "iphone", d.Info.ID
				}
			}
			a.removeLink(c, d)
		})
		return
	}

	t := a.taskOf(d.Info.ID)
	agent := ""
	if t != nil && running(t.State) {
		agent = nonEmpty(t.Agent, locales.AgentGenericStart())
	}
	inControl := d.Status == gateway.DeviceInControl
	ho, handoff := a.handoffFor(d.Info.ID)
	var want controlText
	switch {
	case handoff && !a.isLocked(d) && t != nil && t.State == gateway.TaskPaused:
		// The person took over and the agent waits with phone_handoff.
		inControl = true
		want = controlText{locales.PhoneBannerInControl(), locales.PhoneBannerPaused(agentOf(ho))}
	case handoff && !a.isLocked(d):
		// The agent asked for the person: the phone is theirs until they
		// hand it back, here or with Done on the card.
		inControl = true
		reason := strings.TrimSuffix(strings.TrimSuffix(needTitle(ho), "."), "\u3002") // the agent's words, without the final period
		want = controlText{locales.PhoneBannerYourTurn(), locales.PhoneBannerWaits(agentOf(ho), lowerFirst(reason))}
	case inControl && agent != "":
		want = controlText{locales.PhoneBannerInControl(), locales.PhoneBannerPaused(agent)}
	case inControl:
		want = controlText{locales.PhoneBannerInControl(), locales.PhoneBannerHandBackHint()}
	}

	// While an agent works on its own display in the background, the
	// mirror shows that display, unless the person chose the phone's own
	// screen; the agent's outline belongs on its display only.
	m, bgTask := a.screenFor(d, t, false)

	// The banner has its own room above the mirror, which shrinks for it:
	// over the phone's screen it would hide what the person wants to
	// click there. It comes and goes between the person's gestures on the
	// mirror, as a touch starts the takeover that shows it: a mirror
	// moving under a finger still down would end the touch elsewhere.
	if !m.down && !m.scrolling {
		a.control[d.Info.ID] = want
	}
	banner := a.control[d.Info.ID]

	// Size the phone to the room the window gives it.
	w, h := c.Size()
	availH := h - headerHeight - 2*stagePad - stageGap - dockHeight
	availW := w - sidebarWidth - rightPanelWidth - 2*stagePad
	if banner != (controlText{}) {
		availH -= a.controlBanner(c, d.Info.ID, banner, min(460, availW)) + stageGap
	} else {
		// A reminder that the helper stops working soon takes the
		// banner's place, and room (checkup_iphone.go).
		availH -= a.helperReminder(c, d, min(520, availW))
	}
	watching := m.agent
	shown := t
	if bgTask && !watching {
		shown = nil
	}
	aspect := m.aspect(d)
	const bezel = 9
	ph := min(availH, (availW-2*bezel)/aspect+2*bezel)
	ph = max(ph, 240)
	sw := (ph - 2*bezel) * aspect
	// The bezel takes the screen's shape, turned with the phone (bezel.go).
	radius, inner := bezelRadius(bezelOf(d), sw, ph-2*bezel, bezel)

	// Keyed, the phone and its keys stay themselves as the banner comes
	// and goes before them: the mirror keeps the keyboard's focus.
	fig := mg.Box(c.Key("phone-" + d.Info.ID)).Width(sw + 2*bezel).Height(ph).Shrink(0).Radius(radius).Background(p.phone).Padding(bezel)
	fig.Shadow(0, 2, 6, 0, p.shadowLo).Shadow(0, 18, 44, 0, p.shadowHi)
	if inControl {
		fig.Shadow(0, 0, 0, 2, p.ink2)
	} else {
		// The phone's metal edge, which keeps a dark phone apart from a dark page.
		fig.Shadow(0, 0, 0, 1, p.rim)
	}
	fig.Label(locales.PhoneScreenLabel(d.Info.Name)).Children(func() {
		a.mirrorView(c, m, d, shown, inner, false)
	})

	// The keys: Back, Home and Recents on Android, Home on iPhone, after
	// the switch between the agent's screen and the phone's own.
	dock := mg.Row(c.Key("keys-"+d.Info.ID)).Height(dockHeight).Padding(sp1).Gap(2).AlignItems(mg.Center).
		Radius(rCard).Background(p.card).Border(1, p.line)
	if !p.dark {
		dock.Shadow(0, 1, 2, 0, p.shadowLo).Shadow(0, 4, 14, 0, mg.RGBA(18, 18, 28, 0.06))
	}
	dock.Children(func() {
		if bgTask && (watching || a.phoneChosen(d.Info.ID, t)) {
			which := 0
			if !watching {
				which = 1
			}
			seg := mg.Segmented(c.Key("screen-"+d.Info.ID), &which, locales.PhoneAgentScreen(agentOrTheAgent(t)), locales.PhonePhoneScreen()).Label(locales.PhoneShowOnMirror())
			if seg.Changed() {
				a.bg.phone[d.Info.ID] = ""
				if which == 1 {
					a.bg.phone[d.Info.ID] = t.ID
				}
			}
			dockSeparator(c)
		}
		key := func(ic *mg.SVG, label string, k driver.Key) {
			b := mg.ButtonBase(c).Size(dockKey, dockKey-4).Radius(rControl).Center().Label(label).Tooltip(label)
			col := p.ink2
			if watching {
				// Keys act on the phone's own screen, not the agent's.
				b.Disabled(true).Tooltip(locales.PhoneKeysAgentScreen(agentOrTheAgent(t)))
				col = p.ink3
			} else if b.Hovered() {
				b.Background(p.bg3)
				col = p.ink
			}
			if b.Clicked() && !watching {
				id := d.Info.ID
				a.call(id, locales.PhonePressKey(label), func() error { return a.api.Key(id, k) })
			}
			b.TextColor(col).Children(func() { mg.Icon(c, ic).FontSize(18) })
		}
		if d.Info.Platform != driver.IOS {
			key(icBack, locales.PhoneKeyBack(), driver.KeyBack)
		}
		key(icHome, locales.PhoneKeyHome(), driver.KeyHome)
		if d.Info.Platform != driver.IOS {
			key(icRecents, locales.PhoneKeyRecents(), driver.KeyRecents)
		}
		dockSeparator(c)
		a.moreControls(c, d, m)
	})
}

// The stage: the room around the phone, the gap between the banner, the
// phone and its keys, and the dock of keys under it.
const (
	stagePad   = sp5
	stageGap   = sp4
	dockKey    = 36 // a key's width; its height is 4 less
	dockHeight = dockKey - 4 + 2*sp1 + 2
)

// dockSeparator parts the dock's groups of keys.
func dockSeparator(c *mg.Context) {
	mg.Box(c).Size(1, 18).Background(pal(c).line2).Margin(0, sp1)
}

// controlText is what the "You're in control" banner says: its bold start
// and the rest.
type controlText struct{ bold, rest string }

// controlLines is as many lines as the banner's text takes; an agent's
// longer words end with an ellipsis, and the Needs you card has them all.
const controlLines = 3

// controlBanner is the "You're in control" banner above the mirror, with
// Hand back, width wide. The person keeps control after the agent left,
// until they hand the phone back. It returns its height, for the mirror
// to leave it room.
func (a *app) controlBanner(c *mg.Context, id string, text controlText, width float32) float32 {
	p := pal(c)
	spans := []mg.Span{{Text: text.bold, Weight: fwSemibold, Color: p.ink}, {Text: text.rest, Color: p.ink2}}
	label := locales.PhoneHandBack()
	height := controlBannerHeight(c, spans, label, width)
	b := mg.Row(c).Width(width).Height(height).Shrink(0).
		Padding(bannerPadY, bannerPadY, bannerPadY, bannerPadX).Radius(rCard).Background(p.panel).Border(1, p.line2).Gap(10).AlignItems(mg.Center).
		Role(mg.RoleStatus)
	shadow(c, b).Children(func() {
		mg.Icon(c, icHand).FontSize(18).TextColor(p.ink2)
		mg.RichText(c, spans...).Grow(1).MinWidth(0).MaxLines(controlLines)
		if button(c, label, btnOpt{kind: btnPrimary}).Clicked() {
			a.handBack(id)
		}
	})
	return height
}

// controlBannerHeight works out the banner's height as its row lays it
// out: the words wrap, up to controlLines lines, in what width leaves
// after the padding, the border, the icon, the gaps and Hand back (its
// label, padding and border), and the banner is no shorter than Hand back.
func controlBannerHeight(c *mg.Context, spans []mg.Span, label string, width float32) float32 {
	lw, _ := c.MeasureText(0, mg.Span{Text: label, Size: 14, Weight: 500})
	tw := width - (bannerPadX + bannerPadY + 2) - 18 - 2*10 - (lw + 2*14 + 2)
	_, th := c.MeasureText(max(tw, 1), spans...)
	_, line := c.MeasureText(0, spans...)
	return max(min(th, controlLines*line), 34) + 2*bannerPadY + 2
}

// The control banner's padding: bannerPadY above, below and after Hand
// back, bannerPadX before the hand.
const (
	bannerPadY = 10
	bannerPadX = sp4
)

// moreControls is the ⋯ button under the mirror and its popover.
func (a *app) moreControls(c *mg.Context, d gateway.DeviceView, m *mirror) {
	p := pal(c)
	id := d.Info.ID
	open := a.more[id]
	a.phoneWiFiOf(c, d) // read before the menu opens, which may offer to move it to Wi-Fi
	b := mg.ButtonBase(c.Key("more-"+id)).Size(dockKey, dockKey-4).Radius(rControl).Center().Label(locales.PhoneMoreControls()).Tooltip(locales.PhoneMoreControls())
	col := p.ink2
	if b.Hovered() || open {
		b.Background(p.bg3)
		col = p.ink
	}
	if b.Clicked() {
		open = !open
	}
	b.TextColor(col).Children(func() { mg.Icon(c, icMore).FontSize(18) })
	mg.PopoverBase(c, b, &open, func(panel mg.Element) {
		menuPanel(c, panel).Width(300).Margin(sp2, 0, 0, 0)
		if menuItem(c, locales.PhoneTakeScreenshot(), mg.Color{}).Clicked() {
			open = false
			a.saveScreenshot(m, d.Info.Name)
		}
		if menuItem(c, locales.PhoneCheckup(), mg.Color{}).Clicked() {
			open = false
			a.openCheckup(d)
		}
		a.phoneWiFiMenu(c, d, func() { open = false })
		menuItem(c, locales.PhoneRecordScreen(), p.ink3).Disabled(true).Tooltip(locales.PhoneRecordScreenTooltip())
		a.rotateMenu(c, d, func() { open = false })
		mg.Box(c).Height(1).Background(p.line).Margin(6, 4)
		mg.Row(c).Gap(sp3).Padding(sp2, 10).AlignItems(mg.Center).Children(func() {
			ink := p.ink
			desc := locales.PhoneBackgroundDesc()
			if !d.Features.Background {
				ink = p.ink3
				desc = locales.PhoneBackgroundUnsupported()
			}
			menuText(c, locales.PhoneWorkInBackground(), desc, ink)
			on := a.bg.on[id] && d.Features.Background
			sw := toggle(c.Key("bg-pop-"+id), &on, locales.PhoneWorkInBackground()).Disabled(!d.Features.Background)
			if sw.Changed() && d.Features.Background {
				a.setBackground(d, on)
			}
		})
		if a.canPause() || a.canRemove() {
			mg.Box(c).Height(1).Background(p.line).Margin(6, 4)
		}
		a.pauseMenuItem(c, d, func() { open = false })
		if a.canRemove() {
			if menuItem(c, locales.RemovePhoneMenu(), p.red).Clicked() {
				open = false
				a.askRemove(d)
			}
		}
	})
	a.more[id] = open
}

// saveScreenshot writes the mirror's last picture to the Desktop (or the
// home folder) as a PNG.
func (a *app) saveScreenshot(m *mirror, name string) {
	pic, turn := m.lastPicture()
	if pic == nil {
		a.toasts = append(a.toasts, locales.PhoneNoPicture())
		return
	}
	dir, err := mygo.App.Path(mygo.PathDesktop)
	if err != nil || dir == "" {
		dir, _ = os.UserHomeDir()
	}
	now := time.Now()
	path := filepath.Join(dir, locales.PhoneScreenshotName(name, now.Format("2006-01-02"), now.Format("15.04.05"))+".png")
	go func() {
		img := turnRGBA(pic.RGBA(), turn)
		f, err := os.Create(path)
		if err == nil {
			err = png.Encode(f, img)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		a.post(func() {
			if err != nil {
				a.toasts = append(a.toasts, locales.PhoneScreenshotFailed(err.Error()))
			} else {
				a.toasts = append(a.toasts, locales.PhoneScreenshotSaved(filepath.Base(path)))
			}
		})
	}()
}

// The tiles of All phones: at least tileMinWidth wide, for the phone and
// its padding, and at most tileMaxWidth, tileGap apart, in a scroll view
// with gridPad of padding at its sides. The phone sits in a slot
// tileSlotHeight high, a portrait phone's height.
const (
	tileSlotHeight = 352
	tileMinWidth   = 200
	tileMaxWidth   = 232
	tileGap        = 16
	gridPad        = 16
	gridTop        = sp6
)

// tileColumns returns how many columns the tiles of phones phones take
// in room DIPs, and how wide the tiles are: as many as fit, or one per
// phone when they all fit in a row, sharing the room up to tileMaxWidth
// each.
func tileColumns(room float32, phones int) (n int, width float32) {
	n = max(1, min(int((room+tileGap)/(tileMinWidth+tileGap)), phones))
	width = min((room-float32(n-1)*tileGap)/float32(n), tileMaxWidth)
	return n, max(width, tileMinWidth)
}

// allPhonesGrid shows a tile per phone, live or with its last picture
// (tileStill); a click selects it. The
// tiles sit in columns centered in the room between the sidebar and the
// right panel, a last row that is not full starting in the first column,
// and scroll under the page's header when there are more than the window
// shows. Phones that all fit in one row are centered in it.
func (a *app) allPhonesGrid(c *mg.Context) {
	// The room is known from the window, as for the phone's mirror: the
	// sidebar and the right panel have fixed widths.
	w, _ := c.Size()
	cols, width := tileColumns(w-sidebarWidth-rightPanelWidth-2*gridPad, len(a.devices))
	tracks := make([]mg.Track, cols)
	for i := range tracks {
		tracks[i] = mg.Fixed(width)
	}
	mg.Scroll(c).Grow(1).MinWidth(0).Padding(gridTop, gridPad, gridPad, gridPad).Label(locales.PhoneAllPhones()).
		Children(func() { a.phoneTiles(c, tracks, width) })
}

// phoneTiles are the tiles of All phones, one per phone, in the grid's
// columns, width DIPs wide.
func (a *app) phoneTiles(c *mg.Context, cols []mg.Track, width float32) {
	p := pal(c)
	mg.Grid(c).ColumnTracks(cols...).Gap(tileGap).Justify(mg.Center).Children(func() {
		for _, d := range a.devices {
			d := d
			t := a.taskOf(d.Info.ID)
			asks := d.Status == gateway.DeviceNeedsYou || d.Status == gateway.DeviceNeedsSetup || a.isLocked(d)
			offline := d.Status == gateway.DeviceOffline || d.Info.State == driver.StateOffline
			// Connected, waiting for a step: not open, so nothing to mirror.
			setup := !offline && d.Info.State != driver.StateOnline
			tile := card(c, mg.Box(c.Key("tile-"+d.Info.ID)))
			hovered := tile.Hovered()
			switch {
			case asks:
				// A soft amber halo, not a heavy frame.
				tile.Border(1, p.amberEdge).Shadow(0, 0, 0, 3, p.amberEdge.Alpha(0.16))
			case hovered:
				tile.Border(1, p.line2)
			}
			if offline {
				tile.Background(p.group)
			}
			if a.hasPhoneMenu() {
				tile.ContextMenu(func(m *mg.Menu) { a.phoneMenu(m, d) })
			}
			tile.Children(func() {
				sel := mg.ButtonBase(c).Column().AlignItems(mg.Stretch).Justify(mg.Start).FillWidth().Padding(14).Gap(sp3).Radius(rCard).Label(locales.PhoneShow(d.Info.Name))
				if sel.Clicked() {
					a.selectPhone(d.Info.ID)
				}
				sel.Children(func() {
					// The phone takes its screen's shape, turned with it,
					// in a slot of the same height in every tile, so the
					// rows stay even (bezel.go).
					kind := bezelOf(d)
					slotW := max(168, width-28) // the tile's padding
					slot := mg.Box(c).Size(slotW, tileSlotHeight).Margin(0, mg.Auto).Shrink(0).Center()
					switch {
					case offline, setup:
						pw, ph, sw, sh := fitBezel(kind.defaultAspect(), slotW, tileSlotHeight, 2)
						radius, _ := bezelRadius(kind, sw, sh, 2)
						slot.Children(func() {
							mini := mg.Box(c).Size(pw, ph).Shrink(0).Radius(radius).Border(2, p.line2).Background(p.bg3).Center()
							if offline {
								mini.Children(func() { mg.Icon(c, icWifiOff).FontSize(26).TextColor(p.ink3) })
							} else {
								mini.Children(func() { mg.Icon(c, icAlert).FontSize(26).TextColor(p.amberDot) })
							}
						})
					default:
						var m *mirror
						bgTask := false
						if a.tileStill(d, t) {
							m = a.stillFor(d.Info.ID)
						} else {
							m, bgTask = a.screenFor(d, t, true)
						}
						pw, ph, sw, sh := fitBezel(m.aspect(d), slotW, tileSlotHeight, 5)
						radius, inner := bezelRadius(kind, sw, sh, 5)
						slot.Children(func() {
							mg.Box(c.Key("mini-"+d.Info.ID)).Size(pw, ph).Shrink(0).Radius(radius).Background(p.phone).Padding(5).Children(func() {
								shown := t
								if bgTask && !m.agent {
									shown = nil
								}
								a.mirrorView(c, m, d, shown, inner, true)
							})
						})
					}
					mg.Column(c).Gap(2).Children(func() {
						mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
							statusDot(c, d)
							name := mg.Text(c, d.Info.Name).FontWeight(fwSemibold).SingleLine()
							if offline {
								name.TextColor(p.ink2)
							}
							a.tileBattery(c, d)
						})
						mg.Text(c, statusLine(d)).FontSize(fsCaption).TextColor(p.ink2).Padding(0, 0, 0, 16).SingleLine()
					})
				})
				run := t != nil && running(t.State)
				_, handoff := a.handoffFor(d.Info.ID)
				inControl := d.Status == gateway.DeviceInControl || (handoff && !a.isLocked(d))
				if (run || inControl) && !offline && !setup {
					act := mg.Row(c).Absolute().Left(0).Right(0).Top(150).Justify(mg.Center).Gap(6)
					act.IgnoreInput(!hovered).Opacity(act.Animate("show", b2f(hovered), 150*time.Millisecond))
					act.Children(func() {
						switch {
						case inControl:
							if button(c, locales.PhoneHandBack(), btnOpt{small: true, dark: true}).Clicked() {
								a.handBack(d.Info.ID)
							}
						case !a.isLocked(d):
							if button(c, locales.PhoneTakeOver(), btnOpt{small: true, dark: true}).Clicked() {
								a.takeOver(d.Info.ID)
							}
						}
						if run && button(c, locales.TasksStop(), btnOpt{small: true, kind: btnRed, dark: true}).Clicked() {
							a.stop(d.Info.ID)
						}
					})
				}
				a.tileMore(c, d, hovered)
			})
		}
	})
}

// tileStill reports whether a phone's tile in All phones shows the
// phone's last picture instead of its live screen: an iPhone that no task
// runs on and the person does not hold. While a view of an iPhone's
// display stream is open, iOS shows screen sharing and never locks the
// phone by itself, so All phones, Pinky's home page, would keep an idle
// iPhone awake for as long as it shows. An Android phone's tile stays
// live: its stream (scrcpy, which does not turn the screen on) costs the
// phone nothing, and the phone sleeps and locks as usual.
func (a *app) tileStill(d gateway.DeviceView, t *gateway.TaskView) bool {
	if d.Info.Platform != driver.IOS || d.Status == gateway.DeviceInControl {
		return false
	}
	if t != nil && running(t.State) {
		return false
	}
	_, handoff := a.handoffFor(d.Info.ID)
	return !handoff
}

// tileMore is the ⋯ button on a phone's tile in All phones, which shows
// while the pointer is on the tile; its menu removes the phone.
func (a *app) tileMore(c *mg.Context, d gateway.DeviceView, hovered bool) {
	if !a.canRemove() {
		return
	}
	p := pal(c)
	key := "tile:" + d.Info.ID
	open := a.more[key]
	label := locales.RemovePhoneTileMore(d.Info.Name)
	b := mg.ButtonBase(c.Key("tile-more-"+d.Info.ID)).Absolute().Top(8).Right(8).Size(30, 30).Radius(9).Center().
		Background(p.panel).Border(1, p.line2).Label(label).Tooltip(label)
	show := hovered || open || b.Hovered() || b.Focused()
	b.Opacity(b.Animate("show", b2f(show), 150*time.Millisecond)).IgnoreInput(!show)
	if b.Clicked() {
		open = !open
	}
	col := p.ink2
	if b.Hovered() || open {
		col = p.ink
	}
	b.TextColor(col).Children(func() { mg.Icon(c, icMore).FontSize(16) })
	mg.PopoverBase(c, b, &open, func(panel mg.Element) {
		menuPanel(c, panel).Width(220).Margin(6, 0, 0, 0)
		if menuItem(c, locales.RemovePhoneMenu(), p.red).Clicked() {
			open = false
			a.askRemove(d)
		}
	})
	a.more[key] = open
}

func b2f(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

// stateTitle and stateDetail are the words of a phone page that shows a
// state instead of the phone, such as offline or waiting for a step: a
// title, and a centered line or two under it.
func stateTitle(c *mg.Context, text string) mg.Element {
	return mg.Text(c, text).FontSize(fsTitle3).FontWeight(fwSemibold).TextAlign(mg.Center).Margin(sp1, 0, 0, 0)
}

func stateDetail(c *mg.Context, text string) mg.Element {
	return mg.Text(c, text).FontSize(fsBody).LineHeight(1.5).TextColor(pal(c).ink2).TextAlign(mg.Center).MaxWidth(380)
}
