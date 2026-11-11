//go:build unix

package main

import "syscall"

// detached puts the daemon in its own session, so it outlives the agent
// that started it.
func detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
