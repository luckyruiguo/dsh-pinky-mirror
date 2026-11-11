package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
)

// helperProvider is the demo provider that installs the iPhone helper.
type helperProvider struct {
	driver.Provider

	mu      sync.Mutex
	opens   int
	install func(id, team string, progress func(driver.HelperStep)) error
}

func (p *helperProvider) Open(ctx context.Context, id string) (driver.Device, error) {
	p.mu.Lock()
	p.opens++
	p.mu.Unlock()
	return p.Provider.Open(ctx, id)
}

func (p *helperProvider) CanInstallHelper() bool { return true }

func (p *helperProvider) InstallHelper(_ context.Context, id, team string, progress func(driver.HelperStep)) error {
	return p.install(id, team, progress)
}

func (p *helperProvider) openCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opens
}

func online(g *Gateway, id string) bool {
	for _, d := range g.Devices() {
		if d.Info.ID == id {
			return d.Info.State == driver.StateOnline
		}
	}
	return false
}

// TestInstallHelper installs the helper from the client: the gateway lets
// go of the phone once the install begins and opens it again afterwards;
// it refuses while an agent holds the phone.
func TestInstallHelper(t *testing.T) {
	hp := &helperProvider{Provider: demo.NewProvider(driver.IOS, demo.NewDevice("iphone", "iPhone 13 mini", driver.IOS))}
	g, _ := newTestGateway(t, func(o *Options) { o.Providers = []driver.Provider{hp} })
	defer g.Close()
	if !g.CanInstallHelper() || !online(g, "iphone") {
		t.Fatal("not ready")
	}
	var steps []driver.HelperStep
	hp.install = func(id, team string, progress func(driver.HelperStep)) error {
		if id != "iphone" || team != "TEAMAAAAA1" {
			t.Errorf("install(%q, %q)", id, team)
		}
		progress(driver.HelperBuilding)
		if online(g, "iphone") {
			t.Error("still online while installing")
		}
		progress(driver.HelperInstalling)
		return nil
	}
	opens := hp.openCount()
	if err := g.InstallHelper(context.Background(), "iphone", "TEAMAAAAA1", func(s driver.HelperStep) { steps = append(steps, s) }); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[1] != driver.HelperInstalling {
		t.Errorf("steps %q", steps)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !online(g, "iphone") || hp.openCount() <= opens {
		if time.Now().After(deadline) {
			t.Fatal("not opened again after the install")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A failure comes back as it is.
	failed := &driver.HelperInstallError{Fix: driver.HelperFixUnlock, Err: errors.New("locked")}
	hp.install = func(string, string, func(driver.HelperStep)) error { return failed }
	if err := g.InstallHelper(context.Background(), "iphone", "", nil); !errors.Is(err, failed) {
		t.Errorf("failure: %v", err)
	}
	// A phone being added, not listed yet.
	hp.install = func(id, _ string, _ func(driver.HelperStep)) error {
		if id != "" {
			t.Errorf("unlisted phone: id %q", id)
		}
		return nil
	}
	if err := g.InstallHelper(context.Background(), "", "", nil); err != nil {
		t.Errorf("unlisted phone: %v", err)
	}
	if err := g.InstallHelper(context.Background(), "nobody", "", nil); !errors.Is(err, ErrUnknownDevice) {
		t.Errorf("unknown phone: %v", err)
	}
}

func TestInstallHelperUnsupported(t *testing.T) {
	g, _ := newTestGateway(t)
	defer g.Close()
	if g.CanInstallHelper() {
		t.Error("the demo provider installs the helper")
	}
	if err := g.InstallHelper(context.Background(), "pixel", "", nil); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("a provider without the helper: %v", err)
	}
	if err := g.InstallHelper(context.Background(), "", "", nil); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("no provider: %v", err)
	}
}
