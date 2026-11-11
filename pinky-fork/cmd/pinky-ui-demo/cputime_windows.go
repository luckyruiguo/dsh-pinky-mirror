//go:build windows

package main

import (
	"syscall"
	"time"
)

func cpuTimes() (user, sys time.Duration) {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, 0
	}
	var c, e, k, u syscall.Filetime
	if err := syscall.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return 0, 0
	}
	ft := func(f syscall.Filetime) time.Duration {
		return time.Duration((int64(f.HighDateTime)<<32 | int64(f.LowDateTime)) * 100)
	}
	return ft(u), ft(k)
}
