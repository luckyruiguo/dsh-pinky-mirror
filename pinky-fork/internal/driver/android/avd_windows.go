package android

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detachEmulator runs the emulator in a process group of its own, with a
// console that has no window, so that it outlives Pinky; its own window
// shows as usual.
func detachEmulator(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
}

// hideConsole keeps a console program that a windowed Pinky runs from
// flashing a console window.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
}
