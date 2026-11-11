package main

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/ui"
)

// Two rows of Settings › Safety share the name "Trust" (Trust Bank's
// iPhone app, Trust Wallet's Android app): removing one unblocks that app
// alone. Rows keyed by name used to look unchanged, and removing both
// excluded the name.
func TestUnblockOneOfTwoSameNamedRows(t *testing.T) {
	dir := t.TempDir()
	pol, err := policy.Load(filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := gateway.New(gateway.Options{Policy: pol})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	rg := &runningGateway{Policy: pol, G: g}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bank := screen.App{ID: "sg.trust", Name: "Trust"}
	wallet := screen.App{ID: "com.sixdays.trust", Name: "Trust"}

	old := ui.DefaultSettings()
	old.BlockedApps = []ui.BlockedApp{
		{Name: "Trust", ID: wallet.ID, Kind: "Finance"},
		{Name: "Trust", ID: bank.ID, Kind: "Finance"},
	}
	next := old
	next.BlockedApps = old.BlockedApps[1:]
	applySettings(rg, old, next, log)
	if rule, ok := pol.Blocked(wallet); ok {
		t.Fatalf("the wallet is still blocked by %q", rule)
	}
	if _, ok := pol.Blocked(bank); !ok {
		t.Fatal("removing the wallet's row unblocked the bank")
	}
	if got := pol.BlockedApps([]screen.App{bank, wallet}); len(got) != 1 || got[0].ID != bank.ID {
		t.Fatalf("listed %+v", got)
	}
}

// Settings reads the blocked apps again when a phone comes online: the
// list at start is read before Wi-Fi iPhones are, and missed their apps.
func TestSettingsVersionFollowsThePhones(t *testing.T) {
	online := func(ids ...string) []gateway.DeviceView {
		var out []gateway.DeviceView
		for _, id := range ids {
			out = append(out, gateway.DeviceView{Info: driver.Info{ID: id, State: driver.StateOnline}})
		}
		return out
	}
	a := settingsVersion(3, online("emu"))
	if b := settingsVersion(3, online("emu")); b != a {
		t.Fatal("the same phones gave another version")
	}
	withPhone := settingsVersion(3, online("emu", "iphone"))
	if withPhone == a || withPhone < 0 {
		t.Fatalf("a phone online kept the version: %d %d", a, withPhone)
	}
	if settingsVersion(3, online("iphone", "emu")) != withPhone {
		t.Fatal("the order of the phones changed the version")
	}
	offline := append(online("emu"), gateway.DeviceView{Info: driver.Info{ID: "iphone", State: driver.StateOffline}})
	if settingsVersion(3, offline) != a {
		t.Fatal("a phone not online changed the version")
	}
	if settingsVersion(4, online("emu")) == a {
		t.Fatal("a policy change kept the version")
	}
}
