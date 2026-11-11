package ios

import (
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]osVersion{
		"27.0.1": {27, 0, 1},
		"18.6":   {18, 6, 0},
		"17":     {17, 0, 0},
		" 26.5 ": {26, 5, 0},
		"":       {},
		"x.1":    {},
		"18.x":   {18, 0, 0},
	} {
		if got := parseVersion(in); got != want {
			t.Errorf("parseVersion(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPathFor(t *testing.T) {
	for _, c := range []struct {
		version string
		want    pathKind
		detail  string
	}{
		{"27.0.1", pathCoreDevice, ""},
		{"28.1", pathCoreDevice, ""},
		{"26.5", pathLegacy, ""},
		{"18.6.2", pathLegacy, ""},
		{"17.4", pathLegacy, ""},
		{"17.3.1", pathUnsupported, "Software Update"},
		{"17.0", pathUnsupported, "17.4 or later"},
		{"16.7.10", pathUnsupported, "developer disk image"},
		{"15.8", pathUnsupported, "iOS 15.8"},
		{"", pathUnsupported, "version"},
	} {
		got, detail := pathFor(parseVersion(c.version))
		if got != c.want || !strings.Contains(detail, c.detail) || (c.detail == "") != (detail == "") {
			t.Errorf("pathFor(%q) = %v, %q; want %v with %q", c.version, got, detail, c.want, c.detail)
		}
	}
}
