//go:build device

package ios

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	goios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
)

// TestFindRunnerProbe lists the test runners installed on the phone and
// the one Pinky would use, through lockdown only. PINKY_RUNNERPROBE=1.
func TestFindRunnerProbe(t *testing.T) {
	if os.Getenv("PINKY_RUNNERPROBE") == "" {
		t.Skip("PINKY_RUNNERPROBE not set")
	}
	e, err := goios.GetDevice(os.Getenv("PINKY_DEVICE"))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := installationproxy.New(e)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	apps, err := conn.BrowseUserApps()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range apps {
		id, _ := a["CFBundleIdentifier"].(string)
		if strings.HasSuffix(id, ".xctrunner") || strings.Contains(strings.ToLower(id), "webdriver") {
			name, _ := a["CFBundleName"].(string)
			exe, _ := a["CFBundleExecutable"].(string)
			ver, _ := a["CFBundleShortVersionString"].(string)
			signer, _ := a["SignerIdentity"].(string)
			t.Logf("runner %s name %q exe %q version %q signer %q", id, name, exe, ver, signer)
		}
	}
	t.Logf("Pinky would use: %q", pickWDARunner(apps))
}

// TestWDAStatusProbe asks WDA's /status on the phone's port 8100, through
// usbmuxd, without starting anything. PINKY_RUNNERPROBE=1.
func TestWDAStatusProbe(t *testing.T) {
	if os.Getenv("PINKY_RUNNERPROBE") == "" {
		t.Skip("PINKY_RUNNERPROBE not set")
	}
	e, err := goios.GetDevice(os.Getenv("PINKY_DEVICE"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := newWDAUSB(e).status(ctx)
	t.Logf("WDA on 8100: %+v, %v", st, err)
}
