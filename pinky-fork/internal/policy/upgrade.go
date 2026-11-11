package policy

import (
	"bytes"
	"embed"
	"io/fs"
)

// The rules files earlier versions of Pinky wrote on their first run, as
// they wrote them. A file that is still one of them byte for byte was never
// changed, by hand or in Settings (which rewrites the whole file): its
// preset is the default of its day, not a choice the person made, so it
// takes the bundled defaults of this version, and with them today's
// default preset. Any other file is kept as it is, whatever its preset.
//
// olddefaults holds every earlier defaults.yaml: before-presets.yaml from
// before the first release (six Ask rules spelled out, which
// migrateLegacy reads as preset 1), 2026.1107.1.yaml and 2026.1107.2.yaml
// (also 2026.1107.3 to .8), which set preset 1. When defaults.yaml changes
// again, add the version it replaces. The engine looks at start
// (refreshRulesLocked in disk.go).
//
//go:embed olddefaults/*.yaml
var oldDefaults embed.FS

// upgradeDefaults returns this version's bundled rules file when data is
// one an earlier version wrote and nobody changed.
func upgradeDefaults(data []byte) ([]byte, bool) {
	data = lf(data)
	if bytes.Equal(data, lf(defaultRulesYAML)) {
		return nil, false
	}
	olds, _ := fs.Glob(oldDefaults, "olddefaults/*.yaml")
	for _, name := range olds {
		old, err := oldDefaults.ReadFile(name)
		if err == nil && bytes.Equal(data, lf(old)) {
			return DefaultRulesYAML(), true
		}
	}
	return nil, false
}

// lf turns Windows line ends into Unix ones, which a checkout on Windows
// may have put into the bundled file.
func lf(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }
