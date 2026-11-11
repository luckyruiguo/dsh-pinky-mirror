package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// stopADBAtQuit stops the adb server of the adb that comes with Pinky as
// Pinky quits, once its gateway has closed, when Settings › Advanced ›
// Stop adb when Pinky quits is on (adb.Client.StopPinkyADB: never another
// adb's server, nor one another program is connected to).
//
// It keeps the server when Pinky quits to start again at once: for an
// update, or when the app takes over from a daemon. Both leave the restart
// marker (update.Restarting), which the caller reads as the quit begins:
// the next Pinky removes it once it runs. The next Pinky uses the server
// that runs, so phones on a cable stay connected through the restart.
func stopADBAtQuit(on, fake, restarting bool, log *slog.Logger) {
	if !on || fake {
		return
	}
	if restarting {
		log.Info("android: kept the adb server for the Pinky that starts next")
		return
	}
	// Quitting stays quick: the checks take tens of milliseconds, the
	// server stops at once.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	start := time.Now()
	err := (&adb.Client{}).StopPinkyADB(ctx)
	switch {
	case err == nil:
		log.Info("android: stopped Pinky's adb server", "took", time.Since(start).Round(time.Millisecond))
	case errors.Is(err, adb.ErrNotOwnServer):
		// None runs, or another adb's: not Pinky's to stop.
	case errors.Is(err, adb.ErrServerInUse):
		log.Info("android: left Pinky's adb server running: another program uses it", "err", err)
	default:
		log.Warn("android: stopping Pinky's adb server", "err", err)
	}
}
