//go:build e2e

package main

import (
	"os"
	"strings"

	"github.com/DIYgod/pinky/internal/driver"
)

// In the e2e build, PINKY_PLATFORMS ("android", "ios", or both, separated
// by commas) limits the platforms Pinky looks for phones on, so a check of
// the app with an Android emulator leaves an iPhone that is plugged in
// alone: the iOS provider would ask it about itself even with PINKY_DEVICES
// naming other phones. Without "ios", booted Simulators stay out of Pinky
// too. Release builds look on every platform.
func init() {
	v := strings.TrimSpace(os.Getenv("PINKY_PLATFORMS"))
	if v == "" {
		return
	}
	var android, iOS bool
	for _, p := range strings.Split(v, ",") {
		switch strings.TrimSpace(p) {
		case string(driver.Android):
			android = true
		case string(driver.IOS):
			iOS = true
		}
	}
	providerFactories = platformFactories(android, iOS)
}
