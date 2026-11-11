package ios

import (
	"testing"
	"time"
)

func TestStreamIdle(t *testing.T) {
	start := time.Now()
	m := &media{started: start}
	d := &Device{m: m, done: make(chan struct{})}
	if got, _ := d.takeIdle(start.Add(streamIdle - time.Second)); got != nil {
		t.Fatal("stopped before streamIdle")
	}
	d.watch()
	if got, _ := d.takeIdle(start.Add(2 * streamIdle)); got != nil {
		t.Fatal("stopped with a view open")
	}
	d.unwatch() // the last view closed just now
	closed := d.watchedAt
	if got, _ := d.takeIdle(closed.Add(streamIdle / 2)); got != nil {
		t.Fatal("stopped right after the last view closed")
	}
	d.markInput()
	if got, _ := d.takeIdle(time.Now().Add(streamIdle / 2)); got != nil {
		t.Fatal("stopped right after input")
	}
	d.inputMu.Lock()
	if got, _ := d.takeIdle(time.Now().Add(2 * streamIdle)); got != nil {
		t.Fatal("stopped while input was in progress")
	}
	d.inputMu.Unlock()
	if got, _ := d.takeIdle(time.Now().Add(2 * streamIdle)); got != m || d.m != nil {
		t.Fatalf("idle: took %p, left %p; want the stream taken", got, d.m)
	}
	if got, _ := d.takeIdle(time.Now().Add(3 * streamIdle)); got != nil {
		t.Fatal("took a stream twice")
	}
	d.closed = true
	d.m = &media{started: start}
	if got, _ := d.takeIdle(time.Now().Add(3 * streamIdle)); got != nil {
		t.Fatal("took the stream of a closed device")
	}
}

func TestIdleSince(t *testing.T) {
	now := time.Now()
	if !idleSince(now, time.Minute, now.Add(-2*time.Minute), time.Time{}) {
		t.Error("two minutes is not idle for a minute")
	}
	if idleSince(now, time.Minute, now.Add(-2*time.Minute), now.Add(-time.Second)) {
		t.Error("the latest time did not count")
	}
}
