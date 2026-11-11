package gateway

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
)

// changedProvider is an airProvider whose phones on Wi-Fi may lack the
// mark it left on them, as Android's provider lists one when something
// else answers for the phone over the network (driver.SetupPhoneChanged),
// until the person trusts it.
type changedProvider struct {
	*airProvider

	cmu     sync.Mutex
	changed map[string]bool
	trusted []string
}

func (p *changedProvider) List(ctx context.Context) ([]driver.Info, error) {
	infos, err := p.airProvider.List(ctx)
	p.cmu.Lock()
	defer p.cmu.Unlock()
	for i := range infos {
		if p.changed[infos[i].ID] && infos[i].Connection == "wifi" {
			infos[i].State, infos[i].Setup = driver.StateNeedsSetup, driver.SetupPhoneChanged
			infos[i].Detail = "This is not the phone Pinky set up under this name."
		}
	}
	return infos, err
}

func (p *changedProvider) Open(ctx context.Context, id string) (driver.Device, error) {
	p.cmu.Lock()
	changed := p.changed[id]
	p.cmu.Unlock()
	if changed {
		return nil, fmt.Errorf("not the phone Pinky set up: %w", driver.ErrOffline)
	}
	return p.airProvider.Open(ctx, id)
}

func (p *changedProvider) TrustPhone(_ context.Context, id string) error {
	p.cmu.Lock()
	defer p.cmu.Unlock()
	delete(p.changed, id)
	p.trusted = append(p.trusted, id)
	return nil
}

// TestWiFiPhoneChanged: a phone whose connection over the network leads to
// a device without its mark waits for the person, who trusts it on its
// page; agents are told why they cannot use it.
func TestWiFiPhoneChanged(t *testing.T) {
	p := &changedProvider{airProvider: newAirProvider(demo.NewDevice("pixel", "Pixel 9", driver.Android)),
		changed: map[string]bool{"pixel": true}}
	p.set("pixel", "wifi")
	p.UseOverWiFi("pixel") // the person's phone
	g, _ := newTestGateway(t, func(o *Options) { o.Providers = []driver.Provider{p} })
	g.SetWiFi(true)
	waitFor(t, "the changed phone", func() bool { v, ok := viewOf(g, "pixel"); return ok && v.StatusLine == "Not the same phone" })
	if v, _ := viewOf(g, "pixel"); v.Status != DeviceNeedsSetup || v.Info.Setup != driver.SetupPhoneChanged {
		t.Fatalf("changed phone = %+v", v)
	}

	s := g.OpenSession(agent)
	t.Cleanup(s.Close)
	res := s.Connect(context.Background(), ConnectRequest{Task: "look", Device: "pixel"})
	if res.Status != "device_needs_setup" || res.Setup != driver.SetupPhoneChanged || !strings.Contains(res.Message, "trusts it on its page") {
		t.Fatalf("an agent's connect to the changed phone: %+v", res)
	}
	if _, _, opens := p.state("pixel"); len(opens) != 0 {
		t.Fatalf("opened: %v", opens)
	}

	ctx := context.Background()
	if err := g.TrustPhone(ctx, "nobody"); !errors.Is(err, ErrUnknownDevice) {
		t.Fatalf("TrustPhone of an unknown phone: %v", err)
	}
	if err := g.TrustPhone(ctx, "pixel"); err != nil {
		t.Fatal(err)
	}
	// Online, and the agent that waited has it.
	waitFor(t, "the trusted phone online", func() bool { v, _ := viewOf(g, "pixel"); return v.Status == DeviceWorking })
	if _, _, opens := p.state("pixel"); !slices.Equal(opens, []string{"wifi"}) {
		t.Fatalf("opens %v", opens)
	}
	p.cmu.Lock()
	trusted := slices.Clone(p.trusted)
	p.cmu.Unlock()
	if !slices.Equal(trusted, []string{"pixel"}) {
		t.Fatalf("trusted %v", trusted)
	}
}

// TestTrustPhoneUnsupported: a provider that does not check its phones has
// nothing to trust.
func TestTrustPhoneUnsupported(t *testing.T) {
	p := newAirProvider(demo.NewDevice("ipad", "iPad", driver.IOS))
	g, _ := newTestGateway(t, func(o *Options) { o.Providers = []driver.Provider{p} })
	waitFor(t, "the iPad", func() bool { _, ok := viewOf(g, "ipad"); return ok })
	if err := g.TrustPhone(context.Background(), "ipad"); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("TrustPhone = %v, want ErrUnsupported", err)
	}
}
