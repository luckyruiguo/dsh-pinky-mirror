package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/driver/ios"
)

// providerFactories builds the real device providers. The gateway itself
// never branches on the platform.
var providerFactories = platformFactories(true, true)

// platformFactories returns the providers for Android and for iOS, which
// on a Mac has its Simulators too.
func platformFactories(android, iOS bool) []func(log *slog.Logger) (driver.Provider, error) {
	var f []func(log *slog.Logger) (driver.Provider, error)
	if android {
		f = append(f, newAndroidProvider)
	}
	if iOS {
		f = append(f, newIOSProvider)
		if runtime.GOOS == "darwin" {
			f = append(f, newSimProvider)
		}
	}
	return f
}

// newSimProvider lists the iOS Simulators booted on this Mac as iPhones
// and iPads, and starts them from Add phone. It builds the WebDriverAgent
// helper they are driven through with the Mac's Xcode, into Pinky's data
// folder.
func newSimProvider(log *slog.Logger) (driver.Provider, error) {
	dirs, err := api.DefaultDirs()
	if err != nil {
		return nil, err
	}
	p := ios.NewSimProvider(filepath.Join(dirs.Data, "WebDriverAgent"))
	p.Log = log
	return p, nil
}

func newAndroidProvider(log *slog.Logger) (driver.Provider, error) {
	var noADB sync.Once
	var netFile, idFile string
	if dirs, err := api.DefaultDirs(); err == nil {
		// The phones Pinky reaches over the network without Wireless
		// debugging: moved to Wi-Fi, or away from home through Tailscale.
		netFile = filepath.Join(dirs.Config, "network-phones.json")
		// The marks Pinky left on its phones, to tell them over the
		// network from a device that claims their names.
		idFile = filepath.Join(dirs.Config, "phone-identities.json")
	}
	// adb's own discovery stops seeing the network after the computer's
	// network changes; the driver then reconnects the phones paired over
	// Wi-Fi itself. Not while PINKY_DEVICES limits Pinky to some phones:
	// the others belong to other tools.
	reconnect := strings.TrimSpace(os.Getenv("PINKY_DEVICES")) == ""
	return android.NewProvider(android.Config{
		Client: &adb.Client{},
		// Pinky uses the adb server that runs, or starts one, with the
		// adb of the Android SDK, Homebrew or PATH, else its own
		// (adb.StartServer): at the first List, and whenever the server
		// went away. Without any adb (a build without Pinky's own), it
		// lists no phones and CheckHost says why.
		//
		// A server Pinky starts does not connect by itself to the phones
		// its discovery finds: the driver does (ReconnectWiFi), by the
		// same names, and only to the phones it uses. Under PINKY_DEVICES,
		// where the driver connects nothing by itself, the server does as
		// it always did.
		StartServer: func(ctx context.Context) error {
			t, err := adb.StartServerWith(ctx, adb.ServerOptions{NoAutoConnect: reconnect})
			switch {
			case errors.Is(err, adb.ErrNoADB):
				noADB.Do(func() { log.Warn("android: no adb server, and no adb to start one", "err", err) })
				return err
			case err != nil:
				log.Warn("android: starting an adb server", "err", err)
				return err
			}
			if t.Path != "" {
				log.Info("android: started an adb server", "adb", t.Path, "from", t.Source, "version", t.Version,
					"auto_connect", !reconnect)
			}
			return nil
		},
		ReconnectWiFi: reconnect,
		NetworkFile:   netFile,
		// A phone reached over the network must carry the mark Pinky left
		// on it, so that a device on the network cannot pose as it.
		VerifyWiFi:   true,
		IdentityFile: idFile,
		Logf: func(format string, args ...any) {
			log.Debug(fmt.Sprintf(format, args...), "platform", "android")
		},
		// What the driver did to the adb server (it stopped one of
		// Pinky's that could not reach the network) belongs in the log.
		Infof: func(format string, args ...any) {
			log.Info(fmt.Sprintf(format, args...), "platform", "android")
		},
	}), nil
}

// newIOSProvider always succeeds: without usbmuxd (Linux without the
// package or before an iPhone is plugged in, Windows without Apple
// Devices) it lists no phones and says what is missing (CheckHost), and it
// finds phones once usbmuxd runs.
func newIOSProvider(log *slog.Logger) (driver.Provider, error) {
	p := ios.NewProvider()
	p.Log = log
	// Under PINKY_DEVICES the provider does not even look at the other
	// phones, over a cable or Wi-Fi: other tools use them.
	p.Only = onlyDevices()
	// On Linux and Windows, Pinky finds iPhones on Wi-Fi itself: the
	// system's usbmuxd lists them there not at all, or only while they are
	// on a cable too.
	p.WiFiMux = true
	if dirs, err := api.DefaultDirs(); err == nil {
		// Where Pinky looks for Apple's developer disk image before Xcode's.
		p.DiskImageDir = filepath.Join(dirs.Data, "DeveloperDiskImages")
		// Where Pinky remembers the helper runners it started, to end one
		// it left running after a crash.
		p.RunDir = filepath.Join(dirs.Data, "HelperRuns")
		// Where the window's Install the helper keeps WebDriverAgent's
		// source and its log, as pinky helper install does.
		p.HelperDir = filepath.Join(dirs.Data, "WebDriverAgent")
	}
	if c := p.CheckHost(); c.Missing != "" {
		log.Info("iPhones need software this computer lacks", "missing", c.Missing, "detail", c.Detail)
	}
	return p, nil
}

// realProviders returns the providers for real phones. A provider that
// fails to start is skipped with a warning, so one missing tool (adb,
// usbmuxd) does not take the others down. PINKY_DEVICES, a comma-separated
// list of device ids, limits Pinky to those phones (for development, when
// other tools use the rest).
func realProviders(log *slog.Logger) []driver.Provider {
	only := onlyDevices()
	var out []driver.Provider
	for _, f := range providerFactories {
		p, err := f(log)
		if err != nil {
			log.Warn("device provider unavailable", "err", err)
			continue
		}
		if only != nil {
			// Each provider adds the phones the person adds to its own
			// copy.
			op := &onlyProvider{Provider: p, only: maps.Clone(only)}
			if l, ok := p.(limiter); ok {
				// The provider leaves the other phones alone itself.
				l.Limit(op.allowed)
			}
			p = op
			if pr, ok := op.Provider.(driver.Pairer); ok {
				p = &onlyPairer{onlyProvider: op, pairer: pr}
			}
			if w, ok := op.Provider.(wifiProvider); ok {
				p = &onlyWiFi{onlyProvider: op, wifi: w}
			}
		}
		out = append(out, p)
	}
	return out
}

// limiter is a provider that can leave alone, by itself, the phones Pinky
// may not use: it does not even ask them what they are (Android).
type limiter interface {
	Limit(allowed func(id string) bool)
}

// onlyDevices reads PINKY_DEVICES: nil when it is not set.
func onlyDevices() map[string]bool {
	v := strings.TrimSpace(os.Getenv("PINKY_DEVICES"))
	if v == "" {
		return nil
	}
	only := map[string]bool{}
	for _, id := range strings.Split(v, ",") {
		only[strings.TrimSpace(id)] = true
	}
	return only
}

// onlyProvider hides every device not in only. It is used as a pointer,
// since the gateway compares providers.
type onlyProvider struct {
	driver.Provider

	mu   sync.Mutex
	only map[string]bool
}

// VirtualPhones, StartVirtual and StopVirtual pass the provider's virtual
// phones on (driver.VirtualLauncher). One the person starts joins the
// phones Pinky may use, once it runs; one already running, at once.
func (p *onlyProvider) VirtualPhones(ctx context.Context) (driver.VirtualPhones, error) {
	l, ok := p.Provider.(driver.VirtualLauncher)
	if !ok {
		return driver.VirtualPhones{}, fmt.Errorf("virtual phones: %w", driver.ErrUnsupported)
	}
	return l.VirtualPhones(ctx)
}

func (p *onlyProvider) StartVirtual(ctx context.Context, id string) error {
	l, ok := p.Provider.(driver.VirtualLauncher)
	if !ok {
		return fmt.Errorf("virtual phones: %w", driver.ErrUnsupported)
	}
	if err := l.StartVirtual(ctx, id); err != nil {
		return err
	}
	// An emulator's adb serial is known once it runs.
	go func() {
		for range 90 {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			vp, err := l.VirtualPhones(ctx)
			cancel()
			if err == nil {
				for _, v := range vp.Phones {
					if v.ID == id && v.DeviceID != "" {
						p.allow(v.DeviceID)
						return
					}
				}
			}
			time.Sleep(2 * time.Second)
		}
	}()
	return nil
}

func (p *onlyProvider) StopVirtual(ctx context.Context, id string) error {
	l, ok := p.Provider.(driver.VirtualLauncher)
	if !ok {
		return fmt.Errorf("virtual phones: %w", driver.ErrUnsupported)
	}
	return l.StopVirtual(ctx, id)
}

func (p *onlyProvider) allowed(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.only[id]
}

func (p *onlyProvider) allow(id string) {
	p.mu.Lock()
	p.only[id] = true
	p.mu.Unlock()
}

func (p *onlyProvider) List(ctx context.Context) ([]driver.Info, error) {
	infos, err := p.Provider.List(ctx)
	out := infos[:0]
	for _, info := range infos {
		if p.allowed(info.ID) {
			out = append(out, info)
		}
	}
	return out, err
}

func (p *onlyProvider) Open(ctx context.Context, id string) (driver.Device, error) {
	if !p.allowed(id) {
		return nil, fmt.Errorf("%s is not in PINKY_DEVICES: %w", id, driver.ErrNotFound)
	}
	return p.Provider.Open(ctx, id)
}

// onlyPairer is an onlyProvider whose provider pairs phones over Wi-Fi: it
// keeps pairing available, and a phone the person pairs joins the phones
// Pinky may use.
type onlyPairer struct {
	*onlyProvider
	pairer driver.Pairer
}

func (p *onlyPairer) StartPairing(ctx context.Context) (driver.Pairing, error) {
	pg, err := p.pairer.StartPairing(ctx)
	if err != nil {
		return nil, err
	}
	return &allowOnPair{Pairing: pg, p: p.onlyProvider}, nil
}

func (p *onlyPairer) PairWithCode(ctx context.Context, addr, code string) (driver.Info, error) {
	info, err := p.pairer.PairWithCode(ctx, addr, code)
	if err == nil {
		p.allow(info.ID)
	}
	return info, err
}

// ConnectWiFi connects a paired phone at the address it shows, and lets it
// in as a pairing does.
func (p *onlyPairer) ConnectWiFi(ctx context.Context, addr string) (driver.Info, error) {
	wc, ok := p.pairer.(driver.WiFiConnector)
	if !ok {
		return driver.Info{}, fmt.Errorf("connecting by address: %w", driver.ErrUnsupported)
	}
	info, err := wc.ConnectWiFi(ctx, addr)
	if err == nil {
		p.allow(info.ID)
	}
	return info, err
}

// Tailnet, ConnectTailnet, Away and SetAway pass Tailscale on
// (driver.Tailnet); a phone connected through it joins the phones Pinky
// may use, as a pairing does.
func (p *onlyPairer) Tailnet(ctx context.Context) (driver.TailnetStatus, error) {
	t, ok := p.pairer.(driver.Tailnet)
	if !ok {
		return driver.TailnetStatus{}, fmt.Errorf("tailscale: %w", driver.ErrUnsupported)
	}
	return t.Tailnet(ctx)
}

func (p *onlyPairer) ConnectTailnet(ctx context.Context, addr string) (driver.Info, error) {
	t, ok := p.pairer.(driver.Tailnet)
	if !ok {
		return driver.Info{}, fmt.Errorf("tailscale: %w", driver.ErrUnsupported)
	}
	info, err := t.ConnectTailnet(ctx, addr)
	if err == nil {
		p.allow(info.ID)
	}
	return info, err
}

func (p *onlyPairer) Away(ctx context.Context, id string) (driver.Away, error) {
	t, ok := p.pairer.(driver.Tailnet)
	if !ok || !p.allowed(id) {
		return driver.Away{}, fmt.Errorf("tailscale: %w", driver.ErrUnsupported)
	}
	return t.Away(ctx, id)
}

func (p *onlyPairer) SetAway(ctx context.Context, id string, on bool) (driver.Away, error) {
	t, ok := p.pairer.(driver.Tailnet)
	if !ok || !p.allowed(id) {
		return driver.Away{}, fmt.Errorf("tailscale: %w", driver.ErrUnsupported)
	}
	return t.SetAway(ctx, id, on)
}

// PhoneWiFi and SetPhoneWiFi pass the Wi-Fi switch for Android phones
// without Wireless debugging on (driver.WiFiSwitch).
func (p *onlyPairer) PhoneWiFi(ctx context.Context, id string) (driver.WiFiState, error) {
	w, ok := p.pairer.(driver.WiFiSwitch)
	if !ok || !p.allowed(id) {
		return driver.WiFiState{}, fmt.Errorf("wi-fi: %w", driver.ErrUnsupported)
	}
	return w.PhoneWiFi(ctx, id)
}

func (p *onlyPairer) SetPhoneWiFi(ctx context.Context, id string, on bool) (driver.WiFiState, error) {
	w, ok := p.pairer.(driver.WiFiSwitch)
	if !ok || !p.allowed(id) {
		return driver.WiFiState{}, fmt.Errorf("wi-fi: %w", driver.ErrUnsupported)
	}
	return w.SetPhoneWiFi(ctx, id, on)
}

// UseOverWiFi and TrustPhone pass on what the person says about a phone
// on the network (driver.WiFiUser, driver.PhoneTruster), for the phones
// Pinky may use.
func (p *onlyPairer) UseOverWiFi(id string) {
	if w, ok := p.pairer.(driver.WiFiUser); ok && p.allowed(id) {
		w.UseOverWiFi(id)
	}
}

func (p *onlyPairer) TrustPhone(ctx context.Context, id string) error {
	t, ok := p.pairer.(driver.PhoneTruster)
	if !ok || !p.allowed(id) {
		return fmt.Errorf("trusting a phone: %w", driver.ErrUnsupported)
	}
	return t.TrustPhone(ctx, id)
}

// wifiProvider is a provider that reaches phones over Wi-Fi by itself and
// is told which phones the person removed (iPhones).
type wifiProvider interface {
	driver.WiFiFinder
	driver.Forgetter
}

// onlyWiFi is an onlyProvider whose provider reaches phones over Wi-Fi by
// itself: it passes on which phones are the person's, which to leave
// alone, and whether to use Wi-Fi at all.
type onlyWiFi struct {
	*onlyProvider
	wifi wifiProvider
}

func (p *onlyWiFi) SetWiFi(on bool)       { p.wifi.SetWiFi(on) }
func (p *onlyWiFi) UseOverWiFi(id string) { p.wifi.UseOverWiFi(id) }
func (p *onlyWiFi) Forget(id string)      { p.wifi.Forget(id) }
func (p *onlyWiFi) Remember(id string)    { p.wifi.Remember(id) }
func (p *onlyWiFi) Disconnect(ctx context.Context, id string) error {
	return p.wifi.Disconnect(ctx, id)
}

// RestartDeviceService passes the restart of usbmuxd on, when the provider
// can.
func (p *onlyWiFi) RestartDeviceService(ctx context.Context, reason string) error {
	if r, ok := p.wifi.(driver.DeviceServiceRestarter); ok {
		return r.RestartDeviceService(ctx, reason)
	}
	return fmt.Errorf("restarting the device service: %w", driver.ErrUnsupported)
}

type allowOnPair struct {
	driver.Pairing
	p *onlyProvider
}

func (a *allowOnPair) Wait(ctx context.Context) (driver.Info, error) {
	info, err := a.Pairing.Wait(ctx)
	if err == nil {
		a.p.allow(info.ID)
	}
	return info, err
}

// Progress passes the pairing's progress on (driver.PairingReporter).
func (a *allowOnPair) Progress() driver.PairingProgress {
	if r, ok := a.Pairing.(driver.PairingReporter); ok {
		return r.Progress()
	}
	return driver.PairingProgress{}
}
