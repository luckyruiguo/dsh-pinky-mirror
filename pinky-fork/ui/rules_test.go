package ui

import (
	"slices"
	"sync"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// The form's actions and categories are the policy's.
func TestRuleChoicesArePolicys(t *testing.T) {
	ids := func(list []named) []string {
		var out []string
		for _, n := range list {
			out = append(out, n.id)
		}
		return out
	}
	if got := ids(ruleActionNames); !slices.Equal(got, policy.Actions) {
		t.Errorf("actions %q, policy has %q", got, policy.Actions)
	}
	if got := ids(ruleCategoryNames); !slices.Equal(got, policy.Categories) {
		t.Errorf("categories %q, policy has %q", got, policy.Categories)
	}
}

// TestGatewayRules edits the rules in Settings › Safety against the real
// gateway: adds a rule that blocks, checks the form, changes the rule
// (which asks first, since it blocks), moves it, and removes rules.
func TestGatewayRules(t *testing.T) {
	ga := newGatewayApp(t, true)
	g := ga.w.Gateway
	late := &lateRules{Gateway: g}
	ga.app.api = late
	ga.page, ga.set.sec = pageSettings, secSafety
	tt := mg.NewTester(ga.view, 1280, 2600)
	ga.waitFor(t, "the rules", func() bool { tt.Frame(); return ga.rules.loaded })
	tt.Frame()
	for _, want := range []string{"Rules", "Your rules", "Finance apps", "Password managers", "Built in", "Payments",
		"Blocked by you", "Passwords and codes"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in Settings › Safety: %q", want, tt.Texts())
		}
	}
	// The default asks before payments only; preset 1 adds its rules.
	if hasText(tt, "Installing apps, Opening links") {
		t.Errorf("preset 1's rules show with the default: %q", tt.Texts())
	}
	if err := g.Policy().SetPreset(policy.PresetCommits); err != nil {
		t.Fatal(err)
	}
	ga.rules.lastLoad = time.Time{}
	ga.waitFor(t, "preset 1's rules", func() bool { tt.Frame(); return hasText(tt, "Installing apps, Opening links") })
	file := func() []string {
		var out []string
		for _, r := range g.Rules().File {
			out = append(out, r.Name)
		}
		return out
	}
	deleteIn := func(app screen.App) policy.Verdict {
		return g.Policy().Evaluate(policy.Input{Op: policy.OpTap, App: app,
			Target: &screen.Element{Role: screen.RoleButton, Label: "Delete chat", Bounds: screen.Rect{W: 100, H: 50}}})
	}
	chatter := screen.App{Name: "Chatter", ID: demo.Chatter}
	// saved waits until the gateway has the rules and the client has its
	// answer: the gateway changes before the client's goroutine reports
	// back, which a loaded machine may run late.
	saved := func(what string, rules func() bool) {
		t.Helper()
		ga.waitFor(t, what, func() bool { tt.Frame(); return !ga.rules.saving && !ga.rules.editing && rules() })
	}

	// The form says what is missing.
	if err := tt.Click("Add rule"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	save := func() {
		t.Helper()
		if err := tt.Click("Save"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	tt.Type("Finance apps") // into the name, which has the focus
	save()
	if !hasText(tt, "A rule is already called “Finance apps”.") {
		t.Fatalf("a name taken: %q", tt.Texts())
	}
	ga.rules.form.name = " "
	save()
	if !hasText(tt, "Give the rule a name.") {
		t.Fatalf("no name: %q", tt.Texts())
	}
	ga.rules.form.name = "Payments"
	save()
	if !hasText(tt, "A rule is already called “Payments”.") {
		t.Fatalf("a built-in name: %q", tt.Texts())
	}
	ga.rules.form.name = "No deleting in Chatter"
	save()
	if !hasText(tt, "Fill in at least one of apps, categories, actions or button text.") {
		t.Fatalf("no match: %q", tt.Texts())
	}

	// A rule that blocks deleting in Chatter. The gateway's answer reaches
	// the client late: until it does, the form stays open and saving.
	ga.rules.form.apps, ga.rules.form.labels, ga.rules.form.decision = "Chatter", "delete", "deny"
	if err := tt.Click("Commits"); err != nil { // a check box of the form
		t.Fatal(err)
	}
	release := late.hold()
	save()
	ga.waitFor(t, "the gateway to have the rule", func() bool { return len(file()) == 3 })
	tt.Frame()
	if !ga.rules.saving || !ga.rules.editing {
		t.Fatalf("the form closed before the gateway answered: saving %v, editing %v", ga.rules.saving, ga.rules.editing)
	}
	release()
	saved("the new rule", func() bool { return len(file()) == 3 })
	want := gateway.RuleView{Name: "No deleting in Chatter", Apps: []string{"Chatter"}, Actions: []string{"commit"},
		Labels: []string{"delete"}, Decision: "deny"}
	if got := g.Rules().File[2]; !ruleEqual(got, want) {
		t.Fatalf("rule %+v, want %+v", got, want)
	}
	if v := deleteIn(chatter); v.Decision != policy.Deny || v.Rule != "No deleting in Chatter" {
		t.Fatalf("delete in Chatter: %+v", v)
	}
	tt.Frame()
	if ga.rules.editing || !hasText(tt, "Chatter · Commits · text “delete”") {
		t.Fatalf("the list after adding: %q", tt.Texts())
	}

	// Changing a rule that blocks asks first.
	if err := tt.Click("Change No deleting in Chatter"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	ga.rules.form.decision = "ask"
	save()
	if !ga.rules.confirm || !hasText(tt, "Change “No deleting in Chatter”?") {
		t.Fatalf("no confirmation: %q", tt.Texts())
	}
	tt.Key(0, mg.KeyEnter) // the dialog's Change
	saved("the change", func() bool { return g.Rules().File[2].Decision == "ask" })
	if v := deleteIn(chatter); v.Decision != policy.Ask || !slices.Contains(v.Asks, "No deleting in Chatter") {
		t.Fatalf("delete in Chatter after the change: %+v", v)
	}

	// Moved up.
	if err := tt.Click("Move No deleting in Chatter up"); err != nil {
		t.Fatal(err)
	}
	saved("the move", func() bool {
		return slices.Equal(file(), []string{"Finance apps", "No deleting in Chatter", "Password managers"})
	})

	// Removing a rule that blocks asks first; Escape keeps it.
	if err := tt.Click("Remove Finance apps"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasText(tt, "Remove “Finance apps”?") {
		t.Fatalf("no confirmation: %q", tt.Texts())
	}
	tt.Key(0, mg.KeyEscape)
	tt.Frame()
	if ga.rules.confirm || len(file()) != 3 {
		t.Fatalf("Escape removed it: %v", file())
	}
	// One that asks goes at once.
	if err := tt.Click("Remove No deleting in Chatter"); err != nil {
		t.Fatal(err)
	}
	saved("the removal", func() bool { return slices.Equal(file(), []string{"Finance apps", "Password managers"}) })
	if v := deleteIn(chatter); v.Rule == "No deleting in Chatter" || slices.Contains(v.Asks, "No deleting in Chatter") {
		t.Fatalf("the removed rule still decides: %+v", v)
	}
}

// lateRules is the gateway with its answers to SetRules held back, while
// the test holds them, after the rules changed: a client goroutine that a
// loaded machine runs late.
type lateRules struct {
	*gateway.Gateway
	mu   sync.Mutex
	wait chan struct{}
}

func (l *lateRules) SetRules(base, rules []gateway.RuleView) error {
	err := l.Gateway.SetRules(base, rules)
	l.mu.Lock()
	wait := l.wait
	l.mu.Unlock()
	if wait != nil {
		<-wait
	}
	return err
}

// hold holds the answers until release.
func (l *lateRules) hold() (release func()) {
	ch := make(chan struct{})
	l.mu.Lock()
	l.wait = ch
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		l.wait = nil
		l.mu.Unlock()
		close(ch)
	}
}

func ruleEqual(a, b gateway.RuleView) bool {
	return a.Name == b.Name && a.Decision == b.Decision && slices.Equal(a.Apps, b.Apps) && slices.Equal(a.Categories, b.Categories) &&
		slices.Equal(a.Actions, b.Actions) && slices.Equal(a.Labels, b.Labels)
}
