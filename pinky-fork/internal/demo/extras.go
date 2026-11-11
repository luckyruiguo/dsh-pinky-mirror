package demo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// The optional driver features of round three, as the real platforms have
// them: every demo phone wakes and takes a PIN; Android ones also have a
// Pinky folder and background displays (Open returns them wrapped).

// PIN is the PIN of every demo phone, for unlocking from the computer.
const PIN = "1234"

// ScreenOn implements driver.Waker.
func (d *Device) ScreenOn(context.Context) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.offline {
		return false, driver.ErrOffline
	}
	return !d.screenOff, nil
}

// Wake implements driver.Waker.
func (d *Device) Wake(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.offline {
		return driver.ErrOffline
	}
	d.screenOff = false
	return nil
}

// SetScreen turns the screen off or on. Turning it off does not lock the
// phone; SetLocked does.
func (d *Device) SetScreen(on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.screenOff = !on
}

// Unlock implements driver.Unlocker: PIN unlocks the phone, any other PIN
// leaves it locked.
func (d *Device) Unlock(ctx context.Context, pin string) error {
	if err := d.check(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.locked {
		return nil
	}
	if pin != PIN {
		return fmt.Errorf("demo: wrong PIN: %w", driver.ErrLocked)
	}
	d.locked, d.screenOff = false, false
	return nil
}

// Android wraps an Android demo phone with the features only Android
// phones have.
type Android struct{ *Device }

// Of returns the demo phone of a device that Provider.Open returned.
func Of(dev driver.Device) *Device {
	switch d := dev.(type) {
	case *Device:
		return d
	case Android:
		return d.Device
	}
	return nil
}

type file struct {
	data  []byte
	mtime time.Time
}

// PutFile puts a file into the phone's Pinky folder, as if the person
// saved it there.
func (d *Device) PutFile(p string, data []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.files == nil {
		d.files = map[string]file{}
	}
	d.files[p] = file{data: append([]byte(nil), data...), mtime: time.Now().Truncate(time.Second)}
}

// File returns a file of the Pinky folder.
func (d *Device) File(p string) ([]byte, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f, ok := d.files[p]
	return f.data, ok
}

func cleanPath(p string) (string, bool) {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "" {
			return "", false
		}
	}
	return path.Clean(p), true
}

// ListFiles implements driver.Files.
func (a Android) ListFiles(ctx context.Context) ([]driver.FileInfo, error) {
	if err := a.check(ctx); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []driver.FileInfo
	dirs := map[string]bool{}
	for p, f := range a.files {
		out = append(out, driver.FileInfo{Path: p, Size: int64(len(f.data)), ModTime: f.mtime})
		for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	for dir := range dirs {
		out = append(out, driver.FileInfo{Path: dir, Dir: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Pull implements driver.Files.
func (a Android) Pull(ctx context.Context, p string, w io.Writer) error {
	if err := a.check(ctx); err != nil {
		return err
	}
	clean, ok := cleanPath(p)
	a.mu.Lock()
	f, found := a.files[clean]
	a.mu.Unlock()
	if !ok || !found {
		return fmt.Errorf("demo: %s: %w", p, driver.ErrNotFound)
	}
	_, err := w.Write(f.data)
	return err
}

// Push implements driver.Files.
func (a Android) Push(ctx context.Context, p string, r io.Reader, size int64, mtime time.Time) error {
	if err := a.check(ctx); err != nil {
		return err
	}
	clean, ok := cleanPath(p)
	if !ok {
		return fmt.Errorf("demo: %s: %w", p, driver.ErrNotFound)
	}
	var b bytes.Buffer
	if _, err := io.Copy(&b, io.LimitReader(r, size)); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.files == nil {
		a.files = map[string]file{}
	}
	a.files[clean] = file{data: b.Bytes(), mtime: mtime}
	return nil
}

// OpenBackground implements driver.Backgrounder: a demo phone of its own
// with the same apps, never locked, that closes with Close.
func (a Android) OpenBackground(ctx context.Context, opt driver.BackgroundOptions) (driver.Device, error) {
	if err := a.check(ctx); err != nil {
		return nil, err
	}
	info := a.Info()
	bg := NewDevice(info.ID+"-background", info.Name+" (background)", driver.Android)
	a.mu.Lock()
	bg.noVideo = a.noVideo
	a.mu.Unlock()
	if opt.App != "" {
		if err := bg.Launch(ctx, opt.App); err != nil {
			return nil, err
		}
	}
	return bg, nil
}

var (
	_ driver.Waker        = (*Device)(nil)
	_ driver.Unlocker     = (*Device)(nil)
	_ driver.Files        = Android{}
	_ driver.Backgrounder = Android{}
)

// Checkup implements driver.Checker: a demo phone has all it needs, as a
// Pixel with the companion app would.
func (a Android) Checkup(ctx context.Context) (driver.Checkup, error) {
	if err := a.check(ctx); err != nil {
		return driver.Checkup{}, err
	}
	pass := func(id, detail string) driver.Check {
		return driver.Check{ID: id, Status: driver.CheckPass, Detail: detail}
	}
	return driver.Checkup{Family: "pixel", Checks: []driver.Check{
		pass(driver.CheckADB, "demo phones need no adb"),
		pass(driver.CheckCompanion, "the demo phone reads its own screen"),
		pass(driver.CheckInput, "the demo phone takes taps"),
		pass(driver.CheckScreen, "the demo phone draws its screen"),
		pass(driver.CheckBackground, "the demo phone opens background displays"),
		pass(driver.CheckWiFi, "the demo phone is connected over USB"),
	}}, nil
}

var _ driver.Checker = Android{}
