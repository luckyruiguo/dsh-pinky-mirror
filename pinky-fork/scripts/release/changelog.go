package main

import (
	"errors"
	"fmt"
	"strings"
)

// CHANGELOG.md is a short introduction, then a section per release, newest
// first:
//
//	## 2026.1107.2 (2026-11-07)
//
//	- Short, user-facing notes.
//
// A section starts at a line "## " (a level-two heading) whose first word
// is the version; the rest of the heading is the date in parentheses. Notes
// for the next release wait under "## Unreleased" at the top, which `tag`
// turns into the release's heading. Headings inside fenced code blocks do
// not count.
//
// Each translation of the changelog (translations) has the same headings,
// and under each the same notes in its language, a bullet for each of
// CHANGELOG.md's. tag stamps them together, and latest.json carries each
// release's translated notes beside the English ones.

// unreleased is the heading word of the notes not released yet.
const unreleased = "Unreleased"

// translations are the changelog's translations: the language, as the
// app's catalogs and latest.json name it (cmd/pinky/locales), the file, and
// the heading of its notes on the release page, in that language.
var translations = []struct{ lang, file, heading string }{
	{"zh-Hans", "CHANGELOG.zh-CN.md", "中文"},
}

// translationFile returns the changelog of lang, or false when there is
// none.
func translationFile(lang string) (string, bool) {
	for _, t := range translations {
		if strings.EqualFold(t.lang, lang) {
			return t.file, true
		}
	}
	return "", false
}

// section is one release's part of the changelog.
type section struct {
	version string // the heading's first word: a version or "Unreleased"
	line    int    // the index of the heading line
	notes   string // the Markdown under the heading, trimmed
}

func (s section) isUnreleased() bool { return strings.EqualFold(s.version, unreleased) }

// splitLines splits text into lines without their line breaks (and a "\r"
// of Windows line breaks).
func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// sections returns the changelog's sections, in the file's order.
func sections(text string) []section {
	lines := splitLines(text)
	var out []section
	fence := ""
	start := -1
	flush := func(end int) {
		if start >= 0 {
			out[len(out)-1].notes = strings.TrimSpace(strings.Join(lines[start+1:end], "\n"))
		}
	}
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = trimmed[:3]
			continue
		}
		rest, ok := strings.CutPrefix(l, "## ")
		if !ok {
			continue
		}
		flush(i)
		fields := strings.Fields(rest)
		v := ""
		if len(fields) > 0 {
			v = strings.TrimPrefix(fields[0], "v")
		}
		out = append(out, section{version: v, line: i})
		start = i
	}
	flush(len(lines))
	return out
}

// Notes returns the notes of version in CHANGELOG.md.
func Notes(text, version string) (string, error) { return notesIn(changelogFile, text, version) }

// notesIn returns the notes of version in the changelog file holds.
func notesIn(file, text, version string) (string, error) {
	version = strings.TrimPrefix(version, "v")
	for _, s := range sections(text) {
		if s.version == version || strings.EqualFold(version, unreleased) && s.isUnreleased() {
			if s.notes == "" {
				return "", fmt.Errorf("%s has no notes under its %s heading", file, s.version)
			}
			return s.notes, nil
		}
	}
	return "", fmt.Errorf("%s has no section for %s (a \"## %s (%s)\" heading)", file, version, version, dateOf(version))
}

// bullets counts the top-level bullets of notes.
func bullets(notes string) int {
	n := 0
	for _, l := range splitLines(notes) {
		if strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "* ") || strings.HasPrefix(l, "+ ") {
			n++
		}
	}
	return n
}

// dateOf returns the date of a release version, or YYYY-MM-DD.
func dateOf(version string) string {
	if v, err := ParseVersion(version); err == nil {
		return v.Date()
	}
	return "YYYY-MM-DD"
}

// errNoRelease is the error of a changelog without notes to release.
var errNoRelease = errors.New(`CHANGELOG.md has nothing to release: write this release's notes under a "## Unreleased" heading at its top`)

// Stamp returns the changelog with the notes of the next release under the
// heading of version v: the first section's, when it is "Unreleased" or a
// version that has no tag yet (tagged reports which have one), which
// happens when a release was prepared but its tag not pushed, or another
// release took its version first. It also returns that section's notes.
func Stamp(text string, v Version, tagged func(version string) bool) (string, string, error) {
	return stamp(changelogFile, text, v, tagged)
}

// stamp is Stamp of the changelog file holds.
func stamp(file, text string, v Version, tagged func(version string) bool) (string, string, error) {
	all := sections(text)
	if len(all) == 0 {
		return "", "", errNoRelease
	}
	top := all[0]
	if !top.isUnreleased() {
		if _, err := ParseVersion(top.version); err != nil {
			return "", "", fmt.Errorf("%s's first section is %q, neither \"Unreleased\" nor a release version", file, top.version)
		}
		if tagged(top.version) {
			return "", "", errNoRelease
		}
	}
	if top.notes == "" {
		return "", "", fmt.Errorf("%s has no notes under its %s heading: write what this release changes for people", file, top.version)
	}
	for _, s := range all[1:] {
		if s.isUnreleased() {
			return "", "", fmt.Errorf(`%s has an "Unreleased" section below a release: notes for the next release go at the top`, file)
		}
		if s.version == v.String() {
			return "", "", fmt.Errorf("%s already has a section for %s", file, v)
		}
	}
	lines := strings.Split(text, "\n")
	heading := "## " + v.String() + " (" + v.Date() + ")"
	if strings.HasSuffix(lines[top.line], "\r") {
		heading += "\r"
	}
	lines[top.line] = heading
	return strings.Join(lines, "\n"), top.notes, nil
}

// StampTranslation returns a translation of the changelog, held in file,
// with the next release's notes under the heading of version v, as Stamp
// does for CHANGELOG.md, whose notes of the release are notes. It fails
// when the translation lacks the release's notes, or has another number of
// bullets than notes: each of CHANGELOG.md's needs its translation.
func StampTranslation(file, text string, v Version, notes string, tagged func(version string) bool) (string, string, error) {
	stamped, translated, err := stamp(file, text, v, tagged)
	if errors.Is(err, errNoRelease) {
		return "", "", fmt.Errorf(`%s has no notes for this release: write CHANGELOG.md's Unreleased notes in its language too, under a "## Unreleased" heading at its top`, file)
	}
	if err != nil {
		return "", "", err
	}
	if want, got := bullets(notes), bullets(translated); want != got {
		return "", "", fmt.Errorf("%s has %d notes for this release, and %s has %d: translate each of %s's, in the same order", changelogFile, want, file, got, changelogFile)
	}
	return stamped, translated, nil
}
