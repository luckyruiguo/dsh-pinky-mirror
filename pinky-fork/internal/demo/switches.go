package demo

import (
	"context"
	"fmt"
	"strings"

	"github.com/DIYgod/pinky/internal/driver"
)

// The demo phone's switches (phone_device) and apps' languages, in memory.
// A phone starts upright, light, with no simulated location.

var (
	_ driver.Switcher       = (*Device)(nil)
	_ driver.LocaleLauncher = (*Device)(nil)
)

// Switch implements driver.Switcher.
func (d *Device) Switch(ctx context.Context, name string) (string, error) {
	if err := d.check(ctx); err != nil {
		return "", err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.switchErr(name); err != nil {
		return "", err
	}
	return d.switchValue(name), nil
}

// SetSwitch implements driver.Switcher.
func (d *Device) SetSwitch(ctx context.Context, name, value string) error {
	if err := d.check(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	if err := d.switchErr(name); err != nil {
		d.mu.Unlock()
		return err
	}
	ok := false
	switch name {
	case driver.SwitchRotation:
		switch value {
		case driver.RotationPortrait, driver.RotationLandscape, driver.RotationUpsideDown, driver.RotationReverseLandscape, driver.RotationAuto:
			ok = true
		}
	case driver.SwitchAppearance:
		ok = value == driver.AppearanceLight || value == driver.AppearanceDark || value == driver.AppearanceAuto
	case driver.SwitchLocation:
		_, _, err := driver.ParseLocation(value)
		ok = value == "" || err == nil
	}
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("demo: no %s %q: %w", name, value, driver.ErrUnsupported)
	}
	if d.switches == nil {
		d.switches = map[string]string{}
	}
	d.switches[name] = value
	d.mu.Unlock()
	d.record(Input{Kind: "switch", Text: name + "=" + value})
	return nil
}

func (d *Device) switchErr(name string) error {
	if why, ok := d.noSwitch[name]; ok {
		return fmt.Errorf("%s: %w", why, driver.ErrUnsupported)
	}
	switch name {
	case driver.SwitchRotation, driver.SwitchAppearance, driver.SwitchLocation:
		return nil
	}
	return fmt.Errorf("demo: no switch %q: %w", name, driver.ErrUnsupported)
}

func (d *Device) switchValue(name string) string {
	if v, ok := d.switches[name]; ok {
		return v
	}
	switch name {
	case driver.SwitchRotation:
		return driver.RotationPortrait
	case driver.SwitchAppearance:
		return driver.AppearanceLight
	}
	return ""
}

// SwitchValue returns a switch's value, for tests.
func (d *Device) SwitchValue(name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.switchValue(name)
}

// NoSwitch makes a switch unsupported, saying why.
func (d *Device) NoSwitch(name, why string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.noSwitch == nil {
		d.noSwitch = map[string]string{}
	}
	d.noSwitch[name] = why
}

// SetVirtual makes the phone an emulator or a Simulator (driver.Info.Virtual).
func (d *Device) SetVirtual(on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.info.Virtual = on
}

// LaunchLocale implements driver.LocaleLauncher, as Android's per-app
// languages do: the language stays the app's until the returned func puts
// the old one back.
func (d *Device) LaunchLocale(ctx context.Context, app string, loc driver.Locale) (func(context.Context) error, error) {
	id := ""
	for _, a := range d.apps() {
		if a.ID == app || strings.EqualFold(a.Name, app) {
			id = a.ID
		}
	}
	if id == "" {
		return nil, fmt.Errorf("app %q: %w", app, driver.ErrNotFound)
	}
	tag := loc.Tag()
	d.mu.Lock()
	if d.appLocales == nil {
		d.appLocales = map[string]string{}
	}
	was := d.appLocales[id]
	d.appLocales[id] = tag
	d.mu.Unlock()
	if err := d.Launch(ctx, id); err != nil {
		d.mu.Lock()
		d.appLocales[id] = was
		d.mu.Unlock()
		return nil, err
	}
	return func(ctx context.Context) error {
		if err := d.check(ctx); err != nil {
			return err
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.appLocales[id] != tag {
			return driver.ErrSwitchChanged
		}
		d.appLocales[id] = was
		return nil
	}, nil
}

// AppLocale returns the language an app was opened in, "" for the
// phone's, for tests.
func (d *Device) AppLocale(app string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.appLocales[app]
}

// SetAppLocale sets an app's language, as if the person chose it in
// Settings.
func (d *Device) SetAppLocale(app, tag string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.appLocales == nil {
		d.appLocales = map[string]string{}
	}
	d.appLocales[app] = tag
}
