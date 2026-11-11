package android

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Settle detection reads only the timing and sizes of encoded video
// packets; it decodes nothing.
//
// scrcpy's encoder gets a new frame only when the screen changes. After
// the last change it re-encodes the same frame about ten times, 100 ms
// apart, to sharpen it (Android's "repeat previous frame" encoder option);
// those refinement packets can be large, so size alone does not tell a
// still screen from a moving one. Their timing does: animations, scrolling
// and transitions arrive at the display's refresh rate, while refinements
// arrive 100 ms after the previous frame. So a packet counts as a change
// unless it is a refinement (85 to 125 ms after the previous capture
// time), a key frame or codec configuration (encoder restarts and the
// periodic key frame), or tiny (a blinking cursor). The screen has settled
// once no change has arrived for the quiet period.

const (
	repeatMin = 85 * time.Millisecond
	repeatMax = 125 * time.Millisecond
	// noVideoSettle is the settle wait of a session without video and
	// without the companion's events.
	noVideoSettle = time.Second
)

// settleTracker remembers when the video last changed.
type settleTracker struct {
	mu       sync.Mutex
	fixed    int // configured size threshold in bytes; 0 derives one
	small    int // packets below this size never count as a change
	last     time.Time
	prevPTS  time.Duration
	havePrev bool
}

// defaultSettleBytes derives the size below which a packet is too small to
// count as a change, from the frame size: a blinking cursor or a ticking
// clock stays below it.
func defaultSettleBytes(w, h int) int {
	return min(max(w*h/8000, 200), 1500)
}

func (t *settleTracker) resize(now time.Time, w, h int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fixed > 0 {
		t.small = t.fixed
	} else {
		t.small = defaultSettleBytes(w, h)
	}
	// A new encoding session (rotation, resize or reset) starts with a key
	// frame and refinements; the changes that caused it were counted
	// before it.
	t.havePrev = false
}

// packet classifies one packet and reports whether it counted as a change.
func (t *settleTracker) packet(now time.Time, pts time.Duration, size int, config, key bool) bool {
	if config {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	gap := pts - t.prevPTS
	repeat := t.havePrev && gap >= repeatMin && gap <= repeatMax
	t.prevPTS, t.havePrev = pts, true
	if key || repeat || size < t.small {
		return false
	}
	t.last = now
	return true
}

func (t *settleTracker) lastChange() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}

func (t *settleTracker) bytes() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.small
}

// quietCond is one signal settle waits on: the time of its last change,
// and how long it must stay quiet.
type quietCond struct {
	last  func() time.Time
	quiet time.Duration
}

// waitQuiet returns true once every condition's last change (or start, if
// later) is at least its quiet period in the past, or false when max has
// passed since start.
func waitQuiet(ctx context.Context, start time.Time, max time.Duration, done <-chan struct{}, conds ...quietCond) (bool, error) {
	deadline := start.Add(max)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		now := time.Now()
		settled := true
		for _, c := range conds {
			last := c.last()
			if last.Before(start) {
				last = start
			}
			if now.Sub(last) < c.quiet {
				settled = false
				break
			}
		}
		if settled {
			return true, nil
		}
		if !now.Before(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-done:
			return false, errSessionEnded
		case <-tick.C:
		}
	}
}

// waitSettled waits until the session's video has been still for the
// quiet period, and every extra condition holds, for at most max (or the
// configured default).
func (l *live) waitSettled(ctx context.Context, max time.Duration, extra ...quietCond) (bool, error) {
	if max <= 0 {
		max = l.cfg.SettleMax
	}
	start := time.Now()
	if _, _, err := l.videoSize(ctx); err != nil {
		return false, err
	}
	video := quietCond{last: l.settle.lastChange, quiet: l.cfg.SettleQuiet}
	if !l.video {
		// No frames to watch: a fixed wait, shorter when the companion's
		// events tell when the screen stops changing.
		video = quietCond{last: func() time.Time { return time.Time{} }, quiet: noVideoSettle}
		if len(extra) > 0 {
			video.quiet = l.cfg.SettleQuiet
		}
	}
	conds := append([]quietCond{video}, extra...)
	ok, err := waitQuiet(ctx, start, max, l.done, conds...)
	if err == errSessionEnded {
		return false, l.failure()
	}
	return ok, err
}

// WaitSettled implements driver.Settler. It waits until the main display's
// video has not changed for the quiet period (400 ms by default) and, while
// the companion app is on, no window or content event has arrived for
// 300 ms, or until max passes (3 s when max is 0).
//
// Both must hold, as the design asks. The video sees animations,
// transitions and ripples that send no accessibility events; the events
// see tree changes that hardly move a pixel (a label's text, a node that
// appears under an overlay). A screen whose tree is still but whose
// pixels keep moving (a spinner, a live wallpaper) still runs to max:
// "still loading" is what an agent should hear for a spinner.
func (d *Device) WaitSettled(ctx context.Context, max time.Duration) (bool, error) {
	d.used()
	l, err := d.session(ctx)
	if err != nil {
		return false, err
	}
	var extra []quietCond
	if c, ok := d.settleCond(0); ok {
		extra = append(extra, c)
	}
	return l.waitSettled(ctx, max, extra...)
}

// streamStats counts video packets, for androidctl and measurements.
type streamStats struct {
	mu      sync.Mutex
	recent  []PacketInfo // ring buffer
	next    int
	packets int
	bytes   int64
	keys    int
	first   time.Time
}

// PacketInfo is the size and arrival time of one video packet.
type PacketInfo struct {
	At       time.Time
	PTS      time.Duration // capture time on the phone; 0 for configuration
	Size     int
	Config   bool
	KeyFrame bool
	Change   bool // settle detection counted it as a change
}

const statsKeep = 4096

func (s *streamStats) add(now time.Time, pts time.Duration, size int, config, key, change bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.first.IsZero() {
		s.first = now
	}
	s.packets++
	s.bytes += int64(size)
	if key {
		s.keys++
	}
	info := PacketInfo{At: now, PTS: pts, Size: size, Config: config, KeyFrame: key, Change: change}
	if len(s.recent) < statsKeep {
		s.recent = append(s.recent, info)
	} else {
		s.recent[s.next] = info
		s.next = (s.next + 1) % statsKeep
	}
}

// since returns the packets that arrived at or after t, oldest first.
func (s *streamStats) since(t time.Time) []PacketInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PacketInfo
	n := len(s.recent)
	for i := 0; i < n; i++ {
		p := s.recent[(s.next+i)%n]
		if !p.At.Before(t) {
			out = append(out, p)
		}
	}
	return out
}

// VideoStats describes the main session's video, for diagnostics.
type VideoStats struct {
	Width, Height int
	Codec         string
	SettleBytes   int          // packets at or above this size count as change
	Packets       []PacketInfo // packets since the time asked for
}

// VideoStats returns the main session's video packets since t, starting
// the session if needed.
func (d *Device) VideoStats(ctx context.Context, since time.Time) (VideoStats, error) {
	l, err := d.session(ctx)
	if err != nil {
		return VideoStats{}, err
	}
	w, h, err := l.videoSize(ctx)
	if err != nil {
		return VideoStats{}, err
	}
	return VideoStats{Width: w, Height: h, Codec: string(l.codec), SettleBytes: l.settle.bytes(), Packets: l.stats.since(since)}, nil
}

// errSessionEnded tells waitSettled that the session ended while waiting.
var errSessionEnded = errors.New("android: session ended")
