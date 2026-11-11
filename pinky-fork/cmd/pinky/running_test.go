package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/update"
)

// runningDirs returns dirs under a short folder: Unix socket paths must
// stay short.
func runningDirs(t *testing.T) api.Dirs {
	t.Helper()
	tmp := "/tmp"
	if runtime.GOOS == "windows" {
		tmp = os.TempDir()
	}
	base, err := os.MkdirTemp(tmp, "pk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	d := api.Dirs{Config: filepath.Join(base, "c"), Data: filepath.Join(base, "d"), Run: filepath.Join(base, "r")}
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	return d
}

// serveOther serves a gateway of this kind, as another Pinky would, until
// it quits (when quits) or the test ends. shown gets a value for each show.
func serveOther(t *testing.T, d api.Dirs, kind string, quits bool) (shown chan struct{}, done chan struct{}) {
	t.Helper()
	dev := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	g, err := gateway.New(gateway.Options{Providers: []driver.Provider{demo.NewProvider(driver.Android, dev)}, NoScreenshots: true})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := api.Listen(d)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	shown = make(chan struct{}, 4)
	srv := &api.Server{Gateway: g, MCP: mcp.New(g, "test"), Version: "older", Kind: kind,
		Show: func() { shown <- struct{}{} }}
	if quits {
		srv.Quit = cancel
	}
	done = make(chan struct{})
	go func() { srv.Serve(ctx, ln); ln.Close(); g.Close(); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return shown, done
}

// listenOnly stands in for startGateway: it only takes the socket.
func listenOnly(d api.Dirs) func(gatewayConfig) (*runningGateway, error) {
	return func(gatewayConfig) (*runningGateway, error) {
		ln, err := api.Listen(d)
		if err != nil {
			return nil, err
		}
		return &runningGateway{ln: ln}, nil
	}
}

func TestTakeOver(t *testing.T) {
	c := gatewayConfig{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	t.Run("daemon", func(t *testing.T) {
		d := runningDirs(t)
		_, done := serveOther(t, d, api.KindDaemon, true)
		rg, err := takeOver(c, d, false, listenOnly(d))
		if err != nil {
			t.Fatal(err)
		}
		defer rg.ln.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the daemon still runs")
		}
		// Agents' `pinky mcp` wait for the app as for an update.
		if !update.Restarting(d.Config) {
			t.Error("no restart marker for the agents' shims")
		}
	})

	t.Run("older gateway", func(t *testing.T) {
		d := runningDirs(t)
		serveOther(t, d, "", true) // says nothing of its kind
		rg, err := takeOver(c, d, false, listenOnly(d))
		if err != nil {
			t.Fatal(err)
		}
		rg.ln.Close()
	})

	t.Run("app", func(t *testing.T) {
		d := runningDirs(t)
		shown, _ := serveOther(t, d, api.KindApp, true)
		if _, err := takeOver(c, d, true, listenOnly(d)); !errors.Is(err, errShownOther) {
			t.Fatalf("in the background: %v", err)
		}
		if len(shown) != 0 {
			t.Error("started in the background, it showed the other window")
		}
		if _, err := takeOver(c, d, false, listenOnly(d)); !errors.Is(err, errShownOther) {
			t.Fatal(err)
		}
		select {
		case <-shown:
		case <-time.After(5 * time.Second):
			t.Fatal("the running app was not shown")
		}
		if update.Restarting(d.Config) {
			t.Error("restart marker for another app")
		}
	})

	t.Run("will not quit", func(t *testing.T) {
		d := runningDirs(t)
		serveOther(t, d, api.KindDaemon, false)
		_, err := takeOver(c, d, false, listenOnly(d))
		var other *otherPinkyError
		if !errors.As(err, &other) {
			t.Fatalf("got %v", err)
		}
		if update.Restarting(d.Config) {
			t.Error("the restart marker stayed")
		}
	})
}

// The shim stops waiting for an app that exited without a gateway, and
// starts the daemon instead (launchGateway).
func TestWaitForGatewayExited(t *testing.T) {
	d := runningDirs(t)
	exited := make(chan struct{})
	close(exited)
	start := time.Now()
	if _, err := waitForGateway(d, 10*time.Second, exited); !errors.Is(err, errExited) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("it waited for the timeout")
	}
	// One that handed over to another gateway as it exited is fine.
	serveOther(t, d, api.KindApp, true)
	conn, err := waitForGateway(d, 10*time.Second, exited)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}
