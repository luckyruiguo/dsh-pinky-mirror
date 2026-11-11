// Package update keeps Pinky current: it reads the release feed, downloads
// and verifies a newer build, swaps it in where Pinky is installed and
// starts it, and tells the person what changed (the changelog).
//
// The feed is latest.json in the newest GitHub release of Repo (FeedURL):
//
//	{"version": "2026.1107.1", "tag": "v2026.1107.1", "date": "<RFC 3339>",
//	 "notes": "<Markdown>",
//	 "assets": {"darwin-arm64": {"url": "…", "sha256": "…", "size": 123}, …}}
//
// On macOS an asset is a zip of the signed, notarized Pinky.app (made with
// `ditto -c -k --keepParent`). Development builds (0.0.0-dev) never update.
package update

import (
	"strconv"
	"strings"
)

// DevVersion is the version of builds made without a release version, such
// as `go run ./cmd/pinky`. They never update themselves.
const DevVersion = "0.0.0-dev"

// Parse reads a version: integers separated by dots, such as 2026.1107.1 or
// 0.1.0, with an optional leading "v". It reports false for anything else,
// such as 0.0.0-dev.
func Parse(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		if p == "" || len(p) > 9 {
			return nil, false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return nil, false
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}

// IsRelease reports whether v is a release version: one Parse reads that is
// not all zeros. Development builds are not.
func IsRelease(v string) bool {
	parts, ok := Parse(v)
	if !ok {
		return false
	}
	for _, n := range parts {
		if n != 0 {
			return true
		}
	}
	return false
}

// Compare compares two versions numerically, part by part: -1 when a is
// older than b, 0 when they are the same, +1 when a is newer. A missing
// part counts as 0, so 2026.1107 equals 2026.1107.0. A version that does
// not parse is older than any that does, and equal to another that does
// not.
func Compare(a, b string) int {
	pa, okA := Parse(a)
	pb, okB := Parse(b)
	switch {
	case !okA && !okB:
		return 0
	case !okA:
		return -1
	case !okB:
		return 1
	}
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	}
	return 0
}

// Newer reports whether candidate is a release newer than current, which
// must be a release itself: development builds never see an update.
func Newer(candidate, current string) bool {
	return IsRelease(current) && IsRelease(candidate) && Compare(candidate, current) > 0
}
