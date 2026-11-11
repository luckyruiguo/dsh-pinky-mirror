//go:build unix

package api

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// lock takes the gateway's lock at path. It fails with ErrRunning when
// another gateway holds it; the lock goes away with the process.
func lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		pid := ""
		if b, rerr := os.ReadFile(path); rerr == nil {
			pid = strings.TrimSpace(string(b))
		}
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &RunningError{PID: pid}
		}
		return nil, err
	}
	if err := f.Truncate(0); err == nil {
		f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return f, nil
}

// ownPrivateDir reports whether dir is a folder of this user's that only
// they can reach (mode 0700), as /run/user/<uid> is.
func ownPrivateDir(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
