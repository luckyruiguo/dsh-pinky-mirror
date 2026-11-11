//go:build e2e

package main

import "os"

// The e2e build of a version named by PINKY_E2E_CRASH_VERSION exits as
// it starts, as a release that does not start would, so the end-to-end
// tests of updates see the helper put the old version back. The helper,
// a copy of the old version, runs as usual, and `version` answers, as the
// helper on Linux asks it what it installed.
func init() {
	if v := os.Getenv("PINKY_E2E_CRASH_VERSION"); v != "" && v == version && (len(os.Args) < 2 || os.Args[1] != "update-helper" && os.Args[1] != "version") {
		os.Stderr.WriteString("pinky: this e2e build of " + version + " stands in for a release that does not start\n")
		os.Exit(3)
	}
}
