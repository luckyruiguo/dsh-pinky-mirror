package main

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

func TestPaintDropsSteadyStream(t *testing.T) {
	t0 := time.Unix(0, 0)
	// 60 pictures a second, evenly spaced: a 60 Hz painter keeps every
	// one at every phase but one where a tick falls exactly on them, and a
	// 120 Hz painter keeps every one.
	var even []time.Time
	for i := range 120 {
		even = append(even, t0.Add(time.Duration(i)*time.Second/60))
	}
	if d := paintDrops(even, time.Second/120); d != 0 {
		t.Errorf("120 Hz drops %.3f of an even 60 fps stream, want 0", d)
	}
	if d := paintDrops(even, time.Second/60); d > 0.01 {
		t.Errorf("60 Hz drops %.3f of an even 60 fps stream, want about 0", d)
	}
	// A picture replaced after 2 ms is lost unless a 60 Hz tick falls in
	// those 2 ms: 1 - 2/16.7 of the time, one of three pictures.
	pair := []time.Time{t0, t0.Add(2 * time.Millisecond), t0.Add(40 * time.Millisecond)}
	if d, want := paintDrops(pair, time.Second/60), (1-0.12)/3; math.Abs(d-want) > 0.02 {
		t.Errorf("60 Hz drops %.3f of a picture replaced after 2 ms, want %.3f", d, want)
	}
}

func TestPaintDropsJitter(t *testing.T) {
	// 60 fps with a few milliseconds of jitter, as a phone's stream comes
	// over USB: painting at 60 Hz loses a share of the pictures, painting
	// at 120 Hz hardly any.
	rng := rand.New(rand.NewPCG(1, 2))
	t0 := time.Unix(0, 0)
	var ready []time.Time
	for i := range 600 {
		j := time.Duration((rng.Float64()*2 - 1) * float64(4*time.Millisecond))
		ready = append(ready, t0.Add(time.Duration(i)*time.Second/60+j))
	}
	d60, d120 := paintDrops(ready, time.Second/60), paintDrops(ready, time.Second/120)
	if d60 < 0.05 || d120 > 0.01 {
		t.Errorf("drops: 60 Hz %.3f, 120 Hz %.3f; want over 0.05 and under 0.01", d60, d120)
	}
}

func TestAnalyze(t *testing.T) {
	t0 := time.Unix(100, 0)
	tr := trial{move: t0, end: t0.Add(time.Second), app: -1}
	// A still picture, then pictures that changed every 20 ms from 80 ms
	// after the move, 2 ms to decode, then still pictures again.
	tr.pics = append(tr.pics, pic{at: t0.Add(-50 * time.Millisecond), ready: t0.Add(-48 * time.Millisecond), bytes: 1000})
	for at := 80 * time.Millisecond; at <= 1200*time.Millisecond; at += 20 * time.Millisecond {
		tr.pics = append(tr.pics, pic{at: t0.Add(at), ready: t0.Add(at + 2*time.Millisecond), bytes: 25000, changed: true})
	}
	tr.pics = append(tr.pics, pic{at: t0.Add(1300 * time.Millisecond), bytes: 1000})
	r, ok := analyze(tr)
	if !ok {
		t.Fatal("no moved picture found")
	}
	if r.Latency != 80*time.Millisecond || r.Ready != 82*time.Millisecond {
		t.Errorf("latency %v, ready %v; want 80ms, 82ms", r.Latency, r.Ready)
	}
	// The span ends at the last move plus the latency: 1080 ms.
	if math.Abs(r.FPS-50) > 0.5 || r.GapMedian != 20*time.Millisecond || r.GapMax != 20*time.Millisecond {
		t.Errorf("fps %.1f, gaps %v / %v; want 50, 20ms", r.FPS, r.GapMedian, r.GapMax)
	}
	if r.Moving != 51 {
		t.Errorf("moving %d, want 51", r.Moving)
	}
	if math.Abs(r.Mbps-10) > 0.3 {
		t.Errorf("Mbit/s %.2f, want about 10 (25 kB every 20 ms)", r.Mbps)
	}
}

func TestLumaDiff(t *testing.T) {
	a := make([]byte, 1000)
	b := make([]byte, 1000)
	for i := range b {
		b[i] = byte(i % 10) // noise under the threshold
	}
	if d := lumaDiff(a, b); d != 0 {
		t.Errorf("noise counts as %.3f", d)
	}
	for i := range 10 {
		b[i] = 200
	}
	if d := lumaDiff(a, b); d != 0.01 {
		t.Errorf("10 of 1000 moved samples count as %.3f", d)
	}
}

func TestMovedRows(t *testing.T) {
	// Three rows of four samples; one sample in the last row moves, and one
	// in the first moves too little to count.
	a := []byte{100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100}
	b := []byte{110, 100, 100, 100, 100, 100, 100, 100, 100, 100, 200, 100}
	if n, top, bottom := movedRows(a, b, 4); n != 1 || top != 2 || bottom != 2 {
		t.Errorf("movedRows = %d, %d, %d; want 1, 2, 2", n, top, bottom)
	}
	if n, top, bottom := movedRows(a, a, 4); n != 0 || top != -1 || bottom != -1 {
		t.Errorf("movedRows of the same picture = %d, %d, %d; want 0, -1, -1", n, top, bottom)
	}
}

func TestPainting(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(ms int, repeat bool) pic {
		return pic{ready: t0.Add(time.Duration(ms) * time.Millisecond), repeat: repeat}
	}
	// Two pictures 100 ms apart overlap, a repeat at 1 s and a picture
	// at 1.9 s, cut off at 2 s.
	pics := []pic{at(0, false), at(100, false), at(1000, true), at(1900, false)}
	to := t0.Add(2 * time.Second)
	if got, want := painting(pics, t0, to, true), 450*time.Millisecond; got != want {
		t.Errorf("skipping repeats: %v, want %v", got, want)
	}
	if got, want := painting(pics, t0, to, false), 700*time.Millisecond; got != want {
		t.Errorf("waking for repeats: %v, want %v", got, want)
	}
	// From 50 ms on, the first span counts 300 ms.
	if got, want := painting(pics[:2], t0.Add(50*time.Millisecond), to, true), 300*time.Millisecond; got != want {
		t.Errorf("from 50 ms: %v, want %v", got, want)
	}
}

func TestPeakDiff(t *testing.T) {
	if got := peakDiff([]byte{10, 200, 30}, []byte{12, 180, 30}); got != 20 {
		t.Errorf("peakDiff = %d, want 20", got)
	}
}
