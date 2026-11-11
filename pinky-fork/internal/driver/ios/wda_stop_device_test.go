//go:build device

package ios

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"github.com/danielpaulus/go-ios/ios/syslog"
)

// TestIdleLogProbe reads the phone's syslog for PINKY_LOGSECS seconds and
// prints only the lines holding one of PINKY_LOGKEYS (comma-separated; by
// default power assertions, the idle timer and the display services), for
// what keeps a phone awake. Run it beside TestWDAStopDevice. PINKY_LOGPROBE=1.
func TestIdleLogProbe(t *testing.T) {
	if os.Getenv("PINKY_LOGPROBE") == "" {
		t.Skip("PINKY_LOGPROBE not set")
	}
	secs := 60
	fmt.Sscan(os.Getenv("PINKY_LOGSECS"), &secs)
	e, err := findEntry(os.Getenv("PINKY_DEVICE"), os.Getenv("PINKY_WIFI") != "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := syslog.New(e)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(time.Duration(secs)*time.Second, func() { c.Close() })
	keys := strings.Split(os.Getenv("PINKY_LOGKEYS"), ",")
	if os.Getenv("PINKY_LOGKEYS") == "" {
		keys = []string{"powerd", "assertion", "idle", "dtremotedisplay", "dtuhid", "backlight", "autolock", "auto-lock", "lockscreen", "dimm"}
	}
	for {
		line, err := c.ReadLogMessage()
		if err != nil {
			return
		}
		l := strings.ToLower(line)
		for _, k := range keys {
			if strings.Contains(l, k) {
				if len(line) > 400 {
					line = line[:400]
				}
				t.Log(strings.TrimSpace(line))
				break
			}
		}
	}
}

// TestWDAStopDevice starts the helper's test the way a task connection
// does, then stops it and lists the runner's processes afterwards through
// a tunnel of its own. PINKY_STOPMODE=old stops it as Pinky did before
// (cancel, wait 5 s, close the tunnel); anything else uses stop. Either
// way the test ends any runner left. PINKY_WDASTOP=1.
func TestWDAStopDevice(t *testing.T) {
	if os.Getenv("PINKY_WDASTOP") == "" {
		t.Skip("PINKY_WDASTOP not set")
	}
	id := os.Getenv("PINKY_DEVICE")
	wifi := os.Getenv("PINKY_WIFI") != ""
	conn := "usb"
	if wifi {
		conn = "wifi"
	}
	info := driver.Info{ID: id, Platform: driver.IOS, Connection: conn, State: driver.StateOnline}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	s, err := startWDAFor(ctx, info)
	if err != nil {
		t.Fatal(err)
	}
	if s.run == nil {
		t.Fatal("WebDriverAgent already answered: a runner was left running before the test")
	}
	s.run.logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	d, err := s.open(ctx, info)
	if err != nil {
		s.stop()
		t.Fatalf("open: %v", err)
	}
	pids := func() []int {
		l, err := dialLink(ctx, id, wifi)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		pc, err := openRunnerProcs(l.entry)
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		list, err := pc.ListProcesses()
		if err != nil {
			t.Fatal(err)
		}
		return runnerPids(list, s.run.app.Path)
	}
	t.Logf("running: runner pids %v", pids())
	start := time.Now()
	if os.Getenv("PINKY_STOPMODE") == "old" {
		d.c.deleteSession(ctx)
		s.run.cancel()
		select {
		case <-s.run.done:
			t.Logf("run ended in %v", time.Since(start).Round(time.Millisecond))
		case <-time.After(5 * time.Second):
			t.Log("run still going after 5 s")
		}
		s.l.Close()
	} else {
		d.Close()
	}
	t.Logf("stopped in %v", time.Since(start).Round(time.Millisecond))
	time.Sleep(3 * time.Second)
	left := pids()
	t.Logf("after stop: runner pids %v", left)
	if len(left) > 0 {
		l, err := dialLink(ctx, id, wifi)
		if err == nil {
			n, err := endRunner(ctx, l.entry, l.base, s.run.app.Path)
			t.Logf("cleanup killed %d, err %v", n, err)
			l.Close()
		}
		t.Errorf("the runner outlived the stop: %v", left)
	}
}

// TestRunnerProcsProbe lists the phone's processes that look like a test
// runner (XCTest, WebDriverAgent, a .xctrunner bundle) without starting or
// stopping anything. PINKY_PSPROBE=1.
func TestRunnerProcsProbe(t *testing.T) {
	if os.Getenv("PINKY_PSPROBE") == "" {
		t.Skip("PINKY_PSPROBE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	l, err := dialLink(ctx, os.Getenv("PINKY_DEVICE"), os.Getenv("PINKY_WIFI") != "")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	pc, err := openRunnerProcs(l.entry)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	list, err := pc.ListProcesses()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		low := strings.ToLower(p.Path)
		if strings.Contains(low, "xctest") || strings.Contains(low, "webdriver") || strings.Contains(low, "xctrunner") || strings.Contains(low, "runner.app") {
			t.Logf("pid %d %s", p.Pid, p.Path)
		}
	}
	t.Logf("%d processes", len(list))
}

// TestAppByPathProbe names the installed app whose bundle path holds
// PINKY_APPDIR (a container UUID). PINKY_APPDIR=<uuid>.
func TestAppByPathProbe(t *testing.T) {
	dir := os.Getenv("PINKY_APPDIR")
	if dir == "" {
		t.Skip("PINKY_APPDIR not set")
	}
	e, err := findEntry(os.Getenv("PINKY_DEVICE"), os.Getenv("PINKY_WIFI") != "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := installationproxy.New(e)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	apps, err := c.BrowseUserApps()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range apps {
		if p, _ := a["Path"].(string); strings.Contains(p, dir) {
			t.Logf("%v %v", a["CFBundleIdentifier"], a["CFBundleDisplayName"])
		}
	}
}
