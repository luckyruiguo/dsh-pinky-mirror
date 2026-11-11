// Package awake keeps the computer from sleeping when idle, for a Pinky
// that runs without its window (pinky daemon): an IOKit power assertion on
// macOS, a power request on Windows, a systemd inhibitor lock on Linux.
// The app holds MyGo's Power.KeepAwake instead (ui/power.go), which needs
// the app's main loop, under the same reasons. The display may still
// sleep, and a MacBook still sleeps when its lid closes.
package awake

import (
	"errors"
	"sync"
)

// The reasons Pinky keeps the computer awake, as the system lists them
// (pmset -g assertions on a Mac, powercfg /requests on Windows,
// systemd-inhibit --list on Linux).
const (
	ReasonTask = "Pinky: an agent task is running"
	ReasonAway = "Pinky: a phone is connected through Tailscale"
)

// ErrUnsupported means this system offers nothing Pinky can hold, as a
// Linux without systemd.
var ErrUnsupported = errors.New("keeping the computer awake is not supported here")

// Hold keeps the computer from sleeping when idle, under reason, until
// release is called. It returns once the system holds it, or with the
// system's refusal, which on Linux can take seconds. Calling release more
// than once does nothing. The system drops the hold when the process
// exits, however it exits.
func Hold(reason string) (release func(), err error) {
	r, err := hold(reason)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(r) }, nil
}
