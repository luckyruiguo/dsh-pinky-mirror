//go:build !windows

package adb

import (
	"os"
	"os/exec"
	"syscall"
)

// serverCommand is the server's command: the pipe is its file descriptor
// 3, and it runs in a session of its own, so that it outlives Pinky and
// gets none of its signals.
func serverCommand(path, listen string, reply *os.File) (*exec.Cmd, error) {
	cmd := exec.Command(path, "-L", listen, "fork-server", "server", "--reply-fd", "3")
	cmd.ExtraFiles = []*os.File{reply}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, nil
}

// captureStderr: the server points its standard error at its log file
// once it runs (dup2), which closes Pinky's pipe.
const captureStderr = true

// hideWindow is for Windows.
func hideWindow(*exec.Cmd) {}
