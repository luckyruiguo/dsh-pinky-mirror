package ios

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

func TestStrokeRune(t *testing.T) {
	for _, pin := range []string{"0123456789", "abcXYZ", "p@ss-w0rd!"} {
		strokes, err := pinStrokes(pin)
		if err != nil {
			t.Fatal(err)
		}
		var got []rune
		for _, k := range strokes {
			r, ok := strokeRune(k)
			if !ok {
				t.Fatalf("%q: no rune for %+v", pin, k)
			}
			got = append(got, r)
		}
		if string(got) != pin {
			t.Fatalf("round trip %q -> %q", pin, string(got))
		}
	}
	if r, ok := strokeRune(keyStroke{usage: keyEnter}); !ok || r != '\n' {
		t.Fatalf("enter: %q %v", r, ok)
	}
}

// lockScreen is SpringBoard's tree with the passcode pad: ten keys whose
// labels start with the digit, as on iOS ("2, ABC").
func lockScreen() string {
	var keys []string
	for i, d := range []string{"1", "2, ABC", "3, DEF", "4, GHI", "5, JKL", "6, MNO", "7, PQRS", "8, TUV", "9, WXYZ", "0"} {
		col, row := i%3, i/3
		if d == "0" {
			col, row = 1, 3
		}
		keys = append(keys, fmt.Sprintf(`{"type":"Button","label":%q,"isVisible":"1","isAccessible":"1","rect":{"x":%d,"y":%d,"width":78,"height":78}}`, d, 40+col*110, 300+row*100))
	}
	return `{"type":"Application","label":" ","isVisible":"1","rect":{"x":0,"y":0,"width":375,"height":812},"children":[` +
		`{"type":"StaticText","label":"Enter Passcode","isVisible":"1","isAccessible":"1","rect":{"x":100,"y":200,"width":175,"height":24}},` +
		strings.Join(keys, ",") + `]}`
}

// fakeLockedWDA is WDA on a locked phone whose passcode is pin.
type fakeLockedWDA struct {
	pin string

	mu      sync.Mutex
	locked  bool
	padUp   bool
	typed   string
	homes   int
	keysTyp []string
}

func (f *fakeLockedWDA) key(x, y float64) string {
	tree := lockScreen()
	var root wdaNode
	json.Unmarshal([]byte(tree), &root)
	for _, k := range root.Children {
		r := k.Rect
		if k.Type == "Button" && x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height {
			return strOr(k.Label)[:1]
		}
	}
	return ""
}

func (f *fakeLockedWDA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	path := r.URL.Path
	if strings.HasPrefix(path, "/session/s1") {
		path = strings.TrimPrefix(path, "/session/s1")
	}
	switch r.Method + " " + path {
	case "GET /status":
		fmt.Fprint(w, `{"value":{"ready":true}}`)
	case "POST /session":
		fmt.Fprint(w, `{"value":{"sessionId":"s1"},"sessionId":"s1"}`)
	case "POST /appium/settings":
		fmt.Fprint(w, `{"value":null}`)
	case "GET /wda/locked":
		fmt.Fprintf(w, `{"value":%v}`, f.locked)
	case "POST /wda/homescreen":
		// As on a phone: it also brings SpringBoard to the front, which
		// iOS refuses while locked.
		if f.locked {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"value":{"error":"unknown error","message":"Unable to launch (null) because the device was not, or could not be, unlocked."}}`)
			return
		}
		fmt.Fprint(w, `{"value":null}`)
	case "POST /wda/pressButton":
		f.homes++
		fmt.Fprint(w, `{"value":null}`)
	case "GET /source":
		if f.padUp {
			fmt.Fprintf(w, `{"value":%s}`, lockScreen())
		} else {
			fmt.Fprint(w, `{"value":{"type":"Application","isVisible":"1","rect":{"x":0,"y":0,"width":375,"height":812}}}`)
		}
	case "POST /actions":
		var a struct {
			Actions []struct {
				Actions []map[string]any `json:"actions"`
			} `json:"actions"`
		}
		json.Unmarshal(b, &a)
		acts := a.Actions[0].Actions
		moves := 0
		for _, x := range acts {
			if x["type"] == "pointerMove" {
				moves++
			}
		}
		if moves > 1 {
			f.padUp = true // the swipe up
		} else if f.locked {
			f.typed += f.key(acts[0]["x"].(float64), acts[0]["y"].(float64))
			if f.typed == f.pin {
				f.locked = false
			}
		}
		fmt.Fprint(w, `{"value":null}`)
	case "POST /wda/keys":
		var k struct{ Value []string }
		json.Unmarshal(b, &k)
		f.keysTyp = append(f.keysTyp, k.Value...)
		fmt.Fprint(w, `{"value":null}`)
	default:
		w.WriteHeader(404)
		fmt.Fprintf(w, `{"value":{"error":"unknown command","message":%q}}`, path)
	}
}

func TestWDAUnlock(t *testing.T) {
	f := &fakeLockedWDA{pin: "123456", locked: true}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), scale: 3, width: 1125, height: 2436}
	if err := d.Unlock(context.Background(), "123456"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.typed != "123456" || f.homes != 1 || len(f.keysTyp) != 0 {
		t.Fatalf("typed %q on the pad, %d Home presses, keys %v", f.typed, f.homes, f.keysTyp)
	}
}

func TestWDAUnlockWrongPIN(t *testing.T) {
	f := &fakeLockedWDA{pin: "123456", locked: true}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), scale: 3, width: 1125, height: 2436}
	err := d.Unlock(context.Background(), "654321")
	if err == nil || !strings.Contains(err.Error(), "still locked") {
		t.Fatalf("wrong PIN: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.typed != "654321" {
		t.Fatalf("typed %q: the passcode must be typed once, never again", f.typed)
	}
}

func TestWDAUnlockAlreadyUnlocked(t *testing.T) {
	f := &fakeLockedWDA{pin: "123456"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), scale: 3, width: 1125, height: 2436}
	if err := d.Unlock(context.Background(), "123456"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.homes != 0 || f.typed != "" {
		t.Fatalf("an unlocked phone got %d Home presses and %q", f.homes, f.typed)
	}
}

func TestPadKeys(t *testing.T) {
	var root wdaNode
	if err := json.Unmarshal([]byte(lockScreen()), &root); err != nil {
		t.Fatal(err)
	}
	els := flattenWDA(&root)
	if !passcodePad(els) {
		t.Fatal("pad not found")
	}
	keys, secure := padKeys(els, 3)
	if secure || len(keys) != 10 || keys['5'] != (screen.Point{X: 3 * (40 + 110 + 39), Y: 3 * (300 + 100 + 39)}) {
		t.Fatalf("keys %v, secure %v", keys, secure)
	}
}
