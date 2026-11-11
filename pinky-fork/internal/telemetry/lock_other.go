//go:build !unix && !windows

package telemetry

import "os"

// tryLock cannot lock on this system: crashes are not captured.
func tryLock(*os.File) bool { return false }
