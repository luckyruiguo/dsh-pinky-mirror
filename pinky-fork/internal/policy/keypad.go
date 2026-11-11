package policy

import (
	"strings"
	"unicode"

	"github.com/DIYgod/pinky/internal/screen"
)

// A passcode keypad an app draws where screenshots do not reach (iOS's
// capture-proof layer, field test item 21: Trust's keypad) is a password
// field by other means: each tap on a key enters a digit of the person's
// passcode, and the person watching sees blank space where the agent taps.
// Taps on it fall under the credentials rule, as typing a password does. A
// tap on its other keys counts too: Trust took a tap on its Face ID key for
// a wrong passcode.

// hiddenKeypadDigits is how many different digits the keys that the
// screenshot does not show must carry, at least, to make a keypad: a pad
// has ten, and one may lack its label.
const hiddenKeypadDigits = 9

// HiddenKeypad returns the area of a passcode keypad on the screen that the
// screenshot does not show: keys marked screen.StateNotInScreenshot whose
// labels are single digits ("2", "2 ABC"), at least nine different ones.
// The area holds the digit keys and, with a margin of a quarter key, the
// keys between them, such as Face ID or delete.
func HiddenKeypad(s *screen.Screen) (screen.Rect, bool) {
	if s == nil {
		return screen.Rect{}, false
	}
	var digits [10]bool
	n, keys := 0, 0
	var x0, y0, x1, y1, kw, kh int
	for i := range s.Elements {
		e := &s.Elements[i]
		if e.Source == screen.SourceOCR || e.Bounds.Empty() || !e.Has(screen.StateNotInScreenshot) {
			continue
		}
		d, ok := keyDigit(e.Label)
		if !ok {
			continue
		}
		if !digits[d] {
			digits[d] = true
			n++
		}
		b := e.Bounds
		if keys++; keys == 1 {
			x0, y0, x1, y1 = b.X, b.Y, b.X+b.W, b.Y+b.H
		} else {
			x0, y0 = min(x0, b.X), min(y0, b.Y)
			x1, y1 = max(x1, b.X+b.W), max(y1, b.Y+b.H)
		}
		kw, kh = max(kw, b.W), max(kh, b.H)
	}
	if n < hiddenKeypadDigits {
		return screen.Rect{}, false
	}
	mx, my := kw/4, kh/4
	return screen.Rect{X: x0 - mx, Y: y0 - my, W: x1 - x0 + 2*mx, H: y1 - y0 + 2*my}, true
}

// keyDigit reads the digit of a keypad key's label: one digit, alone or
// before the key's letters or sign ("2", "2 ABC", "2\nABC", "0 +",
// full-width "２").
func keyDigit(label string) (int, bool) {
	label = strings.TrimSpace(label)
	rs := []rune(label)
	if len(rs) == 0 {
		return 0, false
	}
	var d int
	switch r := rs[0]; {
	case r >= '0' && r <= '9':
		d = int(r - '0')
	case r >= '０' && r <= '９':
		d = int(r - '０')
	default:
		return 0, false
	}
	if len(rs) == 1 {
		return d, true
	}
	if !unicode.IsSpace(rs[1]) {
		return 0, false
	}
	for _, r := range rs[2:] {
		if unicode.IsDigit(r) {
			return 0, false
		}
	}
	return d, true
}
