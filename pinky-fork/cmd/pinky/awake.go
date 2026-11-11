package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/DIYgod/pinky/internal/awake"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// The daemon keeps the computer from sleeping when idle as the app does
// (ui/power.go), under the same reasons: while an agent task runs, and
// while a phone is connected through Tailscale with the app's Keep this
// computer awake for phones away on. The app holds MyGo's Power, which
// needs its main loop; the daemon has none and holds internal/awake.

const (
	// awakeLook is how often the daemon looks again without an event, as
	// for a phone that moved to its Tailscale connection.
	awakeLook = 30 * time.Second
	// awakeRetry is how long the daemon waits to try a hold that failed
	// again, as when logind refuses its lock.
	awakeRetry = 5 * time.Minute
)

// awakeSource is what the daemon reads from its gateway.
type awakeSource interface {
	Subscribe(ctx context.Context) <-chan gateway.Event
	Tasks() []gateway.TaskView
	Devices() []gateway.DeviceView
}

// keepDaemonAwake holds the computer awake while src needs it, until ctx
// ends; away says whether a phone away counts (Settings.KeepAwakeAway).
func keepDaemonAwake(ctx context.Context, src awakeSource, away bool, hold func(string) (func(), error), log *slog.Logger) {
	h := &awakeHolds{hold: hold, log: log, held: map[string]func(){}, retry: map[string]time.Time{}}
	defer h.releaseAll()
	look := func() {
		task, phone := awakeNeeds(src.Tasks(), src.Devices())
		h.set(awake.ReasonTask, task)
		h.set(awake.ReasonAway, away && phone)
	}
	events := src.Subscribe(ctx)
	tick := time.NewTicker(awakeLook)
	defer tick.Stop()
	look()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				return
			}
			// One look for a burst of events, as a task's steps.
			for drained := false; !drained; {
				select {
				case _, ok := <-events:
					if !ok {
						return
					}
				default:
					drained = true
				}
			}
			look()
		case <-tick.C:
			look()
		}
	}
}

// awakeNeeds reports whether an agent task runs (working, waiting or
// paused) and whether a phone is connected through Tailscale, as the app's
// anyRunning and anyAway do.
func awakeNeeds(tasks []gateway.TaskView, devs []gateway.DeviceView) (task, away bool) {
	for _, t := range tasks {
		switch t.State {
		case gateway.TaskWorking, gateway.TaskWaiting, gateway.TaskPaused:
			task = true
		}
	}
	for _, d := range devs {
		if d.Info.Via == driver.ViaTailscale && d.Status != gateway.DeviceOffline && d.Info.State == driver.StateOnline {
			away = true
		}
	}
	return task, away
}

// awakeHolds is what the daemon holds, by reason.
type awakeHolds struct {
	hold  func(string) (func(), error)
	log   *slog.Logger
	held  map[string]func()
	retry map[string]time.Time // when to try a hold that failed again
	now   func() time.Time     // tests replace it; nil is time.Now
}

func (h *awakeHolds) set(reason string, on bool) {
	release, held := h.held[reason]
	switch {
	case on && !held:
		now := time.Now
		if h.now != nil {
			now = h.now
		}
		at, failed := h.retry[reason]
		if failed && now().Before(at) {
			return
		}
		r, err := h.hold(reason)
		if err != nil {
			if !failed {
				h.log.Warn("cannot keep the computer awake", "reason", reason, "err", err)
			}
			if errors.Is(err, awake.ErrUnsupported) {
				h.retry[reason] = now().Add(100 * 365 * 24 * time.Hour) // never
			} else {
				h.retry[reason] = now().Add(awakeRetry)
			}
			return
		}
		h.held[reason] = r
		delete(h.retry, reason)
		h.log.Info("keeping the computer awake", "reason", reason)
	case !on && held:
		release()
		delete(h.held, reason)
		h.log.Info("no longer keeping the computer awake", "reason", reason)
	}
}

func (h *awakeHolds) releaseAll() {
	for reason := range h.held {
		h.set(reason, false)
	}
}
