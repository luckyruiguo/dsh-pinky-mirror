package policy

import (
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// keypad lays out a phone keypad's keys, 1-9 then left, 0, right, in a
// 3x4 grid from (100, 1200) with keys 200x150.
func keypad(labels []string, states ...string) []screen.Element {
	var els []screen.Element
	for i, l := range labels {
		if l == "" {
			continue
		}
		els = append(els, screen.Element{Role: screen.RoleButton, Label: l, Source: screen.SourceTree, States: states,
			Bounds: screen.Rect{X: 100 + i%3*200, Y: 1200 + i/3*150, W: 200, H: 150}})
	}
	return els
}

var trustKeys = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "FaceID", "0", "Delete"}

func TestHiddenKeypad(t *testing.T) {
	hidden := &screen.Screen{Width: 1170, Height: 2532, Elements: append(
		[]screen.Element{{Role: screen.RoleText, Label: "Enter passcode", Source: screen.SourceTree, Bounds: screen.Rect{X: 300, Y: 600, W: 500, H: 60}}},
		keypad(trustKeys, screen.StateNotInScreenshot)...)}
	pad, ok := HiddenKeypad(hidden)
	if !ok {
		t.Fatal("no keypad found")
	}
	for _, e := range hidden.Elements[1:] {
		if !pad.Contains(e.Bounds.Center()) {
			t.Errorf("%q outside the keypad %+v", e.Label, pad)
		}
	}
	if pad.Contains(hidden.Elements[0].Bounds.Center()) {
		t.Error("the title counts as a key")
	}

	// Labels with letters, full-width digits, one key without a label.
	phone := []string{"1", "2 ABC", "3\nDEF", "4 GHI", "５", "6 MNO", "7 PQRS", "8 TUV", "", "*", "0 +", "#"}
	if _, ok := HiddenKeypad(&screen.Screen{Elements: keypad(phone, screen.StateNotInScreenshot)}); !ok {
		t.Error("a phone keypad with letters is not found")
	}

	// A keypad the screenshot shows (Calculator, a phone's dialer) is not
	// one, nor are a few hidden digits.
	if _, ok := HiddenKeypad(&screen.Screen{Elements: keypad(trustKeys)}); ok {
		t.Error("a keypad the screenshot shows counts")
	}
	if _, ok := HiddenKeypad(&screen.Screen{Elements: keypad(trustKeys[:6], screen.StateNotInScreenshot)}); ok {
		t.Error("six hidden digits make a keypad")
	}
	// Hidden numbers that are not keys: amounts, dates.
	amounts := []string{"12", "2.5", "3%", "4x", "5 kg", "6-7", "7a", "8.0", "9,0", "10"}
	if _, ok := HiddenKeypad(&screen.Screen{Elements: keypad(amounts, screen.StateNotInScreenshot)}); ok {
		t.Error("numbers that are not keys make a keypad")
	}
	if _, ok := HiddenKeypad(nil); ok {
		t.Error("nil screen")
	}
}
