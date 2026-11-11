package demo

import (
	"context"
	"fmt"
	"sort"

	"github.com/DIYgod/pinky/internal/apppkg"
	"github.com/DIYgod/pinky/internal/driver"
)

// driver.AppManager, as the real phones have it: an Android phone installs
// APKs and refuses an update signed by another key; an iPhone installs an
// .ipa or a .app folder, but not a Simulator build. An installed app opens
// on a screen of its own.

// added is an app installed from files.
type added struct {
	id, name, version, signerKey string
}

// addedScreen starts the screen name of an app installed from files.
const addedScreen = "added:"

// screenOf is the screen an app opens on.
func screenOf(id string) string {
	if s, ok := appScreens[id]; ok {
		return s
	}
	return addedScreen + id
}

func sortedAdded(m map[string]added) []added {
	out := make([]added, 0, len(m))
	for _, a := range m {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// InspectPackage implements driver.AppManager.
func (d *Device) InspectPackage(ctx context.Context, paths []string) (driver.Package, error) {
	if err := d.check(ctx); err != nil {
		return driver.Package{}, err
	}
	p, err := d.readPackage(paths)
	if err != nil {
		return driver.Package{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if a, ok := d.added[p.ID]; ok {
		p.Installed = a.version
	} else {
		for _, a := range d.appsLocked() {
			if a.ID == p.ID {
				p.Installed = "1.0"
			}
		}
	}
	return p, nil
}

func (d *Device) readPackage(paths []string) (driver.Package, error) {
	if d.Info().Platform == driver.IOS {
		if len(paths) != 1 || !apppkg.IsIOSApp(paths[0]) {
			return driver.Package{}, driver.PackageError("An iPhone installs one .ipa or one .app folder.")
		}
		a, err := apppkg.ReadIOSApp(paths[0])
		if err != nil {
			return driver.Package{}, driver.PackageError("%v", err)
		}
		if a.ForSimulator() {
			return driver.Package{}, driver.PackageError("“%s” is built for the iOS Simulator, which a phone cannot run. Build it for a device.", a.BundleFolder)
		}
		return driver.Package{ID: a.BundleID, Name: a.Name, Version: a.VersionText(), Signer: "Demo Team (DEMO123456)", SignerKey: "DEMO123456"}, nil
	}
	for _, p := range paths {
		if !apppkg.IsAPK(p) {
			return driver.Package{}, driver.PackageError("An Android phone installs .apk files.")
		}
	}
	a, err := apppkg.ReadAPKs(paths)
	if err != nil {
		return driver.Package{}, driver.PackageError("%v", err)
	}
	return driver.Package{ID: a.Package, Name: a.Name(), Version: a.Version(), Signer: a.Signer.Text(), SignerKey: a.Signer.SHA256}, nil
}

// AddApp installs an app by id and name, as if from files: for tests that
// need an app the demo phone does not have.
func (d *Device) AddApp(id, name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.added == nil {
		d.added = map[string]added{}
	}
	d.added[id] = added{id: id, name: name}
	delete(d.removed, id)
}

// InstallPackage implements driver.AppManager.
func (d *Device) InstallPackage(ctx context.Context, paths []string) error {
	if err := d.check(ctx); err != nil {
		return err
	}
	p, err := d.readPackage(paths)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if a, ok := d.added[p.ID]; ok && a.signerKey != p.SignerKey && d.info.Platform == driver.Android {
		return driver.PackageError("The installed “%s” is signed with another key, so the phone refused the update. Uninstalling it first deletes its data.", a.name)
	}
	if d.added == nil {
		d.added = map[string]added{}
	}
	d.added[p.ID] = added{id: p.ID, name: p.Name, version: p.Version, signerKey: p.SignerKey}
	delete(d.removed, p.ID)
	return nil
}

// UninstallApp implements driver.AppManager.
func (d *Device) UninstallApp(ctx context.Context, id string) error {
	if err := d.check(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.hasLocked(id) {
		return fmt.Errorf("demo: %s is not installed: %w", id, driver.ErrNotFound)
	}
	if _, ok := d.added[id]; ok {
		delete(d.added, id)
	} else {
		if d.removed == nil {
			d.removed = map[string]bool{}
		}
		d.removed[id] = true
	}
	d.closeLocked(id)
	return nil
}

// StopApp implements driver.AppManager.
func (d *Device) StopApp(ctx context.Context, id string) error {
	if err := d.check(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	if !d.hasLocked(id) {
		d.mu.Unlock()
		return fmt.Errorf("demo: %s is not installed: %w", id, driver.ErrNotFound)
	}
	d.closeLocked(id)
	d.mu.Unlock()
	d.record(Input{Kind: "stop", Text: id})
	return nil
}

func (d *Device) hasLocked(id string) bool {
	for _, a := range d.appsLocked() {
		if a.ID == id {
			return true
		}
	}
	return false
}

// closeLocked goes home when the app is on the screen.
func (d *Device) closeLocked(id string) {
	scr := screenOf(id)
	for _, s := range d.stack {
		if s == scr || (scr == "dinely" && (s == "orders" || s == "cart" || s == "done" || s == "account")) || (scr == "chats" && s == "chat") {
			d.stack = []string{"home"}
			d.focus = ""
			return
		}
	}
}

var _ driver.AppManager = (*Device)(nil)
