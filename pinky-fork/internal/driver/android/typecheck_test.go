package android

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/screen"
)

func TestTypedAs(t *testing.T) {
	prior := "hi "
	for _, tc := range []struct {
		v, text  string
		appended bool
		prior    *string
		want     bool
	}{
		{"你好", "你好", false, nil, true},
		{"你", "你好", false, nil, false},
		{"hi 你好", "你好", true, &prior, true},
		{"你好hi ", "你好", true, &prior, true}, // keys go in at the cursor
		{"hi 你好你好", "你好", true, &prior, false},
		{"hi ", "你好", true, &prior, false},
		{"x你好", "你好", true, nil, true}, // nothing to compare the length with
	} {
		if got := typedAs(tc.v, tc.text, tc.appended, tc.prior); got != tc.want {
			t.Errorf("typedAs(%q, %q, %v) = %v, want %v", tc.v, tc.text, tc.appended, got, tc.want)
		}
	}
}

func TestTypedOtherwise(t *testing.T) {
	err := typedOtherwise("abcd", "abcdefg", false, true, "as keys")
	for _, want := range []string{"after typing as keys", "4 characters where 7 were typed", "it was not submitted", "read the field"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q lacks %q", err, want)
		}
	}
	if err := typedOtherwise("hi 你", "你好", true, false, ""); !strings.Contains(err.Error(), "after typing, the field") ||
		!strings.Contains(err.Error(), "4 characters after 2 were added") || strings.Contains(err.Error(), "submitted") {
		t.Errorf("appended: %q", err)
	}
}

func TestNotLanded(t *testing.T) {
	refused := &companion.Error{Code: "action_failed", Message: "the field does not offer ACTION_SET_TEXT; the app did not accept ACTION_PASTE"}
	err := notLanded("by pasting", refused, true).Error()
	for _, want := range []string{
		"did not reach the field",
		"could not write it (the field does not offer ACTION_SET_TEXT; the app did not accept ACTION_PASTE)",
		"typing it by pasting did not work either",
		"the keyboard composes what it is given",
		"Tap the field",
	} {
		if !strings.Contains(err, want) {
			t.Errorf("%q lacks %q", err, want)
		}
	}
	err = notLanded("as keys", nil, false).Error()
	if !strings.Contains(err, "Typing it as keys did not work.") || strings.Contains(err, "companion") || strings.Contains(err, "composes") {
		t.Errorf("without a refusal: %q", err)
	}
}

func TestFocusedField(t *testing.T) {
	field := &companion.Node{Editable: true, Focused: true, Text: "app"}
	tree := &companion.Tree{Windows: []companion.Window{
		// The keyboard's own search box has focus too; it is not the field.
		{Type: companion.WindowInputMethod, Root: &companion.Node{Editable: true, Focused: true, Text: "keyboard"}},
		{Type: companion.WindowApplication},
		{Type: companion.WindowApplication, Root: &companion.Node{Children: []*companion.Node{
			{Editable: true, Text: "other"},
			{Children: []*companion.Node{field}},
		}}},
	}}
	if got := focusedField(tree); got != field {
		t.Fatalf("focusedField = %+v, want the app's field", got)
	}
	if got := focusedField(&companion.Tree{}); got != nil {
		t.Fatalf("focusedField of no windows = %+v", got)
	}
}

// textPhone is a fake phone with the companion whose set_text answers
// with setText and whose tree shows one focused field reading field.
type textPhone struct {
	*quietPhone
	mu       sync.Mutex
	setText  func(req map[string]any) (any, *companion.Error)
	sets     int
	field    string
	password bool
	noField  bool
}

func newTextPhone(t *testing.T, features ...string) (*textPhone, *Device, func() []string) {
	p := &textPhone{quietPhone: newQuietPhone(t)}
	p.quietPhone.mu.Lock()
	p.features = features
	p.reply = p.answer
	p.quietPhone.mu.Unlock()
	d, logs := quietDevice(t, p.quietPhone, "")
	return p, d, logs
}

func (p *textPhone) answer(req map[string]any) (any, *companion.Error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch req["cmd"] {
	case "set_text":
		p.sets++
		return p.setText(req)
	case "tree":
		var root map[string]any
		if !p.noField {
			root = map[string]any{"node": 7, "bounds": []int{0, 200, 1080, 300}, "editable": true, "focused": true}
			if p.password {
				root["password"] = true
			} else if p.field != "" {
				root["text"] = p.field
			}
		}
		w := map[string]any{"id": 1, "type": "application", "layer": 1, "bounds": []int{0, 0, 1080, 2400}}
		if root != nil {
			w["root"] = root
		}
		return map[string]any{"display": map[string]any{"id": 0, "width": 1080, "height": 2400},
			"windows": []any{w}, "time": time.Now().UnixMilli()}, nil
	}
	return nil, nil
}

func (p *textPhone) set(f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f()
}

func noTap() (screen.Point, bool) { return screen.Point{}, false }

func TestTypeCompanionChecksWhatLanded(t *testing.T) {
	fallback := []string{companion.FeatureSetTextFallback}
	for _, tc := range []struct {
		name      string
		features  []string
		result    map[string]any
		wantErr   string
		wantNote  string
		wantEnter bool
		wantLog   string
	}{
		{name: "landed by the input connection", features: fallback,
			result:    map[string]any{"window": 1, "node": 7, "text": "你好", "verified": true, "method": "input_connection"},
			wantEnter: true, wantLog: "wrote the text by input_connection"},
		{name: "submitted by the app", features: fallback,
			result: map[string]any{"window": 1, "node": 7, "text": "你好", "verified": true, "submitted": true, "method": "set_text"}},
		{name: "reads otherwise", features: fallback,
			result:  map[string]any{"window": 1, "node": 7, "text": "你", "verified": false, "method": "paste"},
			wantErr: "the field reads otherwise than the text (1 characters where 2 were typed; it was not submitted)"},
		{name: "a password field", features: fallback,
			result:   map[string]any{"window": 1, "node": 7, "password": true, "submitted": true, "method": "set_text"},
			wantNote: noteHidden},
		{name: "an app without the fallback", // its verified is not checked, as before
			result:    map[string]any{"window": 1, "node": 7, "text": "你", "verified": false},
			wantEnter: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, d, logs := newTextPhone(t, tc.features...)
			p.set(func() { p.setText = func(map[string]any) (any, *companion.Error) { return tc.result, nil } })
			ctx := context.Background()
			c := d.companionClient(ctx)
			if c == nil {
				t.Fatal("no companion")
			}
			entered := false
			done, refused, err := d.typeCompanion(ctx, c, "你好", false, true, 0, noTap, func() error { entered = true; return nil })
			if !done || refused != nil {
				t.Fatalf("done %v, refused %v", done, refused)
			}
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if entered != tc.wantEnter {
				t.Errorf("Enter pressed: %v, want %v", entered, tc.wantEnter)
			}
			if n := d.TakeInputNote(); n != tc.wantNote {
				t.Errorf("note %q, want %q", n, tc.wantNote)
			}
			if tc.wantLog != "" && !strings.Contains(strings.Join(logs(), "\n"), tc.wantLog) {
				t.Errorf("logs %q lack %q", logs(), tc.wantLog)
			}
		})
	}
}

func TestTypeCompanionRefused(t *testing.T) {
	p, d, _ := newTextPhone(t, companion.FeatureSetTextFallback)
	why := "the field does not offer ACTION_SET_TEXT; input connection: the field did not change; the app did not accept ACTION_PASTE"
	p.set(func() {
		p.setText = func(map[string]any) (any, *companion.Error) {
			return nil, &companion.Error{Code: "action_failed", Message: why}
		}
	})
	ctx := context.Background()
	c := d.companionClient(ctx)
	tap := func() (screen.Point, bool) { return screen.Point{X: 540, Y: 250}, true }
	done, refused, err := d.typeCompanion(ctx, c, "你好", false, false, 0, tap, nil)
	if done || err != nil {
		t.Fatalf("done %v, err %v; want the caller to type the text", done, err)
	}
	var ce *companion.Error
	if !errors.As(refused, &ce) || ce.Message != why {
		t.Fatalf("refused = %v, want the companion's reasons", refused)
	}
	p.set(func() {
		if p.sets != 2 {
			t.Errorf("%d set_text requests, want 2 (focus, then the tap point)", p.sets)
		}
	})
}

func TestFieldCheck(t *testing.T) {
	refused := &companion.Error{Code: "action_failed", Message: "the app did not accept ACTION_PASTE"}
	for _, tc := range []struct {
		name          string
		before, after string
		appended      bool
		password      bool
		noField       bool
		wantErr       string
		wantNote      string
	}{
		{name: "landed", before: "", after: "hello"},
		{name: "appended", before: "hi ", after: "hi hello", appended: true},
		{name: "did not land", before: "old", after: "old",
			wantErr: "the text did not reach the field, which reads as it did before typing. Pinky's companion could not write it (the app did not accept ACTION_PASTE), and typing it by pasting did not work either: the keyboard composes"},
		{name: "cut", before: "", after: "hell", wantErr: "4 characters where 5 were typed"},
		{name: "a password field", password: true, wantNote: noteHidden},
		{name: "no field", noField: true, wantNote: noteUnread},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, d, _ := newTextPhone(t)
			p.set(func() { p.field, p.password, p.noField = tc.before, tc.password, tc.noField })
			ctx := context.Background()
			c := d.companionClient(ctx)
			f := d.checkField(ctx, c, 0)
			if f == nil {
				t.Fatal("no check")
			}
			p.set(func() { p.field = tc.after })
			start := time.Now()
			err := f.after(ctx, "hello", tc.appended, false, false, refused, true)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if n := d.TakeInputNote(); n != tc.wantNote {
				t.Errorf("note %q, want %q", n, tc.wantNote)
			}
			if tc.wantErr == "" && tc.wantNote == "" && time.Since(start) > time.Second {
				t.Errorf("a field that took the text was read for %v", time.Since(start))
			}
		})
	}
	var none *fieldCheck
	if err := none.after(context.Background(), "x", false, false, true, nil, false); err != nil {
		t.Fatalf("a nil check: %v", err)
	}
}
