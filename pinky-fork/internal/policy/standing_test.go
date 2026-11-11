package policy

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// An Always allow on changing a phone's settings or reading an app's logs
// holds for one agent on one phone (and one app for logs), is saved with
// the others, never lifts a rule's asks, and goes with the phone.
func TestStandingAnswers(t *testing.T) {
	dir := t.TempDir()
	rules, apps := filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml")
	e, err := Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SetPreset(PresetPayments); err != nil {
		t.Fatal(err)
	}
	app := screen.App{ID: "dev.pinky.crashtest", Name: "Crash Test"}
	if !e.CanKeepStanding(Grant{Rule: RulePhoneSettings, Agent: "Claude Code", Device: "pixel"}) ||
		e.CanKeepStanding(Grant{Rule: "Payments", Agent: "Claude Code", Device: "pixel", App: app}) || e.CanKeepStanding(Grant{Rule: RulePhoneSettings, Agent: "", Device: "pixel"}) ||
		e.CanKeepStanding(Grant{Rule: RuleAppLogs, Agent: "Claude Code", Device: "pixel"}) {
		t.Fatal("CanKeepStanding")
	}
	if _, err := e.KeepStanding(Grant{Rule: "Payments", Agent: "Claude Code", Device: "pixel", DeviceName: "Pixel 9", App: app}); err != ErrNotExemptable {
		t.Fatalf("a rule's answer kept per phone: %v", err)
	}
	if _, err := e.KeepStanding(Grant{Rule: RulePhoneSettings, Agent: "Claude Code", Device: "pixel", DeviceName: "Pixel 9"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.KeepStanding(Grant{Rule: RuleAppLogs, Agent: "Claude Code", Device: "pixel", DeviceName: "Pixel 9", App: app}); err != nil {
		t.Fatal(err)
	}
	// Keeping it again changes nothing.
	if _, err := e.KeepStanding(Grant{Rule: RuleAppLogs, Agent: "Claude Code", Device: "pixel", DeviceName: "Pixel 9", App: app}); err != nil || len(e.Exemptions()) != 2 {
		t.Fatalf("again: %v %+v", err, e.Exemptions())
	}

	check := func(e *Engine, when string) {
		t.Helper()
		for _, c := range []struct {
			rule, agent, device string
			app                 screen.App
			want                bool
		}{
			{RulePhoneSettings, "Claude Code", "pixel", screen.App{}, true},
			{RulePhoneSettings, "Codex", "pixel", screen.App{}, false},
			{RulePhoneSettings, "Claude Code", "iphone", screen.App{}, false},
			{RuleAppLogs, "Claude Code", "pixel", app, true},
			{RuleAppLogs, "Claude Code", "pixel", screen.App{Name: "crash test"}, true},
			{RuleAppLogs, "Claude Code", "pixel", screen.App{ID: "com.example.notes", Name: "Notes"}, false},
			{RuleAppLogs, "Codex", "pixel", app, false},
		} {
			if got := e.Standing(Grant{Rule: c.rule, Agent: c.agent, Device: c.device, App: c.app}); got != c.want {
				t.Errorf("%s: Standing(%s, %s, %s, %+v) = %v", when, c.rule, c.agent, c.device, c.app, got)
			}
		}
	}
	check(e, "kept")

	// Saved, and read back.
	data, err := os.ReadFile(rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rule: Phone settings", "agent: Claude Code", "device: pixel", "device_name: Pixel 9", "app: dev.pinky.crashtest"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("rules.yaml lacks %q:\n%s", want, data)
		}
	}
	e, err = Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	check(e, "loaded")

	// Not while every action asks.
	if err := e.SetPreset(PresetEvery); err != nil {
		t.Fatal(err)
	}
	if e.Standing(Grant{Rule: RulePhoneSettings, Agent: "Claude Code", Device: "pixel"}) || e.CanKeepStanding(Grant{Rule: RulePhoneSettings, Agent: "Claude Code", Device: "pixel"}) {
		t.Fatal("applies while every action asks")
	}
	if err := e.SetPreset(PresetPayments); err != nil {
		t.Fatal(err)
	}

	// A rule of the person's own, even one with the same name, is not
	// lifted by them, and changing the rules keeps them.
	if err := e.SetFileRules(e.FileRules(), []Rule{{Name: RuleAppLogs, Decision: Ask, Match: Match{App: StringList{"Crash Test"}, Action: StringList{"tap"}}}}); err != nil {
		t.Fatal(err)
	}
	if v := e.Evaluate(Input{Op: OpTap, App: app, Target: &screen.Element{Label: "Crash"}}); v.Decision != Ask || len(v.Exempted) != 0 {
		t.Fatalf("verdict %+v", v)
	}
	if err := e.SetFileRules(e.FileRules(), nil); err != nil {
		t.Fatal(err)
	}
	if len(e.Exemptions()) != 2 {
		t.Fatalf("changing the rules dropped them: %+v", e.Exemptions())
	}

	// Taken back one by one: only its agent, phone and app.
	xs := e.Exemptions()
	if err := e.Unexempt(Exemption{Rule: RuleAppLogs, App: xs[1].App, AppName: xs[1].AppName, Agent: "Codex", Device: "pixel"}); err != nil {
		t.Fatal(err)
	}
	if len(e.Exemptions()) != 2 {
		t.Fatal("took back another agent's answer")
	}
	if err := e.Unexempt(xs[1]); err != nil {
		t.Fatal(err)
	}
	if e.Standing(Grant{Rule: RuleAppLogs, Agent: "Claude Code", Device: "pixel", App: app}) || !e.Standing(Grant{Rule: RulePhoneSettings, Agent: "Claude Code", Device: "pixel"}) {
		t.Fatal("after taking back the logs answer")
	}

	// Removing the phone drops the rest.
	if err := e.ForgetDevice("iphone"); err != nil || len(e.Exemptions()) != 1 {
		t.Fatalf("another phone: %v %+v", err, e.Exemptions())
	}
	if err := e.ForgetDevice("pixel"); err != nil || len(e.Exemptions()) != 0 {
		t.Fatalf("forget: %v %+v", err, e.Exemptions())
	}
	if e, err = Load(rules, apps); err != nil || len(e.Exemptions()) != 0 {
		t.Fatalf("after forgetting, loaded %v %+v", err, e.Exemptions())
	}
}

func TestStandingInTheRulesFile(t *testing.T) {
	bad := map[string]string{
		"no agent":           "always_allow:\n  - rule: Phone settings\n    device: pixel\n",
		"agent only":         "always_allow:\n  - rule: Phone settings\n    agent: Claude Code\n    app: x\n",
		"rule's answer":      "always_allow:\n  - rule: Payments\n    agent: Claude Code\n    device: pixel\n    app: com.dinely.app\n",
		"logs, no app":       "always_allow:\n  - rule: App logs\n    agent: Claude Code\n    device: pixel\n",
		"with":               "always_allow:\n  - rule: Phone settings\n    with: [Commits]\n    agent: Claude Code\n    device: pixel\n",
		"install, no signer": "always_allow:\n  - rule: Installing from the computer\n    agent: Claude Code\n    device: pixel\n    app: com.example.tool\n",
		"install, name only": "always_allow:\n  - rule: Installing from the computer\n    agent: Claude Code\n    device: pixel\n    app_name: Tool\n    signer: ab12\n",
		"install, no device": "always_allow:\n  - rule: Installing from the computer\n    app: com.example.tool\n    signer: ab12\n",
		"signer elsewhere":   "always_allow:\n  - rule: App logs\n    agent: Claude Code\n    device: pixel\n    app: com.example.tool\n    signer: ab12\n",
		"uninstall":          "always_allow:\n  - rule: Uninstalling apps\n    agent: Claude Code\n    device: pixel\n    app: com.example.tool\n",
	}
	for name, src := range bad {
		if _, err := ParseFile([]byte(src)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	f, err := ParseFile([]byte("always_allow:\n  - rule: Phone settings\n    agent: ' Claude Code '\n    device: pixel\n    device_name: Pixel 9\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Exemption{{Rule: RulePhoneSettings, Agent: "Claude Code", Device: "pixel", DeviceName: "Pixel 9"}}
	if !reflect.DeepEqual(f.AlwaysAllow, want) {
		t.Fatalf("parsed %+v, want %+v", f.AlwaysAllow, want)
	}
}

// Always allow on installing from the computer holds for one agent on one
// phone, for one app id signed by one signer: another signer, another id
// (even with the same name) or another agent or phone still asks, and the
// rules' own asks about installs are never lifted.
func TestStandingInstall(t *testing.T) {
	dir := t.TempDir()
	rules, apps := filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml")
	e, err := Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	tool := screen.App{ID: "com.example.tool", Name: "Tool"}
	grant := Grant{Rule: RuleInstall, Agent: "Claude Code", Device: "pixel", DeviceName: "Pixel 9", App: tool,
		Signer: "ab12cd34", SignerName: "Example Dev (ab12cd34)"}
	for _, g := range []Grant{
		{Rule: RuleInstall, Agent: "Claude Code", Device: "pixel", App: tool},                                         // no signer
		{Rule: RuleInstall, Agent: "Claude Code", Device: "pixel", App: screen.App{Name: "Tool"}, Signer: "ab12cd34"}, // no id
		{Rule: RuleUninstall, Agent: "Claude Code", Device: "pixel", App: tool},
	} {
		if e.CanKeepStanding(g) {
			t.Errorf("CanKeepStanding(%+v)", g)
		}
		if _, err := e.KeepStanding(g); err != ErrNotExemptable {
			t.Errorf("KeepStanding(%+v) = %v", g, err)
		}
	}
	if !e.CanKeepStanding(grant) {
		t.Fatal("CanKeepStanding")
	}
	x, err := e.KeepStanding(grant)
	if err != nil {
		t.Fatal(err)
	}
	want := Exemption{Rule: RuleInstall, App: "com.example.tool", AppName: "Tool", Agent: "Claude Code", Device: "pixel",
		DeviceName: "Pixel 9", Signer: "ab12cd34", SignerName: "Example Dev (ab12cd34)"}
	if !reflect.DeepEqual(x, want) {
		t.Fatalf("kept %+v", x)
	}
	// The same app and signer again keeps one answer; another signer is
	// another answer.
	if _, err := e.KeepStanding(grant); err != nil || len(e.Exemptions()) != 1 {
		t.Fatalf("again: %v %+v", err, e.Exemptions())
	}

	check := func(e *Engine, when string) {
		t.Helper()
		for _, c := range []struct {
			g    Grant
			want bool
		}{
			{grant, true},
			{Grant{Rule: RuleInstall, Agent: "Claude Code", Device: "pixel", App: screen.App{ID: "com.example.tool"}, Signer: "ab12cd34"}, true},
			{Grant{Rule: RuleInstall, Agent: "Claude Code", Device: "pixel", App: tool, Signer: "ffff0000"}, false},
			{Grant{Rule: RuleInstall, Agent: "Claude Code", Device: "pixel", App: tool}, false},
			{Grant{Rule: RuleInstall, Agent: "Claude Code", Device: "pixel", App: screen.App{ID: "com.example.other", Name: "Tool"}, Signer: "ab12cd34"}, false},
			{Grant{Rule: RuleInstall, Agent: "Claude Code", Device: "pixel", App: screen.App{Name: "Tool"}, Signer: "ab12cd34"}, false},
			{Grant{Rule: RuleInstall, Agent: "Codex", Device: "pixel", App: tool, Signer: "ab12cd34"}, false},
			{Grant{Rule: RuleInstall, Agent: "Claude Code", Device: "iphone", App: tool, Signer: "ab12cd34"}, false},
			{Grant{Rule: RuleUninstall, Agent: "Claude Code", Device: "pixel", App: tool, Signer: "ab12cd34"}, false},
			{Grant{Rule: RuleAppLogs, Agent: "Claude Code", Device: "pixel", App: tool}, false},
		} {
			if got := e.Standing(c.g); got != c.want {
				t.Errorf("%s: Standing(%+v) = %v", when, c.g, got)
			}
		}
	}
	check(e, "kept")
	data, err := os.ReadFile(rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"rule: Installing from the computer", "signer: ab12cd34", "signer_name: Example Dev (ab12cd34)", "app: com.example.tool"} {
		if !strings.Contains(string(data), s) {
			t.Errorf("rules.yaml lacks %q:\n%s", s, data)
		}
	}
	if e, err = Load(rules, apps); err != nil {
		t.Fatal(err)
	}
	check(e, "loaded")

	// The engine still asks: the answer is the gateway's to use.
	if v := e.Evaluate(Input{Op: OpInstall, Device: "pixel", Launch: tool}); v.Decision != Ask || v.Rule != RuleInstall {
		t.Fatalf("verdict %+v", v)
	}

	// Not while every action asks.
	if err := e.SetPreset(PresetEvery); err != nil {
		t.Fatal(err)
	}
	if e.Standing(grant) || e.CanKeepStanding(grant) {
		t.Fatal("applies while every action asks")
	}
	if err := e.SetPreset(PresetPayments); err != nil {
		t.Fatal(err)
	}

	// Another signer's answer is its own, and taking one back leaves the
	// other.
	other := grant
	other.Signer, other.SignerName = "ffff0000", "Someone Else (ffff0000)"
	if _, err := e.KeepStanding(other); err != nil || len(e.Exemptions()) != 2 {
		t.Fatalf("another signer: %v %+v", err, e.Exemptions())
	}
	if err := e.Unexempt(e.Exemptions()[0]); err != nil {
		t.Fatal(err)
	}
	if e.Standing(grant) || !e.Standing(other) {
		t.Fatalf("after taking back one: %+v", e.Exemptions())
	}
}
