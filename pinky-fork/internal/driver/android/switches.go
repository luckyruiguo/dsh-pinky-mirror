package android

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// The switches agents set while testing an app (driver.Switcher,
// docs/notes/device-switches.md): the screen's rotation and light or dark
// through the shell's window and uimode commands, a simulated location
// through the emulator's console, and an app's own language through
// Android 13's per-app languages.

// Switch implements driver.Switcher.
func (d *Device) Switch(ctx context.Context, name string) (string, error) {
	switch name {
	case driver.SwitchRotation:
		return d.rotation(ctx)
	case driver.SwitchAppearance:
		out, err := d.sh(ctx, "cmd uimode night")
		if err != nil {
			return "", err
		}
		return parseNightMode(out)
	case driver.SwitchLocation:
		if _, err := d.consolePort(); err != nil {
			return "", err
		}
		// The emulator cannot be asked where it is, and its GPS sends
		// fixes only while an app listens: `dumpsys location` has the last
		// one an app got, which is old once a new place has been sent. So
		// the place Pinky sent wins; a change made in the emulator's
		// extended controls goes unseen.
		d.stateMu.Lock()
		sent := d.emuLocation
		d.stateMu.Unlock()
		if sent != "" {
			return sent, nil
		}
		out, err := d.sh(ctx, "dumpsys location")
		if err != nil {
			return "", err
		}
		return gpsLocation(out), nil
	}
	return "", fmt.Errorf("android: no switch %q: %w", name, driver.ErrUnsupported)
}

// SetSwitch implements driver.Switcher.
func (d *Device) SetSwitch(ctx context.Context, name, value string) error {
	switch name {
	case driver.SwitchRotation:
		return d.setRotation(ctx, value)
	case driver.SwitchAppearance:
		mode, ok := map[string]string{driver.AppearanceLight: "no", driver.AppearanceDark: "yes",
			driver.AppearanceAuto: "auto", "custom": "custom"}[value]
		if !ok {
			return fmt.Errorf("android: appearance %q: %w", value, driver.ErrUnsupported)
		}
		out, err := d.sh(ctx, "cmd uimode night "+mode)
		if err != nil {
			return err
		}
		if got, err := parseNightMode(out); err != nil || got != value {
			return fmt.Errorf("android: cmd uimode night %s: %s", mode, strings.TrimSpace(out))
		}
		return nil
	case driver.SwitchLocation:
		port, err := d.consolePort()
		if err != nil {
			return err
		}
		if value == "" {
			return fmt.Errorf("%w: an Android emulator always reports a location; it stays where it was set", driver.ErrUnsupported)
		}
		lat, lon, err := driver.ParseLocation(value)
		if err != nil {
			return err
		}
		// The console takes the longitude first.
		if err := emuCommand(ctx, port, fmt.Sprintf("geo fix %s %s",
			strconv.FormatFloat(lon, 'f', -1, 64), strconv.FormatFloat(lat, 'f', -1, 64))); err != nil {
			return err
		}
		d.stateMu.Lock()
		d.emuLocation = driver.FormatLocation(lat, lon)
		d.stateMu.Unlock()
		return nil
	}
	return fmt.Errorf("android: no switch %q: %w", name, driver.ErrUnsupported)
}

// sh runs a shell command and returns its output, stdout and stderr
// together.
func (d *Device) sh(ctx context.Context, cmd string) (string, error) {
	res, err := d.client.Run(ctx, d.info.ID, cmd)
	if err != nil {
		return "", wrapErr(err)
	}
	return string(res.Stdout) + string(res.Stderr), nil
}

// rotation reads `cmd window user-rotation` (Android 11 and later): "free"
// while the screen follows the phone, "lock N" while it stays at ROTATION_N.
// Older phones keep the same in two system settings.
func (d *Device) rotation(ctx context.Context) (string, error) {
	out, err := d.sh(ctx, "cmd window user-rotation")
	if err != nil {
		return "", err
	}
	if v, ok := parseUserRotation(out); ok {
		return v, nil
	}
	out, err = d.sh(ctx, "settings get system accelerometer_rotation; settings get system user_rotation")
	if err != nil {
		return "", err
	}
	f := strings.Fields(out)
	if len(f) != 2 {
		return "", fmt.Errorf("android: read the rotation: %q", strings.TrimSpace(out))
	}
	if f[0] == "1" {
		return driver.RotationAuto, nil
	}
	n, err := strconv.Atoi(f[1])
	if err != nil || n < 0 || n > 3 {
		n = 0 // user_rotation was never set: upright
	}
	return rotations[n], nil
}

// rotations are the switch's values by Android's ROTATION_ constants.
var rotations = [4]string{driver.RotationPortrait, driver.RotationLandscape, driver.RotationUpsideDown, driver.RotationReverseLandscape}

func parseUserRotation(out string) (string, bool) {
	f := strings.Fields(out)
	switch {
	case len(f) == 1 && f[0] == "free":
		return driver.RotationAuto, true
	case len(f) == 2 && f[0] == "lock":
		if n, err := strconv.Atoi(f[1]); err == nil && n >= 0 && n <= 3 {
			return rotations[n], true
		}
	}
	return "", false
}

func (d *Device) setRotation(ctx context.Context, value string) error {
	n := -1
	for i, r := range rotations {
		if r == value {
			n = i
		}
	}
	if n < 0 && value != driver.RotationAuto {
		return fmt.Errorf("android: rotation %q: %w", value, driver.ErrUnsupported)
	}
	cmd := "cmd window user-rotation free"
	if n >= 0 {
		cmd = fmt.Sprintf("cmd window user-rotation lock %d", n)
	}
	if _, err := d.sh(ctx, cmd); err != nil {
		return err
	}
	if got, err := d.rotation(ctx); err == nil && got == value {
		return nil
	}
	// Before Android 11 the window command has no user-rotation.
	cmd = "settings put system accelerometer_rotation 1"
	if n >= 0 {
		cmd = fmt.Sprintf("settings put system accelerometer_rotation 0; settings put system user_rotation %d", n)
	}
	if _, err := d.sh(ctx, cmd); err != nil {
		return err
	}
	if got, err := d.rotation(ctx); err != nil || got != value {
		return fmt.Errorf("android: the screen did not turn to %s", value)
	}
	return nil
}

// parseNightMode reads `cmd uimode night`: "Night mode: no".
func parseNightMode(out string) (string, error) {
	_, v, ok := strings.Cut(strings.TrimSpace(out), "Night mode: ")
	if !ok {
		return "", fmt.Errorf("android: read the night mode: %q", strings.TrimSpace(out))
	}
	switch v = strings.TrimSpace(v); v {
	case "no":
		return driver.AppearanceLight, nil
	case "yes":
		return driver.AppearanceDark, nil
	case "auto":
		return driver.AppearanceAuto, nil
	case "custom":
		return v, nil // set times, put back as they were
	}
	return "", fmt.Errorf("android: read the night mode: %q", v)
}

// gpsLocation finds the GPS provider's last location in `dumpsys location`,
// "last location=Location[gps 39.237255,-123.150032 hAcc=5.0 ...]": on an
// emulator the last fix its console sent. "" when it has none.
func gpsLocation(dump string) string {
	m := reGPSLast.FindStringSubmatch(dump)
	if m == nil {
		return ""
	}
	lat, err1 := strconv.ParseFloat(m[1], 64)
	lon, err2 := strconv.ParseFloat(m[2], 64)
	if err1 != nil || err2 != nil {
		return ""
	}
	return driver.FormatLocation(lat, lon)
}

var reGPSLast = regexp.MustCompile(`last location=Location\[gps (-?[0-9.]+),(-?[0-9.]+)[ \]]`)

// consolePort returns the emulator console's port: an emulator that adb
// lists as emulator-5554 has its console on 5554. Phones have no console.
func (d *Device) consolePort() (int, error) {
	if port, ok := emulatorPort(d.info.ID); ok {
		return port, nil
	}
	return 0, fmt.Errorf("%w: only Android emulators can simulate a location; a phone needs a mock location app chosen in Developer options, which Pinky does not set up", driver.ErrUnsupported)
}

func emulatorPort(serial string) (int, bool) {
	s, ok := strings.CutPrefix(serial, "emulator-")
	if !ok {
		return 0, false
	}
	port, err := strconv.Atoi(s)
	return port, err == nil && port > 0 && port < 65536
}

// emuCommand sends one command to an emulator's console on this computer,
// as `adb emu` does: the console asks for the token in
// ~/.emulator_console_auth_token first, unless it was started without
// one.
func emuCommand(ctx context.Context, port int, cmd string) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("%w: the emulator's console: %v", driver.ErrOffline, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	r := bufio.NewReader(conn)
	banner, err := consoleReply(r)
	if err != nil {
		return fmt.Errorf("android: the emulator's console: %w", err)
	}
	if strings.Contains(banner, "Authentication required") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("android: the emulator's console asks for its token: %w", err)
		}
		token, err := os.ReadFile(filepath.Join(home, ".emulator_console_auth_token"))
		if err != nil {
			return fmt.Errorf("android: the emulator's console asks for its token: %w", err)
		}
		if _, err := fmt.Fprintf(conn, "auth %s\r\n", strings.TrimSpace(string(token))); err != nil {
			return fmt.Errorf("android: the emulator's console: %w", err)
		}
		if _, err := consoleReply(r); err != nil {
			return fmt.Errorf("android: the emulator's console refused its token: %w", err)
		}
	}
	if _, err := fmt.Fprintf(conn, "%s\r\n", cmd); err != nil {
		return fmt.Errorf("android: the emulator's console: %w", err)
	}
	if _, err := consoleReply(r); err != nil {
		return fmt.Errorf("android: emulator console %q: %w", cmd, err)
	}
	fmt.Fprintf(conn, "quit\r\n")
	return nil
}

// consoleReply reads the console's lines up to "OK", or a "KO: why" line,
// which it returns as an error.
func consoleReply(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return b.String(), err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "OK":
			return b.String(), nil
		case strings.HasPrefix(line, "KO"):
			return b.String(), errors.New(strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "KO"), ":")))
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

// LaunchLocale implements driver.LocaleLauncher with Android 13's per-app
// languages: the app's language is set (`cmd locale set-app-locales`), as
// the phone's Settings set it under the app's language, and then the app
// is opened. The language stays until the restore puts back what the app
// had, usually the phone's own language.
func (d *Device) LaunchLocale(ctx context.Context, app string, loc driver.Locale) (func(context.Context) error, error) {
	t, err := d.resolveApp(ctx, app)
	if err != nil {
		return nil, err
	}
	restore, err := d.setAppLocale(ctx, t, loc)
	if err != nil {
		return nil, err
	}
	l, err := d.inputSession(ctx)
	if err == nil {
		err = d.start(ctx, l, t, 0)
	}
	if err != nil {
		restore(context.WithoutCancel(ctx))
		return nil, err
	}
	return restore, nil
}

// LaunchLocale implements driver.LocaleLauncher on the background display.
func (b *Background) LaunchLocale(ctx context.Context, app string, loc driver.Locale) (func(context.Context) error, error) {
	t, err := b.d.resolveApp(ctx, app)
	if err != nil {
		return nil, err
	}
	if err := b.ready(ctx); err != nil {
		return nil, err
	}
	restore, err := b.d.setAppLocale(ctx, t, loc)
	if err != nil {
		return nil, err
	}
	if err := b.startApp(ctx, t); err != nil {
		restore(context.WithoutCancel(ctx))
		return nil, err
	}
	return restore, nil
}

// setAppLocale gives an app its own language and returns how to put back
// the one it had.
func (d *Device) setAppLocale(ctx context.Context, t launchTarget, loc driver.Locale) (func(context.Context) error, error) {
	if d.props.SDK > 0 && d.props.SDK < 33 {
		return nil, fmt.Errorf("%w: apps have languages of their own from Android 13; this phone has Android %s", driver.ErrUnsupported, d.props.OSVersion)
	}
	user := 0
	if t.clone != nil {
		user = t.clone.user
	}
	tag, err := d.localeTag(ctx, loc)
	if err != nil {
		return nil, err
	}
	was, err := d.appLocales(ctx, t.pkg, user)
	if err != nil {
		return nil, err
	}
	if err := d.setAppLocales(ctx, t.pkg, user, tag); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		now, err := d.appLocales(ctx, t.pkg, user)
		if err != nil {
			return err
		}
		if now != tag {
			return driver.ErrSwitchChanged
		}
		return d.setAppLocales(ctx, t.pkg, user, was)
	}, nil
}

// localeTag returns the tag to give an app: the locale's, with the phone's
// language for a region alone.
func (d *Device) localeTag(ctx context.Context, loc driver.Locale) (string, error) {
	if loc.Language != "" {
		return loc.Tag(), nil
	}
	out, err := d.sh(ctx, "getprop persist.sys.locale; getprop ro.product.locale")
	if err != nil {
		return "", err
	}
	lang := "en"
	for _, f := range strings.Fields(out) {
		lang = f
		break
	}
	// Drop the phone's own region: "en-US" with region DE is "en-DE".
	parts := strings.Split(lang, "-")
	keep := parts[:1]
	if len(parts) > 1 && len(parts[1]) == 4 {
		keep = parts[:2] // a script, "zh-Hans"
	}
	return driver.Locale{Language: strings.Join(keep, "-"), Region: loc.Region}.Tag(), nil
}

// appLocales reads an app's own languages, "ja-JP" or "" for the phone's:
// "Locales for com.android.settings for user 0 are [ja-JP]".
func (d *Device) appLocales(ctx context.Context, pkg string, user int) (string, error) {
	out, err := d.sh(ctx, adb.Command("cmd", "locale", "get-app-locales", pkg, "--user", strconv.Itoa(user)))
	if err != nil {
		return "", err
	}
	return parseAppLocales(pkg, out)
}

func parseAppLocales(pkg, out string) (string, error) {
	i, j := strings.LastIndex(out, "["), strings.LastIndex(out, "]")
	if !strings.Contains(out, "Locales for") || i < 0 || j < i {
		return "", fmt.Errorf("android: read %s's language: %s", pkg, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out[i+1 : j]), nil
}

func (d *Device) setAppLocales(ctx context.Context, pkg string, user int, tags string) error {
	out, err := d.sh(ctx, adb.Command("cmd", "locale", "set-app-locales", pkg, "--user", strconv.Itoa(user), "--locales", tags))
	if err != nil {
		return err
	}
	if s := strings.TrimSpace(out); s != "" {
		return fmt.Errorf("android: set %s's language: %s", pkg, s)
	}
	return nil
}

var (
	_ driver.Switcher       = (*Device)(nil)
	_ driver.LocaleLauncher = (*Device)(nil)
	_ driver.LocaleLauncher = (*Background)(nil)
)
