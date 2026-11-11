package gateway

import (
	"sync"
	"time"
)

// What the gateway keeps of the screen depends on the order of events. A
// recording takes a picture that arrived after its task started and before
// the task ended, and a read vouches for the pictures that arrived before
// it started (video.go); a history screenshot is kept by a read that
// started after the screenshot was taken (shots.go). The gateway orders
// these events by their times, and two equal times cannot say which came
// first. time.Now gives equal times: on Windows it moves only with the
// system's clock tick (under a millisecond to 16 ms), so a picture of the
// person's own use that arrives in the tick a task ended in would pass for
// the task's. So the times of these events come from stamp, which never
// gives the same time twice.

// clock is stamp's state.
var clock = struct {
	mu   sync.Mutex
	last time.Time
	now  func() time.Time // time.Now; tests make it as coarse as Windows'
}{now: time.Now}

// stamp returns the time now, after every time it returned before: a stamp
// taken after another one returned is later than it. While the clock does
// not move, it counts on in nanoseconds.
func stamp() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	t := clock.now()
	if !t.After(clock.last) {
		t = clock.last.Add(time.Nanosecond)
	}
	clock.last = t
	return t
}
