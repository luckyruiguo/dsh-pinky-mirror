package ios

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	goios "github.com/danielpaulus/go-ios/ios"

	"github.com/DIYgod/pinky/internal/driver"
)

// TestInstallHelperFromWindow installs the helper the window's way: while
// Xcode works, List lists the phone as needing its helper installed, with
// what it knew of it; a second install waits its turn; afterwards List
// looks at the phone afresh.
func TestInstallHelperFromWindow(t *testing.T) {
	p := NewProvider()
	p.HelperDir = t.TempDir()
	inspected := map[string]int{}
	p.inspectFn = func(_ context.Context, e goios.DeviceEntry) driver.Info {
		inspected[e.Properties.SerialNumber]++
		return driver.Info{ID: e.Properties.SerialNumber, Name: "Ann's iPhone", State: driver.StateOnline, Backend: backendCoreDevice}
	}
	ctx := context.Background()
	on := entry("on", 1, "Network")
	if got := p.info(ctx, on); got.State != driver.StateOnline {
		t.Fatalf("before: %+v", got)
	}
	var steps []driver.HelperStep
	p.install.installF = func(ctx context.Context, o HelperInstall) error {
		if o.Phone != "on" || o.Team != "TEAMAAAAA1" || o.Dir != p.HelperDir {
			t.Errorf("install %+v", o)
		}
		o.Progress(driver.HelperBuilding)
		o.Out.Write([]byte("Building WebDriverAgent with Xcode\nhalf a line"))
		got := p.info(ctx, on)
		if got.State != driver.StateNeedsSetup || got.Setup != driver.SetupInstallHelper || got.Detail != helperInstallingDetail ||
			got.Name != "Ann's iPhone" || got.Connection != "wifi" {
			t.Errorf("while installing: %+v", got)
		}
		if err := p.InstallHelper(ctx, "other", "", nil); !errors.Is(err, errHelperBusy) {
			t.Errorf("a second install: %v", err)
		}
		if got := p.info(ctx, entry("other", 2, "USB")); got.State != driver.StateOnline {
			t.Errorf("another phone while installing: %+v", got)
		}
		return nil
	}
	if !p.CanInstallHelper() {
		t.Fatal("cannot install")
	}
	n := inspected["on"]
	err := p.InstallHelper(ctx, "on", "TEAMAAAAA1", func(s driver.HelperStep) { steps = append(steps, s) })
	if err != nil || len(steps) != 1 || steps[0] != driver.HelperBuilding {
		t.Fatalf("install: %v, steps %q", err, steps)
	}
	if got := p.info(ctx, on); got.State != driver.StateOnline || inspected["on"] != n+1 {
		t.Errorf("after: %+v, inspected %d times more", got, inspected["on"]-n)
	}

	// A failed install says why, and frees the phone.
	p.install.installF = func(context.Context, HelperInstall) error {
		return &driver.HelperInstallError{Fix: driver.HelperFixUnlock, Err: errors.New("the phone is locked")}
	}
	var failed *driver.HelperInstallError
	if err := p.InstallHelper(ctx, "on", "", nil); !errors.As(err, &failed) || failed.Fix != driver.HelperFixUnlock {
		t.Errorf("failed install: %v", err)
	}
	if got := p.info(ctx, on); got.State != driver.StateOnline {
		t.Errorf("after a failure: %+v", got)
	}
}

func TestLineLog(t *testing.T) {
	var buf bytes.Buffer
	l := &lineLog{log: slog.New(slog.NewTextHandler(&buf, nil))}
	l.Write([]byte("one\ntw"))
	l.Write([]byte("o\n\nthree"))
	l.flush()
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		_, msg, _ := strings.Cut(line, "msg=")
		got = append(got, msg)
	}
	want := `"iphone: helper install: one"|"iphone: helper install: two"|"iphone: helper install: three"`
	if strings.Join(got, "|") != want {
		t.Errorf("lines %q", got)
	}
}
