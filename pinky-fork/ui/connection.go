package ui

import (
	"context"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// How Pinky connects to an iPhone, in Settings › Phones: automatically (by
// iOS version), through CoreDevice (nothing installed, iOS 27 and later),
// or through the WebDriverAgent helper. The gateway saves the choice per
// phone and reconnects it at once.

// connState is the connection choices' part of the client.
type connState struct {
	got     map[string]driver.Connections // by phone, as last read
	at      map[string]time.Time          // when it was read
	loading map[string]bool
	busy    map[string]bool   // a SetConnection is under way
	chosen  map[string]string // what the radio shows while busy
}

// connRefresh is how often Settings › Phones reads a phone's connections
// again while it shows: the helper may have been installed meanwhile.
const connRefresh = 15 * time.Second

// loadConnections reads a phone's connections off the main thread.
func (a *app) loadConnections(id string) {
	cc, ok := a.api.(connectionChooser)
	s := &a.conn
	if !ok || s.loading[id] {
		return
	}
	if s.got == nil {
		s.got, s.at, s.loading = map[string]driver.Connections{}, map[string]time.Time{}, map[string]bool{}
		s.busy, s.chosen = map[string]bool{}, map[string]string{}
	}
	s.loading[id] = true
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		defer cancel()
		got, err := cc.Connections(ctx, id)
		a.post(func() {
			s.loading[id], s.at[id] = false, time.Now()
			if err == nil {
				s.got[id] = got
			} else {
				delete(s.got, id)
			}
		})
	}()
}

// setConnection chooses how a phone is connected; the gateway reconnects it.
func (a *app) setConnection(d gateway.DeviceView, way string) {
	cc, ok := a.api.(connectionChooser)
	id, name := d.Info.ID, d.Info.Name
	s := &a.conn
	if !ok || s.busy[id] {
		return
	}
	s.busy[id], s.chosen[id] = true, way
	go func() {
		err := cc.SetConnection(id, way)
		a.post(func() {
			s.busy[id] = false
			s.at[id] = time.Time{} // read it again
			if err != nil {
				a.toasts = append(a.toasts, locales.ConnectionChangeFailed(name, err.Error()))
			} else {
				a.toasts = append(a.toasts, locales.ConnectionReconnecting(name))
			}
		})
	}()
}

// connectionTitle names a way.
func connectionTitle(way string) string {
	switch way {
	case "coredevice":
		return locales.ConnectionCoreDevice()
	case "wda":
		return locales.ConnectionWDA()
	}
	return locales.ConnectionAuto()
}

// connectionShort names the way a phone is reached now, for Automatic.
func connectionShort(backend string) string {
	switch backend {
	case "coredevice":
		return "CoreDevice"
	case "wda":
		return "WebDriverAgent"
	case "accessibility":
		return locales.ConnectionReadOnly()
	}
	return ""
}

func connectionDesc(way string) string {
	switch way {
	case "coredevice":
		return locales.ConnectionCoreDeviceDesc()
	case "wda":
		return locales.ConnectionWDADesc()
	}
	return locales.ConnectionAutoDesc()
}

// connectionReason says why a way does not work for a phone now.
func connectionReason(w driver.Connection, d gateway.DeviceView) string {
	switch w.Reason {
	case "os_version":
		if w.ID == "coredevice" {
			return locales.ConnectionNeedsIOS27(d.Info.OSVersion)
		}
		return locales.ConnectionIOSTooOld(d.Info.OSVersion)
	case "not_installed":
		return locales.ConnectionNotInstalled()
	case "offline":
		return locales.ConnectionNoAnswer()
	}
	return w.Detail
}

// connectionRows is the phone's connection choice.
func (a *app) connectionRows(c *mg.Context, d gateway.DeviceView) {
	if _, ok := a.api.(connectionChooser); !ok || d.Info.Platform != driver.IOS {
		return
	}
	id := d.Info.ID
	s := &a.conn
	if s.got == nil || (!s.loading[id] && !s.busy[id] && time.Since(s.at[id]) > connRefresh) {
		a.loadConnections(id)
	}
	got, ok := s.got[id]
	if !ok {
		return
	}
	p := pal(c)
	chosen := got.Chosen
	if s.busy[id] {
		chosen = s.chosen[id]
	}
	groupRow(c, mg.Column(c.Key("conn-"+id)).Gap(sp2), false).Children(func() {
		mg.Text(c, locales.ConnectionTitle()).FontWeight(fwMedium)
		mg.Column(c).Gap(sp2).Role(mg.RoleRadioGroup).Label(locales.ConnectionTitle()).Children(func() {
			for _, w := range got.Ways {
				w := w
				pick := chosen
				opt := mg.RadioBase(c.Key("conn-"+id+"-"+w.ID), &pick, w.ID).Gap(sp3).Padding(10, 14).Radius(rControl + 2).AlignItems(mg.Start).FillWidth().Label(connectionTitle(w.ID))
				on := chosen == w.ID
				border := p.line
				if on {
					border = p.accent
					opt.Shadow(0, 0, 0, 1, p.accent)
				} else if opt.Hovered() {
					border = p.line2
				}
				if opt.Changed() && pick != chosen && !s.busy[id] {
					a.setConnection(d, pick)
				}
				opt.Border(1, border).Background(p.panel).Children(func() {
					rb := mg.Box(c).Size(16, 16).Radius(8).Margin(2, 0, 0, 0).Shrink(0).Background(p.panel)
					if on {
						rb.Border(5, p.accentBtn)
					} else {
						rb.Border(1.5, p.line2)
					}
					mg.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
						title := connectionTitle(w.ID)
						if now := connectionShort(got.Current); w.ID == driver.ConnectionAuto && now != "" && got.Chosen == driver.ConnectionAuto {
							title = locales.ConnectionAutoNow(now)
						}
						mg.Text(c, title).FontWeight(fwMedium)
						mg.Text(c, connectionDesc(w.ID)).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
						if !w.Available {
							mg.Text(c, connectionReason(w, d)).FontSize(fsSmall).LineHeight(1.4).TextColor(p.amber)
						}
					})
				})
			}
		})
	})
}
