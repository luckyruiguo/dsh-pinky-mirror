//go:build !windows

package main

import (
	"syscall"
	"time"
)

func cpuTimes() (user, sys time.Duration) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, 0
	}
	return time.Duration(ru.Utime.Nano()), time.Duration(ru.Stime.Nano())
}
