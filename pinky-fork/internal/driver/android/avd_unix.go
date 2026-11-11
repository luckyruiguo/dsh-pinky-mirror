//go:build !windows

package android

import (
	"os/exec"
	"syscall"
)

// detachEmulator runs the emulator in a session of its own, so that it
// outlives Pinky and gets none of its signals.
func detachEmulator(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// hideConsole is for Windows.
func hideConsole(*exec.Cmd) {}
