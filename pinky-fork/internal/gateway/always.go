package gateway

import (
	"errors"
	"fmt"
	"strings"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// "Always allow" on an approval keeps the person's answer in the rules
// file (policy.Exemption): in the app, the rules that asked no longer ask
// about such steps, in later tasks and after a restart, until the person
// takes it back in Settings › Safety. It also allows the rest of the task,
// as Allow for this task does, which covers what an exemption may not
// lift: a task's app list. Approvals it cannot be kept for (the agent's
// own questions, copying files, opening apps and links, every action
// asking) count as Allow for this task, or as Allow once where that keeps
// nothing either (Gateway.Decide), and NeedView.AlwaysAllow tells the
// client not to offer it there.

// Exemption is one of the person's Always allow answers, as Settings ›
// Safety lists it: in the app, Rule no longer asks about the steps it asks
// about, nor do the rules in With about those steps.
//
// An answer to changing a phone's settings, reading an app's logs or
// installing an app names the agent and the phone it is for (Agent,
// DeviceID): that agent no longer asks about it on that phone, for App
// logs in that app only, and for an install for that app id signed by
// that signer only (Signer).
type Exemption struct {
	Rule    string   // the rule the approval named, such as "Payments"
	With    []string // rules that asked with it, such as "Commits"
	AppID   string   // package name or bundle id; empty when the phone gave none
	AppName string   // the app's name; empty when the phone gave none
	// Agent, DeviceID and DeviceName are set on an answer about "Phone
	// settings", "App logs" or "Installing from the computer": the agent's
	// name and the phone's id and name. Empty on the others, which apply
	// to every agent and phone.
	Agent      string
	DeviceID   string
	DeviceName string
	// Signer and SignerName are set on an answer about "Installing from
	// the computer": the signer's key (certificate SHA-256 or team id)
	// and their name.
	Signer     string
	SignerName string
}

// Exemptions lists the person's Always allow answers, oldest first, after
// taking in hand edits of the rules files.
func (g *Gateway) Exemptions() []Exemption {
	g.refreshRules()
	xs := g.policy.Exemptions()
	out := make([]Exemption, len(xs))
	for i, x := range xs {
		out[i] = Exemption{Rule: x.Rule, With: append([]string(nil), x.With...), AppID: x.App, AppName: x.AppName,
			Agent: x.Agent, DeviceID: x.Device, DeviceName: x.DeviceName, Signer: x.Signer, SignerName: x.SignerName}
	}
	return out
}

// RemoveExemption takes back an Always allow, as Exemptions listed it: its
// rules ask again in its app.
func (g *Gateway) RemoveExemption(x Exemption) error {
	return g.policy.Unexempt(policy.Exemption{Rule: x.Rule, App: x.AppID, AppName: x.AppName, Agent: x.Agent, Device: x.DeviceID,
		Signer: x.Signer})
}

// canAlwaysAllow reports whether an approval's Always allow can be kept:
// an action the policy asked about, in an app, not a launch (whose app is
// not the one the panel names), with a rule an exemption may lift, or a
// change of the phone's settings, a read of an app's logs or an install
// (standingAsk);
// not while every action asks, and not while rules.yaml has a hand edit
// that cannot be used.
func (g *Gateway) canAlwaysAllow(info approval.Info) bool {
	if st, ok := standingOf(info); ok {
		return g.policy.CanKeepStanding(st.grant())
	}
	snap, ok := actionSnapshot(info)
	return ok && g.policy.CanExempt(snap.asks, snap.grantApp)
}

// standingAsk is the Data of an approval to change a phone's settings or
// read an app's logs, and part of an install's (appAsk): whom and what its
// Always allow would be for. It is kept per agent and phone, for logs per
// app too, since an agent only reads the logs of the app it names, and for
// an install per app id and signer, so that another app, or a build of the
// same id that someone else signed, still asks. Copying files, handing
// over a screen recording and uninstalling have none: those ask every
// time.
type standingAsk struct {
	rule       string // settingsRule, policy.RuleAppLogs or policy.RuleInstall
	agent      string
	device     string
	deviceName string
	app        screen.App // the app whose logs, or the app installed; zero for settings
	signer     string     // install: driver.Package.SignerKey
	signerName string     // install: driver.Package.Signer
}

// grant is what the policy keeps for the answer.
func (st *standingAsk) grant() policy.Grant {
	return policy.Grant{Rule: st.rule, Agent: st.agent, Device: st.device, DeviceName: st.deviceName,
		App: st.app, Signer: st.signer, SignerName: st.signerName}
}

// standingOf returns an approval's standingAsk.
func standingOf(info approval.Info) (*standingAsk, bool) {
	if info.Kind != approval.KindAction {
		return nil, false
	}
	switch d := info.Data.(type) {
	case *standingAsk:
		return d, true
	case *appAsk:
		return d.standing, d.standing != nil
	}
	return nil, false
}

// standingAllowed reports whether the person always allows the lease's
// agent what rule asks about on its phone (in app, for logs).
func (g *Gateway) standingAllowed(l *lease, rule string, app screen.App) bool {
	return g.policy.Standing(policy.Grant{Rule: rule, Agent: l.session.agent.Name, Device: l.dev.id, App: app})
}

// actionSnapshot returns an approval's action, unless it is a launch.
func actionSnapshot(info approval.Info) (*snapshot, bool) {
	snap, ok := info.Data.(*snapshot)
	return snap, ok && info.Kind == approval.KindAction && !snap.launch
}

// alwaysAllow keeps an approval's Always allow, when it can be kept. The
// engine applies it even when the rules file cannot be saved, which the
// error reports, as it does a rules.yaml that cannot be used (the panel
// may have offered Always allow before it broke).
func (g *Gateway) alwaysAllow(info approval.Info) error {
	if st, ok := standingOf(info); ok {
		x, err := g.policy.KeepStanding(st.grant())
		switch {
		case errors.Is(err, policy.ErrNotExemptable):
			return nil
		case err != nil:
			g.log.Warn("always allow: save the rules file", "rule", st.rule, "agent", st.agent, "device", st.device, "err", err)
			return fmt.Errorf("the step is allowed, but Always allow was not saved: %w", err)
		}
		g.log.Info("always allow", "rule", x.Rule, "agent", x.Agent, "device", x.Device, "app", appLabel(st.app))
		return nil
	}
	snap, ok := actionSnapshot(info)
	if !ok {
		return nil
	}
	x, err := g.policy.Exempt(snap.asks, snap.grantApp)
	switch {
	case errors.Is(err, policy.ErrNotExemptable):
		return nil
	case err != nil:
		g.log.Warn("always allow: save the rules file", "rule", x.Rule, "app", appLabel(snap.grantApp), "err", err)
		return fmt.Errorf("the step is allowed, but Always allow was not saved: %w", err)
	}
	g.log.Info("always allow", "rule", x.Rule, "with", x.With, "app", appLabel(snap.grantApp))
	return nil
}

// exemptedNote tells the agent that an action ran without asking because
// the person always allows it in the app.
func exemptedNote(res *Result, v policy.Verdict, p *prep) *Result {
	if res == nil || len(v.Exempted) == 0 || (res.Status != StatusSettled && res.Status != StatusUncertain) {
		return res
	}
	note := fmt.Sprintf("No approval needed: the person always allows %q in %s.", v.Exempted[0], exemptionApp(grantApp(p)))
	res.Message = strings.TrimSpace(note + " " + res.Message)
	return res
}

// exemptedLine is the second line of the step of an action that ran
// without asking because the person always allows it in the app, so that
// whoever reviews the task can tell a standing answer from one given then:
// "Always allowed: “Payments” in Dinely". It names the rule exemptedNote
// names; the client reads the line back by its pattern (ui/gwtext.go).
func exemptedLine(p *prep) string {
	return fmt.Sprintf("Always allowed: “%s” in %s", p.exempted[0], exemptionApp(grantApp(p)))
}

func exemptionApp(a screen.App) string {
	if l := appLabel(a); l != "" {
		return l
	}
	return "this app"
}
