package ui

import (
	"context"
	"strconv"
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// A phone's battery and storage. Its page's header shows the battery
// beside the page's name; a click opens what Pinky knows of the phone: its
// model and system, the battery (charging or not, an Android phone's
// temperature) and the storage free. Each tile in All phones shows the
// battery too. Only phones that are connected show them: one that is
// offline, paused or waits for a step shows none.
//
// The client asks the gateway (gateway.PhoneVitals) about once a minute
// while a page that shows them builds, which MyGo does not do while the
// window is hidden or covered. The gateway reads the phone at most once a
// minute for the battery and every ten minutes for the storage, never a
// phone that is paused, and without waking it (driver.VitalsReader).

// vitalsSource is a gateway that reads phones' battery and storage.
type vitalsSource interface {
	PhoneVitals(ctx context.Context, deviceID string) (gateway.PhoneVitals, error)
}

// phoneVitalsState is the client's part of it, by phone.
type phoneVitalsState struct {
	got     map[string]gateway.PhoneVitals
	at      map[string]time.Time
	loading map[string]bool
}

func (s *phoneVitalsState) init() {
	if s.got != nil {
		return
	}
	s.got, s.at, s.loading = map[string]gateway.PhoneVitals{}, map[string]time.Time{}, map[string]bool{}
}

// vitalsEvery is how often a page that shows a phone's battery asks for it
// again.
const vitalsEvery = time.Minute

// lowStorage is the share of the storage free below which the storage bar
// turns amber.
const lowStorage = 0.1

// vitalsShown reports whether a phone shows its battery and storage: it is
// connected and open.
func vitalsShown(d gateway.DeviceView) bool {
	return !d.Paused && d.Status != gateway.DeviceOffline && d.Status != gateway.DeviceConnecting &&
		d.Status != gateway.DeviceNeedsSetup && d.Info.State == driver.StateOnline
}

// vitalsOf returns what the gateway last said of a phone's battery and
// storage, and asks again off the main thread when that is old. A phone
// that does not show them returns nothing and is not asked about.
func (a *app) vitalsOf(c *mg.Context, d gateway.DeviceView) gateway.PhoneVitals {
	vs, ok := a.api.(vitalsSource)
	if !ok {
		return gateway.PhoneVitals{}
	}
	s := &a.vit
	s.init()
	id := d.Info.ID
	if !vitalsShown(d) {
		// Forget them, so the phone's page asks at once when it is back.
		delete(s.got, id)
		delete(s.at, id)
		return gateway.PhoneVitals{}
	}
	if !s.loading[id] && time.Since(s.at[id]) >= vitalsEvery {
		s.loading[id] = true
		go func() {
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
			defer cancel()
			v, err := vs.PhoneVitals(ctx, id)
			a.post(func() {
				s.loading[id], s.at[id] = false, time.Now()
				if err == nil {
					s.got[id] = v
				} else {
					delete(s.got, id) // quiet: the battery and storage just do not show
				}
			})
		}()
	}
	c.After(vitalsEvery)
	return s.got[id]
}

// phoneAbout is the button at the end of a phone's page header: its
// battery, or an info mark for a phone without one (an emulator), which
// opens what Pinky knows of the phone. Nothing shows while Pinky knows
// neither its battery nor its storage.
func (a *app) phoneAbout(c *mg.Context, d gateway.DeviceView) {
	v := a.vitalsOf(c, d)
	if v.Empty() {
		return
	}
	p := pal(c)
	id := d.Info.ID
	key := "about:" + id
	open := a.more[key]
	label := locales.PhoneAboutButton(d.Info.Name)
	b := mg.ButtonBase(c.Key("about-"+id)).Height(30).Padding(0, sp2).Radius(rControl).Gap(6).AlignItems(mg.Center).Shrink(0).
		Label(label).Tooltip(label)
	if b.Hovered() || open {
		b.Background(p.bg3)
	}
	if b.Clicked() {
		open = !open
	}
	b.Children(func() {
		if bt := v.Battery; bt != nil {
			batteryBadge(c, *bt, fsSmall)
		} else {
			mg.Icon(c, icInfo).FontSize(17).TextColor(p.ink2)
		}
	})
	mg.PopoverBase(c, b, &open, func(panel mg.Element) {
		menuPanel(c, panel).MinWidth(300).MaxWidth(420).Padding(sp4).Margin(6, 0, 0, 0).
			AttachTo(b, mg.AnchorBottomEnd, mg.AnchorTopEnd)
		phoneAboutPanel(c, d, v)
	})
	a.more[key] = open
}

// tileBattery is a phone's battery at the end of the row of its name in
// All phones.
func (a *app) tileBattery(c *mg.Context, d gateway.DeviceView) {
	b := a.vitalsOf(c, d).Battery
	if b == nil {
		return
	}
	mg.Spacer(c)
	batteryBadge(c, *b, fsCaption)
}

// phoneAboutPanel is what the header's button opens: the phone's model and
// system, then its battery and storage.
func phoneAboutPanel(c *mg.Context, d gateway.DeviceView, v gateway.PhoneVitals) {
	p := pal(c)
	model := d.Info.Model
	if model == "" {
		model = d.Info.Name
	}
	mg.Column(c).Gap(2).Children(func() {
		mg.Text(c, model).FontSize(fsHeadline).FontWeight(fwSemibold)
		if os := gateway.OSName(d.Info); os != "" {
			mg.Text(c, os).FontSize(fsSmall).TextColor(p.ink2)
		}
	})
	mg.Box(c).Height(1).Background(p.line).Margin(sp3, 0)
	mg.Grid(c).ColumnTracks(mg.FitContent(), mg.Fr(1)).Gap(sp3).AlignItems(mg.Center).Children(func() {
		if b := v.Battery; b != nil {
			mg.Text(c, locales.PhoneAboutBattery()).FontSize(fsSmall).TextColor(p.ink2)
			mg.Row(c).Gap(sp2).AlignItems(mg.Center).MinWidth(0).Children(func() {
				batteryGlyph(c, *b)
				mg.Text(c, batteryText(*b)).FontSize(fsSmall).FontFeatures("tnum")
			})
		}
		if s := v.Storage; s != nil {
			mg.Text(c, locales.PhoneAboutStorage()).FontSize(fsSmall).TextColor(p.ink2).AlignSelf(mg.Start)
			mg.Column(c).Gap(sp2).MinWidth(0).Children(func() {
				free, total := gateway.StorageText(s.Free), gateway.StorageText(s.Total)
				mg.Text(c, locales.PhoneAboutStorageFree(free, total)).FontSize(fsSmall).FontFeatures("tnum")
				storageBar(c, *s)
			})
		}
	})
}

// batteryText is the battery's line in the panel: "82% · Charging · 31 °C".
func batteryText(b driver.Battery) string {
	parts := []string{locales.PhoneAboutPercent(b.Level)}
	switch {
	case b.Full && b.Plugged:
		parts = append(parts, locales.PhoneAboutCharged())
	case b.Charging:
		parts = append(parts, locales.PhoneAboutCharging())
	case b.Plugged:
		parts = append(parts, locales.PhoneAboutPluggedIn())
	}
	if b.TempC > 0 {
		parts = append(parts, locales.PhoneAboutTemperature(strconv.FormatFloat(b.TempC, 'f', 0, 64)))
	}
	return strings.Join(parts, " · ")
}

// storageBar shows how much of the storage is used, amber when little is
// free.
func storageBar(c *mg.Context, s driver.Storage) {
	p := pal(c)
	used := float32(1)
	if s.Total > 0 {
		used = min(max(1-float32(s.Free)/float32(s.Total), 0), 1)
	}
	col := p.accent
	if s.Total > 0 && float64(s.Free) < lowStorage*float64(s.Total) {
		col = p.amberDot
	}
	bar := mg.Box(c).Height(5).Radius(3).Background(p.bg3)
	bar.Draw(func(pt *mg.Painter, r mg.Rect) {
		pt.Fill(mg.Rect{X: r.X, Y: r.Y, W: max(r.W*used, r.H), H: r.H}, col, r.H/2)
	})
}

// batteryBadge is the battery's level and a battery filled to it, as a
// computer's menu bar shows them.
func batteryBadge(c *mg.Context, b driver.Battery, size float32) {
	p := pal(c)
	mg.Row(c).Gap(sp1).AlignItems(mg.Center).Shrink(0).Label(locales.PhoneAboutBatteryLabel(b.Level)).Children(func() {
		mg.Text(c, locales.PhoneAboutPercent(b.Level)).FontSize(size).TextColor(p.ink2).FontFeatures("tnum")
		batteryGlyph(c, b)
	})
}

// batteryGlyph is a battery filled to its level: green while it charges,
// red at 20% and below off the charger, and a bolt beside it while it
// charges.
func batteryGlyph(c *mg.Context, b driver.Battery) {
	p := pal(c)
	fill := p.ink2
	switch {
	case b.Charging || b.Full && b.Plugged:
		fill = p.green
	case b.Level <= 20 && !b.Plugged:
		fill = p.red
	}
	const w, h = 22, 11
	level := float32(min(max(b.Level, 0), 100)) / 100
	mg.Row(c).Gap(1).AlignItems(mg.Center).Shrink(0).Children(func() {
		mg.Box(c).Size(w, h).Shrink(0).Radius(3.5).Border(1, p.ink3).Draw(func(pt *mg.Painter, r mg.Rect) {
			// Inside the border, 1.5 from it.
			iw := r.W - 5
			pt.Fill(mg.Rect{X: r.X + 2.5, Y: r.Y + 2.5, W: max(iw*level, 2), H: r.H - 5}, fill, 1.5)
		})
		mg.Box(c).Size(1.5, 4).Shrink(0).Radius(1).Background(p.ink3)
		if b.Charging {
			mg.Icon(c, icBolt).FontSize(12).TextColor(p.green).Margin(0, 0, 0, 2)
		}
	})
}

var (
	icInfo = icon(`<circle cx="12" cy="12" r="9"/><path d="M12 16v-5"/><path d="M12 8h.01"/>`)
	icBolt = filledIcon(`<path d="M13.2 2.5 5 13.6h5.6L9.8 21.5 18 10.4h-5.6z"/>`)
)
