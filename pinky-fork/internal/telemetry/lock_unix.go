//go:build unix

package telemetry

import (
	"os"
	"syscall"
)

// tryLock takes an exclusive lock on f without waiting. The lock goes away
// when the process does.
func tryLock(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}
