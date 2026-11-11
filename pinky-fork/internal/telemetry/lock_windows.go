//go:build windows

package telemetry

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryLock takes an exclusive lock on f without waiting. The lock goes away
// when the process does. It locks one byte far past the end of the file,
// so reading and writing the file stay free.
func tryLock(f *os.File) bool {
	ol := windows.Overlapped{OffsetHigh: 0x7fffffff}
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	return err == nil
}
