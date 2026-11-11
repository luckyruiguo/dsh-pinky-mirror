//go:build !darwin && !windows && !linux

package update

import (
	"errors"
	"log/slog"
)

// CheckSwap finds nothing: Pinky swaps itself in on macOS and Windows only.
func CheckSwap(version string) *Swapped { return nil }

// cleanUpAfter has nothing to clean up here.
func cleanUpAfter(string) {}

// NewInstaller: Pinky replaces itself on macOS and Windows only; elsewhere
// the package manager or the person installs the new version.
func NewInstaller(l Location, logFile string, log *slog.Logger) (Installer, error) {
	if l.Problem != "" {
		return nil, errors.New(l.Problem)
	}
	return nil, errors.New("Pinky can't replace itself on this system; install the new version from the download")
}

// RunHelper is for Windows.
func RunHelper(string) error { return ErrHelperOS }
