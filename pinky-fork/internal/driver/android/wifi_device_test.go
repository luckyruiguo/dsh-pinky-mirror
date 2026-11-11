//go:build device

package android

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestDeviceWifiStream measures what the mirror shows over a phone's
// connection, for several stream settings: Settings scrolls on a
// background display (dark on Android 17) while the test records when
// each frame arrives against the phone's own timestamp. It reports the
// frame rate, the bit rate, how long frames queue on the way (arrival
// minus capture time, above the run's best), how long a swipe takes to
// show, and the longest gap between frames while the screen moves.
//
//	PINKY_DEVICE=3e8be48e [PINKY_WIFI_SECONDS=20] [PINKY_WIFI_ROUNDS=2]
//
// It needs the phone's screen off and the phone locked, as
// TestDeviceBackgroundScreenOff does.
func TestDeviceWifiStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	d := openOff(t, ctx)
	seconds := 20
	if s := os.Getenv("PINKY_WIFI_SECONDS"); s != "" {
		fmt.Sscan(s, &seconds)
	}
	rounds := 2
	if s := os.Getenv("PINKY_WIFI_ROUNDS"); s != "" {
		fmt.Sscan(s, &rounds)
	}
	type setting struct {
		name    string
		bitRate int
		fps     int
		hevc    bool
	}
	settings := []setting{
		{"before: 8 Mbit/s, 60 fps, H.264", 8_000_000, 60, false},
		{"after: 2.5 Mbit/s, 30 fps, H.265", 2_500_000, 30, true},
		{"2.5 Mbit/s, 60 fps, H.265", 2_500_000, 60, true},
		{"4 Mbit/s, 30 fps, H.265", 4_000_000, 30, true},
	}
	if d.info.Connection != "wifi" {
		t.Logf("note: %s is connected over %s", d.info.ID, d.info.Connection)
	}
	throughput(t, ctx, d, "before")
	for round := range rounds {
		for _, s := range settings {
			d.cfg.VideoBitRate = s.bitRate
			d.mu.Lock()
			d.noHEVC = !s.hevc
			d.mu.Unlock()
			r, err := measureStream(ctx, d, s.fps, time.Duration(seconds)*time.Second)
			if err != nil {
				t.Errorf("%s: %v", s.name, err)
				continue
			}
			t.Logf("round %d, %s: %s", round+1, s.name, r)
		}
	}
	throughput(t, ctx, d, "after")
}

// throughput logs how fast the phone sends 8 MB through adb.
func throughput(t *testing.T, ctx context.Context, d *Device, when string) {
	adbPath, err := exec.LookPath("adb")
	if err != nil {
		return
	}
	start := time.Now()
	out, err := exec.CommandContext(ctx, adbPath, "-s", d.info.ID, "exec-out", "head -c 8000000 /dev/zero").Output()
	el := time.Since(start)
	if err != nil {
		t.Logf("throughput %s: %v", when, err)
		return
	}
	t.Logf("adb throughput %s: %.1f Mbit/s (%d bytes in %v)", when, float64(len(out))*8/el.Seconds()/1e6, len(out), el.Round(10*time.Millisecond))
}

type streamResult struct {
	frames, bytes   int
	dur             time.Duration
	queue           []time.Duration // per frame, above the best
	response        []time.Duration // swipe sent to the first frame captured after it that moved
	gaps            []time.Duration // the longest gap while each swipe shows
	moving          []time.Duration // every gap between frames while the screen moves
	codec           driver.Codec
	width, height   int
	keyFrames       int
	biggestKeyFrame int
}

func (r streamResult) String() string {
	pct := func(v []time.Duration, p float64) time.Duration {
		if len(v) == 0 {
			return 0
		}
		s := slices.Clone(v)
		slices.Sort(s)
		return s[min(len(s)-1, int(float64(len(s))*p))].Round(time.Millisecond)
	}
	bunched := 0
	for _, g := range r.moving {
		if g < 8*time.Millisecond {
			bunched++
		}
	}
	return fmt.Sprintf("%s %dx%d: %.1f fps, %.2f Mbit/s; queueing p50 %v p90 %v max %v; swipe to picture p50 %v p90 %v; while moving: gap p50 %v, %d%% of frames within 8 ms of the last, longest per swipe p50 %v max %v; %d key frames, biggest %d KB",
		r.codec, r.width, r.height, float64(r.frames)/r.dur.Seconds(), float64(r.bytes)*8/r.dur.Seconds()/1e6,
		pct(r.queue, 0.5), pct(r.queue, 0.9), pct(r.queue, 1),
		pct(r.response, 0.5), pct(r.response, 0.9), pct(r.moving, 0.5), bunched*100/max(1, len(r.moving)),
		pct(r.gaps, 0.5), pct(r.gaps, 1), r.keyFrames, r.biggestKeyFrame/1024)
}

// measureStream opens a background display with Settings and scrolls it
// for dur, swiping every second.
func measureStream(ctx context.Context, d *Device, fps int, dur time.Duration) (streamResult, error) {
	b, err := d.OpenDisplay(ctx, DisplayOptions{App: "com.android.settings", MaxFPS: fps})
	if err != nil {
		return streamResult{}, err
	}
	defer b.Close()
	st, err := b.Stream(ctx, driver.StreamOptions{})
	if err != nil {
		return streamResult{}, err
	}
	defer st.Close()
	type arrival struct {
		at   time.Time
		pts  time.Duration
		size int
		key  bool
	}
	var mu sync.Mutex
	var got []arrival
	done := make(chan struct{})
	go func() {
		defer close(done)
		for p := range st.Packets() {
			if p.Config {
				continue
			}
			mu.Lock()
			got = append(got, arrival{time.Now(), p.PTS, len(p.Data), p.KeyFrame})
			mu.Unlock()
		}
	}()
	time.Sleep(2 * time.Second) // the first key frame and the app's start
	mu.Lock()
	skip := len(got)
	mu.Unlock()
	start := time.Now()
	var swipes []time.Time
	for i := 0; time.Since(start) < dur; i++ {
		from, to := screen.Point{X: 540, Y: 1800}, screen.Point{X: 540, Y: 600}
		if i%2 == 1 {
			from, to = to, from
		}
		swipes = append(swipes, time.Now())
		if err := b.Swipe(ctx, from, to, 300*time.Millisecond); err != nil {
			return streamResult{}, err
		}
		time.Sleep(time.Until(swipes[len(swipes)-1].Add(time.Second)))
	}
	time.Sleep(time.Second) // what is still on the way
	elapsed := time.Since(start)
	st.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	frames := got[skip:]
	r := streamResult{dur: elapsed, codec: st.Codec()}
	r.width, r.height = st.Size()
	if len(frames) == 0 {
		return r, fmt.Errorf("no frames")
	}
	// Arrival minus capture time, from the first frame: the clocks differ,
	// so only the part above the run's best counts.
	t0, p0 := frames[0].at, frames[0].pts
	offset := func(f arrival) time.Duration { return f.at.Sub(t0) - (f.pts - p0) }
	best := time.Duration(1 << 62)
	for _, f := range frames {
		best = min(best, offset(f))
	}
	sizes := make([]int, 0, len(frames))
	for _, f := range frames {
		r.frames++
		r.bytes += f.size
		r.queue = append(r.queue, offset(f)-best)
		sizes = append(sizes, f.size)
		if f.key {
			r.keyFrames++
			r.biggestKeyFrame = max(r.biggestKeyFrame, f.size)
		}
	}
	// A swipe shows in the first frame much bigger than a still screen's
	// that was captured after the swipe was sent: its capture time on the
	// computer's clock is at most its arrival less its queueing. The gaps
	// count over the next 400 ms of capture, while the screen moves.
	slices.Sort(sizes)
	big := max(sizes[len(sizes)/10]*4, 2000)
	captured := func(f arrival) time.Time { return t0.Add(f.pts - p0 + best) }
	for _, s := range swipes {
		var first arrival
		found := false
		var last time.Time
		var gap time.Duration
		for _, f := range frames {
			c := captured(f)
			if c.Before(s) {
				continue
			}
			if !found && f.size >= big {
				first, found = f, true
			}
			if found && c.Before(captured(first).Add(400*time.Millisecond)) {
				if !last.IsZero() {
					g := f.at.Sub(last)
					gap = max(gap, g)
					r.moving = append(r.moving, g)
				}
				last = f.at
			}
		}
		if found && captured(first).Before(s.Add(time.Second)) {
			r.response = append(r.response, first.at.Sub(s))
			r.gaps = append(r.gaps, gap)
		}
	}
	return r, nil
}
