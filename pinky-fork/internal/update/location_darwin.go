package update

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// checkRunning checks the signature of the running bundle; tests replace
// it. Only a Pinky.app signed by Pinky's team replaces itself, so that a
// build made on this Mac (`make app`, signed ad hoc) is never overwritten
// by a release.
var checkRunning = func(bundle string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return verifySignature(ctx, bundle, false)
}

// locate on macOS: Pinky.app replaces itself where it is, unless macOS
// runs it from a translocated or read-only copy, a Homebrew formula
// manages it, the folder is not writable, or the bundle is not signed by
// Pinky's team. Pinky's Homebrew cask declares auto_updates, so Homebrew
// leaves a cask's Pinky.app to update itself, as here, and upgrades it only
// when the app's own version falls behind the tap (docs/notes/updates.md).
func locate(exe string) Location {
	l := Location{Exe: exe, App: exe, Method: MethodDownload}
	macOS := filepath.Dir(exe)
	contents := filepath.Dir(macOS)
	bundle := filepath.Dir(contents)
	if filepath.Base(macOS) != "MacOS" || filepath.Base(contents) != "Contents" || !strings.HasSuffix(bundle, ".app") {
		l.Problem = "This Pinky doesn't run from Pinky.app, so it can't replace itself."
		return l
	}
	l.App = bundle
	if cmd := homebrew(exe); cmd != "" {
		l.Method, l.Manager, l.Command = MethodManager, "Homebrew", cmd
		return l
	}
	parent := filepath.Dir(bundle)
	switch {
	case strings.Contains(bundle, "/AppTranslocation/"):
		l.Problem = "macOS runs this Pinky from a temporary copy. Move Pinky to Applications and open it from there to get updates automatically."
	case writableDir(parent) != nil:
		if strings.HasPrefix(bundle, "/Volumes/") {
			l.Problem = "Pinky runs from its disk image. Drag it to Applications and open it from there to get updates automatically."
		} else {
			l.Problem = "Pinky can't write to " + parent + ", where it is installed."
		}
	case unix.Access(bundle, unix.W_OK) != nil:
		l.Problem = "Pinky.app belongs to another user of this Mac, so Pinky can't replace it."
	case checkRunning(bundle) != nil:
		l.Problem = "This copy of Pinky isn't signed by Pinky's developer (a build made on this Mac), so it doesn't replace itself."
	default:
		l.Method = MethodSelf
	}
	return l
}

// homebrew returns the command that updates a Pinky that a Homebrew
// formula installed (in the Cellar), or "". A cask moves Pinky.app to the
// Applications folder, and Pinky's cask says it updates itself
// (auto_updates true), so that is Pinky's to do.
func homebrew(exe string) string {
	if strings.Contains(exe, "/Cellar/") {
		return "brew upgrade pinky"
	}
	return ""
}
