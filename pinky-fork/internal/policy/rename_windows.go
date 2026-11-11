package policy

import (
	"errors"

	"golang.org/x/sys/windows"
)

// renameBusy reports whether a rename failed because another program held
// the file open for a moment, which may pass: ERROR_ACCESS_DENIED, which a
// rename over an open file gets, or a sharing or lock violation.
func renameBusy(err error) bool {
	var errno windows.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION, windows.ERROR_LOCK_VIOLATION:
		return true
	}
	return false
}
