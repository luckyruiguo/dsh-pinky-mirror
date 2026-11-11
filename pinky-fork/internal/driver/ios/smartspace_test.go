package ios

import (
	"reflect"
	"testing"
)

func TestSmartSpaces(t *testing.T) {
	for _, c := range []struct {
		name                string
		before, after, text string
		found, ok           bool
		lead, trail         bool
		want                string
	}{
		// What the iPhone 13 mini did on iOS 27.0.1, in Settings' search.
		{"word after a word", "Wall", "Wall paper", "paper", true, true, true, false, "Wallpaper"},
		{"third word", "hello world", "hello world again", "again", true, true, true, false, "hello worldagain"},
		{"hyphen after a word", "Wi", "Wi -Fi", "-Fi", true, true, true, false, "Wi-Fi"},
		{"accented word", "Wall", "Wall café", "café", true, true, true, false, "Wallcafé"},
		{"between two words", "helloworld", "hello big world", "big", true, true, true, true, "hellobigworld"},
		{"before a word", "paper", "Wall paper", "Wall", true, true, false, true, "Wallpaper"},
		{"text with spaces", "Wall", "Wall big paper", "big paper", true, true, true, false, "Wallbig paper"},
		{"text with its own space", "Wall", "Wall paper", " paper", false, true, false, false, ""},
		{"after a space", "Wall ", "Wall paper", "paper", false, true, false, false, ""},
		{"Chinese", "设置", "设置通用", "通用", false, true, false, false, ""},
		{"Latin after Chinese", "通用", "通用Wall", "Wall", false, true, false, false, ""},
		{"empty field", "", "hello", "hello", false, true, false, false, ""},
		{"replaced by the app", "Wall", "WALL PAPER", "paper", false, true, false, false, ""},
		{"no change", "Wall", "Wall", "paper", false, true, false, false, ""},
		{"nothing typed", "Wall", "Wall ", "", false, true, false, false, ""},
		// "a x b": " x" after "a", or "x " before "b"; the keys differ.
		{"two readings", "a b", "a x b", "x", true, false, false, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fix, found, ok := smartSpaces(c.before, c.after, c.text)
			if found != c.found || ok != c.ok {
				t.Fatalf("found %v, ok %v; want %v, %v (%+v)", found, ok, c.found, c.ok, fix)
			}
			if !found || !ok {
				return
			}
			if fix.lead != c.lead || fix.trail != c.trail || fix.want != c.want {
				t.Errorf("got %+v; want lead %v, trail %v, %q", fix, c.lead, c.trail, c.want)
			}
		})
	}
}

func TestCursorSteps(t *testing.T) {
	for _, c := range []struct {
		text string
		n    int
		ok   bool
	}{
		{"paper", 5, true},
		{"café", 4, true}, // é as one code point
		{"你好", 2, true},
		{"👋hi", 3, true},
		{"a\nb", 3, true},
		{"café", 0, false}, // e and a combining accent
		{"👋🏽hi", 0, false},
		{"🇸🇬", 0, false},
		{"❤️", 0, false},
		{"a\r\nb", 0, false},
		{"👨‍👩‍👧", 0, false},
	} {
		n, ok := cursorSteps(c.text)
		if n != c.n || ok != c.ok {
			t.Errorf("cursorSteps(%q) = %d, %v; want %d, %v", c.text, n, ok, c.n, c.ok)
		}
	}
}

func TestSmartSpaceKeys(t *testing.T) {
	bs, l, r := keyStroke{usage: keyBackspace}, keyStroke{usage: keyLeft}, keyStroke{usage: keyRight}
	for _, c := range []struct {
		name string
		fix  smartSpaceFix
		text string
		keys []keyStroke
		ok   bool
	}{
		{"after the text", smartSpaceFix{trail: true}, "Wall", []keyStroke{bs}, true},
		{"before the text", smartSpaceFix{lead: true}, "abc", []keyStroke{l, l, l, bs, r, r, r}, true},
		{"both", smartSpaceFix{lead: true, trail: true}, "ab", []keyStroke{bs, l, l, bs, r, r}, true},
		{"before text Pinky cannot step over", smartSpaceFix{lead: true}, "👋🏽hi", nil, false},
		{"after text Pinky cannot step over", smartSpaceFix{trail: true}, "👋🏽hi", []keyStroke{bs}, true},
	} {
		keys, ok := c.fix.keys(c.text)
		if ok != c.ok || !reflect.DeepEqual(keys, c.keys) {
			t.Errorf("%s: %v, %v; want %v, %v", c.name, keys, ok, c.keys, c.ok)
		}
	}
}
