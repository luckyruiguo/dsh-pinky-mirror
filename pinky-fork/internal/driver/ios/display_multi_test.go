package ios

import (
	"reflect"
	"testing"
)

// duoDisplay is one display of a getdisplayinfo reply with CoreDevice's
// keys (CoreDevice.Display). No reply from a foldable has been seen yet:
// these are made up from those keys, the inner display as the primary one.
func duoDisplay(id int, w, h float64, primary bool, extra map[string]any) map[string]any {
	m := map[string]any{
		"displayId":          uint64(id),
		"primary":            primary,
		"external":           false,
		"bounds":             []any{[]any{0.0, 0.0}, []any{w, h}},
		"pointScale":         3.0,
		"currentOrientation": "rot0",
		"nativeOrientation":  "rot0",
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// innerKey and outerKey are the keys (displayEntry.key) of duoReply's
// displays.
const innerKey, outerKey = "1 2200x2480", "2 1080x2400"

// duoReply is a reply of a foldable with its inner (id 1) and outer (id 2)
// display, either lit.
func duoReply(innerLit, outerLit bool) map[string]any {
	return map[string]any{
		"displays": []any{
			duoDisplay(1, 2200, 2480, true, map[string]any{"active": innerLit}),
			duoDisplay(2, 1080, 2400, false, map[string]any{"active": outerLit}),
		},
		"backlightState": "activeOn",
	}
}

// duoReplyNoIDs is duoReply without displayId, as the replies seen from
// phones with one display come (iOS 27.0 to 27.2).
func duoReplyNoIDs(innerLit, outerLit bool) map[string]any {
	out := duoReply(innerLit, outerLit)
	for _, d := range out["displays"].([]any) {
		delete(d.(map[string]any), "displayId")
	}
	return out
}

func TestParseDisplayInfoFoldable(t *testing.T) {
	open, err := parseDisplayInfo(duoReply(true, false), "")
	if err != nil {
		t.Fatal(err)
	}
	if s := open.Screen; s.DisplayID != 1 || s.Width != 2200 || s.Height != 2480 || s.Backlight != "activeOn" {
		t.Fatalf("unfolded: %+v", s)
	}
	if len(open.Displays) != 2 || open.Displays[1].ID != 2 || open.Displays[1].Lit != -1 {
		t.Fatalf("displays: %+v", open.Displays)
	}

	folded, err := parseDisplayInfo(duoReply(false, true), innerKey)
	if err != nil {
		t.Fatal(err)
	}
	if s := folded.Screen; s.DisplayID != 2 || s.Width != 1080 || s.Height != 2400 {
		t.Fatalf("folded: the outer display is the one lit, got %+v", s)
	}
	if s, ok := folded.screenFor(1); !ok || s.DisplayID != 1 || s.Width != 2200 {
		t.Fatalf("screenFor(1) = %+v, %v", s, ok)
	}
	if _, ok := folded.screenFor(3); ok {
		t.Fatal("screenFor a display not listed")
	}
	if _, ok := folded.screenFor(0); ok {
		t.Fatal("screenFor(0)")
	}
}

// Without "active", each display's backlight says which one is lit; the
// primary one is passed over while it is off.
func TestParseDisplayInfoBacklightPerDisplay(t *testing.T) {
	out := map[string]any{"displays": []any{
		duoDisplay(1, 2200, 2480, true, map[string]any{"backlightState": "off"}),
		duoDisplay(2, 1080, 2400, false, map[string]any{"backlightState": "activeDimmed"}),
	}}
	di, err := parseDisplayInfo(out, "")
	if err != nil {
		t.Fatal(err)
	}
	if di.Screen.DisplayID != 2 || di.Screen.Backlight != "activeDimmed" {
		t.Fatalf("got %+v", di.Screen)
	}
}

// "active" says more than the backlight: a display may keep its backlight
// on for a moment after the phone moved to the other one.
func TestParseDisplaysActiveWins(t *testing.T) {
	ds := parseDisplays(map[string]any{"displays": []any{
		duoDisplay(1, 2200, 2480, true, map[string]any{"active": false, "backlightState": "activeOn"}),
		duoDisplay(2, 1080, 2400, false, map[string]any{"active": true, "backlightState": "off"}),
	}})
	if ds[0].Lit != -1 || ds[1].Lit != 1 {
		t.Fatalf("lit = %d, %d", ds[0].Lit, ds[1].Lit)
	}
	if e, _ := chooseDisplay(ds); e.ID != 2 {
		t.Fatalf("chose %d", e.ID)
	}
}

// While no display is lit (the phone asleep), Pinky stays on the display
// it showed rather than move to the primary one.
func TestParseDisplayInfoKeepsWhileDark(t *testing.T) {
	dark := map[string]any{
		"displays": []any{
			duoDisplay(1, 2200, 2480, true, map[string]any{"backlightState": "off"}),
			duoDisplay(2, 1080, 2400, false, map[string]any{"backlightState": "off"}),
		},
	}
	for _, c := range []struct {
		keep string
		want int
	}{{"", 1}, {innerKey, 1}, {outerKey, 2}, {"7 1x1", 1}} {
		di, err := parseDisplayInfo(dark, c.keep)
		if err != nil {
			t.Fatal(err)
		}
		if di.Screen.DisplayID != c.want {
			t.Errorf("keep %q: showed %d, want %d", c.keep, di.Screen.DisplayID, c.want)
		}
	}
	// Once one lights up, Pinky moves to it.
	di, _ := parseDisplayInfo(duoReply(true, false), outerKey)
	if di.Screen.DisplayID != 1 {
		t.Errorf("lit inner display: showed %d", di.Screen.DisplayID)
	}
}

// iOS 27.2 says the backlight next to "displays"; with several displays
// that is the shown one's backlight too.
func TestParseDisplayInfoTopLevelBacklight(t *testing.T) {
	out := map[string]any{
		"displays": []any{
			duoDisplay(1, 2200, 2480, true, nil),
			duoDisplay(2, 1080, 2400, false, nil),
		},
		"backlightState": "activeDimmed",
	}
	di, err := parseDisplayInfo(out, "")
	if err != nil {
		t.Fatal(err)
	}
	if di.Screen.DisplayID != 1 || di.Screen.Backlight != "activeDimmed" {
		t.Fatalf("got %+v", di.Screen)
	}
	if di.Displays[0].Lit != 0 || di.Displays[1].Lit != 0 {
		t.Fatalf("lit is not known per display: %+v", di.Displays)
	}
}

// An external display (a screen on a cable or AirPlay) is never the phone's
// own picture, lit or not.
func TestChooseDisplayExternalLast(t *testing.T) {
	ds := parseDisplays(map[string]any{"displays": []any{
		duoDisplay(3, 1920, 1080, false, map[string]any{"external": true, "active": true}),
		duoDisplay(1, 1179, 2556, true, nil),
	}})
	if e, _ := chooseDisplay(ds); e.ID != 1 {
		t.Fatalf("chose %d", e.ID)
	}
	if got := streamDisplays(ds, 1, false); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("streamDisplays = %v", got)
	}
}

// A phone streaming its screen makes a wireless display for the stream
// (seen on the iPhone 13 mini, id 2, 1136x2448). Should the display info
// list it, the phone still has one screen: Pinky stays on it, dark or
// not, and never asks a stream of the wireless one.
func TestStreamDisplayNotOwn(t *testing.T) {
	for _, kind := range []any{"wireless", "Virtual", map[string]any{"wireless": map[string]any{}}} {
		reply := func(lit bool) map[string]any {
			return map[string]any{"displays": []any{
				duoDisplay(1, 1125, 2436, true, map[string]any{"active": lit, "type": "integrated"}),
				duoDisplay(2, 1136, 2448, false, map[string]any{"active": true, "type": kind}),
			}}
		}
		for _, lit := range []bool{true, false} {
			di, err := parseDisplayInfo(reply(lit), "1 1125x2436")
			if err != nil {
				t.Fatal(err)
			}
			if di.Screen.DisplayID != 1 || di.Screen.Width != 1125 {
				t.Errorf("%v, lit %v: showed %+v", kind, lit, di.Screen)
			}
			if several(di.Displays) {
				t.Errorf("%v: several displays", kind)
			}
			if got := streamDisplays(di.Displays, di.Screen.DisplayID, false); !reflect.DeepEqual(got, []int{1}) {
				t.Errorf("%v: streamDisplays = %v", kind, got)
			}
		}
	}
	// Nor does it make a fold.
	d := &Device{log: discard}
	di, _ := parseDisplayInfo(map[string]any{"displays": []any{
		duoDisplay(1, 1125, 2436, true, map[string]any{"active": false}),
		duoDisplay(2, 1136, 2448, false, map[string]any{"active": true, "type": "wireless"}),
	}}, "")
	d.noteDisplays(di)
	d.m = &media{display: 1}
	d.streamedLocked(d.want, 1, 1125, 2436)
	d.noteDisplays(di)
	if d.displayMoved() || d.wantID != 1 {
		t.Fatalf("moved to %q", d.want)
	}
	if got := describeDisplays(di.Displays); got != "2 displays: id 1 1125x2436 primary off, id 2 1136x2448 wireless lit" {
		t.Errorf("describe %q", got)
	}
}

// A display without a size is passed over.
func TestChooseDisplaySkipsSizeless(t *testing.T) {
	ds := parseDisplays(map[string]any{"displays": []any{
		map[string]any{"displayId": 2, "primary": false, "active": true},
		duoDisplay(1, 1179, 2556, true, nil),
	}})
	if e, ok := chooseDisplay(ds); !ok || e.ID != 1 {
		t.Fatalf("chose %+v", e)
	}
	if _, err := parseDisplayInfo(map[string]any{"displays": []any{map[string]any{"primary": true}}}, ""); err == nil {
		t.Fatal("a display without a size parsed")
	}
	if _, err := parseDisplayInfo(map[string]any{}, ""); err == nil {
		t.Fatal("a reply without displays parsed")
	}
}

func TestStreamDisplays(t *testing.T) {
	one := func(extra map[string]any) []displayEntry {
		d := map[string]any{
			"primary": true, "bounds": []any{[]any{0.0, 0.0}, []any{1179.0, 2556.0}},
		}
		for k, v := range extra {
			d[k] = v
		}
		return parseDisplays(map[string]any{"displays": []any{d}})
	}
	duo := parseDisplays(duoReply(false, true))
	for _, c := range []struct {
		name  string
		ds    []displayEntry
		shown int
		two   bool
		want  []int
	}{
		{"nothing listed", nil, 0, false, []int{1}},
		{"one display without an id (iOS 27.0)", one(nil), 0, false, []int{1}},
		{"one display with id 1", one(map[string]any{"displayId": 1}), 1, false, []int{1}},
		// As before Pinky knew of displays: 1 first, then its own id.
		{"one display with another id", one(map[string]any{"displayId": int64(5)}), 5, false, []int{1, 5}},
		{"folded", duo, 2, false, []int{2, 1}},
		{"unfolded", parseDisplays(duoReply(true, false)), 1, false, []int{1, 2}},
		{"nothing shown yet", duo, 0, false, []int{2, 1}},
		{"ids not 1", parseDisplays(map[string]any{"displays": []any{
			duoDisplay(4, 2200, 2480, true, map[string]any{"active": false}),
			duoDisplay(5, 1080, 2400, false, map[string]any{"active": true}),
		}}), 5, false, []int{5, 4, 1}},
		// A foldable whose ids are known needs no others, two screens or not.
		{"folded, two screens", duo, 2, true, []int{2, 1}},
		// Ids the reply does not give are tried in turn.
		{"two displays without ids", parseDisplays(duoReplyNoIDs(true, false)), 0, false, []int{1, 2, 3}},
		{"two screens, one listed without an id", one(nil), 0, true, []int{1, 2, 3}},
		{"two screens, one listed with id 2", one(map[string]any{"displayId": 2}), 2, true, []int{2, 1, 3}},
		{"two screens, one listed with id 1", one(map[string]any{"displayId": 1}), 1, true, []int{1, 2, 3}},
	} {
		if got := streamDisplays(c.ds, c.shown, c.two); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDescribeDisplays(t *testing.T) {
	ds := parseDisplays(map[string]any{"displays": []any{
		duoDisplay(1, 2200, 2480, true, map[string]any{"backlightState": "off"}),
		duoDisplay(2, 1080, 2400, false, map[string]any{"active": true}),
		map[string]any{"external": true, "bounds": []any{[]any{0.0, 0.0}, []any{1920.0, 1080.0}}},
	}})
	want := "3 displays: id 1 2200x2480 primary off (off), id 2 1080x2400 lit, no id 1920x1080 external"
	if got := describeDisplays(ds); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got := describeDisplays(ds[1:2]); got != "1 display: id 2 1080x2400 lit" {
		t.Errorf("one display: %q", got)
	}
	if got := describeDisplays(nil); got != "no displays listed" {
		t.Errorf("none: %q", got)
	}
}

// The single display phones have had so far: the reply without ids that
// iOS 27.0 sends keeps the display Pinky always showed.
func TestParseDisplayInfoSingleUnchanged(t *testing.T) {
	out := map[string]any{"displays": []any{map[string]any{
		"primary":            true,
		"bounds":             []any{[]any{0.0, 0.0}, []any{1080.0, 2340.0}},
		"pointScale":         3.0,
		"currentOrientation": "rot0",
		"backlightState":     "off",
	}}}
	di, err := parseDisplayInfo(out, "")
	if err != nil {
		t.Fatal(err)
	}
	if s := di.Screen; s.DisplayID != 0 || s.Width != 1080 || s.Height != 2340 || s.Backlight != "off" {
		t.Fatalf("got %+v", s)
	}
	// Even dark, and with a display kept from before.
	if di, _ = parseDisplayInfo(out, "1 1080x2340"); di.Screen.Width != 1080 {
		t.Fatalf("kept: %+v", di.Screen)
	}
	if got := streamDisplays(di.Displays, di.Screen.DisplayID, false); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("streamDisplays = %v", got)
	}
}
