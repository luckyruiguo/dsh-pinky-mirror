package ios

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

func TestParsePBReply(t *testing.T) {
	reply := map[string]any{
		"command": "PULL_REPLY",
		"pasteboard": map[string]any{
			"items": []any{
				map[string]any{
					"types": []any{"public.utf8-plain-text", "public.html"},
					"data": map[string]any{
						"public.utf8-plain-text": map[string]any{"data": []byte("hi")},
						"public.html":            map[string]any{"isPromised": true, "isAvailable": false, "size": int64(9)},
					},
				},
				map[string]any{
					"types": []any{"public.png"},
					"data":  map[string]any{"public.png": map[string]any{"data": []byte{0x89, 'P'}}},
				},
			},
			"metadata": map[string]any{"changeCount": int64(104), "nonce": "x", "pasteboardName": "general"},
		},
	}
	s, err := parsePBReply(reply, "PULL_REPLY")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Items) != 2 || !s.HasCount || s.ChangeCount != 104 {
		t.Fatalf("got %+v", s)
	}
	if s.complete() {
		t.Error("a promised representation counted as complete")
	}
	if !s.Items[0].Data["public.html"].Promised || !bytes.Equal(s.Items[0].Data["public.utf8-plain-text"].Data, []byte("hi")) {
		t.Errorf("item 0: %+v", s.Items[0])
	}
	// SET gets only what came with data, in the original order.
	wire := s.wireItems()
	if len(wire) != 2 {
		t.Fatalf("wire items: %v", wire)
	}
	first := wire[0].(map[string]any)
	if types := first["types"].([]any); len(types) != 1 || types[0] != "public.utf8-plain-text" {
		t.Errorf("first item's types: %v", types)
	}
	if _, ok := first["data"].(map[string]any)["public.html"]; ok {
		t.Error("a promised representation was sent")
	}

	empty, err := parsePBReply(map[string]any{"command": "SET_REPLY", "pasteboard": map[string]any{"items": []any{}}}, "SET_REPLY")
	if err != nil || len(empty.Items) != 0 || !empty.complete() || len(empty.wireItems()) != 0 {
		t.Errorf("empty pasteboard: %+v, %v", empty, err)
	}
}

func TestParsePBReplyErrors(t *testing.T) {
	restricted := map[string]any{"error": map[string]any{
		"code":     int64(26006),
		"domain":   "com.apple.dt.CoreDeviceError",
		"userInfo": map[string]any{"NSLocalizedDescription": "The pasteboard contains type 'com.apple.is-remote-clipboard' ..."},
	}}
	if _, err := parsePBReply(restricted, "PULL_REPLY"); !errors.Is(err, errPBRestricted) {
		t.Errorf("restricted: %v", err)
	}
	other := map[string]any{"error": map[string]any{"code": int64(1), "domain": "com.apple.dt.CoreDeviceError", "userInfo": map[string]any{"NSLocalizedDescription": "nope"}}}
	var ce *CoreDeviceError
	if _, err := parsePBReply(other, "PULL_REPLY"); !errors.As(err, &ce) || errors.Is(err, errPBRestricted) {
		t.Errorf("other error: %v", err)
	}
	if _, err := parsePBReply(map[string]any{"command": "PUSH"}, "PULL_REPLY"); err == nil {
		t.Error("a PUSH was taken for a PULL_REPLY")
	}
}

func TestTextItems(t *testing.T) {
	items := textItems("你好 👋🏽")
	it := items[0].(map[string]any)
	data := it["data"].(map[string]any)["public.utf8-plain-text"].(map[string]any)["data"].([]byte)
	if string(data) != "你好 👋🏽" {
		t.Errorf("data %q", data)
	}
}

func TestHasCandidates(t *testing.T) {
	bar := []raxElement{
		{Label: "Clear text", Type: xcButton},
		{Label: "啊, 叹词的啊", Ident: "啊", Type: xcCell},
		{Label: "More suggestions", Type: xcButton},
	}
	if !hasCandidates(bar) {
		t.Error("Pinyin's candidate bar was not recognized")
	}
	plain := []raxElement{
		{Label: "微信读书, Apps", Ident: "OpenInstalledAppsSettingsDeepLinks.InstalledAppsSettingsDeepLinks.com.tencent.weread", Type: xcButton},
		{Label: "General", Ident: "com.apple.settings.general", Type: xcCell},
		{Label: "中国联通, Apps", Type: xcButton},
	}
	if hasCandidates(plain) {
		t.Error("search results were taken for candidates")
	}
}

func TestProbeArrived(t *testing.T) {
	for v, want := range map[string]bool{"a": true, "A": true, "Search a": true, "": false, "啊": false, "ㅁ": false} {
		if got := probeArrived(v); got != want {
			t.Errorf("%q: %v, want %v", v, got, want)
		}
	}
}

func TestPinStrokes(t *testing.T) {
	s, err := pinStrokes("1a!")
	if err != nil || len(s) != 3 || s[0].usage != key1 || s[1].usage != keyA || s[2].mod != keyLeftShift {
		t.Errorf("strokes %v, %v", s, err)
	}
	for _, bad := range []string{"", "12 34", "12\n34", "１２３４"} {
		if _, err := pinStrokes(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// fakePuller fails its first n reads with err, then returns snap.
type fakePuller struct {
	fails int
	err   error
	snap  *pbSnapshot
	calls int
}

func (f *fakePuller) pull(ctx context.Context, policy string) (*pbSnapshot, error) {
	f.calls++
	if f.calls <= f.fails {
		return nil, f.err
	}
	return f.snap, nil
}

func TestSavePasteboardRetriesOnce(t *testing.T) {
	ctx := context.Background()
	empty := &pbSnapshot{}
	// A connection the phone closed while idle: the first read fails, the
	// second, on a new connection, works.
	f := &fakePuller{fails: 1, err: errors.New("pasteboard: EOF"), snap: empty}
	if _, kind, err := savePasteboard(ctx, f); err != nil || kind != "empty" || f.calls != 2 {
		t.Fatalf("after one failure: kind %q, err %v, calls %d", kind, err, f.calls)
	}
	// Two failures in a row: the service did not answer.
	f = &fakePuller{fails: 2, err: errors.New("pasteboard: EOF"), snap: empty}
	if _, _, err := savePasteboard(ctx, f); !errors.Is(err, errPasteUnavailable) || f.calls != 2 {
		t.Fatalf("after two failures: err %v, calls %d", err, f.calls)
	}
	// A pasteboard nobody may read is not retried.
	f = &fakePuller{fails: 1, err: errPBRestricted, snap: empty}
	if _, kind, err := savePasteboard(ctx, f); err != nil || kind != "unreadable" || f.calls != 1 {
		t.Fatalf("restricted: kind %q, err %v, calls %d", kind, err, f.calls)
	}
}

func TestKeyStrokesFor(t *testing.T) {
	// No pasteboard service: non-ASCII text is unsupported.
	if _, err := keyStrokesFor("你好", nil); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("no service: %v", err)
	}
	// The service is there but did not answer: try again, not unsupported.
	_, err := keyStrokesFor("你好", errors.New("pasteboard: EOF"))
	if err == nil || errors.Is(err, driver.ErrUnsupported) || !strings.Contains(err.Error(), "try again") {
		t.Fatalf("service did not answer: %v", err)
	}
	// ASCII still goes as keys either way.
	if keys, err := keyStrokesFor("ok", errors.New("pasteboard: EOF")); err != nil || len(keys) != 2 {
		t.Fatalf("ascii: %v %v", keys, err)
	}
}

// A read that timed out is not repeated: the second wait only doubled the
// time before typing by keys (6 s instead of 3, issue #29).
func TestSavePasteboardTimeoutNotRetried(t *testing.T) {
	f := &fakePuller{fails: 2, err: fmt.Errorf("pasteboard: %w", context.DeadlineExceeded), snap: &pbSnapshot{}}
	if _, _, err := savePasteboard(context.Background(), f); !errors.Is(err, errPasteUnavailable) || f.calls != 1 {
		t.Fatalf("timed out: err %v, calls %d", err, f.calls)
	}
}

func TestArrived(t *testing.T) {
	for _, tc := range []struct{ before, after, want string }{
		{"", "a", "a"},
		{"", "A", "A"},
		{"Search", "a", "a"}, // a placeholder that went away
		{"helo", "hello", "l"},
		{"one two", "one ;two", ";"},
		{"Sur", "Suro", "o"},
		{"", "", ""},
		{"abc", "ab", "b"},
	} {
		if got := arrived(tc.before, tc.after); got != tc.want {
			t.Errorf("arrived(%q, %q) = %q, want %q", tc.before, tc.after, got, tc.want)
		}
	}
}

func TestOtherLatin(t *testing.T) {
	for s, want := range map[string]bool{"q": true, "Q": true, ";": true, "a": false, "A": false, "": false, "ab": false, "ö": false, "啊": false, " ": false} {
		if got := otherLatin(s); got != want {
			t.Errorf("otherLatin(%q) = %v, want %v", s, got, want)
		}
	}
}

// TestTypedAs: what reading the field back accepts as the text typed.
func TestTypedAs(t *testing.T) {
	for _, tc := range []struct {
		v, text  string
		appended bool
		prior    string
		spaces   string
		want     bool
	}{
		{"Display", "Display", false, "", "", true},
		{"Sur;iaj", "Display", false, "", "", false},            // Colemak (issue #29)
		{"Onetwo three", "one two three", false, "", "", false}, // corrected as typed by keys
		{"display", "Display", false, "", "", false},
		{"draft", "draft", false, "old text", "", true},
		{"hello world", " world", true, "hello", "", true},
		{"hello world", "world", true, "Search", "", true}, // a placeholder that went away
		{"helloworld", " world", true, "hello", "", false},
		{"hello, world", ", world", true, "hello", "", true},
		{"hello world ", "world", true, "hello", "kept", true}, // smart insert's spaces left
		{" world", "world", false, "", "kept", true},
		{" world", "world", false, "", "", false},
	} {
		if got := typedAs(tc.v, tc.text, tc.appended, &tc.prior, tc.spaces); got != tc.want {
			t.Errorf("typedAs(%q, %q, appended %v, prior %q, %q) = %v, want %v", tc.v, tc.text, tc.appended, tc.prior, tc.spaces, got, tc.want)
		}
	}
}

// Without the old value, appended text is looked for alone.
func TestTypedAsWithoutPrior(t *testing.T) {
	if !typedAs("old text and new", " and new", true, nil, "") || typedAs("old text", " and new", true, nil, "") {
		t.Fatal("appended text without the old value")
	}
}

func TestSpaceProbe(t *testing.T) {
	for _, tc := range []struct{ before, after, want string }{
		{"one", "onex y", "x y"},
		{"one", "onexy", "xy"},
		{"", "X y", "x y"},     // capitalized at the start of a field
		{"Search", "xy", "xy"}, // a placeholder that went away
		{"Search", "x y", "x y"},
		{"one two", "one x ytwo", "x y"}, // the cursor in the middle
		{"one", "one", ""},
		{"one", "onex", ""},
		{"one", "one特惠", ""},
	} {
		if got := spaceProbe(tc.before, tc.after); got != tc.want {
			t.Errorf("spaceProbe(%q, %q) = %q, want %q", tc.before, tc.after, got, tc.want)
		}
	}
}

func TestHiddenText(t *testing.T) {
	for v, want := range map[string]bool{"•••••": true, "●●●": true, "": false, "pass": false, "••a": false} {
		if got := hiddenText(v); got != want {
			t.Errorf("hiddenText(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestLayoutError(t *testing.T) {
	err := layoutError(keyboardProbe{kind: kbLayout, key: ";", got: "o"}, fmt.Errorf("pasteboard: %w", context.DeadlineExceeded))
	msg := err.Error()
	if !errors.Is(err, driver.ErrUnsupported) || !strings.Contains(msg, "did not answer") || !strings.Contains(msg, "not U.S.") ||
		!strings.Contains(msg, `typed "o"`) || !strings.Contains(msg, "Nothing was typed") {
		t.Fatalf("with a pasteboard that did not answer: %v", err)
	}
	if msg := layoutError(keyboardProbe{kind: kbLayout, key: "a", got: "q"}, nil).Error(); !strings.Contains(msg, "could not use the phone's pasteboard") {
		t.Fatalf("without the pasteboard: %v", msg)
	}
}

// A field that reads empty or as before after typing is an error, not a
// note: the text most likely went nowhere (GitHub issue #65). Only a field
// that hides its text leaves a note.
func TestJudgeTyped(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		name     string
		v, text  string
		appended bool
		prior    *string
		checked  string
		note     bool
		err      string
	}{
		{"typed", "闲鱼", "闲鱼", false, str(""), "same", false, ""},
		{"empty after", "", "闲鱼", false, str(""), "unchanged", false, "did not go in"},
		{"empty, prior unread", "", "闲鱼", false, nil, "unchanged", false, "did not go in"},
		{"appended, as before", "abc", "def", true, str("abc"), "unchanged", false, "did not go in"},
		{"replaced, as before", "abc", "def", false, str("abc"), "different", false, "reads otherwise"},
		{"hidden", "•••", "abc", false, str(""), "hidden", true, ""},
		{"cut", "ab", "abc", false, str(""), "different", false, "2 characters where 3"},
	} {
		checked, note, err := judgeTyped(tc.v, tc.text, tc.appended, tc.prior, "paste", nil, "")
		if checked != tc.checked || (note != "") != tc.note {
			t.Errorf("%s: checked %q, note %q", tc.name, checked, note)
		}
		if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
			t.Errorf("%s: err %v, want one with %q", tc.name, err, tc.err)
		}
	}
	if !strings.Contains(errUnfocused.Error(), "no field has focus") {
		t.Error(errUnfocused)
	}
}
