package policy

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

// loadIn loads the rules and apps files from a new folder, as the app
// does. Files in tests are written whole, so the engine reads a changed
// file without waiting for it to settle.
func loadIn(t *testing.T) (e *Engine, rules, apps string) {
	t.Helper()
	old := settle
	settle = func() {}
	t.Cleanup(func() { settle = old })
	dir := t.TempDir()
	rules, apps = filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml")
	e, err := Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	return e, rules, apps
}

// handEdit saves a file as most editors do: to a new file renamed over
// the old one.
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

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// fileHas checks that a file holds every one of want.
func fileHas(t *testing.T, path, what string, want ...string) {
	t.Helper()
	data := readFile(t, path)
	for _, w := range want {
		if !strings.Contains(data, w) {
			t.Errorf("%s: %s lacks %q:\n%s", what, filepath.Base(path), w, data)
		}
	}
}

// handRules is a rules file a person wrote, with a rule that blocks
// deleting and an Always allow for messages in WhatsApp.
const handRules = `# My rules.
preset: 1
mask_codes: false
rules:
  - name: Finance apps
    match: { category: finance }
    decision: deny
  - name: No deleting
    match: { label: delete }
    decision: deny
always_allow:
  - rule: Messages
    app: com.whatsapp
    app_name: WhatsApp
`

var deleteChat = tap(chatter, "Delete chat")

func TestHandEditTakesEffect(t *testing.T) {
	e, rules, _ := loadIn(t)
	if got := e.Refresh(); len(got) != 0 {
		t.Fatalf("changes right after Load: %+v", got)
	}
	// The default asks before payments only: a delete goes through.
	if v := e.Evaluate(deleteChat); v.Decision != Allow || !v.Has(Commit) {
		t.Fatalf("delete before the edit: %+v", v)
	}
	handEdit(t, rules, handRules)
	got := e.Refresh()
	if len(got) != 1 || got[0].Path != rules || got[0].Err != nil {
		t.Fatalf("changes after a hand edit: %+v", got)
	}
	if v := e.Evaluate(deleteChat); v.Decision != Deny || v.Rule != "No deleting" {
		t.Fatalf("delete after the edit: %+v", v)
	}
	if e.HideCodes() {
		t.Error("mask_codes: false is not in force")
	}
	if v := e.Evaluate(message(whatsapp, "hi")); v.Decision != Allow || !slices.Equal(v.Exempted, []string{"Messages"}) {
		t.Errorf("a message in WhatsApp: %+v", v)
	}
	if got := e.Refresh(); len(got) != 0 {
		t.Fatalf("changes with nothing new: %+v", got)
	}

	// A file touched without a change has nothing to take in.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(rules, later, later); err != nil {
		t.Fatal(err)
	}
	if got := e.Refresh(); len(got) != 0 {
		t.Fatalf("changes after a touch: %+v", got)
	}
	// Pinky's own writes are no hand edits.
	if err := e.SetPreset(PresetPayments); err != nil {
		t.Fatal(err)
	}
	if got := e.Refresh(); len(got) != 0 {
		t.Fatalf("changes after Settings wrote the file: %+v", got)
	}
}

// Parts of rules files a person writes.
const (
	financeRule        = "  - name: Finance apps\n    match: { category: finance }\n    decision: deny\n"
	noDeleting         = "  - name: No deleting\n    match: { label: delete }\n    decision: deny\n"
	chatterAsks        = "  - name: Chatter asks\n    match: { app: Chatter, action: commit }\n    decision: ask\n"
	messagesInWhatsApp = "  - rule: Messages\n    app: com.whatsapp\n    app_name: WhatsApp\n"
	paymentsInDinely   = "  - rule: Payments\n    app: com.dinely.app\n    app_name: Dinely\n"
)

// TestHandEditSurvivesSettings edits the files by hand between changes
// from Settings, with no Refresh in between: each change is made on top of
// the edit before it.
func TestHandEditSurvivesSettings(t *testing.T) {
	e, rules, apps := loadIn(t)

	handEdit(t, rules, handRules)
	if err := e.SetPreset(PresetPayments); err != nil {
		t.Fatal(err)
	}
	fileHas(t, rules, "SetPreset", "preset: 2", "mask_codes: false", "name: No deleting", "rule: Messages")
	if v := e.Evaluate(deleteChat); v.Decision != Deny || v.Rule != "No deleting" {
		t.Fatalf("delete after SetPreset: %+v", v)
	}

	// A rule added by hand, then Hide codes.
	handEdit(t, rules, "preset: 2\nmask_codes: false\nrules:\n"+financeRule+noDeleting+chatterAsks+
		"always_allow:\n"+messagesInWhatsApp)
	if err := e.SetHideCodes(true); err != nil {
		t.Fatal(err)
	}
	fileHas(t, rules, "SetHideCodes", "mask_codes: true", "preset: 2", "name: Chatter asks", "rule: Messages")

	// The Always allow taken out by hand, then one from an approval.
	handEdit(t, rules, "preset: 2\nmask_codes: true\nrules:\n"+financeRule+noDeleting+chatterAsks)
	v := e.Evaluate(tap(dinely, pay))
	if v.Decision != Ask || v.Rule != "Payments" {
		t.Fatalf("payment: %+v", v)
	}
	if _, err := e.Exempt(v.Asks, dinely); err != nil {
		t.Fatal(err)
	}
	fileHas(t, rules, "Exempt", "rule: Payments", "name: Chatter asks")
	if strings.Contains(readFile(t, rules), "rule: Messages") {
		t.Errorf("Exempt put back the Always allow taken out by hand:\n%s", readFile(t, rules))
	}

	// One put back by hand, then the other taken back in Settings.
	handEdit(t, rules, "preset: 2\nmask_codes: true\nrules:\n"+financeRule+noDeleting+chatterAsks+
		"always_allow:\n"+paymentsInDinely+messagesInWhatsApp)
	if err := e.Unexempt(Exemption{Rule: "Payments", App: dinely.ID, AppName: dinely.Name}); err != nil {
		t.Fatal(err)
	}
	fileHas(t, rules, "Unexempt", "rule: Messages", "name: Chatter asks")
	if got := e.Exemptions(); len(got) != 1 || got[0].Rule != "Messages" {
		t.Fatalf("exemptions after Unexempt: %+v", got)
	}

	// A phone made read-only by hand, then read-only mode for every phone.
	withPhone := "preset: 2\nmask_codes: true\nread_only_devices: [phone-9]\nrules:\n" + financeRule + noDeleting + chatterAsks +
		"always_allow:\n" + messagesInWhatsApp
	handEdit(t, rules, withPhone)
	if err := e.SetReadOnly("", true); err != nil {
		t.Fatal(err)
	}
	fileHas(t, rules, "SetReadOnly", "read_only: true", "- phone-9", "rule: Messages", "name: Chatter asks")

	// The rules editor: a change made from rules that a hand edit changed
	// since is refused, and leaves the file alone.
	base := e.FileRules()
	edited := "preset: 2\nmask_codes: true\nread_only: true\nrules:\n" + financeRule + noDeleting +
		strings.Replace(chatterAsks, "Chatter asks", "Chatter commits ask", 1) + "always_allow:\n" + messagesInWhatsApp
	handEdit(t, rules, edited)
	if err := e.SetFileRules(base, base[:1]); !errors.Is(err, ErrRulesChanged) {
		t.Fatalf("SetFileRules from rules changed by hand: %v", err)
	}
	if readFile(t, rules) != edited {
		t.Fatalf("a refused change wrote rules.yaml:\n%s", readFile(t, rules))
	}
	names := func() []string {
		var out []string
		for _, r := range e.FileRules() {
			out = append(out, r.Name)
		}
		return out
	}
	if got := names(); !slices.Equal(got, []string{"Finance apps", "No deleting", "Chatter commits ask"}) {
		t.Fatalf("rules after the refused change: %q", got)
	}
	// From the rules as they are now, it works, and keeps the rest.
	if err := e.SetFileRules(e.FileRules(), e.FileRules()[:2]); err != nil {
		t.Fatal(err)
	}
	if got := names(); !slices.Equal(got, []string{"Finance apps", "No deleting"}) {
		t.Fatalf("rules after the change: %q", got)
	}
	fileHas(t, rules, "SetFileRules", "preset: 2", "mask_codes: true", "read_only: true", "rule: Messages")

	// apps.yaml: an app blocked by hand, then one in Settings.
	handEdit(t, apps, "add:\n  blocked:\n    ids: [com.other.app]\n")
	if err := e.SetAppBlocked(chatter, true); err != nil {
		t.Fatal(err)
	}
	fileHas(t, apps, "SetAppBlocked", "com.other.app", "com.chatter.app")
	for _, a := range []screen.App{chatter, {ID: "com.other.app"}} {
		if _, ok := e.Blocked(a); !ok {
			t.Errorf("%s is not blocked", a.ID)
		}
	}
}

func TestInvalidHandEdit(t *testing.T) {
	e, rules, _ := loadIn(t)
	handEdit(t, rules, handRules)
	e.Refresh()

	broken := handRules + "  - rule: [Payments\n"
	handEdit(t, rules, broken)
	got := e.Refresh()
	if len(got) != 1 || got[0].Path != rules || got[0].Err == nil {
		t.Fatalf("changes after a broken edit: %+v", got)
	}
	var fe *FileError
	if p := e.Problems(); len(p) != 1 || p[0].Path != rules || !strings.Contains(p[0].Error(), "rules.yaml cannot be used") {
		t.Fatalf("problems %v", p)
	}
	// The rules in force stay.
	if v := e.Evaluate(deleteChat); v.Decision != Deny || v.Rule != "No deleting" {
		t.Fatalf("delete with a broken file: %+v", v)
	}

	// Settings neither changes them nor writes the file.
	_, exemptErr := e.Exempt([]string{"Payments", "Commits"}, dinely)
	for what, err := range map[string]error{
		"SetPreset":    e.SetPreset(PresetEvery),
		"SetHideCodes": e.SetHideCodes(true),
		"SetReadOnly":  e.SetReadOnly("", true),
		"SetFileRules": e.SetFileRules(e.FileRules(), nil),
		"Unexempt":     e.Unexempt(e.Exemptions()[0]),
		"Exempt":       exemptErr,
	} {
		if !errors.As(err, &fe) || fe.Path != rules {
			t.Errorf("%s with a broken file: %v", what, err)
		}
	}
	if e.CanExempt([]string{"Payments", "Commits"}, dinely) {
		t.Error("CanExempt with a broken file")
	}
	if e.Preset() != PresetCommits || e.HideCodes() || e.ReadOnly("") || len(e.FileRules()) != 2 || len(e.Exemptions()) != 1 {
		t.Errorf("Settings changed the rules in force: preset %d, codes %v, read-only %v, rules %+v, exemptions %+v",
			e.Preset(), e.HideCodes(), e.ReadOnly(""), e.FileRules(), e.Exemptions())
	}
	if readFile(t, rules) != broken {
		t.Fatalf("Settings wrote over the broken file:\n%s", readFile(t, rules))
	}
	// Told once.
	if got := e.Refresh(); len(got) != 0 {
		t.Fatalf("the same problem again: %+v", got)
	}
	// apps.yaml is a file of its own: Settings still blocks apps.
	if err := e.SetAppBlocked(chatter, true); err != nil {
		t.Fatalf("blocking an app with a broken rules.yaml: %v", err)
	}

	// A file that parses but fails the checks counts too.
	handEdit(t, rules, "rules:\n  - name: Everything\n    match: {}\n    decision: deny\n")
	got = e.Refresh()
	if len(got) != 1 || got[0].Err == nil || !strings.Contains(got[0].Err.Error(), "matches everything") {
		t.Fatalf("changes after an edit that fails the checks: %+v", got)
	}

	// Fixed, it takes effect, and Settings works again.
	handEdit(t, rules, strings.Replace(handRules, "preset: 1", "preset: 3", 1))
	got = e.Refresh()
	if len(got) != 1 || got[0].Err != nil {
		t.Fatalf("changes after the fix: %+v", got)
	}
	if e.Preset() != PresetEvery || len(e.Problems()) != 0 {
		t.Fatalf("after the fix: preset %d, problems %v", e.Preset(), e.Problems())
	}
	if err := e.SetHideCodes(true); err != nil {
		t.Fatal(err)
	}
	fileHas(t, rules, "after the fix", "preset: 3", "mask_codes: true", "name: No deleting")

	// Breaking it and putting back the version in force clears the
	// problem too.
	in := readFile(t, rules)
	handEdit(t, rules, "preset: [\n")
	e.Refresh()
	handEdit(t, rules, in)
	if got := e.Refresh(); len(got) != 1 || got[0].Err != nil || len(e.Problems()) != 0 {
		t.Fatalf("changes after putting the file back: %+v, problems %v", got, e.Problems())
	}
}

func TestInvalidAppsEdit(t *testing.T) {
	e, rules, apps := loadIn(t)
	if err := e.SetAppBlocked(chatter, true); err != nil {
		t.Fatal(err)
	}
	broken := "add:\n  blocked: [\n"
	handEdit(t, apps, broken)
	if got := e.Refresh(); len(got) != 1 || got[0].Path != apps || got[0].Err == nil {
		t.Fatalf("changes after a broken edit: %+v", got)
	}
	var fe *FileError
	if err := e.SetAppBlocked(dinely, true); !errors.As(err, &fe) || fe.Path != apps {
		t.Fatalf("SetAppBlocked with a broken apps.yaml: %v", err)
	}
	if _, ok := e.Blocked(chatter); !ok {
		t.Error("the app blocked before is no longer blocked")
	}
	if _, ok := e.Blocked(dinely); ok {
		t.Error("the refused change blocked the app")
	}
	if readFile(t, apps) != broken {
		t.Fatalf("Settings wrote over the broken file:\n%s", readFile(t, apps))
	}
	// rules.yaml is a file of its own.
	if err := e.SetPreset(PresetPayments); err != nil {
		t.Fatalf("SetPreset with a broken apps.yaml: %v", err)
	}
	fileHas(t, rules, "SetPreset", "preset: 2")

	handEdit(t, apps, "add:\n  blocked:\n    names: [Dinely]\n")
	if got := e.Refresh(); len(got) != 1 || got[0].Err != nil {
		t.Fatalf("changes after the fix: %+v", got)
	}
	if _, ok := e.Blocked(dinely); !ok {
		t.Error("the app blocked by hand is not blocked")
	}
	if _, ok := e.Blocked(chatter); ok {
		t.Error("the app taken out by hand is still blocked")
	}
}

// TestRefreshKeepsRuntimeSwitches checks that hand edits, and Reload, keep
// what the files do not hold: read-only mode for one phone and apps added
// at run time.
func TestRefreshKeepsRuntimeSwitches(t *testing.T) {
	e, rules, apps := loadIn(t)
	if err := e.SetReadOnly("phone-1", true); err != nil {
		t.Fatal(err)
	}
	bank := screen.App{ID: "com.example.fakebank", Name: "Fake Bank"}
	e.AddApps(CatFinance, CategoryEntries{IDs: []string{bank.ID}})

	handEdit(t, rules, strings.Replace(string(DefaultRulesYAML()), "preset: 2", "preset: 3", 1))
	handEdit(t, apps, "add:\n  blocked:\n    ids: [com.chatter.app]\n")
	check := func(when string) {
		t.Helper()
		if e.Preset() != PresetEvery {
			t.Errorf("%s: preset %d", when, e.Preset())
		}
		if _, ok := e.Blocked(chatter); !ok {
			t.Errorf("%s: the app blocked by hand is not blocked", when)
		}
		if !e.ReadOnly("phone-1") || e.ReadOnly("phone-2") {
			t.Errorf("%s: read-only for phone-1 %v, phone-2 %v", when, e.ReadOnly("phone-1"), e.ReadOnly("phone-2"))
		}
		if rule, ok := e.Blocked(bank); !ok || rule != "Finance apps" {
			t.Errorf("%s: the app added at run time: %q %v", when, rule, ok)
		}
	}
	if got := e.Refresh(); len(got) != 2 {
		t.Fatalf("changes: %+v", got)
	}
	check("after Refresh")
	if err := e.Reload(); err != nil {
		t.Fatal(err)
	}
	check("after Reload")
}

// TestRemovedFilesStartOver removes both files: the engine writes and uses
// what Pinky ships, as at start.
func TestRemovedFilesStartOver(t *testing.T) {
	e, rules, apps := loadIn(t)
	if err := e.SetPreset(PresetEvery); err != nil {
		t.Fatal(err)
	}
	if err := e.SetAppBlocked(chatter, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{rules, apps} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.Refresh(); len(got) != 2 || got[0].Err != nil || got[1].Err != nil {
		t.Fatalf("changes: %+v", got)
	}
	if e.Preset() != PresetPayments {
		t.Errorf("preset %d", e.Preset())
	}
	if _, ok := e.Blocked(chatter); ok {
		t.Error("an app blocked in the removed apps.yaml is still blocked")
	}
	if readFile(t, rules) != string(DefaultRulesYAML()) || readFile(t, apps) != string(appsTemplate) {
		t.Fatalf("written again:\n%s\n%s", readFile(t, rules), readFile(t, apps))
	}
	for _, p := range []string{rules, apps} {
		if st, err := os.Stat(p); err != nil || !private(st) {
			t.Errorf("%s: %v %v", p, st, err)
		}
	}
}

// TestSaveWhileRulesAreOpen saves a change from Settings while another
// program holds rules.yaml open for a moment, as a virus scanner or the
// person's editor may. Windows refuses the save's rename over the open file
// ("Access is denied"); the save tries again once it is closed, rather than
// fail.
func TestSaveWhileRulesAreOpen(t *testing.T) {
	e, rules, _ := loadIn(t)
	f, err := os.Open(rules)
	if err != nil {
		t.Fatal(err)
	}
	hold := time.AfterFunc(150*time.Millisecond, func() { f.Close() })
	t.Cleanup(func() {
		hold.Stop()
		f.Close() // before the folder is removed: Windows keeps an open file
	})
	if err := e.SetPreset(PresetEvery); err != nil {
		t.Fatalf("saving while rules.yaml is open: %v", err)
	}
	if e.Preset() != PresetEvery {
		t.Errorf("preset %d", e.Preset())
	}
	fileHas(t, rules, "saved", "preset: 3")
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(rules), ".rules.yaml.*")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

// TestHalfDoneSaveIsNotRead saves rules.yaml as an editor that writes in
// place does, with the file empty for a moment: the engine waits for the
// save, rather than take in a file with no rules.
func TestHalfDoneSaveIsNotRead(t *testing.T) {
	e, rules, _ := loadIn(t)
	if err := os.WriteFile(rules, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	settle = func() {
		once.Do(func() {
			if err := os.WriteFile(rules, []byte(handRules), 0o600); err != nil {
				t.Error(err)
			}
		})
	}
	if got := e.Refresh(); len(got) != 1 || got[0].Err != nil {
		t.Fatalf("changes: %+v", got)
	}
	if v := e.Evaluate(deleteChat); v.Rule != "No deleting" {
		t.Fatalf("delete after the save: %+v", v)
	}
	if rule, ok := e.Blocked(chase); !ok || rule != "Finance apps" {
		t.Fatalf("Chase after the save: %q %v", rule, ok)
	}
}

// TestVersion counts the changes of the files: hand edits taken in, ones
// that cannot be used and their fixes, and what Settings wrote, but not a
// look that found nothing new or a change from Settings that changed
// nothing.
func TestVersion(t *testing.T) {
	e, rules, apps := loadIn(t)
	if v := e.Version(); v != 0 {
		t.Fatalf("version after Load: %d", v)
	}
	step := func(what string, changes bool, f func()) {
		t.Helper()
		v := e.Version()
		f()
		if (e.Version() != v) != changes {
			t.Errorf("%s: version %d, then %d", what, v, e.Version())
		}
	}
	step("a hand edit", true, func() { handEdit(t, rules, handRules); e.Refresh() })
	step("nothing new", false, func() { e.Refresh() })
	step("a change from Settings", true, func() { e.SetPreset(PresetEvery) })
	step("a change from Settings that changes nothing", false, func() { e.SetPreset(PresetEvery) })
	step("an edit that cannot be used", true, func() { handEdit(t, apps, "add: [\n"); e.Refresh() })
	step("the same problem", false, func() { e.Refresh() })
	step("the fix", true, func() { handEdit(t, apps, "add: {}\n"); e.Refresh() })
}
