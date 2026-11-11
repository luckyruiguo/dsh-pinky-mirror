package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

// pairingProvider lists the phones it paired.
type pairingProvider struct {
	listed []driver.Info
}

func (p *pairingProvider) Platform() driver.Platform { return driver.Android }

func (p *pairingProvider) List(context.Context) ([]driver.Info, error) {
	return append([]driver.Info(nil), p.listed...), nil
}

func (p *pairingProvider) Open(context.Context, string) (driver.Device, error) {
	return nil, driver.ErrUnsupported
}

func (p *pairingProvider) pair(id string) driver.Info {
	info := driver.Info{ID: id, Platform: driver.Android, State: driver.StateOnline, Connection: "wifi"}
	p.listed = append(p.listed, info)
	return info
}

func (p *pairingProvider) StartPairing(context.Context) (driver.Pairing, error) {
	return &fakePairing{p: p}, nil
}

func (p *pairingProvider) PairWithCode(_ context.Context, addr, _ string) (driver.Info, error) {
	return p.pair("code-" + addr), nil
}

func (p *pairingProvider) ConnectWiFi(_ context.Context, addr string) (driver.Info, error) {
	return p.pair("connect-" + addr), nil
}

type fakePairing struct{ p *pairingProvider }

func (f *fakePairing) QR() string                                { return "WIFI:T:ADB;S:pinky-test;P:secret;;" }
func (f *fakePairing) Wait(context.Context) (driver.Info, error) { return f.p.pair("qr-phone"), nil }
func (f *fakePairing) Close() error                              { return nil }
func (f *fakePairing) Progress() driver.PairingProgress {
	return driver.PairingProgress{Found: true}
}

// TestDeviceFilterKeepsPairing checks that PINKY_DEVICES keeps Wi-Fi
// pairing available and lets the phones paired through it in.
func TestDeviceFilterKeepsPairing(t *testing.T) {
	under := &pairingProvider{listed: []driver.Info{{ID: "usb-phone"}, {ID: "other-phone"}}}
	t.Setenv("PINKY_DEVICES", "usb-phone")
	saved := providerFactories
	t.Cleanup(func() { providerFactories = saved })
	providerFactories = providerFactories[:0:0]
	providerFactories = append(providerFactories, func(*slog.Logger) (driver.Provider, error) { return under, nil })

	ps := realProviders(slog.New(slog.DiscardHandler))
	if len(ps) != 1 {
		t.Fatalf("%d providers", len(ps))
	}
	pr, ok := ps[0].(driver.Pairer)
	if !ok {
		t.Fatalf("%T hides driver.Pairer", ps[0])
	}
	ctx := context.Background()
	ids := func() []string {
		infos, _ := ps[0].List(ctx)
		var out []string
		for _, i := range infos {
			out = append(out, i.ID)
		}
		return out
	}
	if got := ids(); len(got) != 1 || got[0] != "usb-phone" {
		t.Fatalf("listed %v", got)
	}
	pg, err := pr.StartPairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := pg.(driver.PairingReporter); !ok || !r.Progress().Found {
		t.Errorf("%T hides the pairing's progress", pg)
	}
	if _, err := pg.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pr.PairWithCode(ctx, "10.0.0.2:37000", "123456"); err != nil {
		t.Fatal(err)
	}
	wc, ok := ps[0].(driver.WiFiConnector)
	if !ok {
		t.Fatalf("%T hides driver.WiFiConnector", ps[0])
	}
	if _, err := wc.ConnectWiFi(ctx, "10.0.0.2:41000"); err != nil {
		t.Fatal(err)
	}
	got := ids()
	want := map[string]bool{"usb-phone": true, "qr-phone": true, "code-10.0.0.2:37000": true, "connect-10.0.0.2:41000": true}
	if len(got) != len(want) {
		t.Fatalf("listed %v", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("listed %q", id)
		}
	}
}

// limitedProvider leaves alone, by itself, the phones Pinky may not use.
type limitedProvider struct {
	pairingProvider
	allowed func(id string) bool
}

func (p *limitedProvider) Limit(allowed func(id string) bool) { p.allowed = allowed }

// TestDeviceFilterReachesProvider checks that PINKY_DEVICES reaches a
// provider that leaves the other phones alone itself, with the phones the
// person adds through it, and only through it.
func TestDeviceFilterReachesProvider(t *testing.T) {
	a, b := &limitedProvider{}, &limitedProvider{}
	t.Setenv("PINKY_DEVICES", "usb-phone")
	saved := providerFactories
	t.Cleanup(func() { providerFactories = saved })
	providerFactories = []func(*slog.Logger) (driver.Provider, error){
		func(*slog.Logger) (driver.Provider, error) { return a, nil },
		func(*slog.Logger) (driver.Provider, error) { return b, nil },
	}
	ps := realProviders(slog.New(slog.DiscardHandler))
	if len(ps) != 2 || a.allowed == nil || b.allowed == nil {
		t.Fatalf("%d providers, limited: %v %v", len(ps), a.allowed != nil, b.allowed != nil)
	}
	if !a.allowed("usb-phone") || a.allowed("other-phone") {
		t.Error("the limit is not PINKY_DEVICES")
	}
	wc, ok := ps[0].(driver.WiFiConnector)
	if !ok {
		t.Fatalf("%T hides driver.WiFiConnector", ps[0])
	}
	if _, err := wc.ConnectWiFi(context.Background(), "10.0.0.2:41000"); err != nil {
		t.Fatal(err)
	}
	if !a.allowed("connect-10.0.0.2:41000") || b.allowed("connect-10.0.0.2:41000") {
		t.Error("the phone connected through one provider is not let in there alone")
	}
}
