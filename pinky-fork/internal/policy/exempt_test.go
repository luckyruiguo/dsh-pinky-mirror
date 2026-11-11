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

var pay = "Place order · $12.80"

func message(app screen.App, text string) Input {
	return Input{Op: OpType, App: app, Text: text, Submit: true, Target: &screen.Element{Role: screen.RoleInput, Label: "Message"}}
}

// exemptPayment answers a payment in Dinely with Always allow.
func exemptPayment(t *testing.T, e *Engine) Exemption {
	t.Helper()
	v := e.Evaluate(tap(dinely, pay))
	if v.Decision != Ask || !reflect.DeepEqual(v.Asks, []string{"Payments", "Commits"}) {
		t.Fatalf("payment before Always allow: %+v", v)
	}
	if !e.CanExempt(v.Asks, dinely) {
		t.Fatal("CanExempt is false for a payment in Dinely")
	}
	x, err := e.Exempt(v.Asks, dinely)
	if err != nil {
		t.Fatal(err)
	}
	want := Exemption{Rule: "Payments", With: StringList{"Commits"}, App: dinely.ID, AppName: dinely.Name}
	if !reflect.DeepEqual(x, want) {
		t.Fatalf("exemption %+v, want %+v", x, want)
	}
	return x
}

func TestExemptionLiftsOnlyItsAsks(t *testing.T) {
	e := commitsEngine(t)
	exemptPayment(t, e)

	v := e.Evaluate(tap(dinely, pay))
	if v.Decision != Allow || v.Rule != "" || len(v.Asks) != 0 || !reflect.DeepEqual(v.Exempted, []string{"Payments", "Commits"}) {
		t.Fatalf("payment in Dinely after Always allow: %+v", v)
	}
	// Another commit in the same app is not a payment: it still asks.
	if v := e.Evaluate(tap(dinely, "Delete")); v.Decision != Ask || v.Rule != "Commits" || len(v.Exempted) != 0 {
		t.Fatalf("delete in Dinely: %+v", v)
	}
	// A payment in another app still asks.
	if v := e.Evaluate(tap(play, "$4.99")); v.Decision != Ask || v.Rule != "Payments" {
		t.Fatalf("payment in Play Store: %+v", v)
	}
	// The same app reported by name only (some iPhone screens) is Dinely;
	// another app with Dinely's name but another id is not.
	if v := e.Evaluate(tap(screen.App{Name: "dinely"}, pay)); v.Decision != Allow {
		t.Fatalf("Dinely by name: %+v", v)
	}
	if v := e.Evaluate(tap(screen.App{Name: "Dinely", ID: "com.example.dinely"}, pay)); v.Decision != Ask {
		t.Fatalf("another app named Dinely: %+v", v)
	}
	// Payments alone (preset 2) are covered too.
	if err := e.SetPreset(PresetPayments); err != nil {
		t.Fatal(err)
	}
	if v := e.Evaluate(tap(dinely, pay)); v.Decision != Allow || !reflect.DeepEqual(v.Exempted, []string{"Payments"}) {
		t.Fatalf("payment with preset 2: %+v", v)
	}
}

func TestExemptionNeverLiftsDenies(t *testing.T) {
	f, err := ParseFile([]byte(`
preset: 1
rules:
  - name: Finance apps
    match: { category: finance }
    decision: deny
  - name: No deleting
    match: { label: delete }
    decision: deny
always_allow:
  - rule: Payments
    with: [Commits]
    app: com.dinely.app
    app_name: Dinely
  - rule: Commits
    app: com.dinely.app
  - rule: Payments
    with: [Commits]
    app: com.chase.sig.android
  - rule: Messages
    app: com.whatsapp
`))
	if err != nil {
		t.Fatal(err)
	}
	e := New(f, nil)
	if v := e.Evaluate(tap(dinely, pay)); v.Decision != Allow {
		t.Fatalf("exempted payment: %+v", v)
	}
	// A deny rule wins over the exemption for Commits in Dinely.
	if v := e.Evaluate(tap(dinely, "Delete")); v.Decision != Deny || v.Rule != "No deleting" {
		t.Fatalf("delete in Dinely: %+v", v)
	}
	// A blocked (finance) app stays blocked.
	if v := e.Evaluate(tap(chase, "Pay $3")); v.Decision != Deny || v.Rule != "Finance apps" {
		t.Fatalf("payment in Chase: %+v", v)
	}
	// Credentials are never typed.
	pw := Input{Op: OpType, App: dinely, Text: "x", Target: &screen.Element{Role: screen.RoleInput, States: []string{screen.StatePassword}}}
	if v := e.Evaluate(pw); v.Decision != Deny || v.Rule != RuleCredentials {
		t.Fatalf("password in Dinely: %+v", v)
	}
	// Read-only mode, for the phone or the agent, denies what would ask.
	if err := e.SetReadOnly("dev1", true); err != nil {
		t.Fatal(err)
	}
	in := tap(dinely, pay)
	in.Device = "dev1"
	if v := e.Evaluate(in); v.Decision != Deny || v.Rule != RuleReadOnly {
		t.Fatalf("payment on a read-only phone: %+v", v)
	}
	in = tap(dinely, pay)
	in.ReadOnly = true
	if v := e.Evaluate(in); v.Decision != Deny || v.Rule != RuleReadOnly {
		t.Fatalf("payment by a read-only agent: %+v", v)
	}
	// An agent's scope still holds.
	in = tap(dinely, pay)
	in.ScopeApps = []string{"Chatter"}
	if v := e.Evaluate(in); v.Decision != Deny || v.Rule != RuleAgentScope {
		t.Fatalf("payment outside the agent's scope: %+v", v)
	}
	// So does the task's app list: the message is always allowed in
	// WhatsApp, but WhatsApp is not one of the task's apps.
	in = message(whatsapp, "hi")
	if v := e.Evaluate(in); v.Decision != Allow {
		t.Fatalf("exempted message: %+v", v)
	}
	in.TaskApps = []string{"Dinely"}
	v := e.Evaluate(in)
	if v.Decision != Ask || v.Rule != RuleTaskApps || !reflect.DeepEqual(v.Asks, []string{RuleTaskApps}) ||
		!reflect.DeepEqual(v.Exempted, []string{"Messages"}) || !strings.Contains(v.Reason, "task's app list") {
		t.Fatalf("message outside the task's apps: %+v", v)
	}
	if e.CanExempt(v.Asks, whatsapp) {
		t.Fatal("a task's app list can be always allowed")
	}
	if _, err := e.Exempt(v.Asks, whatsapp); !errors.Is(err, ErrNotExemptable) {
		t.Fatalf("Exempt(Task apps) = %v", err)
	}
	launch := Input{Op: OpLaunch, Launch: whatsapp, TaskApps: []string{"Dinely"}}
	if v := e.Evaluate(launch); v.Decision != Deny || !v.Scope {
		t.Fatalf("launch outside the task's apps: %+v", v)
	}
}

func TestExemptionAnotherRuleStillAsks(t *testing.T) {
	f, err := ParseFile([]byte(`
preset: 1
rules:
  - name: Big orders
    match: { label: place order }
    decision: ask
always_allow:
  - rule: Payments
    with: [Commits]
    app: com.dinely.app
`))
	if err != nil {
		t.Fatal(err)
	}
	e := New(f, nil)
	v := e.Evaluate(tap(dinely, pay))
	if v.Decision != Ask || v.Rule != "Big orders" || !reflect.DeepEqual(v.Asks, []string{"Big orders"}) ||
		!reflect.DeepEqual(v.Exempted, []string{"Payments", "Commits"}) || !strings.Contains(v.Reason, "Big orders") {
		t.Fatalf("payment that Big orders asks about too: %+v", v)
	}
	// Always allow on that question adds Big orders, for Big orders.
	x, err := e.Exempt(v.Asks, dinely)
	if err != nil || x.Rule != "Big orders" || len(x.With) != 0 {
		t.Fatalf("second exemption %+v, %v", x, err)
	}
	if v := e.Evaluate(tap(dinely, pay)); v.Decision != Allow || len(v.Exempted) != 3 {
		t.Fatalf("payment after both: %+v", v)
	}
}

func TestExemptionOrderDoesNotMatter(t *testing.T) {
	big := Rule{Name: "Big orders", Match: Match{Label: StringList{"place order"}}, Decision: Ask}
	one := int(PresetCommits)
	payments := Exemption{Rule: "Payments", App: dinely.ID}
	commits := Exemption{Rule: "Commits", With: StringList{"Big orders"}, App: dinely.ID}
	var got []Verdict
	for _, order := range [][]Exemption{{payments, commits}, {commits, payments}} {
		e := New(File{Preset: &one, Rules: []Rule{big}, AlwaysAllow: order}, nil)
		got = append(got, e.Evaluate(tap(dinely, pay)))
	}
	if got[0].Decision != Allow || got[1].Decision != Allow {
		t.Fatalf("verdicts %+v", got)
	}
	// Commits alone does not lift the payment.
	e := New(File{Preset: &one, Rules: []Rule{big}, AlwaysAllow: []Exemption{commits}}, nil)
	if v := e.Evaluate(tap(dinely, pay)); v.Decision != Ask || v.Rule != "Payments" {
		t.Fatalf("payment with only Commits always allowed: %+v", v)
	}
}

func TestExemptionsAndEveryAction(t *testing.T) {
	e := commitsEngine(t)
	exemptPayment(t, e)
	if err := e.SetPreset(PresetEvery); err != nil {
		t.Fatal(err)
	}
	// While every action asks, so do the always allowed ones.
	v := e.Evaluate(tap(dinely, pay))
	if v.Decision != Ask || v.Rule != "Payments" || len(v.Exempted) != 0 {
		t.Fatalf("payment while every action asks: %+v", v)
	}
	if e.CanExempt(v.Asks, dinely) {
		t.Fatal("Always allow offered while every action asks")
	}
	if _, err := e.Exempt(v.Asks, dinely); !errors.Is(err, ErrNotExemptable) {
		t.Fatalf("Exempt while every action asks: %v", err)
	}
	if v := e.Evaluate(tap(dinely, "Orders")); v.Decision != Ask || v.Rule != RuleEvery {
		t.Fatalf("tap while every action asks: %+v", v)
	}
	// They are kept, and apply again with another preset.
	if len(e.Exemptions()) != 1 {
		t.Fatalf("exemptions %+v", e.Exemptions())
	}
	if err := e.SetPreset(PresetCommits); err != nil {
		t.Fatal(err)
	}
	if v := e.Evaluate(tap(dinely, pay)); v.Decision != Allow {
		t.Fatalf("payment back on preset 1: %+v", v)
	}
	// Without an app there is nothing to keep it for.
	if e.CanExempt([]string{"Payments"}, screen.App{}) {
		t.Fatal("Always allow offered without an app")
	}
}

func TestExemptionsSavedAndTakenBack(t *testing.T) {
	dir := t.TempDir()
	rules, apps := filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml")
	e, err := Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	// Messages and Commits ask only with preset 1.
	if err := e.SetPreset(PresetCommits); err != nil {
		t.Fatal(err)
	}
	// An earlier answer about payments alone takes Commits in later.
	if _, err := e.Exempt([]string{"Payments"}, dinely); err != nil {
		t.Fatal(err)
	}
	x := exemptPayment(t, commitsEngine(t)) // the same answer on a fresh engine
	if got, err := e.Exempt([]string{"Payments", "Commits"}, dinely); err != nil || !reflect.DeepEqual(got, x) {
		t.Fatalf("merged exemption %+v, %v", got, err)
	}
	if _, err := e.Exempt([]string{"Messages"}, whatsapp); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"always_allow:", "rule: Payments", "with: [Commits]", "app: com.dinely.app", "app_name: Dinely", "rule: Messages"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("rules.yaml lacks %q:\n%s", want, data)
		}
	}
	st, err := os.Stat(rules)
	if err != nil || !private(st) {
		t.Fatalf("rules.yaml: %v %v", st, err)
	}

	// After a restart.
	e, err = Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Exemptions(); len(got) != 2 || !reflect.DeepEqual(got[0], x) || got[1].Rule != "Messages" {
		t.Fatalf("exemptions after a restart: %+v", got)
	}
	if v := e.Evaluate(tap(dinely, pay)); v.Decision != Allow {
		t.Fatalf("payment after a restart: %+v", v)
	}

	// Taken back in Settings: payments in Dinely ask again.
	if err := e.Unexempt(x); err != nil {
		t.Fatal(err)
	}
	if err := e.Unexempt(x); err != nil {
		t.Fatalf("taking it back twice: %v", err)
	}
	if v := e.Evaluate(tap(dinely, pay)); v.Decision != Ask || v.Rule != "Payments" {
		t.Fatalf("payment after taking it back: %+v", v)
	}
	if err := e.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := e.Exemptions(); len(got) != 1 || got[0].Rule != "Messages" {
		t.Fatalf("exemptions after taking one back: %+v", got)
	}
	if v := e.Evaluate(message(whatsapp, "hi")); v.Decision != Allow {
		t.Fatalf("the other exemption: %+v", v)
	}
}

func TestExemptionsInTheRulesFile(t *testing.T) {
	bad := map[string]string{
		"no rule":      "always_allow:\n  - app: com.dinely.app\n",
		"no app":       "always_allow:\n  - rule: Payments\n",
		"task apps":    "always_allow:\n  - rule: Task apps\n    app: com.dinely.app\n",
		"every action": "always_allow:\n  - rule: Payments\n    with: [Every action]\n    app: com.dinely.app\n",
		"credentials":  "always_allow:\n  - rule: credentials\n    app_name: Dinely\n",
	}
	for name, src := range bad {
		if _, err := ParseFile([]byte(src)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	f, err := ParseFile([]byte("always_allow:\n  - rule: ' Payments '\n    with: Commits\n    app_name: ' Dinely '\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Exemption{{Rule: "Payments", With: StringList{"Commits"}, AppName: "Dinely"}}
	if !reflect.DeepEqual(f.AlwaysAllow, want) {
		t.Fatalf("parsed %+v, want %+v", f.AlwaysAllow, want)
	}
	// The bundled defaults keep none.
	if len(DefaultFile().AlwaysAllow) != 0 {
		t.Fatal("the defaults always allow something")
	}
}
