//go:build !unix

package api

import "os"

// lock on systems without flock only opens the lock file: Listen tells a
// running gateway by its answer on the socket. Windows support is untested.
func lock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
}

// ownPrivateDir: no /run/user/<uid> here.
func ownPrivateDir(string) bool { return false }
