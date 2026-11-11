package ios

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	goios "github.com/danielpaulus/go-ios/ios"

	"github.com/DIYgod/pinky/internal/driver"
)

// Installing the helper from Pinky's window (driver.HelperInstaller): the
// same InstallHelper as `pinky helper install`, for a phone the provider
// lists. While it runs, List lists the phone as needing its helper
// installed, so the gateway lets go of the phone and does not open it, and
// no runner of Pinky's competes with Xcode's; once it is done, List looks
// at the phone afresh.

// helperInstallingDetail is what List says about a phone while Pinky
// installs its helper.
const helperInstallingDetail = "Pinky is installing its helper on this iPhone; keep the phone unlocked until it is done."

// xcodeRecheck is how long CanInstallHelper trusts its last look for Xcode.
const xcodeRecheck = time.Minute

// helperInstalling is the provider's state for installs from the window.
type helperInstalling struct {
	running  string    // the UDID being installed on, "" for none
	xcodeAt  time.Time // when CanInstallHelper last looked for Xcode
	xcodeOK  bool
	installF func(ctx context.Context, o HelperInstall) error // for tests: InstallHelper
}

// CanInstallHelper implements driver.HelperInstaller: on a Mac with Xcode,
// when the app gave the provider its WebDriverAgent folder.
func (p *Provider) CanInstallHelper() bool {
	p.mu.Lock()
	h := &p.install
	if h.installF != nil {
		p.mu.Unlock()
		return true
	}
	if runtime.GOOS != "darwin" || p.HelperDir == "" {
		p.mu.Unlock()
		return false
	}
	if !h.xcodeAt.IsZero() && p.clock().Sub(h.xcodeAt) < xcodeRecheck {
		ok := h.xcodeOK
		p.mu.Unlock()
		return ok
	}
	p.mu.Unlock()
	_, err := findXcode()
	p.mu.Lock()
	h.xcodeAt, h.xcodeOK = p.clock(), err == nil
	p.mu.Unlock()
	return err == nil
}

// errHelperBusy is a second install while one runs: Xcode installs on one
// phone at a time here.
var errHelperBusy = errors.New("Pinky is already installing its helper on a phone")

// InstallHelper implements driver.HelperInstaller.
func (p *Provider) InstallHelper(ctx context.Context, id, team string, progress func(driver.HelperStep)) error {
	if !p.CanInstallHelper() {
		return &driver.HelperInstallError{Fix: driver.HelperFixXcode, Err: errors.New("installing the iPhone helper needs a Mac with Xcode")}
	}
	if id != "" && p.Only != nil && !p.Only[id] {
		return &driver.HelperInstallError{Fix: driver.HelperFixPhone, Err: errors.New(id + " is not among the phones this provider may use")}
	}
	p.mu.Lock()
	if p.install.running != "" {
		p.mu.Unlock()
		return errHelperBusy
	}
	p.install.running = id
	if id == "" {
		// The only phone Xcode reaches, which Pinky does not list yet.
		p.install.running = "?"
	}
	// End a runner that waits for the passcode, and start none until
	// the install is done (info, installingInfo).
	stop := p.wdaWaits[id]
	delete(p.wdaWaits, id)
	delete(p.wdaHolds, id)
	install := p.install.installF
	p.mu.Unlock()
	if stop != nil {
		stop.stop()
	}
	if install == nil {
		install = InstallHelper
	}
	log := p.logger().With("device", id)
	log.Info("iphone: installing the helper", "team", team)
	out := &lineLog{log: log}
	err := install(ctx, HelperInstall{Dir: p.HelperDir, Phone: id, Team: team, Out: out, Progress: progress})
	out.flush()
	p.mu.Lock()
	p.install.running = ""
	p.mu.Unlock()
	// Look at the phone afresh, the helper and its profile too.
	if id != "" {
		p.forgetPhone(id)
		p.mu.Lock()
		delete(p.helpers, id)
		p.mu.Unlock()
	} else {
		p.mu.Lock()
		p.known, p.helpers = nil, nil
		p.mu.Unlock()
	}
	if err != nil {
		log.Warn("iphone: installing the helper failed", "err", err)
		return err
	}
	log.Info("iphone: installed the helper")
	return nil
}

// installingInfo is what List says about a phone while Pinky installs its
// helper on it: what it knew of the phone before, as needing the helper
// installed. ok is false for a phone it is not installing on.
func (p *Provider) installingInfo(e goios.DeviceEntry) (driver.Info, bool) {
	id := e.Properties.SerialNumber
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.install.running != id {
		return driver.Info{}, false
	}
	info := driver.Info{ID: id, Platform: driver.IOS}
	if k, ok := p.known[id]; ok {
		info = k.info
	}
	info.Connection = "usb"
	if e.Properties.ConnectionType == "Network" {
		info.Connection = "wifi"
	}
	info.State, info.Setup, info.Detail = driver.StateNeedsSetup, driver.SetupInstallHelper, helperInstallingDetail
	return info, true
}

// lineLog writes what InstallHelper prints to the log, a line at a time.
type lineLog struct {
	log *slog.Logger
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lineLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadString('\n')
		if err != nil {
			l.buf.WriteString(line) // not a whole line yet
			break
		}
		l.emit(line)
	}
	return len(p), nil
}

func (l *lineLog) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.emit(l.buf.String())
	l.buf.Reset()
}

func (l *lineLog) emit(line string) {
	if line = strings.TrimSpace(line); line != "" {
		l.log.Info("iphone: helper install: " + line)
	}
}

var _ driver.HelperInstaller = (*Provider)(nil)
