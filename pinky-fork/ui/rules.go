package ui

import (
	"errors"
	"slices"
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Settings › Safety › Rules: the rules file's rules, which the person adds,
// changes, moves and removes, and the rules Pinky adds, read-only. The
// gateway checks and saves them in rules.yaml; the strictest matching rule
// wins. Removing or changing a rule that blocks asks the person first.

// named is an id the gateway uses and how the person reads it.
type named struct {
	id   string
	name func() string
}

// ruleActions are the actions a rule may match, in the form's order: the
// gateway's policy.Actions.
var ruleActionNames = []named{
	{"commit", locales.SettingsRulesActionCommit},
	{"payment", locales.SettingsRulesActionPayment},
	{"send_message", locales.SettingsRulesActionSendMessage},
	{"install_app", locales.SettingsRulesActionInstallApp},
	{"open_url", locales.SettingsRulesActionOpenURL},
	{"call", locales.SettingsRulesActionCall},
	{"change_setting", locales.SettingsRulesActionChangeSetting},
	{"tap", locales.SettingsRulesActionTap},
	{"type", locales.SettingsRulesActionType},
	{"swipe", locales.SettingsRulesActionSwipe},
	{"key", locales.SettingsRulesActionKey},
	{"launch", locales.SettingsRulesActionLaunch},
}

// ruleCategoryNames are the app categories a rule may match: the gateway's
// policy.Categories.
var ruleCategoryNames = []named{
	{"finance", locales.SettingsSafetyKindFinance},
	{"passwords", locales.SettingsSafetyKindPasswords},
	{"messaging", locales.SettingsSafetyKindMessaging},
	{"social", locales.SettingsSafetyKindSocial},
	{"stores", locales.SettingsSafetyKindStores},
	{"browsers", locales.SettingsSafetyKindBrowsers},
	{"phone", locales.SettingsSafetyKindPhone},
	{"settings", locales.SettingsSafetyKindSettings},
	{"system", locales.SettingsSafetyKindSystem},
	{"blocked", locales.RuleBlockedByYou},
}

func nameOf(list []named, id string) string {
	for _, n := range list {
		if n.id == id {
			return n.name()
		}
	}
	return id
}

// decisions are the form's choices, the strictest last: Allow cannot stop
// another rule asking, so it is the last offered.
var decisions = []named{
	{"ask", locales.SettingsRulesAsk},
	{"deny", locales.SettingsRulesDeny},
	{"allow", locales.SettingsRulesAllow},
}

// ruleSummary says what a rule matches: "Chatter · Commits, Sending
// messages · text “delete”".
func ruleSummary(r gateway.RuleView) string {
	if r.Name == "Blocked by you" && len(r.Categories) == 1 && r.Categories[0] == "blocked" && len(r.Apps)+len(r.Actions)+len(r.Labels) == 0 {
		return locales.SettingsRulesBlockedDesc()
	}
	var parts []string
	if len(r.Apps) > 0 {
		parts = append(parts, strings.Join(r.Apps, ", "))
	}
	names := func(list []named, ids []string) string {
		out := make([]string, len(ids))
		for i, id := range ids {
			out[i] = nameOf(list, id)
		}
		return strings.Join(out, ", ")
	}
	if len(r.Categories) > 0 {
		parts = append(parts, names(ruleCategoryNames, r.Categories))
	}
	if len(r.Actions) > 0 {
		parts = append(parts, names(ruleActionNames, r.Actions))
	}
	if len(r.Labels) > 0 {
		parts = append(parts, locales.SettingsRulesLabelsSummary(strings.Join(r.Labels, ", ")))
	}
	return strings.Join(parts, " · ")
}

// rulesState is Settings › Safety › Rules.
type rulesState struct {
	view     gateway.RulesView
	loaded   bool
	seq      int
	lastLoad time.Time
	err      string // why the last change of a row was not saved

	editing  bool
	edit     int    // the rule in the form: an index of view.File, or len(view.File) for a new one
	editName string // the name the rule in the form had, to find it again when the rules change
	form     ruleForm
	formErr  string
	saving   bool

	// confirm holds a change that removes or changes a rule that blocks,
	// until the person confirms it, with the rules it was made from.
	confirm     bool
	pending     []gateway.RuleView
	pendingBase []gateway.RuleView
	pendingRule string
	pendingGone bool // removed rather than changed
	pendingForm bool // the change comes from the form
}

// ruleForm is the form of a rule being added or changed.
type ruleForm struct {
	name, apps, labels string
	decision           string // "ask", "deny" or "allow"
	actions, cats      map[string]bool
	others             []string // categories the form does not list, as from apps.yaml
}

func newRuleForm(r gateway.RuleView) ruleForm {
	f := ruleForm{name: r.Name, apps: strings.Join(r.Apps, ", "), labels: strings.Join(r.Labels, ", "),
		decision: r.Decision, actions: map[string]bool{}, cats: map[string]bool{}}
	for _, a := range r.Actions {
		f.actions[a] = true
	}
	for _, c := range r.Categories {
		if slices.ContainsFunc(ruleCategoryNames, func(n named) bool { return n.id == c }) {
			f.cats[c] = true
		} else {
			f.others = append(f.others, c)
		}
	}
	return f
}

// rule is the form's rule.
func (f ruleForm) rule() gateway.RuleView {
	r := gateway.RuleView{Name: strings.TrimSpace(f.name), Apps: splitApps(f.apps), Labels: splitApps(f.labels), Decision: f.decision}
	if r.Decision == "" {
		r.Decision = "ask"
	}
	for _, a := range ruleActionNames {
		if f.actions[a.id] {
			r.Actions = append(r.Actions, a.id)
		}
	}
	for _, c := range ruleCategoryNames {
		if f.cats[c.id] {
			r.Categories = append(r.Categories, c.id)
		}
	}
	r.Categories = append(r.Categories, f.others...)
	return r
}

func (s *rulesState) load(a *app, ed rulesEditor) {
	s.seq++
	seq := s.seq
	s.lastLoad = time.Now()
	go func() {
		v := ed.Rules()
		a.post(func() {
			if seq != s.seq {
				return
			}
			s.view, s.loaded = v, true
			s.findEdited()
		})
	}()
}

// findEdited points the form at the rule it changes in rules read again,
// by name: a hand edit of rules.yaml may have moved it. A rule that is
// gone is added when the form is saved.
func (s *rulesState) findEdited() {
	if !s.editing {
		return
	}
	s.edit = len(s.view.File)
	if s.editName == "" {
		return
	}
	for i, r := range s.view.File {
		if r.Name == s.editName {
			s.edit = i
			return
		}
	}
}

// open opens the form on the i-th rule, or on a new one with i ==
// len(view.File).
func (s *rulesState) open(i int, r gateway.RuleView) {
	s.editing, s.edit, s.form, s.formErr, s.editName = true, i, newRuleForm(r), "", ""
	if i < len(s.view.File) {
		s.editName = s.view.File[i].Name
	}
}

// rulesSection is Settings › Safety › Rules.
func (a *app) rulesSection(c *mg.Context) {
	ed, ok := a.api.(rulesEditor)
	if !ok {
		return
	}
	p := pal(c)
	st := &a.rules
	if !st.loaded || (!st.editing && !st.saving && time.Since(st.lastLoad) > 5*time.Second) {
		st.load(a, ed)
	}
	c.After(5 * time.Second)
	broken := a.fileBroken(rulesYAML)
	mg.Column(c).Gap(10).Label(locales.SettingsRules()).Children(func() {
		mg.Row(c).Gap(sp3).AlignItems(mg.End).Children(func() {
			settingsHeading(c, locales.SettingsRules(), locales.SettingsRulesIntro()).Grow(1).MinWidth(0)
			add := button(c, locales.SettingsRulesAdd(), btnOpt{small: true, icon: icPlus}).Disabled(!st.loaded || st.editing || st.saving || broken)
			if add.Clicked() {
				st.open(len(st.view.File), gateway.RuleView{Decision: "ask"})
			}
		})
		subHeading(c, locales.SettingsRulesYours(), "")
		group(c).Children(func() {
			if st.loaded && len(st.view.File) == 0 && !st.editing {
				groupRow(c, mg.Row(c).AlignItems(mg.Center), true).Children(func() {
					mg.Text(c, locales.SettingsRulesNone()).FontSize(fsSmall).TextColor(p.ink3)
				})
			}
			adding := st.editing && st.edit >= len(st.view.File)
			for i, r := range st.view.File {
				if st.editing && st.edit == i {
					a.ruleForm(c, ed)
					continue
				}
				a.ruleRow(c, ed, i, r, i == len(st.view.File)-1 && !adding)
			}
			if adding {
				a.ruleForm(c, ed)
			}
		})
		if st.err != "" {
			mg.Text(c, st.err).FontSize(fsSmall).TextColor(p.red)
		}
		subHeading(c, locales.SettingsRulesBuiltIn(), locales.SettingsRulesBuiltInDesc())
		group(c).Children(func() {
			for i, r := range st.view.BuiltIn {
				ruleLine(c, "builtin-"+r.Name, ruleName(r.Name), ruleSummary(r), r.Decision, i == len(st.view.BuiltIn)-1 && !st.loaded, nil)
			}
			if st.loaded {
				ruleLine(c, "builtin-credentials", locales.SettingsRulesCredentials(), locales.SettingsRulesCredentialsDesc(), "deny", true, nil)
			}
		})
	})
	if st.confirm {
		title := locales.SettingsRulesWeakenTitle(st.pendingRule)
		ok := locales.SettingsChange()
		if st.pendingGone {
			title, ok = locales.SettingsRulesRemoveTitle(st.pendingRule), locales.SettingsRulesRemoveConfirm()
		}
		switch mg.AlertDialog(c, &st.confirm, title, locales.SettingsRulesWeakenMessage(), locales.SettingsCancel(), ok) {
		case 1:
			a.saveRules(ed, st.pendingBase, st.pending, st.pendingForm)
		}
		if !st.confirm {
			st.pending, st.pendingBase = nil, nil
		}
	}
}

// ruleLine is a rule's row: its name, what it matches, what it does, and
// controls at the end.
func ruleLine(c *mg.Context, key, name, summary, decision string, last bool, controls func()) {
	p := pal(c)
	groupRow(c, mg.Row(c.Key(key)).Gap(sp3).AlignItems(mg.Center), last).Children(func() {
		mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			mg.Text(c, name).FontWeight(fwMedium)
			if summary != "" && summary != name {
				mg.Text(c, summary).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
			}
		})
		// What the rule does, as a chip in its color: allow, ask, block.
		col, tint := p.green, p.green.Alpha(0.12)
		switch decision {
		case "deny":
			col, tint = p.red, p.redTint
		case "ask":
			col, tint = p.amber, p.amberTint
		}
		mg.Text(c, nameOf(decisions, decision)).FontSize(fsCaption).FontWeight(fwSemibold).TextColor(col).
			Padding(2, sp2).Radius(rSmall).Background(tint).Shrink(0)
		if controls != nil {
			controls()
		}
	})
}

// iconButton is a small button with only an icon, named for assistive
// technology and in a tooltip.
func iconButton(c *mg.Context, ic *mg.SVG, label, tip string, disabled bool) mg.Element {
	p := pal(c)
	b := mg.ButtonBase(c).Size(26, 26).Radius(6).Center().Label(label).Tooltip(tip).Disabled(disabled)
	col := p.ink3
	switch {
	case disabled:
		b.Opacity(0.35)
	case b.Hovered():
		b.Background(p.bg3)
		col = p.ink
	}
	b.TextColor(col).Children(func() { mg.Icon(c, ic).FontSize(14) })
	return b
}

func (a *app) ruleRow(c *mg.Context, ed rulesEditor, i int, r gateway.RuleView, last bool) {
	st := &a.rules
	busy := st.editing || st.saving || a.fileBroken(rulesYAML)
	ruleLine(c, "rule-"+r.Name, r.Name, ruleSummary(r), r.Decision, last, func() {
		mg.Row(c).Gap(2).AlignItems(mg.Center).Shrink(0).Children(func() {
			change := button(c, locales.SettingsChange(), btnOpt{small: true}).Label(locales.SettingsRulesChangeLabel(r.Name)).Disabled(busy)
			if change.Clicked() {
				st.open(i, r)
			}
			list := st.view.File
			if iconButton(c, icChevU, locales.SettingsRulesMoveUp(r.Name), locales.SettingsRulesMoveUpTip(), busy || i == 0).Clicked() {
				a.saveRules(ed, list, moved(list, i, i-1), false)
			}
			if iconButton(c, icChevD, locales.SettingsRulesMoveDown(r.Name), locales.SettingsRulesMoveDownTip(), busy || i == len(list)-1).Clicked() {
				a.saveRules(ed, list, moved(list, i, i+1), false)
			}
			if iconButton(c, icX, locales.SettingsRulesRemove(r.Name), locales.SettingsRulesRemoveTip(), busy).Clicked() {
				rest := append(append([]gateway.RuleView(nil), list[:i]...), list[i+1:]...)
				if r.Decision == "deny" {
					a.confirmRules(list, rest, r.Name, true, false)
				} else {
					a.saveRules(ed, list, rest, false)
				}
			}
		})
	})
}

func moved(list []gateway.RuleView, from, to int) []gateway.RuleView {
	out := append([]gateway.RuleView(nil), list...)
	out[from], out[to] = out[to], out[from]
	return out
}

// ruleForm is the form of the rule being added or changed.
func (a *app) ruleForm(c *mg.Context, ed rulesEditor) {
	p := pal(c)
	st := &a.rules
	f := &st.form
	mg.Column(c.Key("rule-form")).Gap(sp3).Padding(sp4).Margin(sp2, sp3).Radius(rCard-2).Border(1, p.line2).Background(p.panel).Children(func() {
		field := func(label string, body func()) {
			mg.Column(c).Gap(sp1).Children(func() {
				mg.Text(c, label).FontSize(fsSmall).FontWeight(fwSemibold)
				body()
			})
		}
		field(locales.SettingsRulesName(), func() {
			mg.TextInput(c.Key("rule-name"), &f.name).Label(locales.SettingsRulesName()).Placeholder(locales.SettingsRulesNamePlaceholder()).MaxWidth(360).AutoFocus()
		})
		field(locales.SettingsRulesDecision(), func() {
			// The choice is kept as an id, so it survives a change of language.
			labels := make([]string, len(decisions))
			for i, d := range decisions {
				labels[i] = d.name()
			}
			choice := nameOf(decisions, f.decision)
			mg.Select(c.Key("rule-decision"), &choice, labels).Label(locales.SettingsRulesDecision()).MaxWidth(200)
			for _, d := range decisions {
				if d.name() == choice {
					f.decision = d.id
				}
			}
			if f.decision == "allow" {
				mg.Text(c, locales.SettingsRulesAllowHint()).FontSize(fsSmall).TextColor(p.amber)
			}
		})
		field(locales.SettingsRulesApps(), func() {
			mg.TextInput(c.Key("rule-apps"), &f.apps).Label(locales.SettingsRulesApps()).Placeholder(locales.SettingsRulesAppsPlaceholder())
		})
		checks := func(key string, list []named, on map[string]bool) {
			mg.Row(c).GapX(16).GapY(6).Wrap().Children(func() {
				for _, n := range list {
					v := on[n.id]
					if mg.Checkbox(c.Key(key+n.id), &v, n.name()).Changed() {
						on[n.id] = v
					}
				}
			})
		}
		field(locales.SettingsRulesCategories(), func() { checks("rule-cat-", ruleCategoryNames, f.cats) })
		field(locales.SettingsRulesActions(), func() { checks("rule-act-", ruleActionNames, f.actions) })
		field(locales.SettingsRulesLabels(), func() {
			mg.TextInput(c.Key("rule-labels"), &f.labels).Label(locales.SettingsRulesLabels()).Placeholder(locales.SettingsRulesLabelsPlaceholder())
		})
		mg.Text(c, locales.SettingsRulesHowItMatches()).FontSize(fsSmall).TextColor(p.ink3)
		if st.formErr != "" {
			mg.Text(c, st.formErr).FontSize(fsBody).TextColor(p.red)
		}
		mg.Row(c).Gap(8).Justify(mg.End).Children(func() {
			if button(c, locales.SettingsCancel(), btnOpt{}).Disabled(st.saving).Clicked() {
				st.editing, st.formErr = false, ""
			}
			if button(c, locales.SettingsSave(), btnOpt{kind: btnPrimary}).Disabled(st.saving || a.fileBroken(rulesYAML)).Clicked() {
				a.submitRuleForm(ed)
			}
		})
	})
}

// submitRuleForm checks the form and saves the rules with it.
func (a *app) submitRuleForm(ed rulesEditor) {
	st := &a.rules
	r := st.form.rule()
	if msg := a.ruleProblem(r, st.edit); msg != "" {
		st.formErr = msg
		return
	}
	list := append([]gateway.RuleView(nil), st.view.File...)
	if st.edit < len(list) {
		old := list[st.edit]
		list[st.edit] = r
		if old.Decision == "deny" && !sameMatch(old, r) {
			a.confirmRules(st.view.File, list, old.Name, false, true)
			return
		}
	} else {
		list = append(list, r)
	}
	a.saveRules(ed, st.view.File, list, true)
}

// ruleProblem says what is wrong with the form's rule, in the person's
// words, or "". The gateway checks again.
func (a *app) ruleProblem(r gateway.RuleView, edit int) string {
	if r.Name == "" {
		return locales.SettingsRulesErrName()
	}
	for i, o := range a.rules.view.File {
		if i != edit && strings.EqualFold(o.Name, r.Name) {
			return locales.SettingsRulesErrNameTaken(r.Name)
		}
	}
	for _, o := range a.rules.view.BuiltIn {
		if strings.EqualFold(o.Name, r.Name) || strings.EqualFold(ruleName(o.Name), r.Name) {
			return locales.SettingsRulesErrNameTaken(r.Name)
		}
	}
	if len(r.Apps)+len(r.Categories)+len(r.Actions)+len(r.Labels) == 0 {
		return locales.SettingsRulesErrMatch()
	}
	return ""
}

// sameMatch reports whether a change keeps a rule's decision and match:
// a rename, which weakens nothing.
func sameMatch(a, b gateway.RuleView) bool {
	return a.Decision == b.Decision && slices.Equal(a.Apps, b.Apps) && slices.Equal(a.Categories, b.Categories) &&
		slices.Equal(a.Actions, b.Actions) && slices.Equal(a.Labels, b.Labels)
}

// confirmRules asks the person before a change that removes or changes a
// rule that blocks.
func (a *app) confirmRules(base, list []gateway.RuleView, rule string, gone, fromForm bool) {
	st := &a.rules
	st.confirm, st.pending, st.pendingRule, st.pendingGone, st.pendingForm = true, list, rule, gone, fromForm
	st.pendingBase = base
}

// saveRules hands the rules to the gateway, with base, the rules the
// change was made from. A change from the form keeps the form open until
// the gateway took it; the list's own changes show at once. When rules.yaml
// was edited by hand since base was read, nothing is saved: the list shows
// the file's rules, and the form stays open on the rule it changes, to
// save again.
func (a *app) saveRules(ed rulesEditor, base, list []gateway.RuleView, fromForm bool) {
	st := &a.rules
	st.saving, st.err = true, ""
	if !fromForm {
		st.view.File = list
	}
	st.seq++ // a read in flight has the old rules
	go func() {
		err := ed.SetRules(base, list)
		a.post(func() {
			st.saving = false
			if err != nil {
				msg := locales.SettingsRulesErrSave(err.Error())
				if errors.Is(err, gateway.ErrRulesChanged) {
					msg = locales.SettingsRulesErrChanged()
				}
				if fromForm {
					st.formErr = msg
				} else {
					st.err = msg
				}
			} else if fromForm {
				st.editing, st.formErr = false, ""
				st.view.File = list
			}
			// Read the rules back, and the Always allow answers, which lose
			// the rules that are gone. The form, still open after a refusal,
			// keeps the list from loading by itself.
			st.lastLoad = time.Time{}
			if st.editing {
				st.load(a, ed)
			}
			a.always.stale()
		})
	}()
}
