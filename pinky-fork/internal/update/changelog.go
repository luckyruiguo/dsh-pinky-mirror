package update

import (
	"regexp"
	"strings"
	"time"
)

// Entry is one version's section of CHANGELOG.md.
type Entry struct {
	Version string
	Date    time.Time // zero when the heading has none
	// Body is the section's Markdown below its heading: bullets.
	Body string
}

var (
	headingRE = regexp.MustCompile(`^##\s+(.*)$`)
	dateRE    = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2})\b`)
)

// ParseChangelog reads CHANGELOG.md: newest first, a "## <version>
// (<YYYY-MM-DD>)" heading per version, bullets below it. Sections whose
// heading names no version (such as "## Unreleased") are left out.
func ParseChangelog(md string) []Entry {
	var out []Entry
	var cur *Entry
	var body []string
	flush := func() {
		if cur != nil {
			cur.Body = strings.TrimSpace(strings.Join(body, "\n"))
			out = append(out, *cur)
		}
		cur, body = nil, nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		if m := headingRE.FindStringSubmatch(line); m != nil {
			flush()
			if e, ok := parseHeading(m[1]); ok {
				cur = &e
			}
			continue
		}
		if strings.HasPrefix(line, "# ") {
			flush()
			continue
		}
		if cur != nil {
			body = append(body, line)
		}
	}
	flush()
	return out
}

// parseHeading reads "2026.1107.1 (2026-11-07)", and also "[2026.1107.1] -
// 2026-11-07" and "v2026.1107.1".
func parseHeading(h string) (Entry, bool) {
	var e Entry
	for _, f := range strings.FieldsFunc(h, func(r rune) bool { return strings.ContainsRune(" \t[]()–—", r) }) {
		if IsRelease(f) {
			e.Version = strings.TrimPrefix(f, "v")
			break
		}
	}
	if e.Version == "" {
		return e, false
	}
	if m := dateRE.FindStringSubmatch(h); m != nil {
		e.Date, _ = time.Parse("2006-01-02", m[1])
	}
	return e, true
}

// Since returns the entries newer than from, up to and including to,
// newest first. With from empty or not a release, only to's own entry.
func Since(entries []Entry, from, to string) []Entry {
	var out []Entry
	for _, e := range entries {
		if Compare(e.Version, to) > 0 {
			continue
		}
		if IsRelease(from) {
			if Compare(e.Version, from) <= 0 {
				continue
			}
		} else if Compare(e.Version, to) != 0 {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Translate returns entries with the bodies of the same versions in
// translation, a translation of the changelog (CHANGELOG.zh-CN.md), where
// it has them, and their own elsewhere.
func Translate(entries, translation []Entry) []Entry {
	bodies := map[string]string{}
	for _, e := range translation {
		if e.Body != "" {
			bodies[e.Version] = e.Body
		}
	}
	out := make([]Entry, len(entries))
	for i, e := range entries {
		if b, ok := bodies[e.Version]; ok {
			e.Body = b
		}
		out[i] = e
	}
	return out
}

// Markdown puts entries back together for a What's new view: a heading
// for each version with its date, as date writes it ("November 7, 2026"
// when nil), then its bullets.
func Markdown(entries []Entry, date func(time.Time) string) string {
	if date == nil {
		date = func(t time.Time) string { return t.Format("January 2, 2006") }
	}
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("### " + e.Version)
		if !e.Date.IsZero() {
			b.WriteString(" · " + date(e.Date))
		}
		if e.Body != "" {
			b.WriteString("\n\n" + e.Body)
		}
	}
	return b.String()
}
