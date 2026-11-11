//go:build !windows

package adb

import (
	"os"
	"strings"
)

// listeningServer returns the server for port and the adb it runs, when
// Pinky started it (ownCommandLine): its program by the full path it ran.
func listeningServer(port string) (int, string, bool) {
	procs, err := serverProcesses(port)
	if err != nil || len(procs) != 1 || !ownCommandLine(procs[0].command, port) {
		return 0, "", false
	}
	exe, _, _ := strings.Cut(procs[0].command, serverLine(port))
	return procs[0].pid, exe, true
}

func myPID() int { return os.Getpid() }
