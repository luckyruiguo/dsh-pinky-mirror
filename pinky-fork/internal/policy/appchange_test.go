package policy

import (
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

func appIn3(op Op, app screen.App) Input { return Input{Op: op, Launch: app, Device: "pixel"} }

func TestAppChangeAsks(t *testing.T) {
	e := Default()
	for _, c := range []struct {
		op   Op
		want Decision
		rule string
	}{
		{OpInstall, Ask, RuleInstall},
		{OpUninstall, Ask, RuleUninstall},
		{OpStop, Allow, ""},
	} {
		v := e.Evaluate(appIn3(c.op, dinely))
		if v.Decision != c.want || v.Rule != c.rule {
			t.Errorf("%s: %+v, want %s %q", c.op, v, c.want, c.rule)
		}
		if c.want == Ask && (len(v.Asks) != 1 || v.Asks[0] != c.rule || v.Reason == "") {
			t.Errorf("%s: asks %v reason %q", c.op, v.Asks, v.Reason)
		}
	}
	// Every preset asks before an install; "Every action" also before a stop.
	for _, p := range []Preset{PresetNone, PresetCommits, PresetPayments, PresetEvery} {
		if err := e.SetPreset(p); err != nil {
			t.Fatal(err)
		}
		if v := e.Evaluate(appIn3(OpInstall, dinely)); v.Decision != Ask || v.Rule != RuleInstall {
			t.Errorf("preset %d, install: %+v", p, v)
		}
		v := e.Evaluate(appIn3(OpStop, dinely))
		if p == PresetEvery {
			if v.Decision != Ask || v.Rule != RuleEvery {
				t.Errorf("every action, stop: %+v", v)
			}
		} else if v.Decision != Allow {
			t.Errorf("preset %d, stop: %+v", p, v)
		}
	}
}

func TestAppChangeRefuses(t *testing.T) {
	// No rules at all: the protections are built in.
	e := New(File{}, nil)
	e.AddApps(CatPasswords, CategoryEntries{IDs: []string{"com.example.vault"}})
	e.AddApps(CatBlocked, CategoryEntries{IDs: []string{"com.example.diary"}})
	for _, c := range []struct {
		app  screen.App
		rule string
	}{
		{screen.App{ID: "app.pinky.companion", Name: "Pinky Companion"}, RulePinkyApps},
		{screen.App{ID: "app.pinky.companion.dev"}, RulePinkyApps},
		{screen.App{ID: "app.pinky.companion@10"}, RulePinkyApps},
		{screen.App{ID: "com.diygod.WebDriverAgentRunner.xctrunner", Name: "WebDriverAgentRunner-Runner"}, RulePinkyApps},
		{screen.App{ID: "cc.diygod.iphoneuse.wda.xctrunner", Name: "Pinky Helper"}, RulePinkyApps},
		{launcher, RuleSystemApps},
		{chase, RuleFinance},
		{screen.App{ID: "com.example.vault", Name: "Vault"}, RulePasswords},
		{screen.App{ID: "com.example.diary", Name: "Diary"}, RuleBlocked},
	} {
		for _, op := range []Op{OpInstall, OpUninstall, OpStop} {
			in := appIn3(op, c.app)
			in.AllowBlocked = true // a task's unblock lifts nothing here
			v := e.Evaluate(in)
			if v.Decision != Deny || v.Rule != c.rule || v.Reason == "" {
				t.Errorf("%s %s: %+v, want deny by %q", op, c.app.ID, v, c.rule)
			}
		}
	}
	// A clone is judged as the app it copies.
	if v := e.Evaluate(appIn3(OpStop, screen.App{ID: "com.chase.sig.android@999", Name: "Chase (clone)"})); v.Decision != Deny {
		t.Errorf("a finance app's clone: %+v", v)
	}
}

func TestAppChangeScope(t *testing.T) {
	e := Default()
	other := screen.App{ID: "com.example.other", Name: "Other"}

	in := appIn3(OpInstall, other)
	in.ScopeApps = []string{"com.dinely.app"}
	if v := e.Evaluate(in); v.Decision != Deny || v.Rule != RuleAgentScope || !v.Scope {
		t.Errorf("install outside the agent's scope: %+v", v)
	}
	in.Launch = dinely
	if v := e.Evaluate(in); v.Decision != Ask {
		t.Errorf("install inside the agent's scope: %+v", v)
	}

	in = appIn3(OpUninstall, whatsapp)
	in.TaskApps = []string{"Dinely"}
	if v := e.Evaluate(in); v.Decision != Deny || v.Rule != RuleTaskApps || !v.Scope {
		t.Errorf("uninstall outside the task's apps: %+v", v)
	}
	in.Launch = dinely
	if v := e.Evaluate(in); v.Decision != Ask || v.Rule != RuleUninstall {
		t.Errorf("uninstall inside the task's apps: %+v", v)
	}
	in = appIn3(OpStop, whatsapp)
	in.TaskApps = []string{"Dinely"}
	if v := e.Evaluate(in); v.Decision != Deny || v.Rule != RuleTaskApps {
		t.Errorf("stop outside the task's apps: %+v", v)
	}
}

func TestAppChangeReadOnly(t *testing.T) {
	e := Default()
	for _, op := range []Op{OpInstall, OpUninstall, OpStop} {
		in := appIn3(op, dinely)
		in.ReadOnly = true
		if v := e.Evaluate(in); v.Decision != Deny || v.Rule != RuleReadOnly {
			t.Errorf("%s by a read-only agent: %+v", op, v)
		}
	}
	if err := e.SetReadOnly("pixel", true); err != nil {
		t.Fatal(err)
	}
	if v := e.Evaluate(appIn3(OpStop, dinely)); v.Decision != Deny || v.Rule != RuleReadOnly {
		t.Errorf("stop on a read-only phone: %+v", v)
	}
	if v := e.Evaluate(Input{Op: OpStop, Launch: dinely, Device: "other"}); v.Decision != Allow {
		t.Errorf("stop on another phone: %+v", v)
	}
	// "Every action" keeps asking about looking around in read-only mode,
	// but not about stopping an app.
	if err := e.SetPreset(PresetEvery); err != nil {
		t.Fatal(err)
	}
	if v := e.Evaluate(appIn3(OpStop, dinely)); v.Decision != Deny || v.Rule != RuleReadOnly {
		t.Errorf("stop on a read-only phone, every action: %+v", v)
	}
}

func TestAppChangeRulesAndAlwaysAllow(t *testing.T) {
	two := int(PresetPayments)
	f := File{Preset: &two, Rules: []Rule{
		{Name: "No Dinely", Match: Match{App: StringList{"com.dinely.app"}}, Decision: Deny},
		{Name: "Chats", Match: Match{Category: StringList{CatMessaging}}, Decision: Ask},
	}, AlwaysAllow: []Exemption{
		{Rule: RuleInstall, App: "com.whatsapp"},
		{Rule: RuleUninstall, App: "com.whatsapp"},
	}}
	e := New(f, nil)
	if v := e.Evaluate(appIn3(OpInstall, dinely)); v.Decision != Deny || v.Rule != "No Dinely" {
		t.Errorf("the person's deny rule: %+v", v)
	}
	// Always allow never lifts the install or uninstall question.
	if v := e.Evaluate(appIn3(OpInstall, whatsapp)); v.Decision != Ask || v.Rule != RuleInstall || len(v.Asks) != 2 || v.Asks[1] != "Chats" {
		t.Errorf("install with an always allow: %+v", v)
	}
	if v := e.Evaluate(appIn3(OpUninstall, whatsapp)); v.Decision != Ask || v.Rule != RuleUninstall {
		t.Errorf("uninstall with an always allow: %+v", v)
	}
	if v := e.Evaluate(appIn3(OpStop, whatsapp)); v.Decision != Ask || v.Rule != "Chats" {
		t.Errorf("stop under the person's ask rule: %+v", v)
	}
	// It lifts the person's own rules as for any action.
	f.AlwaysAllow = []Exemption{{Rule: "Chats", App: "com.whatsapp"}}
	e = New(f, nil)
	if v := e.Evaluate(appIn3(OpStop, whatsapp)); v.Decision != Allow {
		t.Errorf("stop with an always allow for the person's rule: %+v", v)
	}
	if v := e.Evaluate(appIn3(OpInstall, whatsapp)); v.Decision != Ask || v.Rule != RuleInstall || len(v.Asks) != 1 {
		t.Errorf("install with an always allow for the person's rule: %+v", v)
	}
}

func TestIsPinkyApp(t *testing.T) {
	for id, want := range map[string]bool{
		"app.pinky.companion":                         true,
		"app.pinky.companion.dev":                     true,
		"com.facebook.WebDriverAgentRunner.xctrunner": true,
		// Pinky Helper keeps whatever id it was installed with.
		"cc.diygod.iphoneuse.wda.xctrunner":               true,
		"pinky.ABCDE12345.WebDriverAgentRunner.xctrunner": true,
		"com.example.helper.XCTRunner":                    true,
		"com.example.UITests.xctrunner":                   true,
		"app.pinky.companionx":                            false,
		"com.dinely.app":                                  false,
		"com.example.xctrunner.app":                       false,
	} {
		if got := IsPinkyApp(screen.App{ID: id}); got != want {
			t.Errorf("%s: %v", id, got)
		}
	}
}
