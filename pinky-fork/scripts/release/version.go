package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Version is a release version, YEAR.MDD.N: the date of the release in
// Asia/Shanghai, as the year, then the month without a leading zero and the
// two-digit day, and the count of that day's releases, from 1. 2026.1107.2
// is the second release on 7 November 2026, 2027.105.1 the first on 5
// January 2027.
//
// Each part is an integer without leading zeros, so a version is valid
// semver, sorts by its parts, and fits a macOS bundle version (three
// integers) and a Windows version resource (each part at most 65535).
type Version struct {
	Year     int // 2026
	MonthDay int // month*100 + day: 1107 for 7 November, 105 for 5 January
	N        int // that day's count of releases, from 1
}

var versionRe = regexp.MustCompile(`^([0-9]{4})\.([1-9][0-9]{2,3})\.([1-9][0-9]{0,4})$`)

// ParseVersion parses a release version, with or without the tag's "v".
func ParseVersion(s string) (Version, error) {
	m := versionRe.FindStringSubmatch(strings.TrimPrefix(s, "v"))
	if m == nil {
		return Version{}, fmt.Errorf("%q is not a release version (YEAR.MDD.N, as 2026.1107.1)", s)
	}
	year, _ := strconv.Atoi(m[1])
	md, _ := strconv.Atoi(m[2])
	n, _ := strconv.Atoi(m[3])
	month, day := md/100, md%100
	if t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC); month > 12 || t.Month() != time.Month(month) || t.Day() != day {
		return Version{}, fmt.Errorf("%q is not a release version: %d is not a month and day (MDD)", s, md)
	}
	if n > 65535 {
		return Version{}, fmt.Errorf("%q is not a release version: a Windows version part is at most 65535", s)
	}
	return Version{Year: year, MonthDay: md, N: n}, nil
}

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Year, v.MonthDay, v.N) }

// Tag is the release's git tag.
func (v Version) Tag() string { return "v" + v.String() }

// Date is the release's date, as CHANGELOG.md writes it (2026-11-07).
func (v Version) Date() string {
	return fmt.Sprintf("%04d-%02d-%02d", v.Year, v.MonthDay/100, v.MonthDay%100)
}

// Compare returns -1, 0 or 1 as a is older than, the same as or newer than b.
func Compare(a, b Version) int {
	for _, d := range [3]int{a.Year - b.Year, a.MonthDay - b.MonthDay, a.N - b.N} {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return 1
		}
	}
	return 0
}

// shanghai is Asia/Shanghai's time. China has kept UTC+8 without daylight
// saving time since 1991, so a fixed zone gives the same dates without a
// time zone database, which Windows and slim containers lack.
var shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

// day returns the date of t in Asia/Shanghai as a version's first two parts.
func day(t time.Time) (year, monthDay int) {
	t = t.In(shanghai)
	return t.Year(), int(t.Month())*100 + t.Day()
}

// Next returns the version of a release made at now, given the existing
// tags (as "v2026.1107.1"; others are ignored): today's date and one more
// than today's last count. A tag of a later day is an error, as the clock
// or the tag is wrong.
func Next(now time.Time, tags []string) (Version, error) {
	year, md := day(now)
	next := Version{Year: year, MonthDay: md, N: 1}
	for _, tag := range tags {
		if !strings.HasPrefix(tag, "v") {
			continue
		}
		v, err := ParseVersion(tag)
		if err != nil {
			continue
		}
		switch {
		case v.Year == year && v.MonthDay == md:
			next.N = max(next.N, v.N+1)
		case Compare(v, next) > 0:
			return Version{}, fmt.Errorf("tag %s is of a later day than today, %s in Asia/Shanghai: is the clock right?", tag, next.Date())
		}
	}
	return next, nil
}
