// Package policy decides whether an agent's action may run (Allow), needs the
// person's approval (Ask) or is refused (Deny). It detects commits such as
// Pay or Send from the tool call in context, matches the rules file, and
// applies read-only mode, app scopes and the credential rule.
//
// The rules file is the only source of truth for rules. When several rules
// match, the strictest wins: Deny, then Ask, then Allow. The person's
// "Always allow" answers (Exemption, kept in the rules file too) lift asks
// in one app, never a deny.
package policy

import (
	"fmt"
	"strings"

	"github.com/DIYgod/pinky/internal/screen"
)

// Decision is a rule's verdict. A larger value is stricter.
type Decision int

// Decisions, from the most permissive to the strictest.
const (
	Allow Decision = iota
	Ask
	Deny
)

// String returns "allow", "ask" or "deny".
func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	case Deny:
		return "deny"
	}
	return fmt.Sprintf("decision(%d)", int(d))
}

// ParseDecision parses "allow", "ask" or "deny".
func ParseDecision(s string) (Decision, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow, nil
	case "ask":
		return Ask, nil
	case "deny":
		return Deny, nil
	}
	return Allow, fmt.Errorf("unknown decision %q (want allow, ask or deny)", s)
}

// Kind is a policy action detected from a tool call in context.
type Kind string

// Action kinds. The first six are the ones in DESIGN.md; ChangeSetting
// covers "change account or security settings" from the Ask tier.
const (
	Commit        Kind = "commit"
	Payment       Kind = "payment"
	SendMessage   Kind = "send_message"
	InstallApp    Kind = "install_app"
	OpenURL       Kind = "open_url"
	Call          Kind = "call"
	ChangeSetting Kind = "change_setting"
)

// Op is the tool operation being checked.
type Op string

// Operations.
const (
	OpTap    Op = "tap"
	OpType   Op = "type"
	OpSwipe  Op = "swipe"
	OpKey    Op = "key"
	OpLaunch Op = "launch"
)

// Built-in rule names, reported when no rule from the file decided.
const (
	RuleCredentials = "credentials"
	RuleReadOnly    = "Read-only"
	RuleTaskApps    = "Task apps"
	RuleAgentScope  = "Agent scope"
)

// Input is one action in context.
type Input struct {
	Op     Op
	Device string // device id, for per-device read-only mode
	// App is the foreground app. For OpLaunch, Launch is the app being
	// opened and App is ignored; for OpInstall, OpUninstall and OpStop
	// (appchange.go), Launch is the app acted on.
	App    screen.App
	Launch screen.App
	// LaunchURL is set when the launch target is a link rather than an
	// app; Link is the link, and Launch the app that opens it.
	LaunchURL bool
	Link      string
	Screen    *screen.Screen  // the current screen, may be nil
	Target    *screen.Element // the element acted on, may be nil
	Text      string          // OpType
	Submit    bool            // OpType: press Enter afterwards
	LongPress bool            // OpTap
	Key       string          // OpKey
	// TaskApps is the task's app allow-list (ids or names); empty means none.
	TaskApps []string
	// ScopeApps limits the agent to these apps (ids or names); empty means
	// every app.
	ScopeApps []string
	// ReadOnly forces read-only mode, for agents whose scope is read-only.
	ReadOnly bool
	// AllowBlocked is set when the person allowed the app for the task
	// although a rule blocks it (a finance app, or one they blocked): rules
	// that block whole apps do not apply to it. Every other rule still
	// does, and password managers stay blocked.
	AllowBlocked bool
}

// Verdict is the policy's answer for one action.
type Verdict struct {
	Decision Decision
	// Rule names the rule that decided, such as "Payments" or "credentials".
	// It is empty when nothing matched and the action is allowed.
	Rule  string
	Kinds []Kind
	// Asks lists every rule that asked, Rule first, when the decision is
	// Ask. Skipping the question (Allow for this task) needs all of them.
	Asks []string
	// Exempted lists the rules that would have asked but that the person
	// always allows in this app (Exemption). They are not in Asks; when
	// nothing else asks, the decision is Allow.
	Exempted []string
	// Scope marks a denial because the app is outside the agent's scope or
	// the task's allow-list; the gateway reports it as scope_denied.
	Scope bool
	// Reason is one plain sentence for the agent and the approval panel.
	Reason string
}

// Has reports whether the verdict detected the given kind.
func (v Verdict) Has(k Kind) bool {
	for _, x := range v.Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// Rule is one entry of the rules file.
type Rule struct {
	Name     string   `yaml:"name"`
	Match    Match    `yaml:"match"`
	Decision Decision `yaml:"decision"`
}

// Match selects actions. Every field that is set must match; within a field,
// any value matches.
type Match struct {
	// App lists package names, bundle ids or display names.
	App StringList `yaml:"app,omitempty"`
	// Category lists app categories from the app table, such as finance.
	Category StringList `yaml:"category,omitempty"`
	// Action lists kinds (commit, payment, ...) or operations (tap, type,
	// swipe, key, launch).
	Action StringList `yaml:"action,omitempty"`
	// Label lists case-insensitive substrings of the target element's label.
	Label StringList `yaml:"label,omitempty"`
}

// appLevel reports whether the match covers whole apps: no action or label.
func (m Match) appLevel() bool {
	return len(m.Action) == 0 && len(m.Label) == 0 && (len(m.App) > 0 || len(m.Category) > 0)
}

// StringList is a YAML scalar or sequence of strings.
type StringList []string
