//go:build windows || linux

package update

import (
	"io"
	"os"
	"path/filepath"
	"time"
)

// What the updates of an install folder share, on Windows (the installer's
// folder) and Linux (the archive's): the helper of helper.go swaps the
// folder's files, and the new version finds its swap next to the folder.

// CheckSwap looks next to the install folder for a swap that just
// happened (see the macOS one).
func CheckSwap(version string) *Swapped {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return checkSwap(filepath.Dir(exe), version, os.Getpid(), time.Now())
}

// cleanUpAfter removes the work folder of a swap once its helper, which
// runs from a copy of Pinky's executable in it, is through, trying for a
// quarter of an hour: on Windows the old Pinky.exe stays in use while an
// agent's `pinky mcp` that started from it runs. A later start of Pinky
// removes what is left (CheckSwap).
func cleanUpAfter(work string) {
	for deadline := time.Now().Add(15 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if exists(filepath.Join(work, "kept")) || !exists(work) {
			return
		}
		if exists(filepath.Join(work, "done")) && os.RemoveAll(work) == nil {
			return
		}
	}
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
