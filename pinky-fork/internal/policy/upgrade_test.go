package policy

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadFile loads a rules file with the given content, and returns the
// engine and what is on disk afterwards.
func loadFile(t *testing.T, data []byte) (*Engine, []byte) {
	t.Helper()
	dir := t.TempDir()
	rules := filepath.Join(dir, "rules.yaml")
	if data != nil {
		if err := os.WriteFile(rules, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e, err := Load(rules, filepath.Join(dir, "apps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(rules)
	if err != nil {
		t.Fatal(err)
	}
	return e, after
}

// TestUntouchedDefaultsTakeTheNewPreset: a rules file an earlier version
// wrote on its first run, never changed since, takes today's defaults and
// asks before payments only; any file someone changed keeps its preset.
func TestUntouchedDefaultsTakeTheNewPreset(t *testing.T) {
	olds, err := fs.Glob(oldDefaults, "olddefaults/*.yaml")
	if err != nil || len(olds) != 3 {
		t.Fatalf("earlier defaults: %v %v", olds, err)
	}
	for _, name := range olds {
		old, _ := oldDefaults.ReadFile(name)
		old = lf(old) // as committed, whatever the checkout made of it
		for _, data := range [][]byte{old, bytes.ReplaceAll(old, []byte("\n"), []byte("\r\n"))} {
			e, after := loadFile(t, data)
			if e.Preset() != PresetPayments || !bytes.Equal(after, defaultRulesYAML) {
				t.Errorf("%s (CRLF %v): preset %d, file rewritten %v", name, bytes.Contains(data, []byte("\r")),
					e.Preset(), bytes.Equal(after, defaultRulesYAML))
			}
		}
	}

	// A new install, and today's defaults left as they are.
	if e, after := loadFile(t, nil); e.Preset() != PresetPayments || !bytes.Equal(after, defaultRulesYAML) {
		t.Errorf("new install: preset %d", e.Preset())
	}
	if e, _ := loadFile(t, defaultRulesYAML); e.Preset() != PresetPayments {
		t.Errorf("today's defaults: preset %d", e.Preset())
	}

	// Settings rewrote the file: the person chose preset 1, or changed
	// something else; either way it stays as it is.
	old, _ := oldDefaults.ReadFile("olddefaults/2026.1107.2.yaml")
	var saved []byte
	{
		dir := t.TempDir()
		rules := filepath.Join(dir, "rules.yaml")
		os.WriteFile(rules, old, 0o600)
		e, err := Load(rules, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := e.SetPreset(PresetCommits); err != nil {
			t.Fatal(err)
		}
		if err := e.SetHideCodes(false); err != nil {
			t.Fatal(err)
		}
		saved, _ = os.ReadFile(rules)
	}
	if e, after := loadFile(t, saved); e.Preset() != PresetCommits || !bytes.Equal(after, saved) {
		t.Errorf("a file Settings wrote with preset 1: preset %d, rewritten %v", e.Preset(), !bytes.Equal(after, saved))
	}

	// A hand edit of an earlier default, even of a comment, keeps it.
	for name, edit := range map[string][]byte{
		"a comment":  bytes.Replace(old, []byte("# Pinky policy rules."), []byte("# My rules."), 1),
		"a new rule": append(append([]byte(nil), old...), []byte("  - name: No Chatter\n    match: { app: Chatter }\n    decision: deny\n")...),
	} {
		e, after := loadFile(t, edit)
		if e.Preset() != PresetCommits || !bytes.Equal(after, edit) {
			t.Errorf("%s changed by hand: preset %d, rewritten %v", name, e.Preset(), !bytes.Equal(after, edit))
		}
	}

	// Before presets, an untouched file spelled out preset 1's rules; it
	// takes today's defaults too, while one with a rule of the person's
	// own keeps its rules, as before.
	legacy, _ := oldDefaults.ReadFile("olddefaults/before-presets.yaml")
	if !strings.Contains(string(legacy), "decision: ask") || strings.Contains(string(legacy), "\npreset:") {
		t.Fatalf("before-presets.yaml is not a file from before presets:\n%s", legacy)
	}
	mine := append(append([]byte(nil), legacy...), []byte("  - name: No Chatter\n    match: { app: Chatter }\n    decision: deny\n")...)
	if e, _ := loadFile(t, mine); e.Preset() != PresetCommits {
		t.Errorf("an edited file from before presets: preset %d, want %d", e.Preset(), PresetCommits)
	}
}

// TestBundledFilesAsCommitted: the rules files Pinky bundles have Unix
// line ends on every system, as they are committed. A checkout that gives
// them Windows ones (Git for Windows' core.autocrlf, which .gitattributes
// turns off) builds a Pinky unlike the released one, and the tests that
// edit rules.yaml by hand, line by line, fail on it.
func TestBundledFilesAsCommitted(t *testing.T) {
	files := map[string][]byte{"defaults.yaml": defaultRulesYAML, "apps.yaml": bundledAppsYAML}
	olds, _ := fs.Glob(oldDefaults, "olddefaults/*.yaml")
	for _, name := range olds {
		files[name], _ = oldDefaults.ReadFile(name)
	}
	for name, data := range files {
		if bytes.Contains(data, []byte("\r")) {
			t.Errorf("%s has Windows line ends: the checkout changed it (see .gitattributes)", name)
		}
	}
}
