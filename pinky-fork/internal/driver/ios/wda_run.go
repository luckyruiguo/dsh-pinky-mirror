package ios

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	goios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/appservice"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"github.com/danielpaulus/go-ios/ios/testmanagerd"
)

// The WebDriverAgent runner is a UI test bundle inside an app whose bundle
// id ends in ".xctrunner" (Xcode names it after the test target:
// <prefix>.WebDriverAgentRunner.xctrunner). The person builds and installs
// it once with their own signing team (see docs/notes/ios.md); Pinky finds
// it on the phone and starts its test through testmanagerd, the way Xcode's
// "Test" does, every time it opens the phone.

// wdaTestBundle is the runner's test bundle.
const wdaTestBundle = "WebDriverAgentRunner.xctest"

// wdaEnv is the runner's environment, binding both of WDA's servers to
// bind. WDA listens on every interface of the phone by default, Wi-Fi
// included, where anyone on the network could drive the phone around
// Pinky's approvals.
func wdaEnv(bind string) map[string]any {
	return map[string]any{
		"USE_IP":            bind,
		"USE_PORT":          fmt.Sprint(wdaPort),
		"MJPEG_SERVER_PORT": fmt.Sprint(mjpegPort),
	}
}

// wdaBind is the address WDA listens on, which only Pinky can reach. On a
// cable usbmuxd connects on the phone's loopback. On Wi-Fi it connects to
// the phone's network address, which anyone on the network can reach, so
// Pinky goes through its tunnel instead, whose address on the phone only
// this computer reaches.
func wdaBind(base goios.DeviceEntry, t *Tunnel) string {
	if base.Properties.ConnectionType == "Network" && t != nil {
		return t.DeviceAddr().String()
	}
	return "127.0.0.1"
}

// findWDARunner returns the bundle id of the WebDriverAgent runner
// installed on the phone, or "" when there is none.
func findWDARunner(e goios.DeviceEntry) (string, error) {
	r, err := findRunnerApp(e)
	return r.BundleID, err
}

// wdaRunner is the WebDriverAgent runner installed on a phone.
type wdaRunner struct {
	BundleID string
	// AppID is its application identifier, "<team>.<bundle id>", from
	// the entitlements it was signed with; "" when unknown.
	AppID string
	// Path is the app's folder on the phone, where its processes run
	// from; "" when unknown.
	Path string
}

// findRunnerApp finds the WebDriverAgent runner installed on the phone;
// its BundleID is "" when there is none.
func findRunnerApp(e goios.DeviceEntry) (wdaRunner, error) {
	conn, err := installationproxy.New(e)
	if err != nil {
		return wdaRunner{}, fmt.Errorf("installation proxy: %w", err)
	}
	defer conn.Close()
	apps, err := conn.BrowseUserApps()
	if err != nil {
		return wdaRunner{}, fmt.Errorf("list apps: %w", err)
	}
	return pickRunnerApp(apps), nil
}

// pickWDARunner chooses the runner among the installed apps: a test runner
// named after WebDriverAgent. Pinky's own build wins over others.
func pickWDARunner(apps []installationproxy.AppInfo) string { return pickRunnerApp(apps).BundleID }

func pickRunnerApp(apps []installationproxy.AppInfo) wdaRunner {
	var best wdaRunner
	for _, a := range apps {
		id, _ := a["CFBundleIdentifier"].(string)
		if !strings.HasSuffix(id, ".xctrunner") {
			continue
		}
		name, _ := a["CFBundleName"].(string)
		exe, _ := a["CFBundleExecutable"].(string)
		if !strings.Contains(strings.ToLower(id+" "+name+" "+exe), "webdriveragentrunner") {
			continue
		}
		if best.BundleID == "" || strings.Contains(strings.ToLower(id), "pinky") {
			best = wdaRunner{BundleID: id}
			best.Path, _ = a["Path"].(string)
			if ent, ok := a["Entitlements"].(map[string]any); ok {
				best.AppID, _ = ent["application-identifier"].(string)
			}
		}
	}
	return best
}

// wdaLog receives the runner's test log; tests point it at stderr.
var wdaLog io.Writer = io.Discard

// wdaRun is the test run hosting WebDriverAgent on a phone.
type wdaRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	log    *runnerLog

	// What stop needs to make sure the runner ended on the phone: the
	// entry the run went through (the tunnel's), the phone's own entry
	// to open another tunnel when that one is gone, and the runner app.
	entry, base goios.DeviceEntry
	app         wdaRunner
	logger      *slog.Logger // what stop did; nil: nothing
	ended       func()       // called once stop saw the runner gone; nil: none
	stopOnce    sync.Once

	mu  sync.Mutex
	err error
}

// startWDA starts the runner's test, WDA listening on bind. entry must
// reach the phone's testmanagerd: the plain usbmuxd entry below iOS 17, the
// tunnel's entry from 17.4; base is the phone's usbmuxd entry. The run
// lasts until stop, or until the test ends on the phone (the person closed
// the runner, a reboot).
func startWDA(entry, base goios.DeviceEntry, app wdaRunner, bind string) *wdaRun {
	bundleID := app.BundleID
	ctx, cancel := context.WithCancel(context.Background())
	r := &wdaRun{cancel: cancel, done: make(chan struct{}), log: &runnerLog{}, entry: entry, base: base, app: app}
	go func() {
		defer close(r.done)
		dir, err := os.MkdirTemp("", "pinky-wda-")
		if err == nil {
			defer os.RemoveAll(dir)
		}
		_, err = testmanagerd.RunTestWithConfig(ctx, testmanagerd.TestConfig{
			BundleId:           bundleID,
			TestRunnerBundleId: bundleID,
			XctestConfigName:   wdaTestBundle,
			Env:                wdaEnv(bind),
			Device:             entry,
			Listener:           testmanagerd.NewTestListener(r.log, r.log, dir),
		})
		if err == nil && ctx.Err() == nil {
			err = errors.New("the WebDriverAgent test ended")
		}
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
	}()
	return r
}

// alive reports whether the run is still going, and if not, why it ended.
func (r *wdaRun) alive() (bool, error) {
	select {
	case <-r.done:
		r.mu.Lock()
		defer r.mu.Unlock()
		return false, r.err
	default:
		return true, nil
	}
}

// A runner left on the phone is the suspect when the phone stops locking
// by itself (see "The helper's runner and auto-lock" in docs/notes/ios.md).
// Ending the run does not make sure the runner ends: go-ios kills it
// through appservice once its context is cancelled, but over Wi-Fi that
// request can take longer than Pinky waited (requests took up to 25 s
// there), the tunnel it goes through was closed right after, and a run
// that ended with its connection (the phone left the network, the tunnel
// failed) sends no kill at all. So stop also looks for the runner's
// processes and kills them, through another tunnel when the run's is gone.

// wdaStopWait is how long stop gives go-ios to end the run itself.
const wdaStopWait = 5 * time.Second

// wdaEndTimeout bounds making sure the runner ended.
const wdaEndTimeout = 30 * time.Second

// stop ends the run and the runner on the phone. It returns once the
// runner is gone, or after wdaEndTimeout.
func (r *wdaRun) stop() {
	r.stopOnce.Do(func() {
		r.cancel()
		select {
		case <-r.done:
		case <-time.After(wdaStopWait):
		}
		if r.app.Path == "" {
			return
		}
		if endHelper(r.entry, r.base, r.app.Path, r.logger) && r.ended != nil {
			r.ended()
		}
	})
}

// endHelper makes sure the runner from the app at appPath is gone from
// the phone, for up to wdaEndTimeout, logs what it did to logger, and
// reports whether the runner is gone.
func endHelper(entry, base goios.DeviceEntry, appPath string, logger *slog.Logger) bool {
	ctx, cancel := context.WithTimeout(context.Background(), wdaEndTimeout)
	defer cancel()
	killed, err := endRunner(ctx, entry, base, appPath)
	lg := logTo(logger)
	switch {
	case err != nil:
		lg.Warn("iphone: could not make sure the helper ended", "device", base.Properties.SerialNumber, "killed", killed, "err", err)
	case killed > 0:
		lg.Info("iphone: ended the helper left running", "device", base.Properties.SerialNumber, "killed", killed)
	}
	return err == nil
}

// runnerProcs lists and kills processes on a phone (appservice, iOS 17+).
type runnerProcs interface {
	ListProcesses() ([]appservice.Process, error)
	KillProcess(pid int) error
	Close() error
}

// Seams for tests.
var (
	openRunnerProcs = func(e goios.DeviceEntry) (runnerProcs, error) { return appservice.New(e) }
	dialRunnerLink  = func(ctx context.Context, base goios.DeviceEntry) (goios.DeviceEntry, func(), error) {
		l, err := dialLinkEntry(ctx, base)
		if err != nil {
			return goios.DeviceEntry{}, nil, err
		}
		return l.entry, func() { l.Close() }, nil
	}
	runnerRecheck = 300 * time.Millisecond
)

// endRunner kills every process running from the app at appPath and
// returns once none is left, with how many it killed. It goes through
// entry, or through a new tunnel from base when entry no longer reaches
// the phone.
func endRunner(ctx context.Context, entry, base goios.DeviceEntry, appPath string) (int, error) {
	var pc runnerProcs
	err := within(ctx, func() (err error) {
		pc, err = openRunnerProcs(entry)
		return err
	})
	if err != nil {
		if base.Properties.SerialNumber == "" {
			return 0, err
		}
		e, closeLink, lerr := dialRunnerLink(ctx, base)
		if lerr != nil {
			return 0, errors.Join(err, lerr)
		}
		defer closeLink()
		if err = within(ctx, func() (err error) {
			pc, err = openRunnerProcs(e)
			return err
		}); err != nil {
			return 0, err
		}
	}
	defer pc.Close()
	killed := 0
	for {
		var list []appservice.Process
		if err := within(ctx, func() (err error) {
			list, err = pc.ListProcesses()
			return err
		}); err != nil {
			return killed, fmt.Errorf("list processes: %w", err)
		}
		pids := runnerPids(list, appPath)
		if len(pids) == 0 {
			return killed, nil
		}
		for _, pid := range pids {
			if within(ctx, func() error { return pc.KillProcess(pid) }) == nil {
				killed++
			}
		}
		select {
		case <-ctx.Done():
			return killed, fmt.Errorf("the helper still runs: %w", ctx.Err())
		case <-time.After(runnerRecheck):
		}
	}
}

// runnerPids picks the processes that run from the app at appPath.
// appservice gives their executables as file URLs.
func runnerPids(list []appservice.Process, appPath string) []int {
	var pids []int
	for _, p := range list {
		exe := strings.TrimPrefix(p.Path, "file://")
		if inBundle(exe, strings.TrimSuffix(appPath, "/")) {
			pids = append(pids, p.Pid)
		}
	}
	return pids
}

// asking reports whether the phone waits for its passcode to allow
// automation (see runnerLog).
func (r *wdaRun) asking() bool { return r.log != nil && r.log.asking(time.Now()) }

// runnerLog reads the runner's log as it arrives, passing it on to wdaLog,
// for one wait: now and then iOS asks on the phone for its passcode before
// a UI test runs (after a restart, and again later: on iOS 27 half an hour
// later, after the phone had locked), and XCTest waits up to a minute for it.
// The runner logs "<XCTWaiter: 0x...> entering wait loop for 60.00s with
// expectations: `enabling automation mode`" when it starts waiting, and
// "Returning result ... for synchronous waiter <XCTWaiter: 0x...>" when
// the wait ends, either way. A wait that ends at once (automation already
// allowed) is not asking.
type runnerLog struct {
	mu     sync.Mutex
	line   []byte    // the line being read
	waiter string    // the XCTWaiter waiting for automation mode, "" when none
	since  time.Time // when it started waiting
}

// automationGrace is how long the automation wait must last to be a
// question to the person. Without one it ends within a second.
const automationGrace = 5 * time.Second

// maxLogLine bounds a log line runnerLog keeps; the lines it looks for are
// short, and longer lines are cut.
const maxLogLine = 4096

func (l *runnerLog) Write(b []byte) (int, error) {
	n := len(b)
	wdaLog.Write(b)
	l.mu.Lock()
	defer l.mu.Unlock()
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		part := b
		if i >= 0 {
			part = b[:i]
		}
		if room := maxLogLine - len(l.line); room > 0 {
			l.line = append(l.line, part[:min(len(part), room)]...)
		}
		if i < 0 {
			break
		}
		l.read(string(l.line), time.Now())
		l.line = l.line[:0]
		b = b[i+1:]
	}
	return n, nil
}

// read takes one line of the log.
func (l *runnerLog) read(line string, now time.Time) {
	switch {
	case strings.Contains(line, "entering wait loop") && strings.Contains(line, "enabling automation mode"):
		l.waiter, l.since = waiterOf(line), now
	case l.waiter != "" && strings.Contains(line, "for synchronous waiter "+l.waiter):
		l.waiter = ""
	}
}

func (l *runnerLog) asking(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.waiter != "" && now.Sub(l.since) >= automationGrace
}

// waiterOf finds the waiter a log line names, "<XCTWaiter: 0x...>".
func waiterOf(line string) string {
	i := strings.Index(line, "<XCTWaiter: ")
	if i < 0 {
		return "<XCTWaiter>"
	}
	j := strings.IndexByte(line[i:], '>')
	if j < 0 {
		return "<XCTWaiter>"
	}
	return line[i : i+j+1]
}
