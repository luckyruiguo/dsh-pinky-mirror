package ios

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

func TestParseCaption(t *testing.T) {
	cases := []struct {
		in, label, first, value string
		traits                  []string
	}{
		{"Edit, Button", "Edit", "Edit", "", []string{"button"}},
		{"Settings, 1 new item, Updates Frequently", "Settings, 1 new item", "Settings", "", []string{"updates frequently"}},
		{"Search, Page 1 of 2, Adjustable, Activate to open Spotlight.", "Search, Page 1 of 2", "Search", "", []string{"adjustable"}},
		{"2:10PM, Alarm, 0, Toggle", "2:10PM, Alarm", "2:10PM", "0", []string{"toggle"}},
		{"Alarms, Button, Selected, Tab", "Alarms", "Alarms", "", []string{"button", "selected", "tab"}},
		{"编辑，按钮", "编辑", "编辑", "", []string{"button"}},
		{"Button", "Button", "Button", "", nil}, // a label that is a trait word stays
	}
	for _, c := range cases {
		got := parseCaption(c.in)
		if got.label != c.label || got.first != c.first || got.value != c.value || !reflect.DeepEqual(got.traits, c.traits) {
			t.Errorf("parseCaption(%q) = %+v", c.in, got)
		}
	}
}

func TestAXRole(t *testing.T) {
	cases := []struct {
		bits   uint64
		names  []string
		value  string
		role   string
		states []string
	}{
		{0, []string{"button"}, "", screen.RoleButton, nil},
		{0, []string{"button", "selected", "tab"}, "", screen.RoleTab, []string{screen.StateSelected}},
		{0, []string{"toggle"}, "1", screen.RoleSwitch, []string{screen.StateChecked}},
		{0, []string{"toggle"}, "0", screen.RoleSwitch, nil},
		{0, []string{"secure text field"}, "", screen.RoleInput, []string{screen.StateEditable, screen.StatePassword}},
		{0, []string{"header"}, "", screen.RoleText, nil},
		{traitButton | traitNotEnabled, nil, "", screen.RoleButton, []string{screen.StateDisabled}},
		{0, []string{"updates frequently"}, "", screen.RoleOther, nil},
	}
	for _, c := range cases {
		role, states := axRole(c.bits, c.names, c.value)
		if role != c.role || !reflect.DeepEqual(states, c.states) {
			t.Errorf("axRole(%#x, %v, %q) = %s %v", c.bits, c.names, c.value, role, states)
		}
	}
}

func TestKeyboardKeys(t *testing.T) {
	labels := []string{"Cancel", "Search"}
	for _, r := range "qwertyuiopasdfghjklzxcvbnm" {
		labels = append(labels, string(r))
	}
	labels = append(labels, "shift", "delete", "more", "space", "Search", "Results")
	keys := keyboardKeys(labels)
	for i, l := range labels {
		want := i >= 2 && i < len(labels)-1
		if keys[i] != want {
			t.Errorf("%d %q: key=%v, want %v", i, l, keys[i], want)
		}
	}
	if len(keyboardKeys([]string{"1", "2", "3", "+", "4", "5"})) != 0 {
		t.Error("a calculator keypad is not a keyboard")
	}
}

func TestPlaceByOCR(t *testing.T) {
	els := []screen.Element{
		{Role: "other", Label: "Photos", Source: screen.SourceTree},
		{Role: "other", Label: "Settings, 1 new item", Source: screen.SourceTree},
		{Role: "other", Label: "Photos", Source: screen.SourceTree},
		{Role: "other", Label: "Metronome", Source: screen.SourceTree},
		{Role: "other", Label: "DJI Mimo", Source: screen.SourceTree},
		{Role: "image", Label: "Featured Photo", Source: screen.SourceTree},
	}
	match := []string{"Photos", "Settings", "Photos", "Metronome", "DJI Mimo", "Featured Photo"}
	lines := []ocrLine{
		{Text: "Photos", X: 0.1, Y: 0.1, W: 0.1, H: 0.02},
		{Text: "Photos", X: 0.6, Y: 0.3, W: 0.1, H: 0.02},
		{Text: "Settings", X: 0.1, Y: 0.4, W: 0.1, H: 0.02},
		{Text: "Metrono…", X: 0.5, Y: 0.5, W: 0.2, H: 0.02},
		{Text: "AppleHealth DJI Mimo", X: 0.0, Y: 0.6, W: 0.4, H: 0.02},
		{Text: "12:30", X: 0.1, Y: 0.0, W: 0.1, H: 0.02},
	}
	used := placeByOCR(els, match, lines, 1000, 2000)
	centers := map[int]screen.Point{0: {X: 150, Y: 220}, 1: {X: 150, Y: 820}, 2: {X: 650, Y: 620}, 3: {X: 600, Y: 1020}}
	for i, c := range centers {
		if got := els[i].Bounds.Center(); got != c {
			t.Errorf("%s: center %v, want %v", els[i].Label, got, c)
		}
	}
	// "DJI Mimo" sits in the right part of a merged line.
	if b := els[4].Bounds; b.Empty() || b.X < 150 {
		t.Errorf("merged line box %+v", b)
	}
	if !els[5].Bounds.Empty() {
		t.Error("an image without text on screen got a box")
	}
	if used[5] {
		t.Error("unmatched text marked used")
	}
}

// A back button labelled "Settings" shows only a chevron; the word in a
// longer line belongs to the element that matches that line exactly.
func TestPlaceByOCRPrefersExact(t *testing.T) {
	els := []screen.Element{
		{Role: "button", Label: "Settings", Source: screen.SourceTree},
		{Role: "button", Label: "More in Accessibility Settings…", Source: screen.SourceTree},
		{Role: "other", Label: "Search", Source: screen.SourceTree},
	}
	lines := []ocrLine{
		{Text: "More in Accessibility Settings…", X: 0.2, Y: 0.7, W: 0.6, H: 0.02},
		{Text: "Q Search", X: 0.4, Y: 0.8, W: 0.2, H: 0.02},
		{Text: "Searchlight", X: 0.4, Y: 0.9, W: 0.2, H: 0.02},
	}
	placeByOCR(els, nil, lines, 1000, 1000)
	if !els[0].Bounds.Empty() {
		t.Errorf("back button placed at %+v", els[0].Bounds)
	}
	if els[1].Bounds.Empty() || els[1].Bounds.Y != 700 {
		t.Errorf("exact line not placed: %+v", els[1].Bounds)
	}
	if b := els[2].Bounds; b.Empty() || b.Y != 800 || b.X <= 400 {
		t.Errorf("word match: %+v", b)
	}
}

func TestPlaceByOCRPartOfLabel(t *testing.T) {
	els := []screen.Element{
		{Role: "other", Label: "💸 Wallets folder, 20 apps", Source: screen.SourceTree},
		{Role: "other", Label: "App Store", Source: screen.SourceTree},
	}
	match := []string{"💸 Wallets folder", "App Store"}
	lines := []ocrLine{
		{Text: "Wallets", X: 0.1, Y: 0.5, W: 0.1, H: 0.02},
		{Text: "App", X: 0.5, Y: 0.4, W: 0.1, H: 0.02},
	}
	placeByOCR(els, match, lines, 1000, 1000)
	if els[0].Bounds.Empty() || els[0].Bounds.Y != 500 {
		t.Errorf("folder not placed: %+v", els[0].Bounds)
	}
	if !els[1].Bounds.Empty() {
		t.Errorf("a three-letter fragment placed %q: %+v", els[1].Label, els[1].Bounds)
	}
}

func TestPlaceByOCRInputsFirst(t *testing.T) {
	els := []screen.Element{
		{Role: "button", Label: "Search", Source: screen.SourceTree}, // a row, off screen
		{Role: "input", Label: "Search", Source: screen.SourceTree},
	}
	lines := []ocrLine{{Text: "Search", X: 0.2, Y: 0.93, W: 0.2, H: 0.02}}
	placeByOCR(els, nil, lines, 1000, 1000)
	if !els[0].Bounds.Empty() || els[1].Bounds.Empty() {
		t.Fatalf("row %+v, field %+v", els[0].Bounds, els[1].Bounds)
	}
}

func TestScaleAxis(t *testing.T) {
	if scaleAxis(0, 1125) > 100 || scaleAxis(1124, 1125) < 65400 || scaleAxis(-5, 1125) != scaleAxis(0, 1125) || scaleAxis(5000, 1125) != scaleAxis(1124, 1125) {
		t.Fatal("scaleAxis bounds")
	}
	mid := scaleAxis(562, 1125)
	if mid < 32700 || mid > 32850 {
		t.Fatalf("middle maps to %d", mid)
	}
}

// Without the pasteboard service, text that keys cannot type is refused
// before the field is touched (a zero Device panics on any input).
func TestTypeRejectsNonASCII(t *testing.T) {
	d := &Device{l: &link{}}
	err := d.Type(t.Context(), "你好", driver.TypeOptions{})
	if !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestHIDReports(t *testing.T) {
	r := touchReport(touchContact, 0x1234, 0x5678, 0xA1B2C3D4)
	if len(r) != touchReportLen || r[0] != 0x09 || r[1] != 1 || r[2] != 5 || r[3] != 0xC2 ||
		r[4] != 0x34 || r[5] != 0x12 || r[6] != 0x78 || r[7] != 0x56 || r[40] != 2 || r[44] != 0xD4 || r[47] != 0xA1 {
		t.Fatalf("touch report % x", r)
	}
	k := keyboardReport([]uint8{keyA, keyLeftShift}, 0)
	if len(k) != keyboardReportLen || k[0] != 1 || k[1+keyA/8]&(1<<(keyA%8)) == 0 || k[1+keyLeftShift/8]&(1<<(keyLeftShift%8)) == 0 {
		t.Fatalf("keyboard report % x", k)
	}
	if u, shift, ok := asciiUsage('?'); !ok || !shift || u != 0x38 {
		t.Fatal("'?' mapping")
	}
	if _, _, ok := asciiUsage('é'); ok {
		t.Fatal("non-ASCII mapped")
	}
}

func TestKeyboardFromOCR(t *testing.T) {
	var lines []ocrLine
	for i, l := range []string{"W", "e", "t", "y", "a", "S"} {
		lines = append(lines, ocrLine{Text: l, X: float64(i) / 7, Y: 0.68 + float64(i%2)*0.07, W: 0.03})
	}
	lines = append(lines, ocrLine{Text: "Q clock", Y: 0.54}, ocrLine{Text: "A", Y: 0.2})
	top, ok := keyboardFromOCR(lines)
	if !ok || top < 0.6 || top > 0.68 {
		t.Fatalf("keyboard %v top %v", ok, top)
	}
	if _, ok := keyboardFromOCR(lines[:4]); ok {
		t.Fatal("four letters are not a keyboard")
	}
	var index []ocrLine
	for i, l := range []string{"A", "B", "C", "D", "E", "F"} {
		index = append(index, ocrLine{Text: l, X: 0.97, Y: 0.55 + float64(i)*0.03, W: 0.02})
	}
	if _, ok := keyboardFromOCR(index); ok {
		t.Fatal("a section index is not a keyboard")
	}
}

// A keyboard OCR found lies from a little above its top row of keys, where
// the suggestions bar is, to the bottom of the screen; a frame the tree
// gave stays.
func TestNoteOCRKeyboard(t *testing.T) {
	s := &screen.Screen{Width: 1125, Height: 2436}
	noteOCRKeyboard(s, 0.66)
	if want := (screen.Rect{Y: 1461, W: 1125, H: 975}); !s.Keyboard || s.KeyboardFrame != want {
		t.Fatalf("keyboard %v at %+v, want %+v", s.Keyboard, s.KeyboardFrame, want)
	}
	tree := screen.Rect{Y: 1500, W: 1125, H: 936}
	s = &screen.Screen{Width: 1125, Height: 2436, KeyboardFrame: tree}
	if noteOCRKeyboard(s, 0.66); s.KeyboardFrame != tree {
		t.Fatalf("frame %+v, want the tree's %+v", s.KeyboardFrame, tree)
	}
}

func TestQuietSince(t *testing.T) {
	now := time.Now()
	frames := func(sizes ...int) *media {
		m := &media{started: now}
		m.lastRTCP.Store(now.UnixNano())
		for i, sz := range sizes {
			m.frameLog = append(m.frameLog, frameMark{at: now.Add(time.Duration(i-len(sizes)) * 16 * time.Millisecond), size: sz})
		}
		return m
	}
	d := &Device{}
	since := now.Add(-time.Second)
	cases := []struct {
		name  string
		m     *media
		quiet bool
	}{
		{"idle skip frames", frames(1372, 1372, 1373, 1372, 1373), true},
		{"padded still frames after motion", frames(10315, 10316, 10315, 10318, 10315), true},
		{"scrolling", frames(9061, 1856, 951, 1283, 9504, 2163), false},
		{"too few frames to tell", frames(1372, 1372), false},
		{"no frames from a live stream", frames(), true},
	}
	for _, c := range cases {
		if got := d.quietSince(c.m, since); got != c.quiet {
			t.Errorf("%s: quiet=%v, want %v", c.name, got, c.quiet)
		}
	}
	dead := &media{started: now.Add(-time.Minute)}
	if d.quietSince(dead, since) {
		t.Error("a stream that sends nothing is not quiet")
	}
}
