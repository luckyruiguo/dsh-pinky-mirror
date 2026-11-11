//go:build e2e

package ui

import (
	"os"

	mg "github.com/egoist/mygo/ui"
)

// In test builds, PINKY_E2E_URLS names a file that the addresses the app
// would open in the browser are appended to instead, such as Send
// Feedback's new GitHub issue.
func init() {
	path := os.Getenv("PINKY_E2E_URLS")
	if path == "" {
		return
	}
	openURL = func(_ *mg.Context, u string, done func(error)) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err == nil {
			_, err = f.WriteString(u + "\n")
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		done(err)
	}
}
