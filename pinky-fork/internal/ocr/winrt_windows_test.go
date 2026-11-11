//go:build windows

package ocr

import "testing"

// TestWindowsSetup says what the recognizer found, for the logs.
func TestWindowsSetup(t *testing.T) {
	err := Supported()
	t.Logf("Supported: %v; language %q, largest side %d", err, engineSetup.language, engineSetup.maxSide)
}
