package ios

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The stream follows a fold: when the phone moves to its other display,
// the running stream no longer shows the display in use, and live starts
// it again on the new one.
func TestNoteDisplaysFollowsFold(t *testing.T) {
	d := &Device{log: discard}
	parse := func(innerLit, outerLit bool) displayInfo {
		t.Helper()
		di, err := parseDisplayInfo(duoReply(innerLit, outerLit), d.want)
		if err != nil {
			t.Fatal(err)
		}
		return di
	}
	streaming := func(on int) {
		// What startStream leaves: the stream for the display wanted, on
		// the display it got.
		d.mu.Lock()
		d.m = &media{display: on}
		d.streamedLocked(d.want, on, 0, 0)
		d.mu.Unlock()
	}

	si := d.noteDisplays(parse(true, false))
	if si.DisplayID != 1 || d.want != innerKey {
		t.Fatalf("unfolded: %+v", si)
	}
	streaming(1)
	if d.displayMoved() {
		t.Fatal("moved right after the start")
	}

	// Folded: the outer display lights up.
	si = d.noteDisplays(parse(false, true))
	if si.DisplayID != 2 || si.Width != 1080 || d.display().Width != 1080 {
		t.Fatalf("folded: %+v", si)
	}
	if !d.displayMoved() {
		t.Fatal("a fold did not move the stream")
	}
	streaming(2)
	if d.displayMoved() {
		t.Fatal("moved after the restart")
	}

	// The same reply again: nothing moves.
	d.noteDisplays(parse(false, true))
	if d.displayMoved() {
		t.Fatal("moved without a fold")
	}

	// Unfolded again.
	if si = d.noteDisplays(parse(true, false)); si.DisplayID != 1 || !d.displayMoved() {
		t.Fatalf("unfold: %+v, moved %v", si, d.displayMoved())
	}
}

// A stream that fell back to another display than the one wanted shows
// that one: sizes and touches go by it, and nothing moves until the phone
// does.
func TestNoteDisplaysFallback(t *testing.T) {
	d := &Device{log: discard}
	folded, _ := parseDisplayInfo(duoReply(false, true), "")
	unfolded, _ := parseDisplayInfo(duoReply(true, false), "")
	d.noteDisplays(folded)
	if _, ids := d.streamPlanLocked(); !reflect.DeepEqual(ids, []int{2, 1}) {
		t.Fatalf("plan %v", ids)
	}
	// Display 2 gave no picture; display 1 did, of the inner display.
	d.mu.Lock()
	d.m = &media{display: 1}
	d.streamedLocked(outerKey, 1, 1100, 1240)
	d.mu.Unlock()

	if si := d.noteDisplays(folded); si.DisplayID != 1 || si.Width != 2200 {
		t.Fatalf("the stream shows display 1, the screen says %+v", si)
	}
	if d.displayMoved() {
		t.Fatal("moved without a fold")
	}

	// Unfolded: the phone moved to the display the stream already shows.
	if si := d.noteDisplays(unfolded); si.DisplayID != 1 || d.displayMoved() {
		t.Fatalf("unfold onto the streamed display: %+v, moved %v", si, d.displayMoved())
	}
	if d.streamFor != innerKey {
		t.Fatalf("streamFor = %q", d.streamFor)
	}

	// Folded again: a move, and the next start asks first for display 1,
	// which brought the outer display's picture last time.
	d.noteDisplays(folded)
	if !d.displayMoved() {
		t.Fatal("fold: no move")
	}
	if want, ids := d.streamPlanLocked(); want != outerKey || !reflect.DeepEqual(ids, []int{1, 2}) {
		t.Fatalf("plan %q %v", want, ids)
	}
}

// A phone with one display never moves, whatever its reply says.
func TestNoteDisplaysSingle(t *testing.T) {
	d := &Device{log: discard}
	one := func(id int, lit bool) displayInfo {
		di, err := parseDisplayInfo(map[string]any{"displays": []any{duoDisplay(id, 1179, 2556, true, map[string]any{"active": lit})}}, "")
		if err != nil {
			t.Fatal(err)
		}
		return di
	}
	d.noteDisplays(one(1, true))
	d.m = &media{display: 1}
	d.streamedLocked(d.want, 1, 1179, 2556)
	for _, di := range []displayInfo{one(1, false), one(5, true)} {
		d.noteDisplays(di)
		if d.displayMoved() {
			t.Fatalf("moved: %+v", di.Displays)
		}
	}
	// Nor does a foldable without a stream.
	d.m = nil
	folded, _ := parseDisplayInfo(duoReply(false, true), "")
	d.noteDisplays(folded)
	if d.displayMoved() {
		t.Fatal("moved without a stream")
	}
}

// A foldable whose reply names no displayId, as the replies seen from
// phones with one display come: the fold shows as another display being
// in use, the stream tries ids 1, 2 and 3, and each display's next start
// asks first for the id that brought its picture.
func TestFoldWithoutIDs(t *testing.T) {
	d := &Device{log: discard, two: twoScreens("iPhone Duo")}
	parse := func(innerLit, outerLit bool) displayInfo {
		t.Helper()
		di, err := parseDisplayInfo(duoReplyNoIDs(innerLit, outerLit), d.want)
		if err != nil {
			t.Fatal(err)
		}
		return di
	}
	start := func(f *fakeStreams) {
		t.Helper()
		d.mu.Lock()
		want, ids := d.streamPlanLocked()
		ds := d.dinfo.Displays
		d.mu.Unlock()
		m, _, err := f.streamer().try(context.Background(), ids, ds)
		if err != nil {
			t.Fatal(err)
		}
		pw, ph := m.size()
		d.mu.Lock()
		d.m = m
		d.streamedLocked(want, m.display, pw, ph)
		d.mu.Unlock()
	}
	// Folded, the outer display on: display 1 streams it (issue #32).
	folded := &fakeStreams{outcome: map[int]any{1: "picture"}, size: map[int][2]int{1: {1080, 2400}}}
	// Unfolded: display 1 ends its stream, as the report saw ("video
	// receive: EOF"), and display 2 shows the inner one.
	eof := errors.New("video receive: EOF")
	unfolded := &fakeStreams{outcome: map[int]any{1: func(m *media) {
		m.err = eof
		m.stopped.Store(true)
		close(m.done)
	}, 2: "picture"}, size: map[int][2]int{2: {2200, 2480}}}

	si := d.noteDisplays(parse(true, false))
	if si.Width != 2200 || si.DisplayID != 0 {
		t.Fatalf("unfolded: %+v", si)
	}
	start(unfolded)
	if !reflect.DeepEqual(unfolded.started, []int{1, 2}) || d.streamOn != 2 {
		t.Fatalf("unfolded: started %v, streaming %d", unfolded.started, d.streamOn)
	}
	if d.displayMoved() || d.streamShows != "0 2200x2480" {
		t.Fatalf("right after the start: moved %v, shows %q", d.displayMoved(), d.streamShows)
	}
	d.noteDisplays(parse(true, false))
	if d.displayMoved() {
		t.Fatal("moved without a fold")
	}

	// Folded while streaming: a move to the outer display.
	if si = d.noteDisplays(parse(false, true)); si.Width != 1080 || !d.displayMoved() {
		t.Fatalf("fold: %+v, moved %v", si, d.displayMoved())
	}
	start(folded)
	if !reflect.DeepEqual(folded.started, []int{1}) || d.displayMoved() {
		t.Fatalf("folded: started %v, moved %v", folded.started, d.displayMoved())
	}

	// Unfolded again: straight to display 2.
	d.noteDisplays(parse(true, false))
	if !d.displayMoved() {
		t.Fatal("unfold: no move")
	}
	again := &fakeStreams{outcome: unfolded.outcome, size: unfolded.size}
	start(again)
	if !reflect.DeepEqual(again.started, []int{2}) {
		t.Fatalf("unfolded again: started %v", again.started)
	}

	// Asleep, both dark: Pinky stays on the display it showed.
	d.noteDisplays(parse(false, false))
	if d.displayMoved() || d.want != "0 2200x2480" {
		t.Fatalf("asleep: moved %v, want %q", d.displayMoved(), d.want)
	}
}

// A phone with two screens whose reply lists only the display in use, and
// names it display 1 whichever it is: its size tells a fold, and the id
// that streamed the other display does not stand for this one.
func TestFoldOneListed(t *testing.T) {
	d := &Device{log: discard, two: true}
	one := func(w, h float64, extra map[string]any) displayInfo {
		t.Helper()
		di, err := parseDisplayInfo(map[string]any{"displays": []any{duoDisplay(1, w, h, true, extra)}}, d.want)
		if err != nil {
			t.Fatal(err)
		}
		return di
	}
	inner, outer := one(2200, 2480, nil), one(1080, 2400, nil)
	streaming := func(id int) {
		d.mu.Lock()
		d.m = &media{display: id}
		d.streamedLocked(d.want, id, 0, 0)
		d.mu.Unlock()
	}

	d.noteDisplays(inner)
	if _, ids := d.streamPlanLocked(); !reflect.DeepEqual(ids, []int{1, 2, 3}) {
		t.Fatalf("plan %v", ids)
	}
	streaming(2)
	d.noteDisplays(outer)
	if !d.displayMoved() {
		t.Fatal("fold: no move")
	}
	streaming(1)
	// Unfolded: display 1 is what the stream asked for and what the reply
	// says, but it showed the outer display; the stream must move.
	d.noteDisplays(inner)
	if !d.displayMoved() {
		t.Fatal("unfold: no move")
	}
	if _, ids := d.streamPlanLocked(); !reflect.DeepEqual(ids, []int{2, 1, 3}) {
		t.Fatalf("unfold plan %v", ids)
	}
	// Turning the phone is no fold.
	streaming(2)
	d.noteDisplays(one(2480, 2200, map[string]any{"currentOrientation": "rot90"}))
	if d.displayMoved() {
		t.Fatal("a turn moved the stream")
	}
	// Nor is a display that says it is off, or a phone whose backlight is.
	d.noteDisplays(one(1080, 2400, map[string]any{"active": false}))
	if d.displayMoved() {
		t.Fatal("moved to a dark display")
	}
	asleep, _ := parseDisplayInfo(map[string]any{"displays": []any{duoDisplay(1, 1080, 2400, true, nil)}, "backlightState": "off"}, "")
	d.noteDisplays(asleep)
	if d.displayMoved() {
		t.Fatal("moved while the phone is asleep")
	}
	d.noteDisplays(outer)
	if !d.displayMoved() {
		t.Fatal("no move once it is on")
	}
}

// The picture's shape tells which display a stream shows, also when the
// reply's ids say another: sizes and touches go by it, and a fold onto that
// display needs no new stream.
func TestStreamShowsByPicture(t *testing.T) {
	d := &Device{log: discard}
	folded, _ := parseDisplayInfo(duoReply(false, true), "")
	unfolded, _ := parseDisplayInfo(duoReply(true, false), "")
	d.noteDisplays(unfolded)
	// Display 1 streams the outer display, though the reply calls the
	// inner one display 1.
	d.mu.Lock()
	d.m = &media{display: 1}
	d.streamedLocked(innerKey, 1, 1088, 2416)
	d.mu.Unlock()
	if d.streamShows != outerKey || d.display().Width != 1080 || d.good[outerKey] != 1 {
		t.Fatalf("shows %q, screen %+v, good %v", d.streamShows, d.display(), d.good)
	}
	// Still unfolded: the stream shows the other display; that is no
	// move of the phone's, but the screen stays the one streamed.
	if si := d.noteDisplays(unfolded); si.Width != 1080 || d.displayMoved() {
		t.Fatalf("unfolded: %+v, moved %v", si, d.displayMoved())
	}
	// Folded: the phone moved to the display the stream shows.
	if si := d.noteDisplays(folded); si.Width != 1080 || d.displayMoved() || d.streamFor != outerKey {
		t.Fatalf("folded: %+v, moved %v, for %q", si, d.displayMoved(), d.streamFor)
	}
	// Unfolded again: a move.
	d.noteDisplays(unfolded)
	if !d.displayMoved() {
		t.Fatal("unfold: no move")
	}
}

func TestPictureOf(t *testing.T) {
	ds := parseDisplays(duoReply(true, false))
	for _, c := range []struct {
		w, h int
		key  string
	}{
		{2200, 2480, innerKey}, {1100, 1240, innerKey}, {2480, 2200, innerKey},
		{1080, 2400, outerKey}, {1088, 2416, outerKey}, {2400, 1080, outerKey},
		{1500, 2400, ""}, {1000, 1000, ""}, {0, 0, ""},
	} {
		e, ok := pictureOf(ds, c.w, c.h)
		if got := ""; ok {
			got = e.key()
			if got != c.key {
				t.Errorf("%dx%d: %q, want %q", c.w, c.h, got, c.key)
			}
		} else if c.key != "" {
			t.Errorf("%dx%d: none, want %q", c.w, c.h, c.key)
		}
	}
	// Two displays of one shape: the picture cannot tell.
	same := parseDisplays(map[string]any{"displays": []any{
		duoDisplay(1, 1080, 2400, true, nil), duoDisplay(2, 1080, 2400, false, nil),
	}})
	if _, ok := pictureOf(same, 1080, 2400); ok {
		t.Error("told two displays of one shape apart")
	}
}

// A display's uniqueId, when the reply gives one, names it across reads.
func TestDisplayUniqueID(t *testing.T) {
	ds := parseDisplays(map[string]any{"displays": []any{
		duoDisplay(1, 2200, 2480, true, map[string]any{"uniqueId": "inner-uid"}),
		duoDisplay(2, 1080, 2400, false, map[string]any{"uniqueID": "outer-uid"}),
		duoDisplay(3, 1080, 2400, false, nil),
	}})
	if ds[0].UniqueID != "inner-uid" || ds[1].UniqueID != "outer-uid" {
		t.Fatalf("%+v", ds)
	}
	if ds[0].key() != "u inner-uid" || ds[2].key() != "3 1080x2400" {
		t.Fatalf("keys %q %q", ds[0].key(), ds[2].key())
	}
	si := screenOf(ds[0], nil)
	if si.UniqueID != "inner-uid" || si.Key != "u inner-uid" {
		t.Fatalf("%+v", si)
	}
}

func TestTwoScreens(t *testing.T) {
	for model, want := range map[string]bool{
		"iPhone Duo": true, "iPhone19,4": true, "iPhone 17 Pro": false, "iPhone19,3": false, "": false,
	} {
		if got := twoScreens(model); got != want {
			t.Errorf("twoScreens(%q) = %v", model, got)
		}
	}
}

// fakeStreams starts streams that bring a picture or not, by display.
type fakeStreams struct {
	outcome map[int]any    // "picture", "none", an error to start with, or a func(*media) that ends the stream
	size    map[int][2]int // the picture's size, by display
	started []int
	stopped []int
	gone    bool
}

func (f *fakeStreams) streamer() streamDisplay {
	return streamDisplay{
		start: func(_ context.Context, id int) (*media, error) {
			f.started = append(f.started, id)
			m := &media{display: id, ready: make(chan struct{}), done: make(chan struct{})}
			m.width, m.height = f.size[id][0], f.size[id][1]
			switch o := f.outcome[id].(type) {
			case error:
				return nil, o
			case string:
				if o == "picture" {
					close(m.ready)
				}
			case func(*media):
				o(m)
			}
			return m, nil
		},
		stop:   func(m *media) { f.stopped = append(f.stopped, m.display) },
		gone:   func() bool { return f.gone },
		wait:   10 * time.Millisecond,
		log:    discard,
		device: "duo",
	}
}

func TestStreamDisplayFallsBack(t *testing.T) {
	f := &fakeStreams{outcome: map[int]any{2: "none", 1: "picture"}}
	ds := parseDisplays(duoReply(false, true))
	m, nv, err := f.streamer().try(context.Background(), []int{2, 1}, ds)
	if err != nil {
		t.Fatal(err)
	}
	if m.display != 1 || !reflect.DeepEqual(f.started, []int{2, 1}) || !reflect.DeepEqual(f.stopped, []int{2}) {
		t.Fatalf("streams on %d; started %v, stopped %v", m.display, f.started, f.stopped)
	}
	if nv == nil || len(nv.tries) != 1 || nv.tries[0].Display != 2 {
		t.Fatalf("tries before: %+v", nv)
	}

	// The first display works: nothing to tell.
	f = &fakeStreams{outcome: map[int]any{2: "picture"}}
	if m, nv, err = f.streamer().try(context.Background(), []int{2, 1}, ds); err != nil || m.display != 2 || nv != nil {
		t.Fatalf("got %v %+v %v", m, nv, err)
	}
}

func TestStreamDisplayNoVideoAnywhere(t *testing.T) {
	f := &fakeStreams{outcome: map[int]any{}}
	ds := parseDisplays(duoReply(false, true))
	_, _, err := f.streamer().try(context.Background(), []int{2, 1}, ds)
	var nv *noVideoError
	if !errors.As(err, &nv) {
		t.Fatalf("err = %v", err)
	}
	if got := err.Error(); got != "no video from the device (displays 2, 1): context deadline exceeded" {
		t.Errorf("message %q", got)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("not a deadline")
	}
	want := "2 displays: id 1 2200x2480 primary off, id 2 1080x2400 lit. " +
		"Display 2: 0 RTP packets (0 bytes), 0 RTCP. Display 1: 0 RTP packets (0 bytes), 0 RTCP."
	if got := nv.Detail(); got != want {
		t.Errorf("detail\n got %q\nwant %q", got, want)
	}
	if !reflect.DeepEqual(f.stopped, []int{2, 1}) {
		t.Errorf("stopped %v", f.stopped)
	}
}

// A phone with one display tries display 1 alone, and the error names it.
func TestStreamDisplaySingle(t *testing.T) {
	f := &fakeStreams{outcome: map[int]any{}}
	ds := parseDisplays(map[string]any{"displays": []any{map[string]any{
		"primary": true, "bounds": []any{[]any{0.0, 0.0}, []any{1179.0, 2556.0}}, "backlightState": "activeOn",
	}}})
	ids := streamDisplays(ds, 0, false)
	_, _, err := f.streamer().try(context.Background(), ids, ds)
	if err == nil || err.Error() != "no video from the device (display 1): context deadline exceeded" {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(f.started, []int{1}) {
		t.Fatalf("started %v", f.started)
	}
	var nv *noVideoError
	errors.As(err, &nv)
	if got := nv.Detail(); got != "1 display: no id 1179x2556 primary lit (activeOn). Display 1: 0 RTP packets (0 bytes), 0 RTCP." {
		t.Errorf("detail %q", got)
	}
}

// A display the phone refuses to stream is passed over; a refusal that
// no display changes (an app holds the camera) or another failure ends the
// start at once with its own error.
func TestStreamDisplayStartFails(t *testing.T) {
	ds := parseDisplays(duoReply(false, true))
	refused := &CoreDeviceError{Code: 1, Domain: "com.apple.dt.CoreDeviceError", Desc: "invalid display"}
	f := &fakeStreams{outcome: map[int]any{2: refused, 1: "picture"}}
	m, nv, err := f.streamer().try(context.Background(), []int{2, 1}, ds)
	if err != nil || m.display != 1 {
		t.Fatalf("got %v, %v", m, err)
	}
	if nv == nil || nv.tries[0].Err != refused {
		t.Fatalf("tries: %+v", nv)
	}
	if d := nv.Detail(); !strings.Contains(d, "Display 2: start failed: invalid display") {
		t.Errorf("detail %q", d)
	}

	inUse := &CoreDeviceError{Code: errMediaInUse, Domain: "com.apple.dt.CoreDeviceError", Desc: "in use"}
	f = &fakeStreams{outcome: map[int]any{2: inUse, 1: "picture"}}
	if _, _, err = f.streamer().try(context.Background(), []int{2, 1}, ds); err != inUse || len(f.started) != 1 {
		t.Fatalf("in use: %v, started %v", err, f.started)
	}

	other := errors.New("connection reset")
	f = &fakeStreams{outcome: map[int]any{2: other, 1: "picture"}}
	if _, _, err = f.streamer().try(context.Background(), []int{2, 1}, ds); err != other {
		t.Fatalf("other: %v", err)
	}

	// A start that fails after a display without a picture still tells
	// of that display.
	f = &fakeStreams{outcome: map[int]any{2: "none", 1: other}}
	_, _, err = f.streamer().try(context.Background(), []int{2, 1}, ds)
	var e *noVideoError
	if !errors.As(err, &e) || len(e.tries) != 2 || e.cause != other {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(e.Detail(), "Display 1: start failed: connection reset.") {
		t.Errorf("detail %q", e.Detail())
	}
}

// When the connection goes away, no other display is tried.
func TestStreamDisplayLinkGone(t *testing.T) {
	f := &fakeStreams{outcome: map[int]any{1: "picture"}, gone: true}
	_, _, err := f.streamer().try(context.Background(), []int{2, 1}, parseDisplays(duoReply(false, true)))
	if err == nil || !reflect.DeepEqual(f.started, []int{2}) {
		t.Fatalf("err %v, started %v", err, f.started)
	}
}

// A stream the phone ends before its first picture says why.
func TestStreamDisplayEnded(t *testing.T) {
	bye := errors.New("the phone ended the video stream (RTCP BYE)")
	f := &fakeStreams{outcome: map[int]any{1: func(m *media) {
		// What fail does, without the connection a real stream has.
		m.err = bye
		m.stopped.Store(true)
		close(m.done)
	}}}
	_, _, err := f.streamer().try(context.Background(), []int{1}, nil)
	if err == nil || err.Error() != "no video from the device (display 1): "+bye.Error() || !errors.Is(err, bye) {
		t.Fatalf("err = %v", err)
	}
}
