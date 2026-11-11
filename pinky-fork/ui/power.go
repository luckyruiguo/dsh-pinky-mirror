package ui

import (
	"sync"

	"github.com/egoist/mygo"

	"github.com/DIYgod/pinky/internal/awake"
)

// The reasons Pinky keeps the computer awake, as `pmset -g assertions`
// lists them on a Mac: the same as pinky daemon's (internal/awake).
const (
	awakeReason = awake.ReasonTask
	awayReason  = awake.ReasonAway
)

var (
	runningOnce sync.Once

	// powerKeepAwake is MyGo's Power.KeepAwake; tests replace it.
	powerKeepAwake = mygo.Power.KeepAwake

	awakeMu    sync.Mutex
	tasksAwake = awakeHold{reason: awakeReason}
	phoneAway  = awakeHold{reason: awayReason}
)

// keepRunning keeps macOS from putting Pinky in App Nap for the app's
// lifetime, so the gateway keeps its pace while its window is hidden or
// covered and the screen is locked: agents' calls, video and timers. The
// computer still sleeps when idle; keepAwake stops that while tasks run.
// Other systems do not slow down apps out of sight.
func keepRunning() {
	runningOnce.Do(func() { mygo.Power.KeepRunning("Pinky is connected to phones") })
}

// keepAwake keeps the computer from sleeping when idle while on, as while
// an agent task runs, and lets it sleep again once off. The display may
// sleep, and phones are never kept awake. Call it on the main thread.
func keepAwake(on bool) { tasksAwake.set(on) }

// keepAwakeAway keeps the computer from sleeping when idle while on, as
// keepAwake does, for a phone away from its network: agents reach the
// phone through this computer. A MacBook still sleeps when its lid
// closes, unless it runs in clamshell mode (power and an external
// display).
func keepAwakeAway(on bool) { phoneAway.set(on) }

// awakeHold keeps the computer from idle sleep, under its reason, while
// it is on.
type awakeHold struct {
	reason  string
	release func()
}

func (h *awakeHold) set(on bool) {
	awakeMu.Lock()
	defer awakeMu.Unlock()
	switch {
	case on && h.release == nil:
		h.release = powerKeepAwake(h.reason, false)
	case !on && h.release != nil:
		h.release()
		h.release = nil
	}
}
