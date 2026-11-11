package gateway

import (
	"path/filepath"
	"time"

	"github.com/DIYgod/pinky/internal/policy"
)

// RuleView is a policy rule, as Settings › Safety shows and edits it. An
// action matches when every field that is set matches; within a field,
// any value does.
type RuleView struct {
	Name string
	// Apps are package names, bundle ids or app names.
	Apps []string
	// Categories are app table categories (policy.Categories).
	Categories []string
	// Actions are kinds detected in context, such as commit or payment, or
	// operations, such as tap (policy.Actions).
	Actions []string
	// Labels are words in the label of the element acted on.
	Labels []string
	// Decision is "allow", "ask" or "deny".
	Decision string
}

// RulesView is what Settings › Safety shows of the policy rules.
type RulesView struct {
	// File holds the rules file's rules, in order: the ones the person
	// edits.
	File []RuleView
	// BuiltIn holds the rules Pinky adds, read-only: the preset's, which
	// come before File, then the apps blocked in Settings.
	BuiltIn []RuleView
}

// Rules returns the policy rules for Settings › Safety, after taking in
// hand edits of the rules files.
func (g *Gateway) Rules() RulesView {
	g.refreshRules()
	return RulesView{File: ruleViews(g.policy.FileRules()), BuiltIn: ruleViews(g.policy.BuiltInRules())}
}

// ErrRulesChanged means the rules a change was made from are no longer the
// rules file's: someone edited rules.yaml meanwhile. Nothing changed, and
// Rules lists the file's rules now.
var ErrRulesChanged = policy.ErrRulesChanged

// SetRules replaces the rules file's rules, in order, and saves the file
// (policy.Engine.SetFileRules), while the file's rules are still base, the
// ones the change was made from, as Rules listed them. Otherwise it
// returns ErrRulesChanged, so that it never undoes a hand edit. The
// strictest matching rule still wins.
func (g *Gateway) SetRules(base, rules []RuleView) error {
	from, err := policyRules(base)
	if err != nil {
		return err
	}
	to, err := policyRules(rules)
	if err != nil {
		return err
	}
	if err := g.policy.SetFileRules(from, to); err != nil {
		return err
	}
	g.log.Info("rules changed", "rules", len(to))
	return nil
}

func policyRules(rules []RuleView) ([]policy.Rule, error) {
	out := make([]policy.Rule, len(rules))
	for i, r := range rules {
		d, err := policy.ParseDecision(r.Decision)
		if err != nil {
			return nil, err
		}
		out[i] = policy.Rule{Name: r.Name, Decision: d, Match: policy.Match{
			App: r.Apps, Category: r.Categories, Action: r.Actions, Label: r.Labels}}
	}
	return out, nil
}

func ruleViews(rules []policy.Rule) []RuleView {
	out := make([]RuleView, len(rules))
	for i, r := range rules {
		m := r.Match
		out[i] = RuleView{Name: r.Name, Apps: m.App, Categories: m.Category, Actions: m.Action, Labels: m.Label,
			Decision: r.Decision.String()}
	}
	return out
}

// FileProblem is a rules file that was edited by hand but cannot be used,
// as Settings shows it: Pinky keeps the rules it had, and Settings does
// not change the file, until the person fixes it.
type FileProblem struct {
	File  string // "rules.yaml" or "apps.yaml"
	Path  string // where it is, to open it
	Error string // what is wrong, in English, as the YAML parser or the policy's checks say it
}

// FileProblems takes in hand edits of the rules files and lists those that
// cannot be used, rules.yaml first.
func (g *Gateway) FileProblems() []FileProblem {
	g.refreshRules()
	var out []FileProblem
	for _, p := range g.policy.Problems() {
		out = append(out, FileProblem{File: filepath.Base(p.Path), Path: p.Path, Error: p.Err.Error()})
	}
	return out
}

// rulesLoop takes in hand edits of rules.yaml and apps.yaml while Pinky
// runs, so that they apply to the next action without a restart.
func (g *Gateway) rulesLoop() {
	defer g.wg.Done()
	t := time.NewTicker(g.opt.RulesEvery)
	defer t.Stop()
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-t.C:
		}
		g.refreshRules()
	}
}

// refreshRules reads the policy's files again when they changed on disk,
// and logs what it found, with what changes from Settings found.
func (g *Gateway) refreshRules() {
	changes := g.policy.Refresh()
	for _, c := range changes {
		if c.Err != nil {
			g.log.Warn("rules file edited, but it cannot be used; keeping the rules in force", "path", c.Path, "err", c.Err)
		} else {
			g.log.Info("rules file edited; using it", "path", c.Path)
		}
	}
	if len(changes) > 0 {
		// An open approval may no longer offer Always allow, or may again
		// (NeedView.AlwaysAllow).
		g.emit(Event{Kind: EventNeeds})
	}
}
