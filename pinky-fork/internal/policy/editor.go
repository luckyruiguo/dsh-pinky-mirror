package policy

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The rules file's rules, as Settings › Safety edits them. The editor
// writes the same rules.yaml a person may edit by hand, and the engine
// decides as before: the strictest matching rule wins.

// Actions lists what a rule's action may name: the kinds detected in
// context, then the operations.
var Actions = []string{
	string(Commit), string(Payment), string(SendMessage), string(InstallApp), string(OpenURL), string(Call), string(ChangeSetting),
	string(OpTap), string(OpType), string(OpSwipe), string(OpKey), string(OpLaunch),
}

// Categories lists the categories of the bundled app table, and the one
// that holds the apps the person blocks.
var Categories = []string{CatFinance, CatPasswords, CatMessaging, CatSocial, CatStores, CatBrowsers, CatPhone, CatSettings, CatSystem, CatBlocked}

// FileRules returns the rules file's own rules, in order: the ones
// Settings › Safety edits.
func (e *Engine) FileRules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return cloneRules(e.file.Rules)
}

// BuiltInRules returns the rules Pinky adds to the file's, in evaluation
// order: the preset's, which come before the file's, then the built-in
// ones after it (the apps blocked in Settings). Settings shows them
// read-only.
func (e *Engine) BuiltInRules() []Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return cloneRules(append(append([]Rule(nil), presetRules(e.presetLocked())...), builtinRules...))
}

// Errors from SetFileRules, besides the checks ParseFile makes.
var (
	ErrRuleNameTaken    = errors.New("two rules have the same name")
	ErrRuleNameReserved = errors.New("the name belongs to one of Pinky's own rules")
	// ErrRulesChanged means the file's rules are no longer the ones the
	// change was made from: rules.yaml was edited meanwhile. Nothing
	// changed; FileRules returns the file's rules now.
	ErrRulesChanged = errors.New("the rules in rules.yaml changed since they were read; nothing was saved")
)

// SetFileRules replaces the rules file's rules, in order, and saves the
// file, while the file's rules are still base, the ones the change was
// made from (FileRules when the person started it): it takes in hand
// edits of rules.yaml first, and returns ErrRulesChanged when one changed
// the rules, so that it never undoes the edit. It checks the rules as
// ParseFile does, and also refuses a name that two rules share or that
// one of Pinky's own rules has, so that a question names one rule. Always
// allow answers about a rule that is gone are dropped with it. The engine
// applies the rules even when saving fails, but not while rules.yaml has
// an edit that cannot be used, which it returns (FileError).
func (e *Engine) SetFileRules(base, rules []Rule) error {
	rules = tidyRules(rules)
	for i, r := range rules {
		if err := checkRule(i, r); err != nil {
			return err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.syncLocked(&e.rulesDisk); err != nil {
		return err
	}
	if !sameRules(base, e.file.Rules) {
		return ErrRulesChanged
	}
	if sameRules(rules, e.file.Rules) {
		return nil
	}
	old := map[string]bool{}
	for _, r := range e.file.Rules {
		old[r.Name] = true
	}
	seen := map[string]bool{}
	for _, r := range rules {
		k := strings.ToLower(r.Name)
		if seen[k] {
			return fmt.Errorf("%w: %q", ErrRuleNameTaken, r.Name)
		}
		seen[k] = true
		// A rule a file had before keeps its name, as one from before
		// presets may share a preset rule's.
		if reservedName(r.Name) && !old[r.Name] {
			return fmt.Errorf("%w: %q", ErrRuleNameReserved, r.Name)
		}
	}
	kept := map[string]bool{}
	for _, r := range rules {
		kept[r.Name] = true
	}
	gone := map[string]bool{}
	for name := range old {
		if !kept[name] {
			gone[name] = true
		}
	}
	e.file.Rules = rules
	e.dropExemptionsLocked(gone)
	return e.saveRulesLocked()
}

// sameRules reports whether two lists hold the same rules, in order, as
// tidyRules leaves them.
func sameRules(a, b []Rule) bool {
	return slices.EqualFunc(tidyRules(a), tidyRules(b), func(x, y Rule) bool {
		m, n := x.Match, y.Match
		return x.Name == y.Name && x.Decision == y.Decision && slices.Equal(m.App, n.App) &&
			slices.Equal(m.Category, n.Category) && slices.Equal(m.Action, n.Action) && slices.Equal(m.Label, n.Label)
	})
}

// reservedName reports whether a name belongs to one of Pinky's own
// rules: any preset's, or a built-in one.
func reservedName(name string) bool {
	for _, p := range []Preset{PresetCommits, PresetPayments, PresetEvery} {
		for _, r := range presetRules(p) {
			if strings.EqualFold(r.Name, name) {
				return true
			}
		}
	}
	for _, n := range []string{RuleBlocked, RuleCredentials, RuleReadOnly, RuleTaskApps, RuleAgentScope,
		RuleInstall, RuleUninstall, RulePinkyApps, RuleSystemApps} {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// dropExemptionsLocked takes the rules that are gone out of the Always
// allow answers, and drops the answers about them.
func (e *Engine) dropExemptionsLocked(gone map[string]bool) {
	if len(gone) == 0 {
		return
	}
	kept := e.file.AlwaysAllow[:0:0]
	for _, x := range e.file.AlwaysAllow {
		if x.standing() {
			// Not a rule's: the gateway's own approvals.
			kept = append(kept, x)
			continue
		}
		if gone[x.Rule] {
			continue
		}
		with := x.With[:0:0]
		for _, r := range x.With {
			if !gone[r] {
				with = append(with, r)
			}
		}
		if len(with) == 0 {
			with = nil
		}
		x.With = with
		kept = append(kept, x)
	}
	e.file.AlwaysAllow = kept
}

// tidyRules trims names and values and drops empty values.
func tidyRules(rules []Rule) []Rule {
	tidy := func(l StringList) StringList {
		var out StringList
		for _, s := range l {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	out := make([]Rule, len(rules))
	for i, r := range rules {
		out[i] = Rule{Name: strings.TrimSpace(r.Name), Decision: r.Decision, Match: Match{
			App: tidy(r.Match.App), Category: tidy(r.Match.Category), Action: tidy(r.Match.Action), Label: tidy(r.Match.Label)}}
	}
	return out
}

func cloneRules(rules []Rule) []Rule {
	out := make([]Rule, len(rules))
	for i, r := range rules {
		m := r.Match
		out[i] = Rule{Name: r.Name, Decision: r.Decision, Match: Match{
			App:      append(StringList(nil), m.App...),
			Category: append(StringList(nil), m.Category...),
			Action:   append(StringList(nil), m.Action...),
			Label:    append(StringList(nil), m.Label...),
		}}
	}
	return out
}
