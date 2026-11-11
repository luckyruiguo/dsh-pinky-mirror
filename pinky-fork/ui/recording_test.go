package ui

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/recording"
	"github.com/DIYgod/pinky/internal/recording/recordingtest"
	"github.com/DIYgod/pinky/internal/video"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestRecordingPlaysAndSeeks plays a fake recording through the real
// decoder: the picture moves on, a step's mark jumps to that step's
// picture (the fake paints each step its own color), and Pause holds it.
func TestRecordingPlaysAndSeeks(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs the ffmpeg command")
	}
	if err := video.Load(); err != nil {
		t.Skip(err)
	}
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.page = pageActivity
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "Activity", func() bool { tt.Frame(); return ta.act.loaded })
	ta.act.sel = "h-lunch"
	ta.waitFor(t, "the recording", func() bool { tt.Frame(); return hasText(tt, "Watch recording") })
	if err := tt.Click("Watch recording"); err != nil {
		t.Fatal(err)
	}
	pl := ta.rec.pl
	t.Cleanup(ta.closeRecording)
	ta.waitFor(t, "the first pictures", func() bool {
		tt.Frame()
		return pl.bitmap != nil && pl.pos > 300*time.Millisecond
	})
	if pl.state != "playing" || pl.picW != 540 || pl.picH != 1200 {
		t.Fatalf("state %s, picture %dx%d, err %v", pl.state, pl.picW, pl.picH, pl.err)
	}

	var mark gateway.RecordingMark
	idx := -1
	for i, m := range pl.rec.Marks {
		if strings.HasPrefix(m.Text, "Placed the order") {
			mark, idx = m, i
		}
	}
	if err := tt.Click("Jump to " + clock(mark.At) + ": Placed the order"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the step's picture", func() bool {
		tt.Frame()
		return pl.state == "playing" && pl.pos >= mark.At && pl.pos < mark.At+3*time.Second && pl.markAt(pl.pos) == idx
	})
	// The fake paints step idx in its color; look a little below the
	// status bar, away from the digits.
	r, ok := tt.Find("Recording of Reorder last week's lunch")
	if !ok {
		t.Fatal("no player")
	}
	want := [][3]int{{38, 70, 140}, {32, 112, 96}, {130, 60, 120}, {150, 92, 30}, {60, 60, 72}, {44, 104, 150}}[idx%6]
	ta.waitFor(t, "the step's color", func() bool {
		tt.Frame()
		img := tt.Image()
		i := img.PixOffset(int(r.X+r.W/2), int(r.Y+r.H*0.15))
		got := [3]int{int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])}
		for k := range got {
			if d := got[k] - want[k]; d > 24 || d < -24 {
				return false
			}
		}
		return true
	})

	if err := tt.Click("Pause"); err != nil {
		t.Fatal(err)
	}
	held := pl.pos
	time.Sleep(300 * time.Millisecond)
	tt.Frame()
	if pl.state != "paused" || pl.pos != held {
		t.Errorf("after Pause: %s at %v, was %v", pl.state, pl.pos, held)
	}
	before := len(ta.api.RecordingOpens())
	if err := tt.Click("Play"); err != nil {
		t.Fatal(err)
	}
	// The player opens the recording on a goroutine of its own.
	ta.waitFor(t, "the recording to open again", func() bool { return len(ta.api.RecordingOpens()) > before })
	opens := ta.api.RecordingOpens()
	if got := opens[len(opens)-1]; got != held {
		t.Errorf("Play went on from %v, not %v", got, held)
	}
}

// TestRecordingTimelineSeekWhilePaused clicks the timeline while paused and
// expects playback to open there, not at the start, and to stay paused.
func TestRecordingTimelineSeekWhilePaused(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs the ffmpeg command")
	}
	if err := video.Load(); err != nil {
		t.Skip(err)
	}
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.page = pageActivity
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "Activity", func() bool { tt.Frame(); return ta.act.loaded })
	ta.act.sel = "h-lunch"
	ta.waitFor(t, "the recording", func() bool { tt.Frame(); return hasText(tt, "Watch recording") })
	if err := tt.Click("Watch recording"); err != nil {
		t.Fatal(err)
	}
	pl := ta.rec.pl
	t.Cleanup(ta.closeRecording)
	ta.waitFor(t, "the first pictures", func() bool {
		tt.Frame()
		return pl.bitmap != nil && pl.pos > 300*time.Millisecond
	})
	if err := tt.Click("Pause"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if pl.state != "paused" {
		t.Fatalf("before the seek: %s", pl.state)
	}
	r, ok := tt.Find(locales.RecordingPosition())
	if !ok {
		t.Fatal("no timeline")
	}
	want := pl.rec.Length * 3 / 4
	before := len(ta.api.RecordingOpens())
	tt.ClickAt(r.X+r.W*0.75, r.Y+r.H/2)
	ta.waitFor(t, "the seek", func() bool {
		tt.Frame()
		return len(ta.api.RecordingOpens()) > before
	})
	opens := ta.api.RecordingOpens()
	got := opens[len(opens)-1]
	if got < want-time.Second || got > want+2*time.Second {
		t.Errorf("opened at %v, want about %v", got, want)
	}
	ta.waitFor(t, "the paused picture", func() bool {
		tt.Frame()
		return pl.settled && pl.player == nil
	})
	if pl.state != "paused" || pl.pos < got || pl.pos > got+time.Second {
		t.Errorf("after the seek to %v: %s at %v", got, pl.state, pl.pos)
	}
}

// TestRecordingSeekAgainWhilePaused seeks twice in a row while paused and
// expects the player to stay paused, not to show as playing in between.
func TestRecordingSeekAgainWhilePaused(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs the ffmpeg command")
	}
	if err := video.Load(); err != nil {
		t.Skip(err)
	}
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.page = pageActivity
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "Activity", func() bool { tt.Frame(); return ta.act.loaded })
	ta.act.sel = "h-lunch"
	ta.waitFor(t, "the recording", func() bool { tt.Frame(); return hasText(tt, "Watch recording") })
	if err := tt.Click("Watch recording"); err != nil {
		t.Fatal(err)
	}
	pl := ta.rec.pl
	t.Cleanup(ta.closeRecording)
	ta.waitFor(t, "the first pictures", func() bool {
		tt.Frame()
		return pl.bitmap != nil && pl.pos > 300*time.Millisecond
	})
	if err := tt.Click("Pause"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	r, ok := tt.Find(locales.RecordingPosition())
	if !ok {
		t.Fatal("no timeline")
	}
	for _, frac := range []float32{0.4, 0.7} {
		tt.ClickAt(r.X+r.W*frac, r.Y+r.H/2)
		tt.Frame()
		if pl.running() {
			t.Fatalf("after seek at %.0f%%, state %s", frac*100, pl.state)
		}
	}
	ta.waitFor(t, "both seeks", func() bool {
		tt.Frame()
		return pl.state == "paused" && pl.pos >= pl.rec.Length/2
	})
}

// TestRecordingTimelineScrubOffBar presses the timeline, drags off it, and
// releases elsewhere; scrubbing must end and not stick for the next hover.
func TestRecordingTimelineScrubOffBar(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.page = pageActivity
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "Activity", func() bool { tt.Frame(); return ta.act.loaded })
	ta.act.sel = "h-lunch"
	ta.waitFor(t, "the recording", func() bool { tt.Frame(); return hasText(tt, "Watch recording") })
	if err := tt.Click("Watch recording"); err != nil {
		t.Fatal(err)
	}
	pl := ta.rec.pl
	t.Cleanup(ta.closeRecording)
	tt.Frame()
	r, ok := tt.Find(locales.RecordingPosition())
	if !ok {
		t.Fatal("no timeline")
	}
	mid := r.X + r.W*0.5
	tt.Press(mid, r.Y+r.H/2)
	ta.waitFor(t, "scrub started", func() bool {
		tt.Frame()
		return pl.scrubbing
	})
	off := r.X + r.W + 40
	tt.Release(off, r.Y-40)
	ta.waitFor(t, "scrub ended", func() bool {
		tt.Frame()
		return !pl.scrubbing
	})
	tt.Frame()
	if pl.scrubbing {
		t.Fatal("scrubbing stuck after release off the bar")
	}
	if opens := ta.api.RecordingOpens(); opens[len(opens)-1] != pl.rec.Length {
		t.Errorf("released past the end: opened at %v, want the end %v", opens[len(opens)-1], pl.rec.Length)
	}
}

// fileRecAPI hides the fake's OpenRecording, so the player reads its
// recordings with recording.Open.
type fileRecAPI struct{ gateway.API }

// writeStillRecording records the test stream's first second at 10 frames
// a second, nothing for five seconds, as a still screen sends, and its next
// two seconds: key frames at 0 and 6 s, 8 s long.
func writeStillRecording(t *testing.T) string {
	t.Helper()
	return writeTestRecording(t, func(frame int) time.Duration {
		at := time.Duration(frame) * 100 * time.Millisecond
		if frame >= 10 {
			at += 5 * time.Second
		}
		return at
	}, 8*time.Second)
}

// writeTestRecording records testsrc.h264 (30 frames, 10 a second) at the
// times at(frame) and closes at end.
func writeTestRecording(t *testing.T, at func(frame int) time.Duration, end time.Duration) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.mp4")
	w, err := recording.Create(path, driver.H264)
	if err != nil {
		t.Fatal(err)
	}
	frame := 0
	for _, p := range recordingtest.Packets(driver.H264, recordingtest.H264()) {
		if err := w.Write(p, at(frame)); err != nil {
			t.Fatal(err)
		}
		if !p.Config {
			frame++
		}
	}
	if err := w.Close(end); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRecordingSeekFromKeyFrame plays a real recording, whose stream opens
// at the key frame before where it seeks and sends the pictures from there
// at once: neither the picture nor the time goes back before the seek, and
// a paused seek stops at once on the picture at the time asked for, though
// the next picture is seconds away.
func TestRecordingSeekFromKeyFrame(t *testing.T) {
	if err := video.Load(); err != nil {
		t.Skip(err)
	}
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.app.api = fileRecAPI{ta.api}
	ta.openRecording("t", gateway.RecordingView{Path: writeStillRecording(t), Length: 8 * time.Second})
	pl := ta.rec.pl
	t.Cleanup(ta.closeRecording)
	tt := mg.NewTester(func(c *mg.Context) {
		ta.sync()
		ta.recordingView(c, gateway.TaskView{ID: "t"}, pl)
	}, 1280, 820)
	ta.waitFor(t, "the first pictures", func() bool {
		tt.Frame()
		return pl.state == "playing" && pl.pos >= 500*time.Millisecond
	})

	// Playing, to 6.5 s: the stream opens at 6 s.
	pl.seek(6500 * time.Millisecond)
	for start := time.Now(); pl.pos < 7*time.Second; {
		tt.Frame()
		if pl.pos < 6500*time.Millisecond {
			t.Fatalf("at %v after a seek to 6.5 s", pl.pos)
		}
		if time.Since(start) > 10*time.Second {
			t.Fatalf("stuck at %v, %s", pl.pos, pl.state)
		}
	}

	// Paused, to 3 s: the stream opens at 0 s, and its next picture after
	// 3 s is at 6 s.
	pl.pause()
	pl.seek(3 * time.Second)
	start := time.Now()
	ta.waitFor(t, "the paused picture", func() bool {
		tt.Frame()
		if pl.pos != 3*time.Second {
			t.Fatalf("at %v during a paused seek to 3 s", pl.pos)
		}
		return pl.settled && pl.player == nil
	})
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the paused seek took %v", took)
	}
	if pl.state != "paused" || pl.pos != 3*time.Second || pl.bitmap == nil {
		t.Errorf("after a paused seek to 3 s: %s at %v", pl.state, pl.pos)
	}

	// Paused, past the last picture, at 7.9 s: the stream ends without one
	// past where it seeks, and the player stays paused there.
	pl.seek(7950 * time.Millisecond)
	ta.waitFor(t, "the last picture", func() bool {
		tt.Frame()
		return pl.settled && pl.player == nil
	})
	if pl.state != "paused" || pl.pos != 7950*time.Millisecond {
		t.Errorf("after a paused seek to 7.95 s: %s at %v", pl.state, pl.pos)
	}
	if pl.pic != 7900*time.Millisecond {
		t.Errorf("picture at %v after a paused seek to 7.95 s, want 7.9 s", pl.pic)
	}
}

// TestRecordingPausedSeekPicture checks which picture a paused seek shows,
// not only the clock: at 10 fps a seek to 1.899 s must show the 1.8 s
// frame, and a seek past the end the last frame, even when ended runs
// before a paint takes it.
func TestRecordingPausedSeekPicture(t *testing.T) {
	if err := video.Load(); err != nil {
		t.Skip(err)
	}
	const length = 3 * time.Second
	path := writeTestRecording(t, func(frame int) time.Duration {
		return time.Duration(frame) * 100 * time.Millisecond
	}, length)
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.app.api = fileRecAPI{ta.api}
	ta.openRecording("t", gateway.RecordingView{Path: path, Length: length})
	pl := ta.rec.pl
	t.Cleanup(ta.closeRecording)
	tt := mg.NewTester(func(c *mg.Context) {
		ta.sync()
		ta.recordingView(c, gateway.TaskView{ID: "t"}, pl)
	}, 1280, 820)
	ta.waitFor(t, "the first pictures", func() bool {
		tt.Frame()
		return pl.state == "playing" && pl.pic >= 300*time.Millisecond
	})
	pl.pause()

	pl.seek(1899 * time.Millisecond)
	ta.waitFor(t, "the 1.8 s picture", func() bool {
		tt.Frame()
		return pl.settled && pl.player == nil
	})
	if pl.state != "paused" || pl.pos != 1899*time.Millisecond {
		t.Errorf("after a paused seek to 1.899 s: %s at %v", pl.state, pl.pos)
	}
	if pl.pic != 1800*time.Millisecond {
		t.Errorf("picture at %v after a paused seek to 1.899 s, want 1.8 s", pl.pic)
	}

	pl.seek(length)
	ta.waitFor(t, "the last picture", func() bool {
		tt.Frame()
		return pl.settled && pl.player == nil
	})
	if pl.state != "paused" || pl.pos != length {
		t.Errorf("after a paused seek to the end: %s at %v", pl.state, pl.pos)
	}
	if pl.pic != 2900*time.Millisecond {
		t.Errorf("picture at %v after a paused seek to the end, want 2.9 s", pl.pic)
	}
}
