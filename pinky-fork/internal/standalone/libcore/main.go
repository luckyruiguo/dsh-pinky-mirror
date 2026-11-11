// Command libcore is the standalone prototype's Go core as a shared library
// (libpinkycore.so), loaded by its accessibility service (Core.kt) through
// JNI. scripts/standalone.sh builds it with the Android NDK:
//
//	CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC=<ndk clang> \
//		go build -buildmode=c-shared -o libpinkycore.so ./internal/standalone/libcore
//
// Elsewhere it builds as an empty program, so `go build ./...` passes.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/local"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/standalone"
)

func main() {}

var (
	mu      sync.Mutex
	running *standalone.Core
)

// start starts the core once per process: the local driver on the
// service's companion socket, the gateway and agent, and the control
// socket for Pinky's screens.
func start(dataDir, companionSocket, coreSocket, version string, logw io.Writer) error {
	mu.Lock()
	defer mu.Unlock()
	if running != nil {
		return nil
	}
	log := slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	app := local.Standalone
	app.Socket = companionSocket
	ln, err := net.Listen("unix", "@"+coreSocket)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	c, err := standalone.New(standalone.Options{
		Providers: []driver.Provider{local.NewProvider(app)},
		Device:    local.ID,
		DataDir:   dataDir,
		Version:   version,
		// On the phone it drives, ask before commits, messages and
		// settings changes too, not only payments.
		Preset: policy.PresetCommits,
		Gateway: gateway.Options{
			// The prototype has no Activity to show the steps'
			// pictures in, and each costs a capture on the phone.
			NoScreenshots: true,
		},
		Log: log,
	})
	if err != nil {
		ln.Close()
		return err
	}
	running = c
	go func() {
		if err := c.Serve(context.Background(), ln); err != nil {
			log.Error("control socket stopped", "err", err)
		}
	}()
	log.Info("core ready", "data", dataDir, "companion", companionSocket, "control", coreSocket)
	return nil
}
