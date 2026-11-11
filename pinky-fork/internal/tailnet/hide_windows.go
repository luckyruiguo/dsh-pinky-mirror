package tailnet

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps tailscale.exe, a console program, from opening a
// console window when the app runs it.
func hideWindow(cmd *exec.Cmd) {
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow, HideWindow: true}
}
