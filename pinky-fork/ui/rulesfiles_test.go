package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/ui/demogw"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// handEdit saves a rules file as most editors do: to a new file renamed
// over the old one.
func handEdit(t *testing.T, path, data string) {
	t.Helper()
	tmp := path + ".edit"
	if err := os.WriteFile(tmp, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// TestGatewayHandEditedRules edits rules.yaml by hand while Settings ›
// Safety shows, against the real gateway: the list shows the edit; Save in
// a form opened before another edit is refused, and saves on top of it the
// second time; an edit that does not parse shows a note and turns off what
// would change the file, until it is fixed.
func TestGatewayHandEditedRules(t *testing.T) {
	dir := t.TempDir()
	rules := filepath.Join(dir, "rules.yaml")
	ga := newGatewayApp(t, true, func(o *demogw.Options) { o.ConfigDir = dir })
	g := ga.w.Gateway
	ga.page, ga.set.sec = pageSettings, secSafety
	tt := mg.NewTester(ga.view, 1280, 2600)
	ga.waitFor(t, "the rules", func() bool { tt.Frame(); return ga.rules.loaded })
	read := func() string {
		t.Helper()
		data, err := os.ReadFile(rules)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	// Settings reads again at once rather than in a few seconds.
	reload := func() { ga.rules.lastLoad, ga.files.lastLoad = time.Time{}, time.Time{} }

	// A rule added by hand shows in the list.
	handEdit(t, rules, strings.Replace(read(), "\nrules:\n",
		"\nrules:\n  - name: Chatter asks\n    match: { app: Chatter, action: commit }\n    decision: ask\n", 1))
	reload()
	ga.waitFor(t, "the rule added by hand", func() bool { tt.Frame(); return hasText(tt, "Chatter asks") })

	// The form, opened before another edit: Save is refused, and the form
	// stays on its rule, to save again on top of the edit.
	if err := tt.Click("Change Chatter asks"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	ga.rules.form.labels = "delete"
	handEdit(t, rules, strings.Replace(read(), "name: Password managers", "name: Password apps", 1))
	if err := tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	ga.waitFor(t, "the refusal", func() bool { tt.Frame(); return hasText(tt, locales.SettingsRulesErrChanged()) })
	ga.waitFor(t, "the file's rules", func() bool {
		return slices.ContainsFunc(ga.rules.view.File, func(r gateway.RuleView) bool { return r.Name == "Password apps" })
	})
	if !ga.rules.editing || ga.rules.view.File[ga.rules.edit].Name != "Chatter asks" {
		t.Fatalf("the form after the refusal: editing %v on %d of %+v", ga.rules.editing, ga.rules.edit, ga.rules.view.File)
	}
	tt.Frame()
	if err := tt.Click("Save"); err != nil {
		t.Fatal(err)
	}
	ga.waitFor(t, "the change", func() bool {
		tt.Frame()
		f := g.Rules().File
		return !ga.rules.editing && len(f) == 3 && f[0].Name == "Chatter asks" && slices.Equal(f[0].Labels, []string{"delete"}) &&
			f[2].Name == "Password apps"
	})

	// An edit that does not parse: a note says what is wrong, here and in
	// Advanced, and what would change rules.yaml is off.
	broken := read() + "  - name: [Chatter\n"
	handEdit(t, rules, broken)
	reload()
	title := locales.SettingsFileProblemTitle("rules.yaml")
	ga.waitFor(t, "the note", func() bool { tt.Frame(); return hasText(tt, title) })
	for _, want := range []string{locales.SettingsFileProblemDesc(), "What's wrong: yaml: ", "Edit rules.yaml"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in the note: %q", want, tt.Texts())
		}
	}
	if _, ok := tt.Find(locales.SettingsNeedsYou()); !ok {
		t.Error("the Safety tab does not say it needs the person")
	}
	preset := ga.settings.Preset
	for _, label := range []string{locales.SettingsSafetyPresetPayments(), "Add rule", "Change Chatter asks", locales.SettingsSafetyReadOnly()} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if ga.settings.Preset != preset || ga.rules.editing || ga.settings.ReadOnly {
		t.Fatalf("Settings changed rules.yaml's settings: preset %d, form open %v, read-only %v",
			ga.settings.Preset, ga.rules.editing, ga.settings.ReadOnly)
	}
	// Blocked apps are in apps.yaml, which still works.
	if err := tt.Click("Add app"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !ga.set.adding {
		t.Error("Add app is off while only rules.yaml is broken")
	}
	ga.set.adding = false
	ga.set.sec = secAdvanced
	tt.Frame()
	if !hasText(tt, title) {
		t.Errorf("no note in Advanced: %q", tt.Texts())
	}
	ga.set.sec = secSafety
	if read() != broken {
		t.Fatalf("the broken rules.yaml was written over:\n%s", read())
	}

	// Fixed, the note goes, and Settings changes the file again.
	handEdit(t, rules, strings.TrimSuffix(broken, "  - name: [Chatter\n"))
	reload()
	ga.waitFor(t, "the note to go", func() bool { tt.Frame(); return !hasText(tt, title) })
	if err := tt.Click("Add rule"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !ga.rules.editing {
		t.Fatal("Add rule is off after the fix")
	}
}

// TestAppsFileProblem turns off only the blocked apps while apps.yaml
// cannot be used.
func TestAppsFileProblem(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.api.SetFileProblems([]gateway.FileProblem{{File: "apps.yaml", Path: "/config/apps.yaml",
		Error: "yaml: line 2: did not find expected node content"}})
	ta.page, ta.set.sec = pageSettings, secSafety
	tt := mg.NewTester(ta.view, 1280, 2600)
	title := locales.SettingsFileProblemTitle("apps.yaml")
	ta.waitFor(t, "the note", func() bool { tt.Frame(); return hasText(tt, title) })
	if !hasText(tt, "What's wrong: yaml: line 2: did not find expected node content") {
		t.Errorf("the note: %q", tt.Texts())
	}
	if err := tt.Click("Add app"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ta.set.adding {
		t.Error("Add app works while apps.yaml is broken")
	}
	if err := tt.Click(locales.SettingsSafetyPresetPayments()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ta.settings.Preset != 2 {
		t.Errorf("the preset did not change while only apps.yaml is broken: %d", ta.settings.Preset)
	}
}

// TestGatewaySettingsFollowHandEdits edits rules.yaml and apps.yaml by hand
// while Settings › Safety shows, against the real gateway: the preset,
// Read-only mode, Hide codes and the blocked apps show the edits; the next
// change reports them as the edits left them, so that it passes on only
// what the person changed; and a read that a change overtakes is dropped.
func TestGatewaySettingsFollowHandEdits(t *testing.T) {
	dir := t.TempDir()
	rules, apps := filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml")
	ga := newGatewayApp(t, true, func(o *demogw.Options) { o.ConfigDir = dir })
	pol := ga.w.Gateway.Policy()
	// As cmd/pinky's policySettings, with the person's blocked apps only.
	read := func(since int) (Settings, int, bool) {
		v := pol.Version()
		if v == since {
			return Settings{}, v, false
		}
		s := Settings{Preset: int(pol.Preset()), HideCodes: pol.HideCodes(), ReadOnly: pol.ReadOnly("")}
		for _, b := range pol.BlockedApps(nil) {
			s.BlockedApps = append(s.BlockedApps, BlockedApp{Name: nonEmpty(b.Name, b.ID), Kind: "Blocked by you"})
		}
		return s, v, true
	}
	ga.opt.GatewaySettings = read
	var reported [][2]Settings
	ga.opt.OnSettings = func(old, s Settings) {
		reported = append(reported, [2]Settings{old, s})
		// As cmd/pinky's applySettings: what differs, nothing else.
		if s.Preset != old.Preset && s.Preset >= 1 && s.Preset <= 3 {
			pol.SetPreset(policy.Preset(s.Preset))
		}
		if s.HideCodes != old.HideCodes {
			pol.SetHideCodes(s.HideCodes)
		}
		if s.ReadOnly != old.ReadOnly {
			pol.SetReadOnly("", s.ReadOnly)
		}
	}
	ga.page, ga.set.sec = pageSettings, secSafety
	tt := mg.NewTester(ga.view, 1280, 2600)
	// Settings reads again at once rather than in a few seconds.
	now := func() { ga.files.lastLoad, ga.gwSet.lastLoad = time.Time{}, time.Time{} }
	ga.waitFor(t, "the first read", func() bool { now(); tt.Frame(); return ga.gwSet.version >= 0 })
	if s := ga.settings; s.Preset != 2 || !s.HideCodes || s.ReadOnly || len(s.BlockedApps) != 0 {
		t.Fatalf("settings at first: %+v", s)
	}

	data, err := os.ReadFile(rules)
	if err != nil {
		t.Fatal(err)
	}
	handEdit(t, rules, strings.NewReplacer("\npreset: 2\n", "\npreset: 3\n", "\nmask_codes: true\n", "\nmask_codes: false\n",
		"\nread_only: false\n", "\nread_only: true\n").Replace(string(data)))
	handEdit(t, apps, "add:\n  blocked:\n    names: [Chatter]\n")
	ga.waitFor(t, "the hand edits", func() bool {
		now()
		tt.Frame()
		s := ga.settings
		return s.Preset == 3 && !s.HideCodes && s.ReadOnly && len(s.BlockedApps) == 1
	})
	if !hasText(tt, "Chatter") {
		t.Errorf("the app blocked by hand is not listed: %q", tt.Texts())
	}

	// Hide codes turned back on: the change has the hand edits as before.
	if err := tt.Click(locales.SettingsSafetyHideCodes()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(reported) != 1 {
		t.Fatalf("changes reported: %+v", reported)
	}
	if old, s := reported[0][0], reported[0][1]; old.Preset != 3 || !old.ReadOnly || old.HideCodes || len(old.BlockedApps) != 1 ||
		s.Preset != 3 || !s.ReadOnly || !s.HideCodes || len(s.BlockedApps) != 1 {
		t.Fatalf("the change: from %+v to %+v", old, s)
	}
	// It lands on top of the hand edits.
	if pol.Preset() != policy.PresetEvery || !pol.ReadOnly("") || !pol.HideCodes() {
		t.Fatalf("policy after the change: preset %d, read-only %v, hide codes %v", pol.Preset(), pol.ReadOnly(""), pol.HideCodes())
	}
	if data, _ := os.ReadFile(rules); !strings.Contains(string(data), "preset: 3") || !strings.Contains(string(data), "read_only: true") ||
		!strings.Contains(string(data), "mask_codes: true") {
		t.Fatalf("rules.yaml after the change:\n%s", data)
	}
	if _, ok := pol.Blocked(screen.App{Name: "Chatter"}); !ok {
		t.Fatal("the app blocked by hand is no longer blocked")
	}

	// A read that a change overtakes is dropped, and made again.
	release := make(chan struct{})
	ga.opt.GatewaySettings = func(int) (Settings, int, bool) { <-release; return Settings{Preset: 1}, 999, true }
	ga.gwSet.lastLoad = time.Time{}
	tt.Frame() // starts the read
	if err := tt.Click(locales.SettingsSafetyHideCodes()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	close(release)
	ga.waitFor(t, "the read to end", func() bool { return !ga.gwSet.reading })
	if ga.gwSet.version == 999 || ga.settings.Preset != 3 {
		t.Fatalf("an overtaken read was taken in: version %d, preset %d", ga.gwSet.version, ga.settings.Preset)
	}
}
