package ui

import (
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Always allow: an approval's More options offer it when the gateway can
// keep it (NeedView.AlwaysAllow), and Settings › Safety › Always allowed
// lists what the person always allows, with a button that takes each
// back. The gateway keeps them in the rules file.

// The gateway's own approvals whose Always allow holds for one agent on
// one phone (gateway.Exemption.Agent), for App logs in one app, and for
// installs for one app id by one signer.
const (
	rulePhoneSettings = "Phone settings"
	ruleAppLogs       = "App logs"
	ruleInstall       = "Installing from the computer"
)

// alwaysAllowedDetail is the panel's confirmation after Always allow on
// one of those, or "" for the others.
func alwaysAllowedDetail(n gateway.NeedView) string {
	switch n.Rule {
	case rulePhoneSettings:
		return locales.PanelAlwaysAllowedSettingsDetail(agentOf(n), n.DeviceName)
	case ruleAppLogs:
		return locales.PanelAlwaysAllowedLogsDetail(agentOf(n), nonEmpty(n.App, locales.PanelThisApp()), n.DeviceName)
	case ruleInstall:
		return locales.PanelAlwaysAllowedInstallDetail(agentOf(n), nonEmpty(n.App, locales.PanelThisApp()), n.DeviceName)
	}
	return ""
}

// alwaysRow is how Settings › Safety › Always allowed shows an answer: the
// name its icon and first line show, the line under it, and the label of
// its button that takes it back.
func alwaysRow(x gateway.Exemption) (name, sub, remove string) {
	app := nonEmpty(x.AppName, x.AppID)
	if x.DeviceID == "" {
		return app, "", locales.SettingsSafetyAlwaysRemoveItem(ruleAction(x.Rule), app)
	}
	device := nonEmpty(x.DeviceName, x.DeviceID)
	switch x.Rule {
	case ruleAppLogs:
		return app, locales.SettingsSafetyAlwaysAgentOn(x.Agent, device),
			locales.SettingsSafetyAlwaysRemoveAgentItem(locales.RuleActionAppLogsOf(app), x.Agent, device)
	case ruleInstall:
		return app, locales.SettingsSafetyAlwaysAgentOnSigner(x.Agent, device, nonEmpty(x.SignerName, x.Signer)),
			locales.SettingsSafetyAlwaysRemoveAgentItem(locales.RuleActionInstallOf(app), x.Agent, device)
	}
	return device, x.Agent, locales.SettingsSafetyAlwaysRemoveAgentItem(locales.RuleActionPhoneSettings(), x.Agent, device)
}

// presetEvery is Settings.Preset for "Ask before every action"
// (policy.PresetEvery), while which Always allow answers do not apply.
const presetEvery = 3

// alwaysAllowLabel is the More options item: "Always allow payments in
// Dinely".
func alwaysAllowLabel(n gateway.NeedView) string {
	switch n.Rule {
	case rulePhoneSettings:
		return locales.NeedsAlwaysAllowSettings(agentOf(n), n.DeviceName)
	case ruleAppLogs:
		return locales.NeedsAlwaysAllowLogs(agentOf(n), nonEmpty(n.App, locales.PanelThisApp()), n.DeviceName)
	case ruleInstall:
		return locales.NeedsAlwaysAllowInstall(agentOf(n), nonEmpty(n.App, locales.PanelThisApp()), n.DeviceName)
	}
	if n.Rule != "" && n.App != "" {
		return locales.NeedsAlwaysAllowIn(kindNoun(n.Rule), n.App)
	}
	return locales.NeedsAlwaysAllow(kindNoun(n.Rule))
}

// alwaysState is Settings › Safety's list of Always allow answers.
type alwaysState struct {
	list     []gateway.Exemption
	loaded   bool
	seq      int
	lastLoad time.Time
}

func (s *alwaysState) load(a *app, k exemptionKeeper) {
	s.seq++
	seq := s.seq
	s.lastLoad = time.Now()
	go func() {
		list := k.Exemptions()
		a.post(func() {
			if seq != s.seq {
				return
			}
			s.list, s.loaded = list, true
		})
	}()
}

// stale makes Settings read the list again when it shows next.
func (s *alwaysState) stale() { s.lastLoad = time.Time{} }

// alwaysAllowed is Settings › Safety › Always allowed.
func (a *app) alwaysAllowed(c *mg.Context) {
	k, ok := a.api.(exemptionKeeper)
	if !ok {
		return
	}
	p := pal(c)
	st := &a.always
	if !st.loaded || time.Since(st.lastLoad) > 5*time.Second {
		st.load(a, k)
	}
	c.After(5 * time.Second)
	mg.Column(c).Gap(10).Label(locales.SettingsSafetyAlways()).Children(func() {
		settingsHeading(c, locales.SettingsSafetyAlways(), locales.SettingsSafetyAlwaysIntro())
		if a.settings.Preset == presetEvery && len(st.list) > 0 {
			mg.Text(c, locales.SettingsSafetyAlwaysEvery()).FontSize(fsSmall).LineHeight(1.4).TextColor(p.amber)
		}
		if !st.loaded {
			return
		}
		group(c).Children(func() {
			if len(st.list) == 0 {
				groupRow(c, mg.Row(c).AlignItems(mg.Center), true).Children(func() {
					mg.Text(c, locales.SettingsSafetyAlwaysNone()).FontSize(fsSmall).TextColor(p.ink3)
				})
			}
			for i, x := range st.list {
				x := x
				name, sub, remove := alwaysRow(x)
				row := mg.Row(c.Key("always-" + x.Rule + "\x00" + x.AppID + "\x00" + x.AppName + "\x00" + x.Agent + "\x00" + x.DeviceID + "\x00" + x.Signer)).Gap(sp3).AlignItems(mg.Center)
				groupRow(c, row, i == len(st.list)-1).Padding(sp2, sp3, sp2, sp4).Children(func() {
					if x.DeviceID != "" && x.Rule == rulePhoneSettings {
						mg.Row(c).Size(28, 28).Radius(7).Center().Background(p.bg3).Children(func() {
							mg.Icon(c, icSettings).FontSize(15).TextColor(p.ink2)
						})
					} else {
						appIcon(c, name, 28, 7, 13, "")
					}
					mg.Column(c).Grow(1).MinWidth(0).Children(func() {
						mg.Text(c, name).FontWeight(fwMedium).SingleLine()
						if sub != "" {
							mg.Text(c, sub).FontSize(fsCaption).TextColor(p.ink3).SingleLine()
						}
					})
					mg.Text(c, ruleName(x.Rule)).FontSize(fsSmall).TextColor(p.ink2)
					// While rules.yaml cannot be used, the gateway would refuse.
					off := a.fileBroken(rulesYAML)
					rm := mg.ButtonBase(c).Size(26, 26).Radius(rSmall).Center().Disabled(off).
						Label(remove).Tooltip(locales.SettingsSafetyAlwaysRemove())
					if off {
						rm.Opacity(0.35)
					} else if rm.Hovered() {
						rm.Background(p.bg3)
					}
					if rm.Clicked() {
						a.takeBackAlways(k, x)
					}
					rm.TextColor(p.ink3).Children(func() { mg.Icon(c, icX).FontSize(14) })
				})
			}
		})
	})
}

// takeBackAlways removes an Always allow answer: its rules ask again in
// its app.
func (a *app) takeBackAlways(k exemptionKeeper, x gateway.Exemption) {
	st := &a.always
	kept := st.list[:0:0]
	for _, y := range st.list {
		if y.Rule != x.Rule || y.AppID != x.AppID || y.AppName != x.AppName || y.Agent != x.Agent || y.DeviceID != x.DeviceID || y.Signer != x.Signer {
			kept = append(kept, y)
		}
	}
	st.list = kept
	st.seq++ // a read in flight may still list it
	a.call("", locales.CallTakeBackAlways(), func() error {
		err := k.RemoveExemption(x)
		a.post(st.stale)
		return err
	})
}
