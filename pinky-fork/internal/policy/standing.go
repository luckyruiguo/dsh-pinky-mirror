package policy

import (
	"fmt"
	"strings"

	"github.com/DIYgod/pinky/internal/screen"
)

// Some approvals do not come from the rules but from the gateway itself:
// changing a phone's settings (RulePhoneSettings), reading an app's logs
// (RuleAppLogs) and installing an app from the computer (RuleInstall).
// Their Always allow is an Exemption too, kept with the others under
// always_allow and listed in Settings › Safety, but it names the agent
// and the phone it is for (Exemption.Agent and Device); for App logs the
// app, and for an install the app id and the signer: the person trusts
// one agent with one phone, not every agent with every phone, and with
// builds of one app from one signer, not whatever an agent installs. The
// gateway asks with Standing before it asks the person; the engine never
// lifts a rule's asks with one.

// RulePhoneSettings is the rule changing the phone's settings (rotation,
// light or dark, location, language) asks under.
const RulePhoneSettings = "Phone settings"

// Grant is whom and what a standing Always allow is for.
type Grant struct {
	Rule       string // RulePhoneSettings, RuleAppLogs or RuleInstall
	Agent      string // the agent's name
	Device     string // the phone's id
	DeviceName string // how Settings names the phone; KeepStanding only
	// App is the app whose logs are read or which is installed; zero for
	// settings. An install needs its id.
	App screen.App
	// Signer identifies who signed an install exactly (driver.Package.
	// SignerKey: the certificate's SHA-256, or the team id), and
	// SignerName names them for the person (driver.Package.Signer).
	Signer     string
	SignerName string
}

// standingRule reports whether a rule's Always allow is kept per agent
// and phone.
func standingRule(rule string) bool {
	return rule == RulePhoneSettings || rule == RuleAppLogs || rule == RuleInstall
}

// standing reports whether an exemption is a per-agent and per-phone
// answer rather than one that lifts a rule's asks.
func (x Exemption) standing() bool { return x.Device != "" }

// checkStanding checks a rules file's always_allow entry that names a
// phone.
func checkStanding(x Exemption) error {
	switch {
	case !standingRule(x.Rule):
		return fmt.Errorf("always_allow %q names a device: only %q, %q and %q answers name an agent and a device",
			x.Rule, RulePhoneSettings, RuleAppLogs, RuleInstall)
	case x.Agent == "":
		return fmt.Errorf("always_allow %q names a device but no agent", x.Rule)
	case len(x.With) > 0:
		return fmt.Errorf("always_allow %q names a device, so it cannot list other rules", x.Rule)
	case x.Rule == RuleAppLogs && x.App == "" && x.AppName == "":
		return fmt.Errorf("always_allow %q has no app or app_name", x.Rule)
	case x.Rule == RuleInstall && (x.App == "" || x.Signer == ""):
		return fmt.Errorf("always_allow %q needs an app and a signer", x.Rule)
	case x.Rule != RuleInstall && (x.Signer != "" || x.SignerName != ""):
		return fmt.Errorf("always_allow %q names a signer: only %q answers do", x.Rule, RuleInstall)
	}
	return nil
}

// grantFor reports whether a standing exemption is for a grant.
func (x Exemption) grantFor(g Grant) bool {
	if !x.standing() || x.Rule != g.Rule || x.Agent != g.Agent || x.Device != g.Device {
		return false
	}
	switch g.Rule {
	case RuleAppLogs:
		return x.matches(g.App)
	case RuleInstall:
		// The exact app id and signer: a name proves nothing about a build.
		return x.App == clonePackage(strings.TrimSpace(g.App.ID)) && x.Signer == strings.TrimSpace(g.Signer)
	}
	return true
}

// Standing reports whether the person always allows an agent what a
// grant's rule asks about on a device: for RuleAppLogs in the app, for
// RuleInstall builds of the app by the signer. It does not while every
// action asks (PresetEvery).
func (e *Engine) Standing(g Grant) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if _, ok := e.standingLocked(g); !ok {
		return false
	}
	for _, x := range e.file.AlwaysAllow {
		if x.grantFor(g) {
			return true
		}
	}
	return false
}

// CanKeepStanding reports whether KeepStanding would keep an answer: not
// while every action asks, nor while rules.yaml has an edit that cannot
// be used (FileError).
func (e *Engine) CanKeepStanding(g Grant) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.standingLocked(g)
	return ok && e.rulesDisk.err == nil
}

// KeepStanding keeps an Always allow for an agent on a device (for
// RuleAppLogs in an app, for RuleInstall for an app and a signer), and
// saves the rules file. ErrNotExemptable means it cannot be kept; the
// engine applies it even when saving fails, but not while rules.yaml has
// an edit that cannot be used, which it returns (FileError).
func (e *Engine) KeepStanding(g Grant) (Exemption, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.syncLocked(&e.rulesDisk); err != nil {
		return Exemption{}, err
	}
	x, ok := e.standingLocked(g)
	if !ok {
		return Exemption{}, ErrNotExemptable
	}
	for i := range e.file.AlwaysAllow {
		y := &e.file.AlwaysAllow[i]
		if !y.standing() || y.Rule != x.Rule || y.Agent != x.Agent || y.Device != x.Device ||
			(x.Rule == RuleAppLogs && !y.sameApp(x)) ||
			(x.Rule == RuleInstall && (y.App != x.App || y.Signer != x.Signer)) {
			continue
		}
		changed := false
		update := func(dst *string, v string, always bool) {
			if v != "" && *dst != v && (always || *dst == "") {
				*dst, changed = v, true
			}
		}
		update(&y.DeviceName, x.DeviceName, true)
		update(&y.App, x.App, false)
		update(&y.AppName, x.AppName, x.Rule == RuleInstall)
		update(&y.SignerName, x.SignerName, true)
		if !changed {
			return *y, nil
		}
		return *y, e.saveRulesLocked()
	}
	e.file.AlwaysAllow = append(e.file.AlwaysAllow, x)
	return x, e.saveRulesLocked()
}

// standingLocked is the answer KeepStanding would keep.
func (e *Engine) standingLocked(g Grant) (Exemption, bool) {
	agent, device := strings.TrimSpace(g.Agent), strings.TrimSpace(g.Device)
	if !standingRule(g.Rule) || agent == "" || device == "" || e.presetLocked() == PresetEvery {
		return Exemption{}, false
	}
	x := Exemption{Rule: g.Rule, Agent: agent, Device: device, DeviceName: strings.TrimSpace(g.DeviceName)}
	switch g.Rule {
	case RuleAppLogs:
		x.App, x.AppName = clonePackage(strings.TrimSpace(g.App.ID)), strings.TrimSpace(g.App.Name)
		if x.App == "" && x.AppName == "" {
			return Exemption{}, false
		}
	case RuleInstall:
		x.App, x.AppName = clonePackage(strings.TrimSpace(g.App.ID)), strings.TrimSpace(g.App.Name)
		x.Signer, x.SignerName = strings.TrimSpace(g.Signer), strings.TrimSpace(g.SignerName)
		if x.App == "" || x.Signer == "" {
			return Exemption{}, false
		}
	}
	return x, true
}
