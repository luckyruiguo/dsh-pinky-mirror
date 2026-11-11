package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/update"
)

// TestFeedTheAppReads: the latest.json a release publishes is a feed that
// the app reads whole, in the repository it reads it from
// (docs/notes/updates.md, the feed).
func TestFeedTheAppReads(t *testing.T) {
	if defaultReleasesRepo != update.Repo {
		t.Errorf("releases are published in %s, the app looks in %s", defaultReleasesRepo, update.Repo)
	}
	dir := t.TempDir()
	writeArchives(t, dir, "2026.1107.1")
	m, _, err := BuildManifest(dir, "2026.1107.1", defaultReleasesRepo, "- One.", time.Date(2026, 11, 7, 8, 9, 10, 0, time.UTC), false)
	if err != nil {
		t.Fatal(err)
	}
	m.LocalizedNotes = map[string]string{}
	for _, tr := range translations {
		m.LocalizedNotes[tr.lang] = "- 一。"
	}
	path := filepath.Join(dir, "latest.json")
	if err := writeJSON(path, m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := update.ParseFeed(data)
	if err != nil {
		t.Fatalf("the app does not read latest.json: %v\n%s", err, data)
	}
	if f.Version != "2026.1107.1" || f.Tag != "v2026.1107.1" || f.Notes != "- One." || !f.Date.Equal(time.Date(2026, 11, 7, 8, 9, 10, 0, time.UTC)) {
		t.Errorf("the app reads %+v", f)
	}
	// The app in each translation's language shows its notes, and in
	// English the English ones.
	for _, tr := range translations {
		if got := f.NotesIn(tr.lang); got != "- 一。" {
			t.Errorf("the app in %s reads the notes %q", tr.lang, got)
		}
	}
	if got := f.NotesIn("en"); got != "- One." {
		t.Errorf("the app in English reads the notes %q", got)
	}
	for _, platform := range []string{"darwin-arm64", "darwin-amd64", "windows-amd64", "linux-amd64"} {
		a, ok := f.Asset(platform)
		w := m.Assets[platform]
		if !ok || a.URL != w.URL || !strings.EqualFold(a.SHA256, w.SHA256) || a.Size != w.Size {
			t.Errorf("the app reads the %s asset as %+v (%v), the release wrote %+v", platform, a, ok, w)
		}
	}
	// The app runs the Windows asset as the installer, and unpacks the
	// macOS ones as zips of Pinky.app.
	if a, _ := f.Asset("windows-amd64"); !strings.HasSuffix(a.URL, "/pinky-setup-2026.1107.1-amd64.exe") {
		t.Errorf("the Windows asset is %s, not the installer", a.URL)
	}
	if a, _ := f.Asset("darwin-arm64"); !strings.HasSuffix(a.URL, ".zip") {
		t.Errorf("the macOS asset is %s, not a zip", a.URL)
	}
}
