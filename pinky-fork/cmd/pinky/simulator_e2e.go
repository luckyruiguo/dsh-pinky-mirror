//go:build e2e

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/ios"
	"howett.net/plist"
)

// In the e2e build, PINKY_WDA_URL adds a WebDriverAgent server, such as one
// running on the iOS Simulator, as an iPhone, so the desktop app can be
// checked and filmed with a simulator. Release builds never list one.
//
//	PINKY_WDA_URL=http://127.0.0.1:18200 PINKY_MJPEG_URL=http://127.0.0.1:19200 \
//	  PINKY_WDA_NAME="Test iPhone" PINKY_WDA_OS=26.5 PINKY_SIM_UDID=<udid> build/pinky-e2e
//
// The phone's id is the URL, so PINKY_DEVICES can name it. With
// PINKY_WDA_ONLY=1 it is the only phone: Pinky does not look for Android
// phones or for iPhones on USB at all. PINKY_SIM_UDID names the simulator,
// whose app list (from simctl) lets agents open apps by name, and links in
// the apps that declare their schemes, as on a phone.
func init() {
	url := os.Getenv("PINKY_WDA_URL")
	if url == "" {
		return
	}
	name := os.Getenv("PINKY_WDA_NAME")
	if name == "" {
		name = "iPhone Simulator"
	}
	p := &wdaURLProvider{
		info: driver.Info{
			ID:         url,
			Platform:   driver.IOS,
			Name:       name,
			Model:      os.Getenv("PINKY_WDA_MODEL"),
			OSVersion:  os.Getenv("PINKY_WDA_OS"),
			Connection: "usb",
			Backend:    "wda",
			State:      driver.StateOnline,
			Virtual:    true,
		},
		mjpeg: os.Getenv("PINKY_MJPEG_URL"),
		udid:  os.Getenv("PINKY_SIM_UDID"),
	}
	factory := func(*slog.Logger) (driver.Provider, error) { return p, nil }
	if os.Getenv("PINKY_WDA_ONLY") != "" {
		providerFactories = []func(*slog.Logger) (driver.Provider, error){factory}
		return
	}
	providerFactories = append(providerFactories, factory)
}

// wdaURLProvider lists one iPhone: the WebDriverAgent server at a URL.
type wdaURLProvider struct {
	info  driver.Info
	mjpeg string
	udid  string
}

func (p *wdaURLProvider) Platform() driver.Platform { return driver.IOS }

func (p *wdaURLProvider) List(context.Context) ([]driver.Info, error) {
	return []driver.Info{p.info}, nil
}

func (p *wdaURLProvider) Open(ctx context.Context, id string) (driver.Device, error) {
	if id != p.info.ID {
		return nil, fmt.Errorf("%s: %w", id, driver.ErrNotFound)
	}
	d, err := ios.OpenWDAURL(ctx, p.info.ID, p.mjpeg)
	if err != nil || p.udid == "" {
		return d, err
	}
	apps, schemes, err := simApps(ctx, p.udid)
	if err != nil {
		return d, nil // apps open by bundle id only, as without a UDID
	}
	d.SetLinkSchemes(func(context.Context) (map[string][]driver.App, error) { return schemes, nil })
	return &simDevice{WDADevice: d, apps: apps}, nil
}

// simDevice is a simulator's WDA device that knows the simulator's apps,
// which a phone reports through its installation proxy. Every other method,
// optional ones included, is the WDA device's own.
type simDevice struct {
	*ios.WDADevice
	apps []driver.App
}

func (d *simDevice) Apps(context.Context) ([]driver.App, error) { return d.apps, nil }

func (d *simDevice) Launch(ctx context.Context, app string) error {
	return d.WDADevice.Launch(ctx, d.bundleID(app))
}

func (d *simDevice) LaunchLocale(ctx context.Context, app string, loc driver.Locale) (func(context.Context) error, error) {
	return d.WDADevice.LaunchLocale(ctx, d.bundleID(app), loc)
}

// bundleID is the bundle id of the app with this name, or app itself.
func (d *simDevice) bundleID(app string) string {
	want := strings.TrimSpace(app)
	for _, a := range d.apps {
		if strings.EqualFold(a.Name, want) {
			return a.ID
		}
	}
	return want
}

// simApps lists a simulator's apps with their names, through simctl, and
// by URL scheme (lowercase) the apps that declare it in their Info.plist.
func simApps(ctx context.Context, udid string) ([]driver.App, map[string][]driver.App, error) {
	raw, err := exec.CommandContext(ctx, "xcrun", "simctl", "listapps", udid).Output()
	if err != nil {
		return nil, nil, fmt.Errorf("simctl listapps: %w", err)
	}
	conv := exec.CommandContext(ctx, "plutil", "-convert", "json", "-o", "-", "-")
	conv.Stdin = bytes.NewReader(raw)
	js, err := conv.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("plutil: %w", err)
	}
	var m map[string]struct {
		CFBundleDisplayName string
		CFBundleName        string
		ApplicationType     string
		Path                string
	}
	if err := json.Unmarshal(js, &m); err != nil {
		return nil, nil, err
	}
	apps := make([]driver.App, 0, len(m))
	schemes := map[string][]driver.App{}
	for id, a := range m {
		name := a.CFBundleDisplayName
		if name == "" {
			name = a.CFBundleName
		}
		app := driver.App{ID: id, Name: name, System: a.ApplicationType == "System"}
		apps = append(apps, app)
		for _, s := range bundleSchemes(a.Path) {
			schemes[s] = append(schemes[s], app)
		}
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].ID < apps[j].ID })
	return apps, schemes, nil
}

// bundleSchemes reads the URL schemes an app bundle declares, in lowercase.
func bundleSchemes(bundle string) []string {
	b, err := os.ReadFile(filepath.Join(bundle, "Info.plist"))
	if err != nil {
		return nil
	}
	var info struct {
		CFBundleURLTypes []struct {
			CFBundleURLSchemes []string
		}
	}
	if _, err := plist.Unmarshal(b, &info); err != nil {
		return nil
	}
	var out []string
	for _, t := range info.CFBundleURLTypes {
		for _, s := range t.CFBundleURLSchemes {
			if s = strings.ToLower(strings.TrimSpace(s)); s != "" && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}
