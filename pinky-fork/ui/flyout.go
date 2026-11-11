package ui

import (
	"fmt"
	"log"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/assets"
	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
)

// newTray puts Pinky's icon in the menu bar (the notification area on
// Windows and Linux). A click opens the flyout; on Linux, whose tray icons
// report no clicks, its menu does.
func (a *app) newTray() {
	img, template := assets.TrayColor, false
	if runtime.GOOS == "darwin" {
		img, template = assets.TrayTemplate, true
	}
	t, err := mygo.NewTray(mygo.TrayOptions{ID: "pinky", Icon: img, IconIsTemplate: template, ToolTip: "Pinky"})
	if err != nil {
		log.Printf("pinky: tray: %v", err)
		return
	}
	a.tray = t
	if runtime.GOOS == "linux" {
		t.SetMenu(a.trayMenu())
		mygo.App.OnLocaleChanged(func(string) { t.SetMenu(a.trayMenu()) })
	} else {
		t.OnClick(a.toggleFlyout)
		t.OnRightClick(func() {
			if a.flyout != nil {
				a.flyout.Close()
			}
			t.SetMenu(a.trayMenu())
			defer t.SetMenu(nil)
			t.PopUpMenu()
		})
	}
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { t.Destroy() })
}

// trayMenu is the tray icon's menu, made anew each time it shows so its
// labels follow the language.
func (a *app) trayMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: locales.FlyoutOpenPinky(), Click: func(*mygo.MenuItem, *mygo.Window) { a.showMain() }},
		{Label: locales.FlyoutShowPhones(), Click: func(*mygo.MenuItem, *mygo.Window) { a.toggleFlyout() }},
		mygo.Separator(),
		{Label: locales.FlyoutMenuStopAll(), Click: func(*mygo.MenuItem, *mygo.Window) { a.stopAll() }},
		mygo.Separator(),
		{Label: locales.TelSendFeedbackMenu(), Hidden: a.tel() == nil, Click: func(*mygo.MenuItem, *mygo.Window) { a.openFeedback() }},
		{Role: mygo.RoleQuit},
	})
}

// updateTray shows how many things wait for the person next to the icon.
func (a *app) updateTray() {
	if a.tray == nil {
		return
	}
	n := len(a.needs)
	tip := "Pinky"
	title := ""
	if n > 0 {
		tip = locales.FlyoutTrayTooltip(n)
		title = fmt.Sprint(n)
	}
	a.tray.SetToolTip(tip)
	if runtime.GOOS == "darwin" {
		a.tray.SetTitle(title)
	}
}

// flyoutHeight sizes the flyout to its rows, as the prototype's 320 × 304
// fits one need and three phones.
func (a *app) flyoutHeight() int {
	h := 6 + 8 // padding
	if len(a.needs) > 0 {
		h += 26 + 36*min(len(a.needs), 4)
	}
	h += 26 + 4 + 36*max(1, len(a.devices))
	h += 13 + 36 // Open Pinky
	if len(a.devices) > 0 {
		h += 36 // Stop all
	}
	return h
}

func (a *app) toggleFlyout() {
	if a.flyout != nil {
		a.flyout.Close()
		return
	}
	a.flyout = mygo.NewFlyout(mygo.FlyoutOptions{
		Tray: a.tray, Placement: mygo.PlacementBottom, Gap: 4,
		Width: 320, Height: a.flyoutHeight(), Focusable: true, Shadow: true,
		Content: mg.View(a.flyoutView),
	})
	a.flyout.OnClosed(func() { a.flyout = nil })
}

// flyoutView is the menu bar flyout: what needs the person, the phones,
// Open Pinky and Stop all.
func (a *app) flyoutView(c *mg.Context) {
	p := applyTheme(c)
	if a.flyout != nil {
		if w, h := a.flyout.Size(); w > 0 && h != a.flyoutHeight() {
			a.flyout.SetSize(320, a.flyoutHeight())
		}
	}
	if c.Shortcut(0, mg.KeyEscape) && a.flyout != nil {
		a.flyout.Close()
	}
	sec := func(text string) {
		mg.Text(c, text).FontSize(fsCaption).FontWeight(fwSemibold).TextColor(p.ink3).Padding(6, 10, 4, 10)
	}
	mg.Column(c).Fill().Background(p.panel).Padding(6, 8, 8, 8).Children(func() {
		if len(a.needs) > 0 {
			sec(locales.NeedsSection())
			for i, n := range a.needs {
				if i == 4 {
					break
				}
				n := n
				row := flyRow(c.Key("n-" + n.ID)).Background(p.amberTint)
				if row.Clicked() {
					if a.flyout != nil {
						a.flyout.Close()
					}
					if inPanel(n) {
						a.showPanel(true)
					} else {
						a.openNeed(n.ID)
					}
				}
				row.Label(locales.FlyoutNeedLabel(n.DeviceName, needTitle(n))).Children(func() {
					dot(c, p.amberDot, false)
					mg.RichText(c,
						mg.Span{Text: n.DeviceName, Weight: 600},
						mg.Span{Text: " · " + needShort(n)},
					).Grow(1).MinWidth(0).SingleLine()
					label := locales.FlyoutReview()
					if n.Kind == gateway.NeedUnlock {
						label = locales.FlyoutUnlock()
					}
					mg.Text(c, label).FontWeight(fwMedium).TextColor(p.accentInk)
				})
			}
		}
		sec(locales.FlyoutPhones())
		if len(a.devices) == 0 {
			mg.Text(c, locales.FlyoutNoPhones()).FontSize(fsSmall).TextColor(p.ink2).Padding(8, 10)
		}
		for _, d := range a.devices {
			d := d
			row := flyRow(c.Key("d-" + d.Info.ID))
			if d.Status != gateway.DeviceOffline && row.Clicked() {
				if a.flyout != nil {
					a.flyout.Close()
				}
				a.selectPhone(d.Info.ID)
				a.showMain()
			}
			row.Label(locales.FlyoutPhoneLabel(d.Info.Name, statusLine(d))).Children(func() {
				statusDot(c, d)
				name := mg.Text(c, d.Info.Name).FontWeight(fwSemibold).Shrink(0)
				if d.Status == gateway.DeviceOffline {
					name.TextColor(p.ink2)
				}
				mg.Text(c, statusLine(d)).FontSize(fsSmall).TextColor(p.ink2).Grow(1).MinWidth(0).SingleLine()
			})
		}
		mg.Spacer(c)
		mg.Box(c).Height(1).Background(p.line).Margin(6, 6)
		open := flyRow(c).Label(locales.FlyoutOpenPinky())
		if open.Clicked() {
			if a.flyout != nil {
				a.flyout.Close()
			}
			a.showMain()
		}
		open.Children(func() { mg.Text(c, locales.FlyoutOpenPinky()) })
		if len(a.devices) == 0 {
			return // nothing to stop yet, as in the sidebar
		}
		stop := flyRow(c).Label(locales.FlyoutStopAll())
		if stop.Clicked() {
			a.stopAll()
		}
		stop.Children(func() {
			mg.Text(c, locales.FlyoutStopAll()).TextColor(p.red).Grow(1)
			mg.Text(c, shortcutLabel()).FontSize(fsSmall).TextColor(p.ink3)
		})
	})
}

func flyRow(c *mg.Context) mg.Element {
	p := pal(c)
	row := mg.ButtonBase(c).MinHeight(36).Padding(6, 10).Radius(8).Gap(10).AlignItems(mg.Center).Justify(mg.Start).FillWidth()
	if row.Hovered() {
		row.Background(p.bg3)
	}
	return row
}

// needShort is a need in a few words: "Pay $12.80".
func needShort(n gateway.NeedView) string {
	t := needTitle(n)
	if len(t) > 0 && t[len(t)-1] == '?' {
		t = t[:len(t)-1]
	}
	t = strings.TrimSuffix(t, "\uff1f") // a translated title's full-width question mark
	switch n.Kind {
	case gateway.NeedUnlock:
		return locales.FlyoutLocked()
	case gateway.NeedSetup:
		return locales.FlyoutPasscode()
	}
	return t
}
