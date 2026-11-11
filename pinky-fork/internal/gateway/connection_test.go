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

// choosingProvider is the demo provider with a choice of connections.
type choosingProvider struct {
	driver.Provider

	mu    sync.Mutex
	ways  map[string]string
	opens int
}

func (p *choosingProvider) Open(ctx context.Context, id string) (driver.Device, error) {
	p.mu.Lock()
	p.opens++
	p.mu.Unlock()
	return p.Provider.Open(ctx, id)
}

func (p *choosingProvider) SetConnection(id, way string) error {
	if way != "" && way != "coredevice" && way != "wda" {
		return driver.ErrUnsupported
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ways == nil {
		p.ways = map[string]string{}
	}
	p.ways[id] = way
	return nil
}

func (p *choosingProvider) Connections(_ context.Context, id string) driver.Connections {
	p.mu.Lock()
	defer p.mu.Unlock()
	return driver.Connections{Chosen: p.ways[id], Ways: []driver.Connection{{ID: "", Available: true}, {ID: "coredevice", Available: true}, {ID: "wda"}}}
}

func (p *choosingProvider) state() (string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ways["pixel"], p.opens
}

func TestSetConnection(t *testing.T) {
	dir := t.TempDir()
	newChooser := func() *choosingProvider {
		return &choosingProvider{Provider: demo.NewProvider(driver.Android, demo.NewDevice("pixel", "Pixel 9", driver.Android))}
	}
	cp := newChooser()
	g, _ := newTestGateway(t, func(o *Options) { o.Providers = []driver.Provider{cp}; o.DataDir = dir })
	if _, opens := cp.state(); opens != 1 {
		t.Fatalf("opened %d times at start", opens)
	}
	if err := g.SetConnection("pixel", "wda"); err != nil {
		t.Fatal(err)
	}
	// The phone reconnects the chosen way at once.
	deadline := time.Now().Add(5 * time.Second)
	for {
		way, opens := cp.state()
		if way == "wda" && opens >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("way %q, %d opens: not reconnected", way, opens)
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, err := g.Connections(context.Background(), "pixel")
	if err != nil || got.Chosen != "wda" || len(got.Ways) != 3 {
		t.Fatalf("connections %+v, %v", got, err)
	}
	if err := g.SetConnection("pixel", "bogus"); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("a way the driver lacks: %v", err)
	}
	if err := g.SetConnection("nobody", "wda"); !errors.Is(err, ErrUnknownDevice) {
		t.Fatalf("unknown phone: %v", err)
	}
	g.Close()

	// The choice is saved and given to the provider before discovery.
	cp2 := newChooser()
	g2, _ := newTestGateway(t, func(o *Options) { o.Providers = []driver.Provider{cp2}; o.DataDir = dir })
	if way, _ := cp2.state(); way != "wda" {
		t.Fatalf("after a restart the provider has %q", way)
	}
	g2.Close()
}

func TestSetConnectionUnsupported(t *testing.T) {
	g, _ := newTestGateway(t)
	if err := g.SetConnection("pixel", "wda"); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("a provider without a choice: %v", err)
	}
	if _, err := g.Connections(context.Background(), "pixel"); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("connections of a provider without a choice: %v", err)
	}
}
