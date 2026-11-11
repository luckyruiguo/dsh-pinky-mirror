package ui

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/agents"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

type settingsSection int

const (
	secSafety settingsSection = iota
	secPhones
	secAgents
	secPrivacy
	secAdvanced
)

type settingsState struct {
	sec     settingsSection
	adding  bool
	newApp  string
	confirm bool
}

// changed reports a change in Settings, with the settings as they were
// before it.
func (a *app) changed() {
	a.changes++
	old := a.reported
	a.reported = a.settings.clone()
	if a.opt.OnSettings != nil {
		a.opt.OnSettings(old, a.settings.clone())
	}
}

// clone returns s with a list of blocked apps of its own.
func (s Settings) clone() Settings {
	s.BlockedApps = append([]BlockedApp(nil), s.BlockedApps...)
	return s
}

func (a *app) settingsPage(c *mg.Context) {
	p := pal(c)
	s := &a.set
	pageHeader(c, func() { pageTitle(c, locales.SettingsPageTitle()) })
	mg.Row(c).Grow(1).MinHeight(0).AlignItems(mg.Stretch).Children(func() {
		mg.Column(c).Width(200).Shrink(0).Padding(sp3, 10).Gap(2).BorderWidth(0, 1, 0, 0).BorderColor(p.line).
			Role(mg.RoleTabList).Label(locales.SettingsSectionsLabel()).Children(func() {
			tab := func(sec settingsSection, ic *mg.SVG, name string, needs bool) {
				on := s.sec == sec
				b := mg.ButtonBase(c.Key(sec)).Height(32).Padding(0, 10, 0, rowInset).Radius(rControl).Gap(10).AlignItems(mg.Center).Justify(mg.Start).
					Role(mg.RoleTab).Selected(on).Label(name)
				switch {
				case on:
					b.Background(p.sel)
					selectionMark(c, b)
				case b.Hovered():
					b.Background(p.bg3)
				}
				if b.Clicked() {
					s.sec = sec
				}
				b.TextColor(navInk(c, b, on)).Children(func() {
					mg.Icon(c, ic).FontSize(16)
					mg.Text(c, name).FontSize(fsBody).Grow(1)
					if needs {
						dot(c, p.amberDot, false).Label(locales.SettingsNeedsYou())
					}
				})
			}
			tab(secPhones, icAndroid, locales.SettingsSectionPhones(), false)
			agentsWait := false
			for _, n := range a.needs {
				agentsWait = agentsWait || newAgentNeed(n)
			}
			tab(secAgents, icPlug, locales.SettingsSectionAgents(), agentsWait)
			tab(secSafety, icShield, locales.SettingsSectionSafety(), len(a.files.list) > 0)
			tab(secPrivacy, icEyeOff, locales.SettingsSectionPrivacy(), false)
			tab(secAdvanced, icSliders, locales.SettingsSectionAdvanced(), false)
			a.feedbackLink(c)
		})
		a.watchFileProblems(c)
		a.watchGatewaySettings(c)
		mg.Scroll(c).Grow(1).MinWidth(0).Padding(28, 40).Children(func() {
			mg.Column(c).MaxWidth(640).Gap(28).Children(func() {
				switch s.sec {
				case secPhones:
					a.settingsPhones(c)
				case secAgents:
					a.settingsAgents(c)
				case secPrivacy:
					a.settingsPrivacy(c)
				case secAdvanced:
					a.settingsAdvanced(c)
				default:
					a.settingsSafety(c)
				}
			})
		})
	})
}

func settingsTitle(c *mg.Context, title, desc string) {
	p := pal(c)
	mg.Column(c).Margin(0, 0, -sp1, 0).Children(func() {
		mg.Text(c, title).FontSize(fsTitle2).FontWeight(fwSemibold).Role(mg.RoleHeading)
		mg.Text(c, desc).FontSize(fsSmall).LineHeight(1.45).TextColor(p.ink2).Margin(6, 0, 0, 0)
	})
}

// settingsHeading is the heading of a group of settings, with what it is
// for under it.
func settingsHeading(c *mg.Context, title, desc string) mg.Element {
	p := pal(c)
	return mg.Column(c).Gap(3).Children(func() {
		mg.Text(c, title).FontSize(fsHeadline).FontWeight(fwSemibold)
		if desc != "" {
			mg.Text(c, desc).FontSize(fsSmall).LineHeight(1.45).TextColor(p.ink2)
		}
	})
}

// subHeading names a part of a group of settings, as Your rules and
// Built-in rules do under Rules, with what it is for under it.
func subHeading(c *mg.Context, title, desc string) mg.Element {
	p := pal(c)
	return mg.Column(c).Gap(2).Margin(sp1, 0, 0, 0).Children(func() {
		mg.Text(c, title).FontSize(fsSmall).FontWeight(fwSemibold).TextColor(p.ink2)
		if desc != "" {
			mg.Text(c, desc).FontSize(fsCaption).LineHeight(1.4).TextColor(p.ink3)
		}
	})
}

// rowWords are the title and description of a setting's row.
func rowWords(c *mg.Context, words mg.Element, title, desc string) {
	p := pal(c)
	words.Grow(1).MinWidth(0).Gap(2).Children(func() {
		mg.Text(c, title).FontWeight(fwMedium)
		if desc != "" {
			mg.Text(c, desc).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
		}
	})
}

// toggleRow is a setting row with a switch; clicking its words toggles
// it too, as a form's field label does. It reports a change, which it
// saves with the settings.
func (a *app) toggleRow(c *mg.Context, key, title, desc string, last bool, on *bool) bool {
	changed := a.toggleRowFor(c, key, title, desc, last, on)
	if changed {
		a.changed()
	}
	return changed
}

// toggleRowFor is toggleRow for a choice the gateway keeps rather than the
// settings file.
func (a *app) toggleRowFor(c *mg.Context, key, title, desc string, last bool, on *bool) bool {
	changed := false
	r := groupRow(c, mg.Row(c.Key(key)).Gap(sp4).AlignItems(mg.Center), last)
	r.Children(func() {
		words := mg.Column(c).Cursor(mg.CursorPointer)
		if words.Clicked() {
			*on = !*on
			changed = true
		}
		rowWords(c, words, title, desc)
		if toggle(c.Key(key+"-switch"), on, title).Changed() {
			changed = true
		}
	})
	return changed
}

// settingRow is the prototype's .trow in Settings: a label and a
// description, with a control at the end.
func settingRow(c *mg.Context, title, desc string, last bool, control func()) {
	r := groupRow(c, mg.Row(c).Gap(sp4).AlignItems(mg.Center), last)
	r.Children(func() {
		rowWords(c, mg.Column(c), title, desc)
		if control != nil {
			control()
		}
	})
}

func (a *app) settingsSafety(c *mg.Context) {
	p := pal(c)
	st := &a.settings
	settingsTitle(c, locales.SettingsSectionSafety(), locales.SettingsSafetyIntro())
	a.fileProblemNotes(c)
	a.offWhile(c, rulesYAML).Gap(10).Label(locales.SettingsSafetyAskGroup()).Children(func() {
		settingsHeading(c, locales.SettingsSafetyAskTitle(), "")
		presets := []struct{ title, desc string }{
			{locales.SettingsSafetyPresetCommits(), locales.SettingsSafetyPresetCommitsDesc()},
			{locales.SettingsSafetyPresetPayments(), locales.SettingsSafetyPresetPaymentsDesc()},
			{locales.SettingsSafetyPresetEvery(), locales.SettingsSafetyPresetEveryDesc()},
		}
		group(c).Role(mg.RoleRadioGroup).Label(locales.SettingsSafetyAskTitle()).Children(func() {
			for i, pr := range presets {
				v := i + 1
				opt := mg.RadioBase(c.Key(v), &st.Preset, v).Gap(sp3).AlignItems(mg.Start).FillWidth()
				opt = groupRow(c, opt, v == len(presets))
				on := st.Preset == v
				if opt.Hovered() && !on {
					opt.Background(p.bg3.Alpha(0.5))
				}
				if opt.Changed() {
					a.changed()
				}
				opt.Children(func() {
					rb := mg.Box(c).Size(18, 18).Radius(9).Margin(1, 0, 0, 0).Shrink(0).Background(p.panel)
					if on {
						rb.Border(5.5, p.accentBtn)
					} else {
						rb.Border(1.5, p.line2)
					}
					rowWords(c, mg.Column(c), pr.title, pr.desc)
				})
			}
		})
	})
	a.offWhile(c, appsYAML).Gap(10).Label(locales.SettingsSafetyBlockedApps()).Children(func() {
		mg.Row(c).Gap(sp3).AlignItems(mg.End).Children(func() {
			settingsHeading(c, locales.SettingsSafetyBlockedApps(), locales.SettingsSafetyBlockedIntro()).Grow(1).MinWidth(0)
			if button(c, locales.SettingsSafetyAddApp(), btnOpt{small: true, icon: icPlus}).Clicked() {
				a.set.adding = !a.set.adding
			}
		})
		if a.set.adding {
			a.addBlockedApp(c)
		}
		if len(st.BlockedApps) == 0 {
			return
		}
		group(c).Children(func() {
			for i, b := range st.BlockedApps {
				b := b
				// Two apps may share a name (Trust Bank's iPhone app and Trust
				// Wallet's Android app): the id tells their rows apart.
				row := mg.Row(c.Key("b-" + b.ID + "\x00" + b.Name)).Gap(sp3).AlignItems(mg.Center)
				groupRow(c, row, i == len(st.BlockedApps)-1).Padding(sp2, sp3, sp2, sp4).Children(func() {
					appIcon(c, b.Name, 28, 7, 13, blockedColor(b.Name))
					mg.Text(c, b.Name).FontWeight(fwMedium).Grow(1)
					mg.Text(c, blockedKind(b.Kind)).FontSize(fsSmall).TextColor(p.ink2)
					rm := mg.ButtonBase(c).Size(26, 26).Radius(rSmall).Center().Label(locales.SettingsSafetyUnblockApp(b.Name)).Tooltip(locales.SettingsSafetyUnblock())
					if rm.Hovered() {
						rm.Background(p.bg3)
					}
					if rm.Clicked() {
						st.BlockedApps = withoutBlocked(st.BlockedApps, b)
						a.changed()
					}
					rm.TextColor(p.ink3).Children(func() { mg.Icon(c, icX).FontSize(14) })
				})
			}
		})
	})
	a.alwaysAllowed(c)
	group(c).Label(locales.SettingsSafetyProtections()).Children(func() {
		a.offWhile(c, rulesYAML).Children(func() {
			if a.toggleRow(c, "ro", locales.SettingsSafetyReadOnly(), locales.SettingsSafetyReadOnlyDesc(), false, &st.ReadOnly) {
				a.applyReadOnly(st.ReadOnly)
			}
		})
		desc := locales.SettingsSafetyUnlockDescOther()
		switch runtime.GOOS {
		case "darwin":
			desc = locales.SettingsSafetyUnlockDescMac()
		case "windows":
			desc = locales.SettingsSafetyUnlockDescWindows()
		}
		if a.pin.ready && !a.pin.supported {
			settingRow(c, locales.SettingsUnlockFromComputer(), locales.UnlockNotAvailable(), false, nil)
		} else {
			a.toggleRow(c, "unlock", locales.SettingsUnlockFromComputer(), desc, false, &st.UnlockFromComputer)
		}
		a.offWhile(c, rulesYAML).Children(func() {
			a.toggleRow(c, "codes", locales.SettingsSafetyHideCodes(), locales.SettingsSafetyHideCodesDesc(), true, &st.HideCodes)
		})
	})
	a.rulesSection(c)
}

// withoutBlocked returns list without the row of b, the same app by id
// and name; other apps of that name stay.
func withoutBlocked(list []BlockedApp, b BlockedApp) []BlockedApp {
	out := make([]BlockedApp, 0, len(list))
	for _, x := range list {
		if x.ID != b.ID || x.Name != b.Name {
			out = append(out, x)
		}
	}
	return out
}

// blockedKinds name the kinds of blocked apps, which the settings keep in
// English: policy.CategoryLabel's (cmd/pinky) and the defaults'.
var blockedKinds = map[string]func() string{
	"Finance":        locales.SettingsSafetyKindFinance,
	"Passwords":      locales.SettingsSafetyKindPasswords,
	"Blocked by you": locales.RuleBlockedByYou,
	"Banking":        locales.SettingsSafetyKindBanking,
	"Wallet":         locales.SettingsSafetyKindWallet,
	"Messaging":      locales.SettingsSafetyKindMessaging,
	"Social":         locales.SettingsSafetyKindSocial,
	"Stores":         locales.SettingsSafetyKindStores,
	"Browsers":       locales.SettingsSafetyKindBrowsers,
	"Phone":          locales.SettingsSafetyKindPhone,
	"Settings":       locales.SettingsSafetyKindSettings,
	"System":         locales.SettingsSafetyKindSystem,
}

// blockedKind is a blocked app's kind as Settings › Safety shows it. A
// category of the person's own app table keeps its name.
func blockedKind(kind string) string {
	if f := blockedKinds[kind]; f != nil {
		return f()
	}
	return kind
}

func blockedColor(name string) string {
	switch name {
	case "Northbank":
		return "#1e3a8a"
	case "Vaultly":
		return "#334155"
	case "Coinpouch":
		return "#b45309"
	}
	return ""
}

func (a *app) settingsPhones(c *mg.Context) {
	p := pal(c)
	settingsTitle(c, locales.SettingsSectionPhones(), locales.SettingsPhonesIntro())
	mg.Column(c).Gap(sp4).Label(locales.SettingsSectionPhones()).Children(func() {
		if len(a.devices) == 0 {
			group(c).Children(func() {
				groupRow(c, mg.Row(c).AlignItems(mg.Center), true).Children(func() {
					mg.Text(c, locales.SettingsPhonesNone()).FontSize(fsSmall).TextColor(p.ink2)
				})
			})
		}
		for _, d := range a.devices {
			d := d
			// Each phone is a group: its name and state, then what can be
			// set for it, then Checkup and Remove at its foot.
			group(c.Key(d.Info.ID)).Label(d.Info.Name).Children(func() {
				if foundOnWiFi(d) {
					// Found on Wi-Fi: nothing to set until the person says
					// it is theirs (wifi.go).
					groupRow(c, mg.Row(c).Gap(sp4).AlignItems(mg.Center), true).Children(func() {
						line := locales.SettingsPhonesFound()
						if l := foundLine(d); l != "" {
							line = l + " · " + line
						}
						phoneHead(c, d, line)
						a.foundButtons(c, d, true)
					})
					return
				}
				groupRow(c, mg.Row(c).Gap(sp4).AlignItems(mg.Center), false).Children(func() {
					line := osLine(d)
					if cl := companionLine(a.companionFor(d)); cl != "" {
						line += " · " + cl
					}
					phoneHead(c, d, line)
					mg.Row(c).Gap(6).AlignItems(mg.Center).Shrink(0).Children(func() {
						word := locales.SettingsPhonesConnected()
						switch {
						case d.Status == gateway.DeviceConnecting:
							dot(c, p.ink3, false)
							word = locales.SettingsPhonesConnecting()
						case d.Status == gateway.DeviceOffline:
							hollowDot(c)
							word = locales.SettingsPhonesOffline()
						case d.Info.State != driver.StateOnline:
							dot(c, p.amberDot, false)
							word = locales.SettingsPhonesNeedsSetup()
						default:
							dot(c, p.green, false)
						}
						mg.Text(c, word).FontSize(fsSmall).TextColor(p.ink2)
					})
				})
				// Unlocking from this computer: the phone's PIN. A saved one
				// can be removed even with unlocking turned off.
				if a.pin.saved[d.Info.ID] || (d.Features.Unlock && a.settings.UnlockFromComputer) {
					groupRow(c, mg.Column(c).Gap(sp2).Justify(mg.Center), false).Children(func() {
						mg.Text(c, locales.SettingsUnlockFromComputer()).FontWeight(fwMedium)
						a.pinForm(c, d, "settings")
					})
				}
				if d.Features.Background {
					a.backgroundRow(c, d, false)
				}
				a.connectionRows(c, d)
				a.awayRow(c, d)
				a.phoneWiFiRow(c, d)
				mg.Row(c).Padding(sp2, sp4).Gap(sp2).AlignItems(mg.Center).Justify(mg.End).Children(func() {
					if button(c, locales.PhoneCheckup(), btnOpt{small: true}).Label(locales.SettingsPhonesCheckupLabel(d.Info.Name)).Clicked() {
						a.openCheckup(d)
					}
					if a.canRemove() && button(c.Key("remove-"+d.Info.ID), locales.RemovePhoneButton(), btnOpt{small: true}).
						Label(locales.RemovePhoneButtonLabel(d.Info.Name)).Clicked() {
						a.askRemove(d)
					}
				})
			})
		}
	})
	mg.Row(c).Children(func() {
		if button(c, locales.SettingsPhonesAdd(), btnOpt{icon: icPlus}).Clicked() {
			a.page = pageAddPhone
			a.add.reset(a)
		}
	})
	keepAwake := a.awayKeepAwakeShown(c)
	group(c).Children(func() {
		if a.toggleRow(c, "ios-wifi", locales.SettingsPhonesWiFi(), locales.SettingsPhonesWiFiDesc(), !keepAwake, &a.settings.IOSWiFi) {
			a.applyWiFi(a.settings.IOSWiFi)
		}
		if keepAwake {
			a.awayKeepAwakeRow(c)
		}
	})
}

// phoneHead is the head of a phone's group in Settings › Phones: its
// kind's picture, its name, and a line on how it is connected.
func phoneHead(c *mg.Context, d gateway.DeviceView, line string) {
	p := pal(c)
	ic := icAndroid
	if d.Info.Platform == driver.IOS {
		ic = icIphone
	}
	mg.Box(c).Size(36, 36).Radius(rControl).Background(p.bg3).Shrink(0).Justify(mg.Center).AlignItems(mg.Center).Children(func() {
		mg.Icon(c, ic).FontSize(20).TextColor(p.ink2)
	})
	mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
		mg.Text(c, d.Info.Name).FontSize(fsHeadline).FontWeight(fwSemibold)
		mg.Text(c, line).FontSize(fsSmall).TextColor(p.ink2)
	})
}

// agentsState is Settings › Agents': the gateway's agents and the scopes
// being chosen.
type agentsState struct {
	list     []gateway.AgentView
	loaded   bool
	err      error
	seq      int
	lastLoad time.Time
	asks     int               // open first-connection requests, when last read
	editing  string            // the agent whose scope is being changed
	choice   map[string]string // scope mode being chosen, by agent or need id
	apps     map[string]string // apps typed for "Use only the apps I choose"
}

func (s *agentsState) load(a *app) {
	al, ok := a.api.(agentLister)
	if !ok {
		return
	}
	s.seq++
	seq := s.seq
	s.lastLoad = time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
		defer cancel()
		list, err := al.Agents(ctx)
		a.post(func() {
			if seq != s.seq {
				return
			}
			sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
			s.list, s.err, s.loaded = list, err, true
		})
	}()
}

// splitApps reads the apps field: names separated by commas, including
// the full-width comma and the ideographic comma of Chinese text.
func splitApps(s string) []string {
	var out []string
	for _, a := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\uff0c' || r == '\u3001' }) {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// scopePicker is the select of what an agent may do, with the apps field
// for "Use only the apps I choose". It returns the scope chosen.
func (a *app) scopePicker(c *mg.Context, key, label string, initial gateway.Scope) gateway.Scope {
	st := &a.agents
	if st.choice == nil {
		st.choice, st.apps = map[string]string{}, map[string]string{}
	}
	// The choice is kept as a mode, so it survives a change of language.
	mode, ok := st.choice[key]
	if !ok {
		mode = string(initial.Mode)
		st.apps[key] = strings.Join(initial.Apps, ", ")
	}
	choice := scopeLabel(gateway.ScopeMode(mode))
	labels := make([]string, len(scopeChoices))
	for i, sc := range scopeChoices {
		labels[i] = sc.label()
	}
	mg.Select(c.Key("scope-"+key), &choice, labels).Label(label).MaxWidth(360)
	scope := gateway.Scope{Mode: scopeOf(choice)}
	st.choice[key] = string(scope.Mode)
	mg.Text(c, scopeHint(scope.Mode)).FontSize(fsSmall).TextColor(pal(c).ink2).MaxWidth(360).Margin(4, 0, 0, 0)
	if scope.Mode == gateway.ScopeApps {
		apps := st.apps[key]
		mg.TextInput(c.Key("apps-"+key), &apps).Placeholder(locales.SettingsAgentsAppsPlaceholder()).Label(locales.SettingsAgentsApps()).MaxWidth(360)
		st.apps[key] = apps
		scope.Apps = splitApps(apps)
	}
	return scope
}

// approveAgent allows an agent's first connection with the chosen scope:
// SetAgentScope, which also approves the agent, then Decide, so the
// gateway keeps the person's choice when the agent picks up the answer.
func (a *app) approveAgent(n gateway.NeedView, scope gateway.Scope) {
	a.remember(n, gateway.AllowOnce, "")
	scoper, ok := a.api.(agentScoper)
	name := n.Agent
	a.call(n.DeviceID, locales.SettingsAgentsApprove(), func() error {
		if ok && name != "" {
			scoper.SetAgentScope(name, scope)
		}
		return a.api.Decide(n.ID, gateway.AllowOnce, "")
	})
}

func (a *app) settingsAgents(c *mg.Context) {
	p := pal(c)
	st := &a.agents
	settingsTitle(c, locales.SettingsSectionAgents(), locales.SettingsAgentsIntro())
	_, canList := a.api.(agentLister)
	_, canScope := a.api.(agentScoper)
	if canList {
		if !st.loaded || time.Since(st.lastLoad) > 5*time.Second || st.lastLoad.IsZero() {
			st.load(a)
		}
		c.After(5 * time.Second)
	}
	// First connections waiting for the person, as the prototype's request.
	for _, n := range a.needs {
		if !newAgentNeed(n) {
			continue
		}
		n := n
		mg.Column(c.Key("req-"+n.ID)).Padding(sp4, sp5).Radius(rCard).Background(p.amberTint).Border(1, p.amberLine).Gap(sp3).
			Label(locales.SettingsAgentsRequest()).Children(func() {
			mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
				dot(c, p.amberDot, false)
				mg.Text(c, locales.SettingsNeedsYou()).FontSize(fsSmall).FontWeight(fwSemibold).TextColor(p.amber)
			})
			mg.Column(c).Gap(sp1).Children(func() {
				mg.Text(c, needTitle(n)).FontSize(fsHeadline).FontWeight(fwSemibold)
				if d := agentRequestDetail(n, canScope); d != "" {
					mg.Text(c, d).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
				}
			})
			a.agentRequestControls(c, n)
		})
	}
	type row struct {
		name, line string
		state      string // "working", "allowed", "waiting"
		scope      gateway.Scope
		known      bool
	}
	var rows []row
	working := map[string]bool{}
	for _, t := range a.tasks {
		if running(t.State) && t.Agent != "" {
			working[t.Agent] = true
		}
	}
	switch {
	case canList && st.loaded:
		for _, ag := range st.list {
			r := row{name: ag.Name, line: scopeLine(ag.Scope), scope: ag.Scope, known: true, state: "allowed"}
			if !ag.Approved {
				r.line, r.state = locales.SettingsAgentsNotAllowed(), "waiting"
			}
			if working[ag.Name] {
				r.state = "working"
			}
			rows = append(rows, r)
		}
	case a.opt.Agents != nil:
		for _, ag := range a.opt.Agents {
			r := row{name: ag.Name, line: nonEmpty(ag.Scope, locales.SettingsAgentsViaMCP()), state: "allowed"}
			if ag.Connected {
				r.state = "working"
			}
			rows = append(rows, r)
		}
	default:
		seen := map[string]bool{}
		for _, t := range append(append([]gateway.TaskView(nil), a.tasks...), a.act.tasks...) {
			if t.Agent != "" && !seen[t.Agent] {
				seen[t.Agent] = true
				r := row{name: t.Agent, line: locales.SettingsAgentsViaMCP(), state: "allowed"}
				if working[t.Agent] {
					r.state = "working"
				}
				rows = append(rows, r)
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	}
	group(c).Label(locales.SettingsAgentsList()).Children(func() {
		if len(rows) == 0 {
			none := locales.SettingsAgentsNone()
			for _, st := range a.setup.list {
				if st.State == agents.Connected {
					none = locales.SettingsAgentsNoneYet()
				}
			}
			groupRow(c, mg.Row(c).AlignItems(mg.Center), true).Children(func() {
				mg.Text(c, none).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
			})
		}
		for i, r := range rows {
			r := r
			last := i == len(rows)-1
			box := groupRow(c, mg.Column(c.Key("agent-"+r.name)).Gap(sp3).Justify(mg.Center), last)
			box.Children(func() {
				mg.Row(c).Gap(sp3).AlignItems(mg.Center).Children(func() {
					appIcon(c, r.name, 32, rControl, 14, "#5450d6")
					mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						mg.Text(c, r.name).FontWeight(fwMedium)
						mg.Text(c, r.line).FontSize(fsSmall).TextColor(p.ink2)
					})
					mg.Row(c).Gap(6).AlignItems(mg.Center).Shrink(0).Children(func() {
						switch r.state {
						case "working":
							dot(c, p.green, false)
							mg.Text(c, locales.SettingsAgentsWorking()).FontSize(fsSmall).TextColor(p.ink2)
						case "waiting":
							dot(c, p.amberDot, false)
							mg.Text(c, locales.SettingsAgentsWaiting()).FontSize(fsSmall).TextColor(p.ink2)
						default:
							dot(c, p.ink3, false)
							mg.Text(c, locales.SettingsAgentsAllowed()).FontSize(fsSmall).TextColor(p.ink2)
						}
					})
					if canScope && r.known && r.state != "waiting" {
						label := locales.SettingsChange()
						if st.editing == r.name {
							label = locales.SettingsClose()
						}
						if button(c, label, btnOpt{small: true}).Label(locales.SettingsAgentsChangeLabel(r.name)).Clicked() {
							if st.editing == r.name {
								st.editing = ""
							} else {
								st.editing = r.name
								delete(st.choice, r.name)
							}
						}
					}
				})
				if st.editing == r.name && canScope {
					mg.Column(c).Gap(8).Padding(0, 0, 0, 44).Children(func() {
						scope := a.scopePicker(c, r.name, locales.SettingsAgentsScopeLabel(r.name), r.scope)
						if button(c, locales.SettingsSave(), btnOpt{kind: btnPrimary}).AlignSelf(mg.Start).Clicked() {
							if sc, ok := a.api.(agentScoper); ok {
								name := r.name
								go func() {
									sc.SetAgentScope(name, scope)
									a.post(func() { a.agents.load(a) })
								}()
							}
							st.editing = ""
						}
					})
				}
			})
		}
	})
	if !canList {
		mg.Text(c, locales.SettingsAgentsUnlisted()).FontSize(fsCaption).TextColor(p.ink3)
	}
	a.askSettings(c)
	a.connectAgents(c)
	a.remoteAgents(c)
}

// keepChoices are the retention limits Settings offers, with the current
// one when it is something else (from the settings file).
func keepChoices(cur int) []int {
	out := []int{7, 30, 90, 365, 0}
	for _, d := range out {
		if d == cur {
			return out
		}
	}
	return append([]int{cur}, out...)
}

// keepLabel is a retention limit as the select shows it, in the language
// shown. The select maps a label back to its limit in the same frame.
func keepLabel(days int) string { return keepLabelIn(locales.In(i18n.Locale()), days) }

// keepLabelIn names a retention in the language of m; feedback's
// diagnostics are English.
func keepLabelIn(m locales.Messages, days int) string {
	switch {
	case days <= 0:
		return m.SettingsPrivacyKeepForever()
	case days == 365:
		return m.SettingsPrivacyKeepYear()
	}
	return m.SettingsPrivacyKeepDays(days)
}

func (a *app) settingsPrivacy(c *mg.Context) {
	p := pal(c)
	st := &a.settings
	settingsTitle(c, locales.SettingsSectionPrivacy(), locales.SettingsPrivacyIntro())
	group(c).Label(locales.SettingsSectionPrivacy()).Children(func() {
		settingRow(c, locales.SettingsPrivacyKeep(), locales.SettingsPrivacyKeepDesc(), false, func() {
			choices := keepChoices(st.KeepDays)
			labels := make([]string, len(choices))
			for i, d := range choices {
				labels[i] = keepLabel(d)
			}
			cur := keepLabel(st.KeepDays)
			if mg.Select(c.Key("keep"), &cur, labels).Label(locales.SettingsPrivacyKeep()).Changed() {
				for i, l := range labels {
					if l == cur {
						st.KeepDays = choices[i]
					}
				}
				a.changed()
				a.applyRetention(st.KeepDays)
			}
		})
		if a.toggleRow(c, "rec", locales.SettingsPrivacyRecord(), locales.SettingsPrivacyRecordDesc(), false, &st.Record) {
			a.applyRecording(st.Record)
		}
		settingRow(c, locales.SettingsPrivacyDeleteAll(), locales.SettingsPrivacyDeleteAllDesc(), true, func() {
			b := button(c, locales.SettingsPrivacyDeleteButton(), btnOpt{})
			if _, ok := a.api.(historyWiper); !ok {
				b.Disabled(true).Tooltip(locales.SettingsPrivacyCannotDelete())
			} else if b.Clicked() {
				a.set.confirm = true
			}
		})
	})
	a.privacyTelemetry(c)
	if a.set.confirm {
		switch mg.AlertDialog(c, &a.set.confirm, locales.SettingsPrivacyDeleteTitle(),
			locales.SettingsPrivacyDeleteMessage(),
			locales.SettingsCancel(), locales.SettingsPrivacyDeleteConfirm()) {
		case 1:
			a.wipeHistory()
		}
	}
	_ = p
}

// wipeHistory deletes the gateway's history, from Settings › Privacy.
func (a *app) wipeHistory() {
	w, ok := a.api.(historyWiper)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		err := w.WipeHistory(ctx)
		a.post(func() {
			if err != nil {
				a.toasts = append(a.toasts, locales.SettingsPrivacyDeleteFailed(err.Error()))
				return
			}
			a.mediaCache = map[string]*mediaEntry{}
			a.act.tasks, a.act.sel = nil, ""
			a.act.load(a)
			a.toasts = append(a.toasts, locales.SettingsPrivacyDeleted())
		})
	}()
}

func (a *app) settingsAdvanced(c *mg.Context) {
	p := pal(c)
	st := &a.settings
	settingsTitle(c, locales.SettingsSectionAdvanced(), locales.SettingsAdvancedIntro())
	group(c).Label(locales.SettingsSectionAdvanced()).Children(func() {
		a.languageRow(c)
		a.toggleRow(c, "stop-adb", locales.SettingsAdvancedStopAdb(), locales.SettingsAdvancedStopAdbDesc(), false, &st.StopADBOnQuit)
		a.toggleRow(c, "dev", locales.SettingsAdvancedDevMode(), locales.SettingsAdvancedDevModeDesc(), false, &st.DeveloperMode)
		if st.DeveloperMode {
			var parts []string
			for _, d := range a.devices {
				if d.Info.Backend != "" {
					parts = append(parts, fmt.Sprintf("%s: %s", d.Info.Name, d.Info.Backend))
				}
			}
			backends := strings.Join(parts, ". ")
			if backends == "" {
				backends = locales.SettingsAdvancedNoPhones()
			} else {
				backends += "."
			}
			settingRow(c, locales.SettingsAdvancedBackends(), backends, false, nil)
			conn := locales.SettingsAdvancedConnStdio(agentCommand())
			if st.RemoteAgents {
				conn = locales.SettingsAdvancedConnHTTP(agentCommand(), RemoteURL(st.RemoteAddr))
			}
			settingRow(c, locales.SettingsAdvancedConnection(), conn, false, nil)
			if v := videoLine(a); v != "" {
				settingRow(c, locales.SettingsAdvancedVideo(), v, false, nil)
			}
		}
		settingRow(c, locales.SettingsAdvancedRulesFile(), locales.SettingsAdvancedRulesFileDesc(), true, func() {
			b := button(c, locales.SettingsAdvancedEditRules(), btnOpt{})
			if a.opt.RulesFile == "" {
				b.Disabled(true).Tooltip(locales.SettingsAdvancedNoRulesFile())
			} else if b.Clicked() {
				path := a.opt.RulesFile
				go func() {
					if err := mygo.Shell.OpenPath(path); err != nil {
						a.post(func() { a.toasts = append(a.toasts, locales.SettingsAdvancedRulesOpenFailed(err.Error())) })
					}
				}()
			}
		})
	})
	a.fileProblemNotes(c)
	a.settingsUpdates(c)
	_ = p
}
