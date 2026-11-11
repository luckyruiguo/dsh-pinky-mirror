package ui

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Phones away from this computer's network, through Tailscale: the Add
// phone › Android step that connects to a phone on the tailnet, the Use
// away from home row of each Android phone in Settings › Phones with the
// dialog that turns it on, and keeping the computer awake while a phone
// is connected that way.

// tailnetter is a gateway that reaches phones through Tailscale
// (gateway.Tailnet and the rest).
type tailnetter interface {
	Tailnet(ctx context.Context) (driver.TailnetStatus, error)
	ConnectTailnet(ctx context.Context, addr string) (driver.Info, error)
	Away(ctx context.Context, deviceID string) (driver.Away, error)
	SetAway(ctx context.Context, deviceID string, on bool) (driver.Away, error)
}

// tailscaleDownload is where Tailscale's apps are.
const tailscaleDownload = "https://tailscale.com/download"

// tailnetState is the client's part of it.
type tailnetState struct {
	st   driver.TailnetStatus
	have bool
	at   time.Time
	busy bool
	err  string

	// Connecting a phone on the tailnet, by its address.
	connecting map[string]bool
	connErr    map[string]string // what went wrong, in the person's words
	refused    map[string]bool   // its adb does not listen there
	port       map[string]string // the Wireless debugging port typed

	// Use away from home, by phone.
	away        map[string]driver.Away
	awayAt      map[string]time.Time
	awayLoading map[string]bool
	awayBusy    map[string]bool
	awayErr     map[string]string

	// The dialog that turns it on.
	ask     string
	askName string
	askOpen bool
}

func (s *tailnetState) init() {
	if s.connecting != nil {
		return
	}
	s.connecting, s.connErr, s.refused, s.port = map[string]bool{}, map[string]string{}, map[string]bool{}, map[string]string{}
	s.away, s.awayAt, s.awayLoading = map[string]driver.Away{}, map[string]time.Time{}, map[string]bool{}
	s.awayBusy, s.awayErr = map[string]bool{}, map[string]string{}
}

// tailnetEvery is how often a page that shows the tailnet reads it again.
const tailnetEvery = 3 * time.Second

// tailnetStatus returns what the gateway last said about Tailscale, and
// reads it again off the main thread when it is old.
func (a *app) tailnetStatus(c *mg.Context) (driver.TailnetStatus, bool) {
	tn, ok := a.api.(tailnetter)
	s := &a.tn
	if !ok {
		return driver.TailnetStatus{}, false
	}
	s.init()
	if !s.busy && time.Since(s.at) >= tailnetEvery {
		s.busy = true
		go func() {
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
			defer cancel()
			st, err := tn.Tailnet(ctx)
			a.post(func() {
				s.busy, s.at = false, time.Now()
				if err != nil {
					s.err = err.Error()
					return
				}
				s.st, s.have, s.err = st, true, ""
			})
		}()
	}
	c.After(tailnetEvery)
	return s.st, s.have
}

// addTailnet is Add phone › Android › Connect through Tailscale.
func (a *app) addTailnet(c *mg.Context) {
	p := pal(c)
	heading(c, locales.TailnetTitle(), locales.TailnetDetail())
	st, have := a.tailnetStatus(c)
	if !have {
		if a.tn.err != "" {
			guideProblem(c, locales.TailnetReadFailed(), a.tn.err)
		} else {
			guideWaiting(c, locales.TailnetReading())
		}
		return
	}
	running := st.State == driver.TailnetRunning
	here, phone, connect := guideNow, guideLater, guideLater
	if running {
		here, phone = guideDone, guideNow
		if len(st.Phones) > 0 {
			phone, connect = guideDone, guideNow
		}
	}
	mg.Column(c).Gap(14).Role(mg.RoleList).Label(locales.TailnetStepsLabel()).Children(func() {
		guideRow(c, 1, locales.TailnetStepHere(), here, func() { a.tailnetHere(c, st) })
		if running {
			// Done shows what it is connected to.
			mg.Text(c, locales.TailnetHereRunning(st.Tailnet, st.Self)).FontSize(fsCaption).TextColor(p.ink3).Margin(-8, 0, 0, 36)
		}
		guideRow(c, 2, locales.TailnetStepPhone(), phone, func() {
			guideText(c, locales.TailnetPhoneSteps())
			guideNote(c, locales.AwayOneVPN())
			guideWaiting(c, locales.TailnetPhoneNone())
		})
		guideRow(c, 3, locales.TailnetStepConnect(), connect, func() {
			guideText(c, locales.TailnetConnectDetail())
			for _, ph := range st.Phones {
				a.tailnetPhoneRow(c, ph)
			}
		})
	})
	guideNote(c, locales.TailnetSafety())
}

// tailnetHere says what to do about Tailscale on this computer.
func (a *app) tailnetHere(c *mg.Context, st driver.TailnetStatus) {
	text, open, get := "", false, false
	switch st.State {
	case driver.TailnetNotInstalled:
		text, get = locales.TailnetHereNotInstalled(), true
	case driver.TailnetNeedsLogin:
		text, open = locales.TailnetHereNeedsLogin(), true
	case driver.TailnetStopped:
		text, open = locales.TailnetHereStopped(), true
	case driver.TailnetStarting:
		guideWaiting(c, locales.TailnetHereStarting())
		return
	default:
		text, open = locales.TailnetHereNotRunning(), true
	}
	if runtime.GOOS == "linux" && open {
		// No app to open: tailscaled runs as a service.
		text, open = locales.TailnetHereLinux("sudo tailscale up"), false
	}
	guideText(c, text)
	if st.Detail != "" && st.State != driver.TailnetNotInstalled {
		guideNote(c, st.Detail)
	}
	mg.Row(c).Gap(10).Children(func() {
		if get && button(c, locales.TailnetGet(), btnOpt{small: true, kind: btnPrimary}).Clicked() {
			openURL(c, tailscaleDownload, func(error) {})
		}
		if open && button(c, locales.TailnetOpen(), btnOpt{small: true, kind: btnPrimary}).Clicked() {
			openTailscale()
		}
	})
}

// openTailscale opens Tailscale's app, where the person signs in or turns
// it on.
func openTailscale() {
	path := "/Applications/Tailscale.app"
	if runtime.GOOS == "windows" {
		path = `C:\Program Files\Tailscale\tailscale-ipn.exe`
	}
	go mygo.Shell.OpenPath(path)
}

// tailnetPhoneRow is a phone on the tailnet, with what Pinky can do about
// it.
func (a *app) tailnetPhoneRow(c *mg.Context, ph driver.TailnetPhone) {
	p := pal(c)
	s := &a.tn
	known := a.device(ph.ID)
	name := ph.Name
	if known != nil {
		name = known.Info.Name
	}
	mg.Column(c.Key("tn-"+ph.Addr)).Gap(8).Padding(10, 12).Radius(10).Border(1, p.line).Children(func() {
		mg.Row(c).Gap(10).AlignItems(mg.Center).Children(func() {
			if ph.Online {
				dot(c, p.green, false)
			} else {
				hollowDot(c)
			}
			mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				mg.Text(c, name).FontWeight(fwSemibold)
				line := ph.Addr + " · " + locales.TailnetPhoneOffline()
				switch {
				case ph.Connected && ph.Relayed:
					line = ph.Addr + " · " + locales.TailnetPhoneRelayed()
				case ph.Connected:
					line = ph.Addr + " · " + locales.TailnetPhoneConnected()
				case ph.Online:
					line = ph.Addr + " · " + locales.TailnetPhoneOnline()
				}
				mg.Text(c, line).FontSize(fsSmall).TextColor(p.ink2)
			})
			switch {
			case s.connecting[ph.Addr]:
				spinner(c)
			case ph.Connected && known != nil:
				if button(c, locales.TailnetShow(), btnOpt{small: true}).Clicked() {
					a.selectPhone(known.Info.ID)
					a.add.step = ""
				}
			case ph.Online:
				if button(c, locales.TailnetConnect(), btnOpt{small: true, kind: btnPrimary}).Clicked() {
					a.connectTailnet(ph.Addr, name)
				}
			}
		})
		if msg := s.connErr[ph.Addr]; msg != "" {
			if s.refused[ph.Addr] {
				guideProblem(c, locales.TailnetNotListening(name), locales.TailnetNotListeningDetail())
				a.tailnetPortForm(c, ph, name)
			} else {
				guideProblem(c, msg, "")
			}
		}
	})
}

// tailnetPortForm connects at the port Wireless debugging shows, which a
// phone on Wi-Fi with it on takes through Tailscale too.
func (a *app) tailnetPortForm(c *mg.Context, ph driver.TailnetPhone, name string) {
	s := &a.tn
	port := s.port[ph.Addr]
	mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
		in := mg.TextInput(c.Key("tn-port-"+ph.Addr), &port).Placeholder("37123").Label(locales.TailnetPortField()).MaxWidth(120)
		s.port[ph.Addr] = port
		n, err := strconv.Atoi(strings.TrimSpace(port))
		ok := err == nil && n > 0 && n < 65536
		b := button(c, locales.TailnetConnect(), btnOpt{small: true}).Label(locales.TailnetConnectPort()).Disabled(!ok || s.connecting[ph.Addr])
		if (b.Clicked() || in.Submitted()) && ok && !s.connecting[ph.Addr] {
			a.connectTailnetAt(ph.Addr, ph.Addr+":"+strconv.Itoa(n), name)
		}
	})
}

// connectTailnet connects to a phone on the tailnet at the port Pinky sets
// phones up with.
func (a *app) connectTailnet(addr, name string) { a.connectTailnetAt(addr, addr, name) }

func (a *app) connectTailnetAt(key, addr, name string) {
	tn, ok := a.api.(tailnetter)
	s := &a.tn
	if !ok || s.connecting[key] {
		return
	}
	s.connecting[key], s.connErr[key], s.refused[key] = true, "", false
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
		defer cancel()
		info, err := tn.ConnectTailnet(ctx, addr)
		var devs []gateway.DeviceView
		if err == nil {
			devs = a.api.Devices()
		}
		a.post(func() {
			s.connecting[key] = false
			s.at = time.Time{} // read the tailnet again
			if err != nil {
				s.refused[key] = errors.Is(err, driver.ErrNotListening)
				s.connErr[key] = tailnetError(err, name)
				return
			}
			a.devices = devs
			if info.State == driver.StateUnauthorized {
				s.connErr[key] = locales.TailnetAllow(name)
				return
			}
			a.selectPhone(info.ID)
			a.add.step = ""
			a.toasts = append(a.toasts, locales.TailnetConnected(nonEmpty(info.Name, name)))
			if d := a.device(info.ID); d != nil {
				a.openCheckup(*d)
			}
			a.afterRefresh(false)
		})
	}()
}

// tailnetError says in plain words why connecting through Tailscale failed.
func tailnetError(err error, name string) string {
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, driver.ErrNotListening):
		return locales.TailnetNotListening(name)
	case errors.Is(err, driver.ErrPairNoNetwork):
		return locales.PairingErrNoNetwork()
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timed out") || strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "no route") || strings.Contains(msg, "unreachable"):
		return locales.TailnetUnreachable(name)
	}
	return locales.TailnetConnectFailed(name, err.Error())
}

// awayEvery is how often Settings › Phones reads a phone's Use away from
// home again while it shows.
const awayEvery = 10 * time.Second

// awayOf returns what the gateway last said about a phone's Use away from
// home, and reads it again off the main thread when it is old.
func (a *app) awayOf(c *mg.Context, id string) (driver.Away, bool) {
	tn, ok := a.api.(tailnetter)
	s := &a.tn
	if !ok {
		return driver.Away{}, false
	}
	s.init()
	if !s.awayLoading[id] && !s.awayBusy[id] && time.Since(s.awayAt[id]) >= awayEvery {
		s.awayLoading[id] = true
		go func() {
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
			defer cancel()
			aw, err := tn.Away(ctx, id)
			a.post(func() {
				s.awayLoading[id], s.awayAt[id] = false, time.Now()
				if err == nil {
					s.away[id] = aw
				}
			})
		}()
	}
	c.After(awayEvery)
	aw, have := s.away[id]
	return aw, have
}

// awayRow is a phone's Use away from home in Settings › Phones.
func (a *app) awayRow(c *mg.Context, d gateway.DeviceView) {
	if d.Info.Platform != driver.Android || d.Info.Virtual {
		return
	}
	p := pal(c)
	s := &a.tn
	id, name := d.Info.ID, d.Info.Name
	aw, have := a.awayOf(c, id)
	if !have {
		return
	}
	var desc, note string
	var action func()
	// setUp opens Add phone › Android › Use Tailscale, the steps for
	// Tailscale on both sides.
	setUp := func() {
		if button(c, locales.AwayHowTo(), btnOpt{small: true}).Clicked() {
			a.page, a.add.step = pageAddPhone, "tailnet"
			a.add.snapshot(a)
		}
	}
	switch {
	case s.awayBusy[id]:
		desc = locales.AwayBusy()
	case aw.On && (aw.Listening || d.Info.Via == driver.ViaTailscale):
		desc = locales.AwayOnDesc(name, aw.Addr)
		action = func() {
			if button(c, locales.AwayTurnOff(), btnOpt{small: true}).Clicked() {
				a.setAway(d, false)
			}
		}
	case aw.On && d.Status != gateway.DeviceOffline && aw.Missing == "":
		desc = locales.AwayStoppedDesc(name)
		action = func() {
			if button(c, locales.AwayTurnOnAgain(), btnOpt{small: true, kind: btnPrimary}).Clicked() {
				a.setAway(d, true)
			}
		}
	case aw.On:
		desc = locales.AwayOnDesc(name, aw.Addr)
		action = func() {
			if button(c, locales.AwayTurnOff(), btnOpt{small: true}).Clicked() {
				a.setAway(d, false)
			}
		}
	case aw.Missing == driver.AwayNoTailscaleHere:
		desc, action = locales.AwayNoTailscaleHere(), setUp
	case aw.Missing == driver.AwayNoTailscaleThere:
		// Tailscale may be installed and signed in, and off because
		// another VPN took its place.
		desc, note, action = locales.AwayNoTailscalePhone(name), locales.AwayOneVPN(), setUp
	case aw.Missing == driver.AwayOtherTailnet:
		desc, action = locales.AwayOtherTailnet(name), setUp
	case aw.Missing == driver.AwayNotConnected:
		desc = locales.AwayNotConnected(name)
	default:
		desc = locales.AwayOffDesc(name)
		action = func() {
			if button(c, locales.AwayTurnOn(), btnOpt{small: true}).Clicked() {
				s.ask, s.askName, s.askOpen = id, name, true
			}
		}
	}
	groupRow(c, mg.Column(c).Gap(sp1).Justify(mg.Center), false).Children(func() {
		mg.Row(c).Gap(sp4).AlignItems(mg.Center).Children(func() {
			mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				mg.Text(c, locales.AwayTitle()).FontWeight(fwMedium)
				mg.Text(c, desc).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
				if note != "" {
					mg.Text(c, note).FontSize(fsCaption).LineHeight(1.4).TextColor(p.ink3)
				}
			})
			if s.awayBusy[id] {
				spinner(c)
			} else if action != nil {
				action()
			}
		})
		if msg := s.awayErr[id]; msg != "" {
			mg.Text(c, msg).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus)
		}
	})
}

// setAway turns a phone's Use away from home on or off.
func (a *app) setAway(d gateway.DeviceView, on bool) {
	tn, ok := a.api.(tailnetter)
	s := &a.tn
	id, name := d.Info.ID, d.Info.Name
	if !ok || s.awayBusy[id] {
		return
	}
	s.init()
	s.awayBusy[id], s.awayErr[id] = true, ""
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
		defer cancel()
		aw, err := tn.SetAway(ctx, id, on)
		a.post(func() {
			s.awayBusy[id] = false
			s.awayAt[id] = time.Time{}
			if err != nil {
				s.awayErr[id] = locales.AwayFailed(name, err.Error())
				return
			}
			s.away[id] = aw
			if on {
				a.toasts = append(a.toasts, locales.AwayTurnedOn(name))
			} else {
				a.toasts = append(a.toasts, locales.AwayTurnedOff(name))
			}
		})
	}()
}

// awayDialog asks before a phone's adb listens on the network.
func (a *app) awayDialog(c *mg.Context) {
	s := &a.tn
	if !s.askOpen {
		return
	}
	d := a.device(s.ask)
	if d == nil {
		s.askOpen = false
		return
	}
	p := pal(c)
	title := locales.AwayDialogTitle(s.askName)
	sheet(c.Key("away-dialog"), &s.askOpen, func() {
		mg.Column(c).Width(480).Gap(14).Label(title).Children(func() {
			mg.Text(c, title).FontSize(fsTitle3).FontWeight(fwSemibold).Role(mg.RoleHeading)
			mg.Text(c, locales.AwayDialogWhat()).FontSize(fsSmall).TextColor(p.ink2)
			mg.Column(c).Gap(6).Padding(10, 12).Radius(8).Background(p.bg2).Border(1, p.line).Children(func() {
				for _, note := range []string{locales.AwayDialogExposure(), locales.AwayDialogUntil(), locales.AwayDialogDrop()} {
					mg.Text(c, note).FontSize(fsSmall).TextColor(p.ink2)
				}
			})
			mg.Row(c).Gap(8).Justify(mg.End).AlignItems(mg.Center).Children(func() {
				if button(c, locales.AwayDialogCancel(), btnOpt{}).Clicked() {
					s.askOpen = false
				}
				if button(c, locales.AwayDialogConfirm(), btnOpt{kind: btnPrimary}).Clicked() {
					s.askOpen = false
					a.setAway(*d, true)
				}
			})
		})
	})
}

// anyAway reports whether a phone is connected through Tailscale, which
// the driver uses only while the phone has no connection on this
// computer's network: the phone is away.
func (a *app) anyAway() bool {
	for _, d := range a.devices {
		if d.Info.Via == driver.ViaTailscale && d.Status != gateway.DeviceOffline && d.Info.State == driver.StateOnline {
			return true
		}
	}
	return false
}

// setAwayAwake keeps the computer from idle sleep while a phone is away
// and the person wants that (Settings › Phones).
func (a *app) setAwayAwake(on bool) {
	if on == a.awayAwakeOn || a.awayAwake == nil {
		return
	}
	a.awayAwakeOn = on
	a.awayAwake(on)
}

// awayKeepAwakeShown reports whether Settings › Phones shows Keep this
// computer awake for phones away: once Tailscale is on this computer.
func (a *app) awayKeepAwakeShown(c *mg.Context) bool {
	st, have := a.tailnetStatus(c)
	return have && st.State != driver.TailnetNotInstalled
}

// awayKeepAwakeRow is Settings › Phones' Keep this computer awake for
// phones away, the last row of its group.
func (a *app) awayKeepAwakeRow(c *mg.Context) {
	if a.toggleRow(c, "away-awake", locales.AwayKeepAwake(), locales.AwayKeepAwakeDesc(), true, &a.settings.KeepAwakeAway) {
		a.setAwayAwake(a.settings.KeepAwakeAway && a.anyAway())
	}
}

// tailnetDiagnostics are Tailscale's lines in the diagnostics: whether it
// runs on this computer, the Android devices on its tailnet, and each
// Android phone's Use away from home. Tailnet and device names stay out:
// the Redactor takes out e-mail addresses, but not a tailnet named after a
// domain or a GitHub account, nor what someone called a device. Pinky's
// phones go by model, as the diagnostics' list of phones has them.
func tailnetDiagnostics(ctx context.Context, tn tailnetter, phones []driver.Info) []string {
	st, err := tn.Tailnet(ctx)
	if errors.Is(err, gateway.ErrNoTailnet) {
		return nil
	}
	if err != nil {
		return []string{"Tailscale: couldn't read its status: " + err.Error()}
	}
	model := map[string]string{}
	for _, p := range phones {
		model[p.ID] = nonEmpty(p.Model, string(p.Platform))
	}
	head := "Tailscale: " + strings.ReplaceAll(st.State, "_", " ")
	if st.Detail != "" {
		head += " (" + st.Detail + ")"
	}
	if st.State == driver.TailnetRunning {
		head += " · tailnet name not shown · Android devices on it: " + strconv.Itoa(len(st.Phones))
	}
	lines := []string{head}
	for i, ph := range st.Phones {
		parts := []string{"offline"}
		if ph.Online {
			parts[0] = "online"
		} else if !ph.LastSeen.IsZero() {
			parts[0] += ", last seen " + ph.LastSeen.UTC().Format(time.DateTime) + " UTC"
		}
		switch m, known := model[ph.ID]; {
		case known:
			parts = append(parts, "Pinky's "+m)
		case ph.ID != "":
			parts = append(parts, "a phone Pinky has seen")
		default:
			parts = append(parts, "not one of Pinky's phones")
		}
		switch {
		case ph.Connected && ph.Relayed:
			parts = append(parts, "connected through a relay")
		case ph.Connected:
			parts = append(parts, "connected")
		}
		lines = append(lines, fmt.Sprintf("Tailscale: Android device %d: %s", i+1, strings.Join(parts, " · ")))
	}
	for _, p := range phones {
		if p.Platform != driver.Android {
			continue
		}
		aw, err := tn.Away(ctx, p.ID)
		if err != nil {
			lines = append(lines, fmt.Sprintf("Use away from home on %s: %v", model[p.ID], err))
			continue
		}
		parts := []string{"off"}
		if aw.On {
			parts[0] = "on"
		}
		if aw.Listening {
			parts = append(parts, "adb listens on the network")
		}
		if aw.Missing != "" {
			parts = append(parts, "missing "+aw.Missing)
		}
		lines = append(lines, fmt.Sprintf("Use away from home on %s: %s", model[p.ID], strings.Join(parts, " · ")))
	}
	return lines
}
