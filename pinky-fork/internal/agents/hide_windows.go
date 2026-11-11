package agents

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a console program that an app runs, such as an agent's
// command (claude.cmd, codex.exe), from opening a console window.
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
}
