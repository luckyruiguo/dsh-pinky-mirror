package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Manifest is latest.json, which every release carries: the newest is at
// https://github.com/<releases repo>/releases/latest/download/latest.json,
// where the app's updater reads it.
type Manifest struct {
	Version string `json:"version"` // 2026.1107.1
	Tag     string `json:"tag"`     // v2026.1107.1
	Date    string `json:"date"`    // RFC 3339, when the release was assembled
	Notes   string `json:"notes"`   // Markdown: the version's section of CHANGELOG.md
	// LocalizedNotes are Notes in other languages, by the language tags of
	// the app's catalogs (zh-Hans): the version's section of each
	// translation of the changelog that has one. Versions of the app before
	// it read only Notes.
	LocalizedNotes map[string]string `json:"localized_notes,omitempty"`
	Assets         map[string]Asset  `json:"assets"` // by platform, as darwin-arm64
}

// Asset is the archive of one platform that the updater installs.
type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// archives names the file of each platform that the updater installs, by
// version: a zip of the signed, notarized and stapled Pinky.app on macOS
// (made with ditto), the installer on Windows, which the updater opens and
// which installs over the installed version, and the archive `mygo build`
// makes on Linux, which its install.sh installs.
var archives = []struct{ platform, name string }{
	{"darwin-arm64", "pinky-%s-darwin-arm64.zip"},
	{"darwin-amd64", "pinky-%s-darwin-amd64.zip"},
	{"windows-amd64", "pinky-setup-%s-amd64.exe"},
	{"linux-amd64", "pinky-%s-linux-amd64.tar.gz"},
}

// archiveName returns the file name of a platform's archive.
func archiveName(platform, version string) string {
	for _, a := range archives {
		if a.platform == platform {
			return fmt.Sprintf(a.name, version)
		}
	}
	panic("no archive for " + platform)
}

// downloadURL returns where GitHub serves a release's file.
func downloadURL(repo, tag, name string) string {
	return "https://github.com/" + repo + "/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(name)
}

// fileSum returns the SHA-256 and size of a file.
func fileSum(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// BuildManifest returns the manifest of the release of version in dir,
// which holds every platform's archive. A partial manifest (a dry run's)
// leaves out the platforms whose archives are missing, and names them.
func BuildManifest(dir, version, repo, notes string, now time.Time, partial bool) (m *Manifest, missing []string, err error) {
	v, err := ParseVersion(version)
	if err != nil {
		return nil, nil, err
	}
	m = &Manifest{
		Version: v.String(),
		Tag:     v.Tag(),
		Date:    now.UTC().Format(time.RFC3339),
		Notes:   notes,
		Assets:  map[string]Asset{},
	}
	for _, a := range archives {
		name := fmt.Sprintf(a.name, m.Version)
		sum, size, err := fileSum(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			missing = append(missing, name)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		m.Assets[a.platform] = Asset{URL: downloadURL(repo, m.Tag, name), SHA256: sum, Size: size}
	}
	if len(missing) > 0 && !partial {
		return nil, missing, fmt.Errorf("%s lacks %s", dir, strings.Join(missing, ", "))
	}
	return m, missing, nil
}

// PageNotes returns the notes of a release's page on GitHub: the English
// ones, then the notes in each language the manifest has them in, after a
// rule and a heading in that language.
func PageNotes(m *Manifest) string {
	page := m.Notes
	for _, t := range translations {
		if notes := strings.TrimSpace(m.LocalizedNotes[t.lang]); notes != "" {
			page += "\n\n---\n\n### " + t.heading + "\n\n" + notes
		}
	}
	return page
}

// writeJSON writes v to path as indented JSON.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// readManifest reads a latest.json.
func readManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &m, nil
}

// checksumsName is the file of every published file's SHA-256.
const checksumsName = "SHA256SUMS"

// WriteChecksums writes SHA256SUMS in dir, in the format of sha256sum, for
// the other files there.
func WriteChecksums(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var lines []string
	for _, e := range entries {
		if !e.Type().IsRegular() || e.Name() == checksumsName || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		sum, _, err := fileSum(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		lines = append(lines, sum+"  "+e.Name()+"\n") // ReadDir sorts by name
	}
	return os.WriteFile(filepath.Join(dir, checksumsName), []byte(strings.Join(lines, "")), 0o644)
}
