package ui

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/video"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// logAPI is a gateway whose phone has the stream a test gives it, or none
// at all: the phone went away.
type logAPI struct {
	*fakeapi.API
	err    error
	stream driver.Stream
}

func (l *logAPI) Stream(context.Context, string, driver.StreamOptions) (driver.Stream, error) {
	if l.stream != nil {
		return l.stream, nil
	}
	return nil, l.err
}

// logPosts keeps what the client posts to the main thread, as a test
// without a window does.
type logPosts struct {
	mu sync.Mutex
	q  []func()
}

func (l *logPosts) post(fn func()) {
	l.mu.Lock()
	l.q = append(l.q, fn)
	l.mu.Unlock()
}

// until runs what was posted, as the main thread does, until cond holds.
func (l *logPosts) until(t *testing.T, cond func() bool) {
	t.Helper()
	for range 500 {
		l.mu.Lock()
		var fn func()
		if len(l.q) > 0 {
			fn, l.q = l.q[0], l.q[1:]
		}
		l.mu.Unlock()
		if fn != nil {
			fn()
		}
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the mirror")
}

// logMirror builds a mirror of a phone, with a log that writes into buf,
// as the app's own log does.
func logMirror(t *testing.T, buf *bytes.Buffer, api *logAPI) (*mirror, *logPosts) {
	t.Helper()
	a := newApp(api, Options{NoTray: true, Shortcut: "-", Log: slog.New(slog.NewTextHandler(buf, nil))})
	posts := &logPosts{}
	a.postHook = posts.post
	t.Cleanup(a.end)
	return &mirror{a: a, id: "pixel-9"}, posts
}

// TestMirrorLogsItsLife: what the mirror does reaches the log the
// person's feedback carries: the stream opening, the stream failing, the
// stream ending after it played, and the mirror closing. The lines say
// which phone, and carry no picture of it.
func TestMirrorLogsItsLife(t *testing.T) {
	var buf bytes.Buffer
	api := &logAPI{API: fakeapi.New(fakeapi.Options{Scenario: fakeapi.NoPhones}), err: errors.New("the phone went away")}
	m, posts := logMirror(t, &buf, api)

	m.open()
	posts.until(t, func() bool { return m.state == "failed" })
	m.ended(errors.New("EOF")) // the stream ended after it played
	m.close()

	got := buf.String()
	t.Logf("the log:\n%s", got)
	for _, want := range []string{
		`msg="pinky: mirror pixel-9: opening the stream"`,
		`msg="pinky: mirror pixel-9: stream opened" err="the phone went away"`,
		`msg="pinky: mirror pixel-9: stream failed" err="the phone went away" retry_in=3s`,
		`msg="pinky: mirror pixel-9: stream failed" err=EOF retry_in=2s`,
		`msg="pinky: mirror pixel-9: closed"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the log lacks %s:\n%s", want, got)
		}
	}
	// The mirror still shows the failure and tries again, as before.
	if m.state != "failed" || m.err == nil || m.err.Error() != "EOF" {
		t.Errorf("the mirror's state %q, error %v", m.state, m.err)
	}
	if d := time.Until(m.retry); d < time.Second || d > 3*time.Second {
		t.Errorf("the mirror tries again in %v, want about two seconds", d)
	}
}

// TestMirrorLogsAgentDisplay: an agent's display says so in the log, so
// that a background stream is told from the person's own.
func TestMirrorLogsAgentDisplay(t *testing.T) {
	var buf bytes.Buffer
	api := &logAPI{API: fakeapi.New(fakeapi.Options{Scenario: fakeapi.NoPhones}), err: errors.New("the phone went away")}
	m, posts := logMirror(t, &buf, api)
	m.agent = true

	m.open()
	posts.until(t, func() bool { return m.state == "failed" })

	got := buf.String()
	t.Logf("the log:\n%s", got)
	if want := `msg="pinky: mirror pixel-9: opening the stream" agent=true`; !strings.Contains(got, want) {
		t.Errorf("the log lacks %s:\n%s", want, got)
	}
}

// TestMirrorLogsTheStreamEnding: a stream that plays and then ends is
// written to the log with its error, as the picture that goes black and
// comes back. It needs FFmpeg, both the decoder and the command that
// makes a test stream.
func TestMirrorLogsTheStreamEnding(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs the ffmpeg command")
	}
	if err := video.Load(); err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := video.StartTestStream(ctx, video.TestStreamOptions{
		Codec: driver.H264, Width: 320, Height: 640, FPS: 30, Source: "testsrc2"})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	api := &logAPI{API: fakeapi.New(fakeapi.Options{Scenario: fakeapi.NoPhones}), stream: st}
	m, posts := logMirror(t, &buf, api)

	m.open()
	posts.until(t, func() bool { return m.player != nil })
	// The stream ends, as a phone that goes away makes it.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	posts.until(t, func() bool { return m.state == "failed" })

	got := buf.String()
	t.Logf("the log:\n%s", got)
	for _, want := range []string{
		`msg="pinky: mirror pixel-9: opening the stream"`,
		`msg="pinky: mirror pixel-9: stream opened"`,
		`msg="pinky: mirror pixel-9: stream failed" err=EOF retry_in=2s`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the log lacks %s:\n%s", want, got)
		}
	}
}
