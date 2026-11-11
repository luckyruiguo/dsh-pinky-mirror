package telemetry

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestAgainstTheWorker sends a real usage report and a real crash report
// to a running telemetry Worker (worker/), for a check before deploying:
//
//	cd worker && wrangler d1 migrations apply pinky-telemetry --local && wrangler dev
//	PINKY_TELEMETRY_TEST_URL=http://127.0.0.1:8787 go test -run TestAgainstTheWorker ./internal/telemetry
//
// The reports are stored; never point it at the deployed Worker
// (worker/smoke.sh checks that one with test reports).
func TestAgainstTheWorker(t *testing.T) {
	url := os.Getenv("PINKY_TELEMETRY_TEST_URL")
	if url == "" {
		t.Skip("set PINKY_TELEMETRY_TEST_URL to a Worker run with wrangler dev")
	}
	t.Setenv("DO_NOT_TRACK", "")
	dir := t.TempDir()
	runChildTo(t, "panic", dir, "daemon")
	cr := capture(t, dir)
	clk := newClock()
	clk.t = time.Now().Add(-25 * time.Hour)
	c := Open(Options{Dir: dir, Version: "0.0.0-dev", Backend: &HTTP{URL: url, Version: "0.0.0-dev"}, Crashes: cr, Usage: true, Now: clk.now})
	c.Count("app.starts")
	c.Add("tools.phone_tap", 7)
	c.SeePhone("android.wifi", "3e8be48e")
	c.Max("phones.max_online", 1)
	clk.advance(25 * time.Hour)
	ctx := context.Background()
	c.sendUsage(ctx)
	if c.st.Failures != 0 || len(c.Report().Counts) != 0 {
		t.Fatalf("usage not sent: failures %d, counts %v", c.st.Failures, c.Report().Counts)
	}
	c.sendCrashes(ctx)
	if left := c.Crashes(); len(left) != 0 {
		t.Fatalf("crash report not sent: %+v", left)
	}
	t.Logf("install id %s, crash %s", c.InstallID(), cr.Found()[0].ID)
}
