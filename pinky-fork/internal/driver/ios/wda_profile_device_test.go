//go:build device

package ios

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	goios "github.com/danielpaulus/go-ios/ios"
	"howett.net/plist"
)

// TestHelperProfileOnPhone reads, and only reads, the phone's provisioning
// profiles and when the WebDriverAgent helper's runs out: the installation
// proxy's app list and misagent's CopyAll. It logs the helper's profile
// alone, not the others.
//
//	PINKY_DEVICE=<udid> scripts/devlock.sh <udid> go test -tags device -count=1 -v -run TestHelperProfileOnPhone ./internal/driver/ios/
func TestHelperProfileOnPhone(t *testing.T) {
	id := os.Getenv("PINKY_DEVICE")
	if id == "" {
		t.Skip("PINKY_DEVICE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	e, err := findEntry(id, false)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	runner, err := findRunnerApp(e)
	if err != nil {
		t.Fatal(err)
	}
	if runner.BundleID == "" {
		t.Skip("no WebDriverAgent helper on the phone")
	}
	t.Logf("helper %s (application identifier known: %v), found in %v", runner.BundleID, runner.AppID != "", time.Since(t0).Round(time.Millisecond))

	t0 = time.Now()
	profiles, err := readProfiles(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d provisioning profiles read in %v", len(profiles), time.Since(t0).Round(time.Millisecond))
	bare, _ := helperProfile(profiles, wdaRunner{BundleID: runner.BundleID}, id)
	pr, ok := helperProfile(profiles, runner, id)
	if !ok {
		t.Fatalf("no profile grants %s on this phone", runner.BundleID)
	}
	if bare.UUID != pr.UUID {
		t.Logf("by bundle id alone another profile would count, expiring %s", bare.Expires.Local().Format(time.RFC3339))
	}
	t.Logf("the helper's profile %q: expires %s, in %v", pr.Name, pr.Expires.Local().Format(time.RFC3339), time.Until(pr.Expires).Round(time.Minute))

	// What kind of account signed it, from the same profile.
	conn, err := goios.ConnectToService(e, misagentService)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payloads, err := copyProfiles(conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d of %d payloads parsed", len(profiles), len(payloads))
	for _, b := range payloads {
		got, err := parseProfile(b)
		if err != nil || got.UUID != pr.UUID {
			continue
		}
		content, _ := signedContent(b)
		var raw struct {
			TeamName     string    `plist:"TeamName"`
			TimeToLive   int       `plist:"TimeToLive"`
			CreationDate time.Time `plist:"CreationDate"`
		}
		plist.Unmarshal(content, &raw)
		t.Logf("free Apple ID (Personal Team): %v; time to live %d days; created %s; encoding starts % x",
			strings.Contains(raw.TeamName, "Personal Team"), raw.TimeToLive, raw.CreationDate.Local().Format(time.RFC3339), b[:min(4, len(b))])
	}

	// The provider's own read gives the same date.
	exp, err := helperExpiry(ctx, e, runner)
	if err != nil || !exp.Equal(pr.Expires) {
		t.Fatalf("helperExpiry = %v, %v; want %v", exp, err, pr.Expires)
	}
}
