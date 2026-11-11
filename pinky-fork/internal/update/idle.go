package update

import (
	"fmt"
	"strings"
	"time"
)

// CheckEvery is how often Pinky looks for a new version while it runs.
const CheckEvery = 4 * time.Hour

// QuietFor is how long Pinky must be left alone, by the person and by
// agents, before it restarts by itself for an update.
const QuietFor = 10 * time.Minute

// Activity is what keeps Pinky from restarting for an automatic update.
type Activity struct {
	// Tasks are agent tasks running, waiting for the person or paused.
	Tasks int
	// Needs are approvals, unlocks and handoffs waiting for the person.
	Needs int
	// Busy is other work under way, in words: "unlocking Pixel 9",
	// "installing Pinky Companion on Mi 10 Pro", "recording".
	Busy []string
	// LastInput is when the person last used Pinky's windows; LastTask is
	// when an agent task last ran.
	LastInput, LastTask time.Time
}

// Idle reports whether Pinky may restart now for an update, and if not,
// why, in words for logs.
func Idle(now time.Time, a Activity) (bool, string) {
	switch {
	case a.Tasks > 0:
		return false, fmt.Sprintf("%d agent task(s) running", a.Tasks)
	case a.Needs > 0:
		return false, fmt.Sprintf("%d request(s) waiting for you", a.Needs)
	case len(a.Busy) > 0:
		return false, strings.Join(a.Busy, ", ")
	case !a.LastInput.IsZero() && now.Sub(a.LastInput) < QuietFor:
		return false, "you used Pinky in the last " + QuietFor.String()
	case !a.LastTask.IsZero() && now.Sub(a.LastTask) < QuietFor:
		return false, "an agent worked in the last " + QuietFor.String()
	}
	return true, ""
}
