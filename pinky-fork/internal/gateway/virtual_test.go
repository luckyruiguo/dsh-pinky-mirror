package gateway

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// virtProvider is a netProvider whose Pixel is an emulator: an AVD the
// gateway starts and stops (driver.VirtualLauncher). The AVD runs as the
// Pixel only while the provider lists it.
type virtProvider struct {
	*netProvider
	launched []string // "start id", "stop id"
}

const testAVD = "Pixel_9_API_36"

func (p *virtProvider) List(ctx context.Context) ([]driver.Info, error) {
	infos, err := p.netProvider.List(ctx)
	for i := range infos {
		if infos[i].ID == "pixel" {
			infos[i].Virtual = true
		}
	}
	return infos, err
}

func (p *virtProvider) VirtualPhones(context.Context) (driver.VirtualPhones, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := driver.VirtualPhone{ID: testAVD, Name: "Pixel 9 API 36", Model: "Pixel 9", OSVersion: "16"}
	if !p.unplugged["pixel"] {
		v.Running, v.DeviceID = true, "pixel"
	}
	return driver.VirtualPhones{Platform: driver.Android, Phones: []driver.VirtualPhone{v}, Where: "/sdk"}, nil
}

func (p *virtProvider) StartVirtual(_ context.Context, id string) error {
	if id != testAVD {
		return driver.ErrNotFound
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.launched = append(p.launched, "start "+id)
	delete(p.unplugged, "pixel")
	return nil
}

func (p *virtProvider) StopVirtual(_ context.Context, id string) error {
	if id != testAVD {
		return driver.ErrNotFound
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.launched = append(p.launched, "stop "+id)
	p.unplugged["pixel"] = true
	return nil
}

func (p *virtProvider) launches() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.launched)
}

// TestVirtualPhones: Add phone lists the provider's virtual phones and
// starts and stops them. A virtual phone the person removed stays off the
// list while it runs, across restarts, unlike a phone on a cable, until
// they start or use it from Add phone.
func TestVirtualPhones(t *testing.T) {
	dir := t.TempDir()
	np, _, _ := newTwoPhones()
	p := &virtProvider{netProvider: np}
	g := twoPhones(t, dir, p)
	ctx := context.Background()
	waitOpen(t, g, "pixel")

	vps, err := g.VirtualPhones(ctx)
	if err != nil || len(vps) != 1 || vps[0].Platform != driver.Android || len(vps[0].Phones) != 1 ||
		vps[0].Phones[0].DeviceID != "pixel" {
		t.Fatalf("VirtualPhones = %+v, %v", vps, err)
	}

	if err := g.RemoveDevice(ctx, "pixel", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	waitIDs(t, g, "mi")
	time.Sleep(100 * time.Millisecond) // a few discoveries
	waitIDs(t, g, "mi")
	g.Close()
	np2, _, _ := newTwoPhones()
	p = &virtProvider{netProvider: np2}
	g = twoPhones(t, dir, p)
	time.Sleep(100 * time.Millisecond)
	waitIDs(t, g, "mi")

	// Used from Add phone while it runs.
	if err := g.StartVirtual(ctx, driver.Android, testAVD); err != nil {
		t.Fatal(err)
	}
	waitIDs(t, g, "mi", "pixel")
	waitOpen(t, g, "pixel")

	if err := g.StopVirtual(ctx, driver.Android, testAVD); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the Pixel offline", func() bool {
		for _, d := range g.Devices() {
			if d.Info.ID == "pixel" {
				return d.Status == DeviceOffline
			}
		}
		return false
	})
	if err := g.StartVirtual(ctx, driver.Android, testAVD); err != nil {
		t.Fatal(err)
	}
	waitOpen(t, g, "pixel")
	if got := p.launches(); !slices.Equal(got, []string{"start " + testAVD, "stop " + testAVD, "start " + testAVD}) {
		t.Errorf("launches %q", got)
	}

	if err := g.StartVirtual(ctx, driver.Android, "nope"); !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("StartVirtual of an unknown AVD: %v", err)
	}
	if err := g.StartVirtual(ctx, driver.IOS, "x"); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("StartVirtual of an iOS Simulator without its provider: %v", err)
	}
}

// TestVirtualPhonesNone: without a provider that starts virtual phones,
// there are none, and nothing to start.
func TestVirtualPhonesNone(t *testing.T) {
	g, _ := newTestGateway(t)
	vps, err := g.VirtualPhones(context.Background())
	if err != nil || len(vps) != 0 {
		t.Errorf("VirtualPhones = %+v, %v", vps, err)
	}
	if err := g.StopVirtual(context.Background(), driver.Android, testAVD); !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("StopVirtual: %v", err)
	}
}
