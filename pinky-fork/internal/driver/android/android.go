// Package android is Pinky's Android driver. It talks to phones through
// the adb server (package adb) and the bundled scrcpy server (package
// scrcpy): one long-lived scrcpy session per device carries the video
// stream, input and settle detection, and `uiautomator dump` provides the
// element tree until the companion app takes over.
package android

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/tailnet"
)

// Config tunes the driver. The zero value works.
type Config struct {
	// Client talks to the adb server; nil means the default server.
	Client *adb.Client

	// MaxSize and MaxFPS set the long-lived video session's stream when no
	// Stream call asked for something else. Zero means native size and
	// the device's frame rate.
	MaxSize int
	MaxFPS  int
	// VideoBitRate in bits per second; zero is scrcpy's default (8 Mbps),
	// or 2.5 Mbps over Wi-Fi (see Device.baseOptions).
	VideoBitRate int
	// VideoCodec is "h264" or "h265"; empty picks H.265 over Wi-Fi and
	// H.264 otherwise. A phone whose H.265 encoder fails uses H.264.
	VideoCodec string

	// SettleMax is the settle limit when WaitSettled gets none (3 s).
	SettleMax time.Duration
	// SettleQuiet is how long the video must stay still to count as
	// settled (400 ms).
	SettleQuiet time.Duration
	// SettleBytes is the packet size, in bytes, at or above which a video
	// packet counts as a change. Zero derives it from the frame size.
	SettleBytes int

	// Browser finds the phones' wireless debugging services on the network
	// without the adb server, whose own discovery stops working after the
	// computer's network changes. Nil means Pinky's mDNS browser (package
	// mdns).
	Browser Browser
	// ReconnectWiFi has List reconnect the phones this computer paired
	// with over Wi-Fi when they are on the network and the adb server has
	// not connected them: it does so by itself only while its discovery
	// works. Off by default, since it searches the network now and then.
	ReconnectWiFi bool
	// KnownHosts lists the guids of the phones the adb server paired with
	// ("adb-<serial>-<suffix>"), for ReconnectWiFi. Nil reads the server's
	// ~/.android/adb_known_hosts.pb.
	KnownHosts func() ([]string, error)

	// Tailnet reads Tailscale's status on this computer, for the phones
	// away from its network (tailnet.go); nil means tailnet.Read.
	Tailnet func(ctx context.Context) (tailnet.Status, error)
	// NetworkFile keeps the phones whose adb Pinky had listen on the
	// network, with their addresses (network.go); "" keeps them in memory
	// only.
	NetworkFile string

	// VerifyWiFi has the driver check that a phone it reaches over the
	// network is the phone it set up, and refuse one that is not
	// (identity.go): a phone on a cable, one the person pairs, connects or
	// trusts in Pinky, and the first time one Pinky knew before
	// (UseOverWiFi), is marked; any other phone on the network waits for
	// the person. Off by default, since it writes a file on the phones.
	VerifyWiFi bool
	// IdentityFile keeps the phones' marks for VerifyWiFi; "" keeps them
	// in memory only.
	IdentityFile string

	// StartServer, when set, starts an adb server when List finds none
	// (cmd/pinky passes adb.StartServerWith): at once the first time, and
	// when a server that ran went away, once it has stayed away for
	// serverAway, so that a tool that restarts it comes first (an adb
	// client that finds a server of another version kills it and starts
	// its own). After a failure, List tries again after serverRetry,
	// doubling up to serverRetryMax.
	StartServer func(ctx context.Context) error
	// StopServer stops the adb server that runs when Pinky started it, in
	// this run or an earlier one, and returns adb.ErrNotOwnServer for any
	// other, which it leaves running; nil means Client.StopOwnServer. The
	// driver stops and starts again (StartServer) a server of its own that
	// cannot reach the local network (offnet.go).
	StopServer func(ctx context.Context) error

	// Logf, when set, receives debug messages.
	Logf func(format string, args ...any)
	// Infof, when set, receives the messages worth keeping in the log at
	// any level, such as an adb server the driver stopped; nil sends them
	// to Logf.
	Infof func(format string, args ...any)
}

func (c *Config) defaults() {
	if c.Client == nil {
		c.Client = &adb.Client{}
	}
	if c.Browser == nil {
		c.Browser = mdnsBrowser{}
	}
	if c.StopServer == nil {
		c.StopServer = c.Client.StopOwnServer
	}
	if c.KnownHosts == nil {
		c.KnownHosts = readKnownHosts
	}
	if c.Tailnet == nil {
		c.Tailnet = tailnet.Read
	}
	if c.SettleMax <= 0 {
		c.SettleMax = 3 * time.Second
	}
	if c.SettleQuiet <= 0 {
		c.SettleQuiet = 400 * time.Millisecond
	}
}

func (c *Config) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

func (c *Config) infof(format string, args ...any) {
	if c.Infof != nil {
		c.Infof(format, args...)
		return
	}
	c.logf(format, args...)
}

// Provider discovers and opens Android devices.
type Provider struct {
	cfg Config

	mu    sync.Mutex
	props map[string]deviceProps // by serial and transport id
	// aidSerials maps the android_id of a phone that hides its serial from
	// the shell to the serial it was seen with (phoneID).
	aidSerials map[string]string
	// forgotten holds the phones the person removed from Pinky, by id,
	// which the driver no longer connects to by itself (forget.go).
	forgotten map[string]bool
	paused    map[string]bool // phones the person paused in Pinky (pause.go)

	wifi *reconnector // nil without Config.ReconnectWiFi
	// net keeps the phones reached over the network without Wireless
	// debugging, and link reconnects them (network.go).
	net  *netStore
	link *netLink
	// ids checks the phones reached over the network (identity.go); nil
	// without Config.VerifyWiFi.
	ids *identity

	server serverWatch
	// dial connects to an address on the network from Pinky's own
	// process, to tell a server that cannot reach it (offnet.go).
	dial func(ctx context.Context, addr string) error
	// usbNodes lists the computer's USB devices as Windows sees them, to
	// find phones without a driver (usbdriver.go); nil elsewhere.
	usbNodes func() ([]usbNode, error)
	// avd starts the Android Virtual Devices (avd.go).
	avd *avdLauncher

	// only limits the driver to some phones, by id; nil: all (limit.go).
	only func(id string) bool
	// letIn holds the adb serials of the connections to the phones the
	// person added in Pinky while it is limited.
	letIn map[string]bool
}

// NewProvider returns a provider with the given configuration.
func NewProvider(cfg Config) *Provider {
	cfg.defaults()
	p := &Provider{cfg: cfg, props: map[string]deviceProps{}, usbNodes: systemUSBNodes, avd: newAVDLauncher(), dial: dialOwn}
	if cfg.ReconnectWiFi {
		p.wifi = newReconnector(p)
	}
	p.net = &netStore{path: cfg.NetworkFile, logf: cfg.logf}
	p.link = newNetLink(p)
	if cfg.VerifyWiFi {
		p.ids = newIdentity(p)
	}
	return p
}

// Platform implements driver.Provider.
func (p *Provider) Platform() driver.Platform { return driver.Android }

// deviceProps are the device facts read once per connection.
type deviceProps struct {
	Name, Model, OSVersion string
	SDK                    int
	// HardwareSerial is ro.serialno (else ro.boot.serialno), the same
	// over USB and Wi-Fi.
	HardwareSerial string
	// AndroidID is the phone's android_id, read only when the shell may
	// read neither serial property (phoneID).
	AndroidID string
	// AVD is the name of the Android Virtual Device an emulator runs, ""
	// on a phone.
	AVD string
}

// List implements driver.Provider. It reads each online device's
// properties once per connection, starts an adb server when none runs
// (Config.StartServer), and on Windows adds the phones plugged in without
// a driver for USB debugging, which adb cannot see.
func (p *Provider) List(ctx context.Context) ([]driver.Info, error) {
	entries, err := p.cfg.Client.Devices(ctx)
	if errors.Is(err, adb.ErrServerNotRunning) && p.cfg.StartServer != nil {
		if p.server.claim(time.Now(), p.cfg.logf) {
			serr := p.cfg.StartServer(ctx)
			p.server.done(time.Now(), serr)
			if serr == nil {
				entries, err = p.cfg.Client.Devices(ctx)
			}
		}
		if err != nil && p.server.noADB() {
			// No adb at all: no phones, which CheckHost explains.
			return nil, nil
		}
	}
	if err != nil {
		return nil, wrapErr(err)
	}
	if p.server.answered() {
		if v, err := p.cfg.Client.Version(ctx); err == nil {
			p.server.setVersion(v)
		}
	}
	entries = p.usable(entries)
	if p.wifi != nil {
		p.wifi.kick(entries)
	}
	p.link.kick(entries)
	infos := make([]driver.Info, len(entries))
	props := make([]*deviceProps, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		infos[i] = infoFromEntry(e)
		if e.State != adb.StateDevice {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			if pr, err := p.deviceProps(pctx, e); err == nil {
				infos[i].Name, infos[i].Model, infos[i].OSVersion = pr.Name, pr.Model, pr.OSVersion
				if infos[i].Virtual {
					infos[i].VirtualID = pr.AVD
					p.avd.ranAt(pr.AVD, e.Serial)
				}
				props[i] = &pr
			} else if errors.Is(err, errStarting) {
				infos[i].State = driver.StateOffline
				infos[i].Detail = "The phone is still starting."
			} else {
				p.cfg.logf("android: read properties of %s: %v", e.Serial, err)
			}
		}()
	}
	wg.Wait()
	// The ids, those of the connections that name the phone's serial
	// first, so that the phone's connections at addresses take that
	// serial in this listing already (phoneID).
	for _, named := range []bool{true, false} {
		for i, e := range entries {
			if props[i] != nil && p.knowsSerial(e) == named {
				infos[i].ID = p.phoneID(e, *props[i])
			}
		}
	}
	if p.ids != nil {
		p.ids.apply(ctx, entries, infos, props)
	}
	infos = mergeTransports(p.netInfos(infos))
	return append(infos, p.driverlessPhones(infos)...), nil
}

// CheckHost implements driver.HostChecker, from what the last List found:
// "adb" when there is no adb to start a server with (a build without
// Pinky's own, on a computer without one), "adb_server" when no server
// runs and starting one failed, "adb_old" when the server that runs is
// older than protocol 41, "adb_no_network" when the server cannot reach
// phones on the local network and Pinky could not restart it (offnet.go),
// and "adb_restarts" when the server went away three times or more within
// ten minutes, as when another program keeps restarting it with an adb of
// another version.
func (p *Provider) CheckHost() driver.HostCheck { return p.server.check(time.Now()) }

// phoneID is the id Pinky knows a phone by: the adb serial, except over
// Wi-Fi, where it is the phone's hardware serial (the serial adb gives a
// USB connection), so the phone keeps one id, and its PIN, settings and
// history, whichever way it is connected. Without one, a connection that
// Wireless debugging's discovery made has the serial in its name
// ("adb-<serial>-<suffix>._adb-tls-connect._tcp"); one at an address
// keeps the address.
func phoneID(e adb.DeviceEntry, pr deviceProps) string {
	if !e.Wireless() {
		return e.Serial
	}
	if pr.HardwareSerial != "" {
		return pr.HardwareSerial
	}
	if s := connectSerial(e.Serial); s != "" {
		return s
	}
	return e.Serial
}

// connectSerial is the serial in the name of a connection that Wireless
// debugging's discovery made, "" for any other.
func connectSerial(serial string) string {
	g, ok := strings.CutSuffix(serial, connectSuffix)
	if !ok {
		return ""
	}
	return guidSerial(g)
}

// namesSerial reports whether a connection's adb serial is, or names, the
// phone's serial: a cable's, or one Wireless debugging's discovery made.
func namesSerial(e adb.DeviceEntry) bool {
	return !e.Wireless() || connectSerial(e.Serial) != ""
}

// knowsSerial reports whether a connection tells the phone's serial
// without its shell: it names it (namesSerial), or the reconnector made it
// for a paired phone, whose guid holds it.
func (p *Provider) knowsSerial(e adb.DeviceEntry) bool {
	return namesSerial(e) || (p.wifi != nil && p.wifi.guidAt(e.Serial) != "")
}

// phoneID is phoneID, also for a phone that hides its serial from the
// shell, which then reads neither ro.serialno nor ro.boot.serialno: its
// connections at addresses ("192.168.1.5:41235"; a new port each time
// Wireless debugging restarts) take the serial in the guid of the paired
// phone the reconnector connected there, else the serial that a
// connection which names it (namesSerial) had with the same android_id,
// else "android-<android_id>". Each would else be a phone of its own: a
// Galaxy S10+ was listed some 40 times, all online.
func (p *Provider) phoneID(e adb.DeviceEntry, pr deviceProps) string {
	id := phoneID(e, pr)
	if pr.HardwareSerial != "" || pr.AndroidID == "" {
		return id
	}
	if p.wifi != nil && !namesSerial(e) {
		// An address the reconnector connected for a paired phone: the
		// phone's guid holds its serial.
		if s := guidSerial(p.wifi.guidAt(e.Serial)); s != "" {
			id = s
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if id != e.Serial || !e.Wireless() {
		if p.aidSerials == nil {
			p.aidSerials = map[string]string{}
		}
		p.aidSerials[pr.AndroidID] = id
		return id
	}
	if s := p.aidSerials[pr.AndroidID]; s != "" {
		return s
	}
	return "android-" + pr.AndroidID
}

// mergeTransports lists a phone connected both ways once, by its USB
// connection while that works.
func mergeTransports(infos []driver.Info) []driver.Info {
	at := map[string]int{}
	var out []driver.Info
	for _, info := range infos {
		j, seen := at[info.ID]
		if !seen {
			at[info.ID] = len(out)
			out = append(out, info)
			continue
		}
		if better(info, out[j]) {
			out[j] = info
		}
	}
	return out
}

// better reports whether a connection of a phone is the one to use rather
// than another: an online one before one that is not, then USB before
// Wi-Fi, then the local network before Tailscale.
func better(a, b driver.Info) bool {
	if (a.State == driver.StateOnline) != (b.State == driver.StateOnline) {
		return a.State == driver.StateOnline
	}
	if (a.Connection == "usb") != (b.Connection == "usb") {
		return a.Connection == "usb"
	}
	return a.Via == "" && b.Via != ""
}

func (p *Provider) deviceProps(ctx context.Context, e adb.DeviceEntry) (deviceProps, error) {
	key := e.Serial + "#" + e.TransportID
	p.mu.Lock()
	pr, ok := p.props[key]
	p.mu.Unlock()
	if ok {
		return pr, nil
	}
	if !p.mayUse(e) {
		return deviceProps{}, errNotUsed(e.Serial)
	}
	out, err := p.cfg.Client.Shell(ctx, e.Serial, propsCommand)
	if err != nil {
		return deviceProps{}, err
	}
	lines := strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n")
	if starting(lines) {
		return deviceProps{}, errStarting
	}
	for len(lines) < 5 {
		lines = append(lines, "")
	}
	pr = deviceProps{Model: strings.TrimSpace(lines[0]), OSVersion: strings.TrimSpace(lines[1])}
	serial := func(s string) string {
		if s = strings.TrimSpace(s); s == "unknown" {
			return ""
		}
		return s
	}
	pr.HardwareSerial = serial(lines[4])
	for _, l := range lines[5:] {
		if v, ok := strings.CutPrefix(l, "pinky-boot="); ok && pr.HardwareSerial == "" {
			pr.HardwareSerial = serial(v)
		}
		if v, ok := strings.CutPrefix(l, "pinky-aid="); ok {
			if v = strings.TrimSpace(v); v != "" && v != "null" {
				pr.AndroidID = v
			}
		}
		if v, ok := strings.CutPrefix(l, "pinky-avd="); ok && emulatorSerial(e.Serial) {
			pr.AVD = strings.TrimSpace(v)
		}
	}
	fmt.Sscan(strings.TrimSpace(lines[2]), &pr.SDK)
	pr.Name = strings.TrimSpace(lines[3])
	if pr.Name == "" || pr.Name == "null" {
		pr.Name = pr.Model
	}
	if pr.Model == "" {
		pr.Model = strings.ReplaceAll(e.Model, "_", " ")
	}
	if pr.AVD != "" {
		// An emulator: named as Android Studio names its AVD, unless the
		// person renamed it in its settings, and made as the AVD's device.
		a := readAVD(p.avd.avdHome(), pr.AVD)
		if pr.Name == pr.Model {
			pr.Name = a.Display
		}
		if a.Model != "" {
			pr.Model = a.Model
		}
	}
	p.mu.Lock()
	p.props[key] = pr
	p.mu.Unlock()
	return pr, nil
}

// dropProps forgets what was read of a connection's phone: adb may have
// made it again to another device under the same serial and transport id.
func (p *Provider) dropProps(serial string) {
	p.mu.Lock()
	for k := range p.props {
		if strings.HasPrefix(k, serial+"#") {
			delete(p.props, k)
		}
	}
	p.mu.Unlock()
}

// propsCommand reads what deviceProps keeps, one line each: the model, the
// Android version, the SDK level, the name the person gave the phone and
// ro.serialno; then ro.boot.serialno, an emulator's AVD (the property
// was ro.kernel.qemu.avd_name before Android 12), whether the phone has
// finished starting, and the android_id only when the shell may read
// neither serial property (the settings command takes a moment).
const propsCommand = "getprop ro.product.model; getprop ro.build.version.release; getprop ro.build.version.sdk; " +
	"settings get global device_name; getprop ro.serialno; echo pinky-boot=$(getprop ro.boot.serialno); " +
	"echo pinky-avd=$(getprop ro.boot.qemu.avd_name)$(getprop ro.kernel.qemu.avd_name); " +
	"echo pinky-booted=$(getprop sys.boot_completed); " +
	"[ -n \"$(getprop ro.serialno)$(getprop ro.boot.serialno)\" ] || echo pinky-aid=$(settings get secure android_id)"

// errStarting is deviceProps' error for a phone adb lists before it has
// finished starting, as an emulator that boots or a phone that restarts:
// its system services are not all up, so its settings answer "cmd: Can't
// find service: settings" and it has no screen size yet. Its properties
// are read again once it has started.
var errStarting = fmt.Errorf("the phone is still starting: %w", driver.ErrOffline)

// starting reports whether propsCommand's output says the phone has not
// finished starting (sys.boot_completed is not 1 yet).
func starting(lines []string) bool {
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, "pinky-booted="); ok {
			return strings.TrimSpace(v) != "1"
		}
	}
	return false
}

func infoFromEntry(e adb.DeviceEntry) driver.Info {
	info := driver.Info{
		ID:         e.Serial,
		Platform:   driver.Android,
		Name:       strings.ReplaceAll(e.Model, "_", " "),
		Model:      strings.ReplaceAll(e.Model, "_", " "),
		Connection: "usb",
		Backend:    "scrcpy",
		Virtual:    emulatorSerial(e.Serial),
	}
	if e.Wireless() {
		info.Connection = "wifi"
		if viaTailnet(e.Serial) {
			info.Via = driver.ViaTailscale
		}
	}
	if info.Name == "" {
		info.Name = e.Serial
	}
	switch e.State {
	case adb.StateDevice:
		info.State = driver.StateOnline
	case adb.StateUnauthorized, adb.StateAuthorizing:
		info.State = driver.StateUnauthorized
		info.Detail = "Unlock the phone and allow USB debugging for this computer."
	case adb.StateNoPerm:
		// Linux: adb may not open the phone's USB device. (Add phone
		// shows "Found <phone>, but <detail>", the first letter lowered.)
		info.State = driver.StateNeedsSetup
		info.Setup = setupUSBPermission
		info.Detail = "This computer has no permission to use the phone over USB"
		if e.Detail != "" {
			info.Detail += " (" + e.Detail + ")"
		}
		info.Detail += ". Install the udev rules for Android phones (android-sdk-platform-tools-common on Debian and Ubuntu, android-udev on Arch, android-tools on Fedora), make sure you are in the plugdev group, then unplug the phone and plug it in again."
	case adb.StateRecovery, adb.StateBootloader, adb.StateSideload, adb.StateRescue:
		info.State = driver.StateOffline
		info.Detail = "The phone is in " + e.State + " mode."
	default:
		info.State = driver.StateOffline
		info.Detail = "adb reports the phone as " + e.State + "; reconnect the cable or turn USB debugging off and on."
	}
	return info
}

// Open implements driver.Provider. Open each device once and share the
// result: every open device runs its own scrcpy session.
func (p *Provider) Open(ctx context.Context, id string) (driver.Device, error) {
	return p.OpenDevice(ctx, id)
}

// OpenDevice is Open with the concrete type, for callers that use the
// Android-only features (background mode).
func (p *Provider) OpenDevice(ctx context.Context, id string) (*Device, error) {
	entries, err := p.devices(ctx)
	if err != nil {
		return nil, wrapErr(err)
	}
	// The phone's connections: its adb serial, or (over Wi-Fi) its hardware
	// serial; USB first.
	var best *driver.Info
	var bestEntry adb.DeviceEntry
	var bestProps deviceProps
	var refused error // why a connection over the network was passed over
	for _, e := range entries {
		info := infoFromEntry(e)
		var pr deviceProps
		if info.State == driver.StateOnline {
			if pr, err = p.deviceProps(ctx, e); err != nil {
				if e.Serial == id {
					return nil, wrapErr(err)
				}
				continue
			}
			info.Name, info.Model, info.OSVersion = pr.Name, pr.Model, pr.OSVersion
		}
		if e.Serial != id && p.phoneID(e, pr) != id {
			continue
		}
		if p.ids != nil && info.State == driver.StateOnline && !trusted(e) {
			// Over the network: only the phone Pinky set up (identity.go).
			v, err := p.ids.verify(ctx, e, p.phoneID(e, pr))
			if err != nil || v != idSame {
				setVerdict(&info, v, err)
				refused = fmt.Errorf("android: %s at %s: %s: %w", id, e.Serial, info.Detail, driver.ErrOffline)
				continue
			}
		}
		if best == nil || better(info, *best) {
			c := info
			best, bestEntry, bestProps = &c, e, pr
		}
	}
	if best == nil && refused != nil {
		return nil, refused
	}
	if best == nil {
		return nil, fmt.Errorf("android: device %q: %w", id, driver.ErrNotFound)
	}
	if best.State != driver.StateOnline {
		return nil, fmt.Errorf("android: %s is %s: %s: %w", id, best.State, best.Detail, driver.ErrOffline)
	}
	info := *best
	info.ID = bestEntry.Serial // the device talks to adb by its serial
	cfg := p.cfg
	if p.ids != nil && !trusted(bestEntry) {
		// Each request checks first that the connection is still the
		// phone: adb may make it again to another device (identity.go).
		ids := p.ids
		cfg.Client = &adb.Client{Addr: p.cfg.Client.Addr, Guard: func(ctx context.Context, serial string) error {
			return ids.guard(ctx, serial, id)
		}}
	}
	d := newDevice(cfg, info, bestProps)
	d.phoneID = id
	return d, nil
}

// wrapErr marks errors that mean the phone is gone as driver.ErrOffline.
func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, driver.ErrOffline) {
		return err
	}
	if errors.Is(err, adb.ErrDeviceNotFound) || errors.Is(err, adb.ErrDeviceOffline) ||
		errors.Is(err, adb.ErrServerNotRunning) || errors.Is(err, adb.ErrUnauthorized) {
		return fmt.Errorf("%w: %v", driver.ErrOffline, err)
	}
	return err
}

var (
	_ driver.Provider    = (*Provider)(nil)
	_ driver.HostChecker = (*Provider)(nil)
)

// The setup steps of a phone that needs the computer set up (Info.Setup),
// the values of driver.SetupUSBPermission and driver.SetupUSBDriver on the
// onboarding branch.
const (
	setupUSBPermission = "usb_permission" // Linux: no udev rule for the phone, or not in the plugdev group
	setupUSBDriver     = "usb_driver"     // Windows: no driver for the phone's USB debugging interface
)
