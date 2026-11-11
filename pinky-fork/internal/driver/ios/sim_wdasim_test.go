//go:build wdasim

package ios

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// TestSimProvider drives a booted Simulator the way Pinky does: it builds
// WebDriverAgent with the Mac's Xcode (downloading its source when
// PINKY_SIM_DIR has none), starts it on the Simulator, opens Settings and
// goes home.
//
//	PINKY_SIM_UDID=<udid> PINKY_SIM_DIR=<folder to keep the build in> \
//	  go test -tags wdasim -count=1 -v -run TestSimProvider ./internal/driver/ios/
func TestSimProvider(t *testing.T) {
	udid := os.Getenv("PINKY_SIM_UDID")
	if udid == "" {
		t.Skip("PINKY_SIM_UDID not set")
	}
	dir := os.Getenv("PINKY_SIM_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	p := NewSimProvider(dir)
	p.Log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	start := time.Now()
	var info driver.Info
	for {
		infos, err := p.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		info = driver.Info{}
		for _, i := range infos {
			if i.ID == udid {
				info = i
			}
		}
		if info.ID == "" {
			t.Fatalf("%s is not listed: %+v", udid, infos)
		}
		if info.State == driver.StateOnline {
			break
		}
		if info.Setup == driver.SetupSimulatorHelper {
			t.Fatalf("build failed: %s", info.Detail)
		}
		time.Sleep(2 * time.Second)
	}
	t.Logf("online after %s: %+v", time.Since(start).Round(time.Second), info)

	start = time.Now()
	d, err := p.Open(ctx, udid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("opened in %s", time.Since(start).Round(time.Millisecond))
	sp := d.(*simPhone)
	base := sp.run.url()

	step := time.Now()
	lap := func(what string) {
		t.Logf("%s took %s", what, time.Since(step).Round(time.Millisecond))
		step = time.Now()
	}
	apps, err := d.Apps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lap("apps")
	found := false
	for _, a := range apps {
		if a.ID == "com.apple.Preferences" && a.Name == "Settings" && a.System {
			found = true
		}
		if a.ID == "com.facebook.WebDriverAgentRunner.xctrunner" {
			t.Error("the helper is listed as an app")
		}
	}
	if !found {
		t.Errorf("Settings not among %d apps", len(apps))
	}
	if err := d.Launch(ctx, "settings"); err != nil {
		t.Fatal(err)
	}
	lap("launch")
	time.Sleep(time.Second)
	s, err := d.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lap("observe")
	if s.App.ID != "com.apple.Preferences" || s.App.Name == "" {
		t.Errorf("in front: %+v", s.App)
	}
	if err := d.Key(ctx, driver.KeyHome); err != nil {
		t.Fatal(err)
	}
	lap("home")
	if lr, ok := d.(driver.LockReporter); ok {
		if locked, err := lr.Locked(ctx); err != nil || locked {
			t.Errorf("locked = %v, %v", locked, err)
		}
	}
	lap("locked")
	if ck, ok := d.(driver.Checker); ok {
		cu, err := ck.Checkup(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cu.Checks {
			t.Logf("check %s: %s %s", c.ID, c.Status, c.Detail)
			if c.Status == driver.CheckFail {
				t.Errorf("check %s failed: %s", c.ID, c.Detail)
			}
		}
	}
	lap("checkup")

	start = time.Now()
	d.Close()
	t.Logf("closed in %s", time.Since(start).Round(time.Millisecond))
	if _, err := http.Get(base + "/status"); err == nil {
		t.Error("WebDriverAgent still answers after Close")
	}
	if _, err := os.Stat(sp.run.dir); !os.IsNotExist(err) {
		t.Errorf("the run's folder stayed: %v", err)
	}
}
