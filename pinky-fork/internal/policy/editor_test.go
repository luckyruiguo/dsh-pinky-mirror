package policy

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

var chatter = screen.App{Name: "Chatter", ID: "com.chatter.app"}

func TestEditRules(t *testing.T) {
	dir := t.TempDir()
	rulesPath := filepath.Join(dir, "rules.yaml")
	e, err := Load(rulesPath, filepath.Join(dir, "apps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// Commits ask only with preset 1.
	if err := e.SetPreset(PresetCommits); err != nil {
		t.Fatal(err)
	}
	rules := e.FileRules()
	if len(rules) != 2 || rules[0].Name != "Finance apps" || rules[1].Name != "Password managers" {
		t.Fatalf("default rules %+v", rules)
	}
	rules = append(rules,
		Rule{Name: " Send in Chatter ", Match: Match{App: StringList{"com.chatter.app", " "}, Action: StringList{"commit", "send_message"}}, Decision: Ask},
		Rule{Name: "No deleting", Match: Match{Label: StringList{"delete"}}, Decision: Deny},
		Rule{Name: "Dinely is fine", Match: Match{App: StringList{"Dinely"}}, Decision: Allow},
	)
	if err := e.SetFileRules(e.FileRules(), rules); err != nil {
		t.Fatal(err)
	}
	got := e.FileRules()
	if got[2].Name != "Send in Chatter" || !reflect.DeepEqual(got[2].Match.App, StringList{"com.chatter.app"}) {
		t.Fatalf("not tidied: %+v", got[2])
	}
	// The preset's rules come first; the new rule asks too.
	if v := e.Evaluate(tap(chatter, "Send")); v.Decision != Ask || v.Rule != "Commits" || !reflect.DeepEqual(v.Asks, []string{"Commits", "Send in Chatter"}) {
		t.Fatalf("send in Chatter: %+v", v)
	}
	// The strictest still wins: a deny over the preset's ask, an ask over
	// an allow.
	if v := e.Evaluate(tap(chatter, "Delete chat")); v.Decision != Deny || v.Rule != "No deleting" {
		t.Fatalf("delete in Chatter: %+v", v)
	}
	if v := e.Evaluate(tap(dinely, pay)); v.Decision != Ask || v.Rule != "Payments" {
		t.Fatalf("payment in Dinely with an allow rule for it: %+v", v)
	}

	// Saved, and read back after a restart.
	data, err := os.ReadFile(rulesPath)
	if err != nil || !strings.Contains(string(data), "name: Send in Chatter") || !strings.Contains(string(data), "decision: deny") {
		t.Fatalf("rules.yaml (%v):\n%s", err, data)
	}
	e2, err := Load(rulesPath, filepath.Join(dir, "apps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e2.FileRules(), e.FileRules()) {
		t.Fatalf("after a restart %+v, want %+v", e2.FileRules(), e.FileRules())
	}

	// Order names the result among rules that ask alike.
	rules = e.FileRules()
	rules = append(rules, Rule{Name: "Chatter commits", Match: Match{App: StringList{"Chatter"}, Action: StringList{"commit"}}, Decision: Ask})
	if err := e.SetPreset(PresetNone); err != nil {
		t.Fatal(err)
	}
	if err := e.SetFileRules(e.FileRules(), rules); err != nil {
		t.Fatal(err)
	}
	if v := e.Evaluate(tap(chatter, "Send")); v.Rule != "Send in Chatter" {
		t.Fatalf("first rule: %+v", v)
	}
	n := len(rules)
	rules[n-1], rules[n-4] = rules[n-4], rules[n-1]
	if err := e.SetFileRules(e.FileRules(), rules); err != nil {
		t.Fatal(err)
	}
	if v := e.Evaluate(tap(chatter, "Send")); v.Rule != "Chatter commits" || len(v.Asks) != 2 {
		t.Fatalf("after moving Chatter commits up: %+v", v)
	}
}

func TestEditRulesChecks(t *testing.T) {
	e := Default()
	before := e.FileRules()
	ok := Rule{Name: "Chatter", Match: Match{App: StringList{"Chatter"}}, Decision: Ask}
	bad := map[string][]Rule{
		"no name":        {{Match: Match{App: StringList{"Chatter"}}, Decision: Ask}},
		"blank name":     {{Name: "  ", Match: Match{App: StringList{"Chatter"}}, Decision: Ask}},
		"matches all":    {{Name: "All", Match: Match{App: StringList{" "}}, Decision: Deny}},
		"unknown action": {{Name: "Fly", Match: Match{Action: StringList{"fly"}}, Decision: Ask}},
		"bad decision":   {{Name: "Odd", Match: Match{App: StringList{"Chatter"}}, Decision: Decision(7)}},
		"same name":      {ok, {Name: "chatter", Match: Match{Label: StringList{"x"}}, Decision: Deny}},
		"preset's name":  {{Name: "payments", Match: Match{App: StringList{"Dinely"}}, Decision: Ask}},
		"built-in name":  {{Name: RuleCredentials, Match: Match{Action: StringList{"type"}}, Decision: Deny}},
	}
	for name, rules := range bad {
		if err := e.SetFileRules(e.FileRules(), rules); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := e.SetFileRules(e.FileRules(), bad["same name"]); !errors.Is(err, ErrRuleNameTaken) {
		t.Errorf("same name: %v", err)
	}
	if err := e.SetFileRules(e.FileRules(), bad["preset's name"]); !errors.Is(err, ErrRuleNameReserved) {
		t.Errorf("preset's name: %v", err)
	}
	if got := e.FileRules(); !reflect.DeepEqual(got, before) {
		t.Fatalf("a refused change changed the rules: %+v", got)
	}

	// A file from before presets may keep a rule named like a preset's.
	f, err := ParseFile([]byte("rules:\n  - name: Payments\n    match: { action: payment }\n    decision: deny\n"))
	if err != nil {
		t.Fatal(err)
	}
	migrateLegacy(&f)
	e = New(f, nil)
	rules := e.FileRules()
	rules[0].Match.App = StringList{"Dinely"}
	if err := e.SetFileRules(e.FileRules(), rules); err != nil {
		t.Fatalf("keeping a legacy name: %v", err)
	}
}

func TestEditRulesDropsTheirAlwaysAllow(t *testing.T) {
	one := int(PresetCommits)
	big := Rule{Name: "Big orders", Match: Match{Label: StringList{"place order"}}, Decision: Ask}
	e := New(File{Preset: &one, Rules: []Rule{big}, AlwaysAllow: []Exemption{
		{Rule: "Big orders", App: dinely.ID},
		{Rule: "Payments", With: StringList{"Commits", "Big orders"}, App: dinely.ID},
		{Rule: "Messages", App: chatter.ID},
	}}, nil)
	if v := e.Evaluate(tap(dinely, pay)); v.Decision != Allow {
		t.Fatalf("payment before: %+v", v)
	}
	// Renamed, the rule is another one: its answers go.
	big.Name = "Large orders"
	if err := e.SetFileRules(e.FileRules(), []Rule{big}); err != nil {
		t.Fatal(err)
	}
	want := []Exemption{
		{Rule: "Payments", With: StringList{"Commits"}, App: dinely.ID},
		{Rule: "Messages", App: chatter.ID},
	}
	if got := e.Exemptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("exemptions %+v, want %+v", got, want)
	}
	if v := e.Evaluate(tap(dinely, pay)); v.Decision != Ask || v.Rule != "Large orders" {
		t.Fatalf("payment after: %+v", v)
	}
}

func TestBuiltInRules(t *testing.T) {
	e := Default()
	names := func() []string {
		var out []string
		for _, r := range e.BuiltInRules() {
			out = append(out, r.Name)
		}
		return out
	}
	if got := names(); !reflect.DeepEqual(got, []string{"Payments", RuleBlocked}) {
		t.Fatalf("built in with the default, preset 2: %q", got)
	}
	if err := e.SetPreset(PresetCommits); err != nil {
		t.Fatal(err)
	}
	want := []string{"Payments", "Messages", "Commits", "Installs and links", "Calls", "Settings changes", RuleBlocked}
	if got := names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("built in with preset 1: %q", got)
	}
	if err := e.SetPreset(PresetEvery); err != nil {
		t.Fatal(err)
	}
	if got := names(); len(got) != 8 || got[6] != RuleEvery {
		t.Fatalf("built in with preset 3: %q", got)
	}
	// Copies: changing them changes nothing.
	r := e.BuiltInRules()
	r[0].Match.Action[0] = "tap"
	if e.BuiltInRules()[0].Match.Action[0] != string(Payment) {
		t.Fatal("BuiltInRules shares its slices")
	}
}
