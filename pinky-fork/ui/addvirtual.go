package ui

import (
	"context"
	"slices"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Add phone › Virtual phone: the iOS Simulators and Android emulators on
// this computer (gateway.VirtualPhones). The person starts one here, and
// Pinky uses it as a phone once it runs. Agents have no tool for this.

// virtualLauncher is a gateway that lists, starts and shuts down virtual
// phones (gateway.VirtualPhones).
type virtualLauncher interface {
	VirtualPhones(ctx context.Context) ([]driver.VirtualPhones, error)
	StartVirtual(ctx context.Context, platform driver.Platform, id string) error
	StopVirtual(ctx context.Context, platform driver.Platform, id string) error
}

// virtState is the Virtual phone step's.
type virtState struct {
	list   []driver.VirtualPhones
	err    string    // why the last listing failed
	at     time.Time // when the listing shown began
	busy   bool
	loaded bool
	acts   map[string]*virtAct      // by virtKey
	all    map[driver.Platform]bool // Show all
}

// virtAct is a Start or Shut down the person clicked.
type virtAct struct {
	op   string    // "start" or "stop"
	done time.Time // when the gateway answered; zero while it works
	err  string
}

// virtualRows is how many virtual phones of a platform show before Show
// all.
const virtualRows = 6

// virtualBoot is how long a virtual phone that started shows Starting…
// while it runs but Pinky does not use it yet, as an emulator booting.
const virtualBoot = 3 * time.Minute

func virtKey(pf driver.Platform, id string) string { return string(pf) + "/" + id }

// launcher returns the gateway's virtual phones, nil when it has none.
func (a *app) launcher() virtualLauncher {
	l, _ := a.api.(virtualLauncher)
	return l
}

// pollVirtual lists the virtual phones off the UI thread, every 2 s while
// the step shows.
func (a *app) pollVirtual(c *mg.Context, l virtualLauncher) {
	st := &a.virt
	if !st.busy && time.Since(st.at) >= 2*time.Second {
		st.busy = true
		asked := time.Now()
		go func() {
			ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
			list, err := l.VirtualPhones(ctx)
			cancel()
			a.post(func() {
				st.busy, st.at, st.loaded = false, asked, true
				if err != nil {
					st.err = err.Error()
					return
				}
				st.list, st.err = list, ""
			})
		}()
	}
	c.After(2 * time.Second)
}

func (a *app) addVirtual(c *mg.Context) {
	p := pal(c)
	st := &a.virt
	detail := locales.AddVirtualDetailPC()
	if guideOS == "darwin" {
		detail = locales.AddVirtualDetailMac()
	}
	heading(c, locales.AddVirtualTitle(), detail)
	l := a.launcher()
	if l == nil {
		return
	}
	a.pollVirtual(c, l)
	switch {
	case !st.loaded:
		statusBox(c, func() {
			spinner(c)
			mg.Text(c, locales.AddVirtualLooking())
		})
		return
	case len(st.list) == 0 && st.err != "":
		statusBox(c, func() {
			mg.Icon(c, icAlert).FontSize(16).TextColor(p.amberDot).Shrink(0)
			mg.Text(c, locales.AddVirtualListFailed(st.err)).Grow(1).MinWidth(0)
		})
		return
	}
	list := slices.Clone(st.list)
	slices.SortStableFunc(list, func(x, y driver.VirtualPhones) int {
		if (x.Platform == driver.IOS) != (y.Platform == driver.IOS) {
			if x.Platform == driver.IOS {
				return -1
			}
			return 1
		}
		return 0
	})
	for _, vp := range list {
		a.virtualSection(c.Key("virt-"+string(vp.Platform)), vp)
	}
}

// virtualSection is one platform's virtual phones, or what the computer
// lacks for them.
func (a *app) virtualSection(c *mg.Context, vp driver.VirtualPhones) {
	p := pal(c)
	st := &a.virt
	title := locales.AddVirtualAndroid()
	if vp.Platform == driver.IOS {
		title = locales.AddVirtualIOS()
	}
	mg.Column(c).Gap(8).Label(title).Children(func() {
		sectionLabel(c, title).Margin(0)
		if msg := virtualMissing(vp); msg != "" {
			mg.Row(c).Gap(10).Padding(sp3, 14).Radius(rControl).Background(p.bg2).AlignItems(mg.Start).Role(mg.RoleStatus).Children(func() {
				mg.Icon(c, icAlert).FontSize(16).TextColor(p.amberDot).Shrink(0).Margin(1, 0, 0, 0)
				mg.Text(c, msg).FontSize(fsBody).Grow(1).MinWidth(0)
			})
			return
		}
		phones := vp.Phones
		more := len(phones) > virtualRows && !st.all[vp.Platform]
		if more {
			phones = phones[:virtualRows]
		}
		mg.Column(c).Radius(rCard).Background(p.group).Border(1, p.line).Clip().Children(func() {
			for i, v := range phones {
				a.virtualRow(c.Key(v.ID), vp.Platform, v, i == 0)
			}
		})
		if more && linkButton(c, locales.AddVirtualShowAll(len(vp.Phones))).Clicked() {
			if st.all == nil {
				st.all = map[driver.Platform]bool{}
			}
			st.all[vp.Platform] = true
		}
		if vp.Platform == driver.IOS {
			mg.Text(c, locales.AddVirtualHelperNote()).FontSize(fsSmall).TextColor(p.ink2)
		}
	})
}

// virtualMissing puts into words why a platform has no virtual phones to
// show; "" when it has some.
func virtualMissing(vp driver.VirtualPhones) string {
	switch vp.Missing {
	case "":
	case "xcode":
		return locales.AddVirtualNoXcode()
	case "android_sdk":
		return locales.AddVirtualNoSDK()
	case "emulator":
		return locales.AddVirtualNoEmulator(vp.Where)
	default:
		return vp.Detail
	}
	switch {
	case len(vp.Phones) > 0:
		return ""
	case vp.Detail != "":
		return locales.AddVirtualListFailed(vp.Detail)
	case vp.Platform == driver.IOS:
		return locales.AddVirtualNoSimulators()
	default:
		return locales.AddVirtualNoAVDs()
	}
}

// virtualRow is one virtual phone: its name, what it is, and Start, Use,
// Open or Shut down.
func (a *app) virtualRow(c *mg.Context, pf driver.Platform, v driver.VirtualPhone, first bool) {
	p := pal(c)
	st := &a.virt
	act := st.acts[virtKey(pf, v.ID)]
	inPinky := false
	if v.DeviceID != "" {
		if d := a.device(v.DeviceID); d != nil && d.Info.State != driver.StateOffline {
			inPinky = true
		}
	}
	// An act waits until a listing that began after the gateway answered
	// shows what it did.
	waiting := act != nil && act.err == "" && (act.done.IsZero() || !st.at.After(act.done))
	started := act != nil && act.op == "start" && act.err == ""
	starting := v.Starting || started && waiting ||
		started && v.Running && !inPinky && time.Since(act.done) < virtualBoot
	stopping := act != nil && act.op == "stop" && waiting

	row := mg.Row(c).Gap(sp3).Padding(10, sp4).MinHeight(52).AlignItems(mg.Center).Role(mg.RoleListItem).Label(v.Name)
	if !first {
		// The line between rows is inset from both sides, as a group's.
		row.DrawOver(func(pt *mg.Painter, r mg.Rect) {
			pt.Fill(mg.Rect{X: r.X + sp4, Y: r.Y, W: r.W - 2*sp4, H: 1}, p.line, 0)
		})
	}
	row.Children(func() {
		ic := icAndroid
		if pf == driver.IOS {
			ic = icIphone
		}
		mg.Icon(c, ic).FontSize(20).TextColor(p.ink2).Shrink(0)
		mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			mg.Text(c, v.Name).FontWeight(fwSemibold)
			mg.Row(c).Gap(6).AlignItems(mg.Center).Children(func() {
				if v.Running {
					dot(c, p.green, false)
				}
				mg.Text(c, virtualLine(pf, v)).FontSize(fsSmall).TextColor(p.ink2)
			})
			switch {
			case v.Unavailable != "" && !v.Running:
				mg.Text(c, locales.AddVirtualCantStart(v.Unavailable)).FontSize(fsSmall).TextColor(p.amber)
			case act != nil && act.err != "":
				mg.Text(c, act.err).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus)
			case v.Failed != "" && !starting:
				mg.Text(c, locales.AddVirtualStartFailed(v.Name, v.Failed)).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus)
			}
		})
		mg.Row(c).Gap(10).AlignItems(mg.Center).Shrink(0).Children(func() {
			switch {
			case stopping:
				spinner(c)
			case starting:
				spinner(c)
				mg.Text(c, locales.AddVirtualStarting()).FontSize(fsSmall).TextColor(p.ink2)
			case inPinky:
				if quietButton(c, locales.AddVirtualShutDown()).Clicked() {
					a.virtualAct(pf, v, "stop")
				}
				if button(c, locales.AddVirtualOpen(), btnOpt{kind: btnPrimary, small: true}).Clicked() {
					a.selectPhone(v.DeviceID)
					a.add.step = ""
				}
			case v.Running:
				if quietButton(c, locales.AddVirtualShutDown()).Clicked() {
					a.virtualAct(pf, v, "stop")
				}
				if button(c, locales.AddVirtualUse(), btnOpt{kind: btnPrimary, small: true}).Clicked() {
					a.virtualAct(pf, v, "start")
				}
			case v.Unavailable == "":
				if button(c, locales.AddVirtualStart(), btnOpt{small: true}).Clicked() {
					a.virtualAct(pf, v, "start")
				}
			}
		})
	})
}

// virtualLine is what a virtual phone is: "iPhone 17 · iOS 26.5 ·
// Running".
func virtualLine(pf driver.Platform, v driver.VirtualPhone) string {
	sys := "Android"
	if pf == driver.IOS {
		sys = "iOS"
	}
	s := v.Model
	if v.OSVersion != "" {
		if s != "" {
			s += " · "
		}
		s += sys + " " + v.OSVersion
	}
	if v.Running {
		if s != "" {
			s += " · "
		}
		s += locales.AddVirtualRunning()
	}
	return s
}

// virtualAct starts (or uses) or shuts down a virtual phone off the UI
// thread, with what failed under its row.
func (a *app) virtualAct(pf driver.Platform, v driver.VirtualPhone, op string) {
	l := a.launcher()
	if l == nil {
		return
	}
	st := &a.virt
	if st.acts == nil {
		st.acts = map[string]*virtAct{}
	}
	act := &virtAct{op: op}
	st.acts[virtKey(pf, v.ID)] = act
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
		var err error
		if op == "stop" {
			err = l.StopVirtual(ctx, pf, v.ID)
		} else {
			err = l.StartVirtual(ctx, pf, v.ID)
		}
		cancel()
		a.post(func() {
			act.done = time.Now()
			st.at = time.Time{} // list them again now
			switch {
			case err == nil:
			case op == "stop":
				act.err = locales.AddVirtualStopFailed(v.Name, err.Error())
			default:
				act.err = locales.AddVirtualStartFailed(v.Name, err.Error())
			}
		})
	}()
}

// canStartVirtual reports whether a phone's page can start the virtual
// phone it is.
func (a *app) canStartVirtual(d gateway.DeviceView) bool {
	return d.Info.Virtual && a.launcher() != nil && (d.Info.VirtualID != "" || d.Info.Platform == driver.IOS)
}

// startVirtualPhone starts the virtual phone a phone's page shows, or has
// a booted Simulator's helper built again.
func (a *app) startVirtualPhone(d gateway.DeviceView) {
	id := d.Info.VirtualID
	if id == "" {
		id = d.Info.ID // a Simulator's UDID
	}
	a.virtualAct(d.Info.Platform, driver.VirtualPhone{ID: id, Name: d.Info.Name}, "start")
}

// startVirtualButton is Start on the page of a virtual phone that is not
// running, then Starting… until it runs, virtualBoot passes or a listing
// says it quit while it started.
func (a *app) startVirtualButton(c *mg.Context, d gateway.DeviceView) {
	p := pal(c)
	id := d.Info.VirtualID
	if id == "" {
		id = d.Info.ID
	}
	act := a.virt.acts[virtKey(d.Info.Platform, id)]
	starting := act != nil && act.op == "start" && act.err == "" && (act.done.IsZero() || time.Since(act.done) < virtualBoot)
	failed := ""
	if v, ok := a.listedVirtual(d.Info.Platform, id); ok && act != nil && !act.done.IsZero() && a.virt.at.After(act.done) && !v.Starting && !v.Running {
		starting, failed = false, v.Failed
	}
	mg.Column(c).AlignItems(mg.Center).Gap(8).Children(func() {
		if starting {
			mg.Row(c).Gap(8).AlignItems(mg.Center).Role(mg.RoleStatus).Children(func() {
				spinner(c)
				mg.Text(c, locales.AddVirtualStarting()).FontSize(fsSmall).TextColor(p.ink2)
			})
			if l := a.launcher(); l != nil {
				a.pollVirtual(c, l)
			}
			c.After(time.Second)
			return
		}
		if button(c, locales.AddVirtualStart(), btnOpt{kind: btnPrimary}).Clicked() {
			a.startVirtualPhone(d)
		}
		switch {
		case act != nil && act.err != "":
			mg.Text(c, act.err).FontSize(fsSmall).TextColor(p.red).TextAlign(mg.Center).Role(mg.RoleStatus)
		case failed != "":
			mg.Text(c, locales.AddVirtualStartFailed(d.Info.Name, failed)).FontSize(fsSmall).TextColor(p.red).TextAlign(mg.Center).Role(mg.RoleStatus)
		}
	})
}

// listedVirtual is the virtual phone as the last listing has it.
func (a *app) listedVirtual(pf driver.Platform, id string) (driver.VirtualPhone, bool) {
	for _, vp := range a.virt.list {
		if vp.Platform != pf {
			continue
		}
		for _, v := range vp.Phones {
			if v.ID == id {
				return v, true
			}
		}
	}
	return driver.VirtualPhone{}, false
}
