//go:build device && !windows

package main

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// TestRemoveEmulatorOverWiFi removes a phone on the real adb server and
// Android driver, wired as the app wires them, with an emulator connected
// by address as a phone on Wi-Fi (emulator_helpers_test.go): it is added as
// Add phone adds one it connects by address, removed, kept off the list
// when its connection comes back and after a restart, and added back by
// connecting it again from Pinky.
//
//	PINKY_E2E_EMULATOR=emulator-5560 scripts/devlock.sh emulator-5560 \
//	  go test -tags device -run TestRemoveEmulatorOverWiFi ./cmd/pinky
func TestRemoveEmulatorOverWiFi(t *testing.T) {
	_, addr, hardware := testEmulator(t)
	adb := startPrivateADB(t, false)
	t.Setenv("ANDROID_ADB_SERVER_PORT", adb.port)
	t.Setenv("PINKY_DEVICES", "")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dir := t.TempDir()
	open := func() *gateway.Gateway {
		t.Helper()
		p, err := newAndroidProvider(log)
		if err != nil {
			t.Fatal(err)
		}
		g, err := gateway.New(gateway.Options{Providers: []driver.Provider{p}, DataDir: dir, Logger: log,
			DiscoverEvery: 500 * time.Millisecond, NoScreenshots: true})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	listed := func(g *gateway.Gateway) (gateway.DeviceView, bool) {
		for _, d := range g.Devices() {
			if d.Info.ID == hardware {
				return d, true
			}
		}
		return gateway.DeviceView{}, false
	}
	online := func(g *gateway.Gateway) bool {
		d, ok := listed(g)
		return ok && d.Status != gateway.DeviceOffline
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	g := open()
	defer func() { g.Close() }()
	info, err := g.ConnectWiFi(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != hardware || info.Connection != "wifi" {
		t.Fatalf("connected %+v, want %s over Wi-Fi", info, hardware)
	}
	waitUntil(t, "the emulator to come online", 60*time.Second, func() bool { return online(g) })

	if err := g.RemoveDevice(ctx, hardware, gateway.RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := listed(g); ok {
		t.Fatal("still listed after removing it")
	}
	if state, ok := adb.serials(t)[addr]; ok {
		t.Fatalf("the connection stayed (%s)", state)
	}

	// Its connection comes back, made by someone else: off the list.
	adb.connect(t, addr)
	time.Sleep(3 * time.Second)
	if _, ok := listed(g); ok {
		t.Fatal("the removed emulator came back by itself")
	}
	// After a restart too, and Pinky leaves that connection alone.
	g.Close()
	g = open()
	time.Sleep(3 * time.Second)
	if _, ok := listed(g); ok {
		t.Fatal("the removed emulator came back after a restart")
	}
	if adb.serials(t)[addr] != "device" {
		t.Fatalf("Pinky dropped a connection at start: %v", adb.serials(t))
	}

	// Connected again from Pinky, as the connect step of Add phone does:
	// it is back.
	if _, err := g.ConnectWiFi(ctx, addr); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the emulator to come back", 60*time.Second, func() bool { return online(g) })
}

// waitUntil waits up to d for cond, which it checks every 100 ms.
func waitUntil(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); !cond(); time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}
