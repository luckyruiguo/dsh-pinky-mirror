//go:build !windows

package tailnet

import "os/exec"

func hideWindow(*exec.Cmd) {}
