//go:build device

package mdns

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestDeviceBrowse searches the real network for wireless debugging
// services. With PINKY_DEVICE set to a phone's serial, it waits for that
// phone's connect service ("adb-<serial>-…"), which shows while Wireless
// debugging is on and the phone is awake (a phone with its screen off may
// drop mDNS questions).
//
//	PINKY_DEVICE=3e8be48e go test -tags device -run TestDeviceBrowse ./internal/driver/android/mdns
//
// On macOS the test binary needs Local Network access: a binary the system
// has not allowed sends without an error and gets nothing back, which Err
// reports as ErrNotAllowed.
func TestDeviceBrowse(t *testing.T) {
	b, err := Browse([]string{"_adb-tls-connect._tcp", "_adb-tls-pairing._tcp"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	serial := os.Getenv("PINKY_DEVICE")
	start := time.Now()
	for time.Since(start) < 15*time.Second {
		for _, s := range b.Services() {
			if serial != "" && s.Type == "_adb-tls-connect._tcp" && strings.HasPrefix(s.Instance, "adb-"+serial+"-") && s.Addr() != "" {
				t.Logf("found %v after %v", s, time.Since(start).Round(time.Millisecond))
				for _, s := range b.Services() {
					t.Logf("  %v", s)
				}
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("Err: %v", b.Err())
	for _, s := range b.Services() {
		t.Logf("  %v", s)
	}
	if serial != "" {
		t.Errorf("no connect service of %s in 15 s", serial)
	}
}
