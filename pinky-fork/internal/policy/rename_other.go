//go:build !windows

package policy

// renameBusy reports whether a rename failed because another program held
// the file open. Only Windows refuses a rename over an open file.
func renameBusy(error) bool { return false }
