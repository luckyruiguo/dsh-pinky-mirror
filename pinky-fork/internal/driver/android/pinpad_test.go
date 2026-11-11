package android

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/screen"
)

// Lock screen dumps in testdata/keyguard: real-* were taken from our
// emulators, synthetic-* were made up from what is known of each OEM lock
// screen (each file says what it is based on). See the README there.

func keyguardNodes(t *testing.T, name string) []lockNode {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "keyguard", name))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(name, ".json") {
		var tr companion.Tree
		if err := json.Unmarshal(b, &tr); err != nil {
			t.Fatal(err)
		}
		return companionLockNodes(&tr)
	}
	tr, err := parseUIDump(b)
	if err != nil {
		t.Fatal(err)
	}
	var nodes []lockNode
	for _, r := range tr.Roots {
		nodes = appendUINodes(nodes, r)
	}
	return nodes
}

// asOEM drops AOSP's resource ids and focus, as readBouncer does for
// padOnlyDevice: the pad must then be read from its labels and layout.
func asOEM(nodes []lockNode) []lockNode {
	out := append([]lockNode(nil), nodes...)
	for i := range out {
		out[i].id, out[i].focused = "", false
	}
	return out
}

// checkPadKeys checks that each digit's tap point is on a node of that
// digit: labelled with it, or (for keys without a label) named by its id.
func checkPadKeys(t *testing.T, nodes []lockNode, pad *pinPad) {
	t.Helper()
	for d, k := range pad.keys {
		p := k.Center()
		ok := false
		for i := range nodes {
			n := &nodes[i]
			if n.bounds.Contains(p) && (nodeDigit(n) == d || nodeDigit(n) == -1 && idDigit(n.id) == d) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("digit %d: the tap at %v is on no key of that digit", d, p)
		}
	}
}

func TestPinPadDumps(t *testing.T) {
	type want struct {
		kind    bouncerKind
		pad     bool // taps on a pad read from its labels
		confirm string
		unsure  bool
	}
	cases := []struct {
		file string
		oem  bool // read as an OEM pad (asOEM)
		want want
	}{
		{"real-aosp10-emulator-pin.xml", false, want{kind: bouncerPIN}},
		{"real-aosp10-emulator-pin.xml", true, want{kind: bouncerPIN, pad: true, confirm: "Enter"}},
		{"real-aosp10-emulator-pin.companion.json", false, want{kind: bouncerPIN}},
		{"real-aosp10-emulator-pin.companion.json", true, want{kind: bouncerPIN, pad: true, confirm: "Enter"}},
		{"real-aosp10-emulator-lockscreen.xml", false, want{kind: bouncerNone}},
		{"real-aosp10-emulator-lockscreen.xml", true, want{kind: bouncerNone}},
		{"real-aosp16-emulator-pin.xml", false, want{kind: bouncerPIN}},
		{"real-aosp16-emulator-pin.xml", true, want{kind: bouncerPIN, pad: true, confirm: "Enter"}},
		{"real-aosp17-emulator-pin.xml", false, want{kind: bouncerPIN}},
		{"real-aosp17-emulator-pin.xml", true, want{kind: bouncerPIN, pad: true, confirm: "Enter"}},
		{"real-aosp17-emulator-lockscreen.xml", false, want{kind: bouncerNone}},
		{"real-aosp17-emulator-lockscreen.xml", true, want{kind: bouncerNone}},
		{"synthetic-miui14-pin.xml", false, want{kind: bouncerPIN, pad: true}},
		{"synthetic-oneui6-pin.xml", false, want{kind: bouncerPIN, pad: true, confirm: "OK"}},
		{"synthetic-coloros13-pin.xml", false, want{kind: bouncerPIN, pad: true}},
		{"synthetic-funtouchos-pin.xml", false, want{kind: bouncerPIN, pad: true}},
		{"synthetic-emui10-pin.xml", false, want{kind: bouncerPIN}},
		{"synthetic-magicos7-pin.xml", false, want{kind: bouncerPIN, pad: true}},
		{"synthetic-aosp16-compose-pin.xml", false, want{kind: bouncerPIN, pad: true, confirm: "Enter"}},
		{"synthetic-lineage-scrambled-pin.xml", false, want{kind: bouncerPIN}},
		{"synthetic-lineage-scrambled-pin.xml", true, want{kind: bouncerPIN, pad: true, confirm: "Enter"}},
		{"synthetic-emergency-dialer.xml", false, want{kind: bouncerNone}},
		{"synthetic-originos-fingerprint.xml", false, want{kind: bouncerNone}},
	}
	for _, c := range cases {
		name := c.file
		if c.oem {
			name += " as OEM"
		}
		t.Run(name, func(t *testing.T) {
			nodes := keyguardNodes(t, c.file)
			if c.oem {
				nodes = asOEM(nodes)
			}
			b := classifyBouncer(nodes)
			if b.kind != c.want.kind || (b.pad != nil) != c.want.pad || b.unsure != c.want.unsure {
				t.Fatalf("classifyBouncer = %v, pad %v, unsure %v; want %+v", b.kind, b.pad != nil, b.unsure, c.want)
			}
			if b.kind == bouncerPIN && !b.enabled {
				t.Error("the pad reads as refusing attempts")
			}
			if b.pad == nil {
				return
			}
			checkPadKeys(t, nodes, b.pad)
			label := ""
			if !b.pad.confirm.Empty() {
				p := b.pad.confirm.Center()
				for i := range nodes {
					if n := &nodes[i]; n.bounds.Contains(p) && isConfirmKey(n) {
						label = n.text + n.desc
					}
				}
				if label == "" {
					t.Errorf("the confirm tap at %v is on no confirm key", p)
				}
			}
			if label != c.want.confirm {
				t.Errorf("confirm key %q, want %q", label, c.want.confirm)
			}
		})
	}
}

// TestPinPadRealPositions pins the taps on the Android 10 emulator's pad.
func TestPinPadRealPositions(t *testing.T) {
	b := classifyBouncer(asOEM(keyguardNodes(t, "real-aosp10-emulator-pin.xml")))
	if b.pad == nil {
		t.Fatal("no pad")
	}
	want := map[int]screen.Point{1: {X: 243, Y: 952}, 5: {X: 539, Y: 1164}, 9: {X: 836, Y: 1376}, 0: {X: 539, Y: 1588}}
	for d, p := range want {
		if got := b.pad.keys[d].Center(); got != p {
			t.Errorf("digit %d at %v, want %v", d, got, p)
		}
	}
	if got := b.pad.confirm.Center(); got != (screen.Point{X: 836, Y: 1588}) {
		t.Errorf("Enter at %v", got)
	}
}

// TestPinPadScrambled reads LineageOS's scrambled pad by its labels, also
// when its positional ids are there to mislead.
func TestPinPadScrambled(t *testing.T) {
	nodes := keyguardNodes(t, "synthetic-lineage-scrambled-pin.xml")
	// Keep the keys' ids but lose the PIN field, so the pad is read from
	// its keys.
	for i := range nodes {
		if idName(nodes[i].id) == "pinEntry" {
			nodes[i].id, nodes[i].focused = "", false
		}
	}
	b := classifyBouncer(nodes)
	if b.pad == nil {
		t.Fatalf("no pad: %+v", b)
	}
	checkPadKeys(t, nodes, b.pad)
	// The top left key shows 7, the bottom middle one 4.
	if c := b.pad.keys[7].Center(); c.X > 300 || c.Y > 1520 {
		t.Errorf("7 at %v, want the top left key", c)
	}
	if c := b.pad.keys[4].Center(); c.X < 400 || c.X > 680 || c.Y < 1940 {
		t.Errorf("4 at %v, want the bottom middle key", c)
	}
}

// TestPinPadColorOSZero takes the 0 key's digit from its id, which agrees
// with the other keys' labels; ids that disagree are not used.
func TestPinPadColorOSZero(t *testing.T) {
	nodes := keyguardNodes(t, "synthetic-coloros13-pin.xml")
	if b := classifyBouncer(nodes); b.pad == nil || b.pad.keys[0].Empty() {
		t.Fatalf("no pad: %+v", b)
	}
	// Swap two keys' ids: the ids no longer agree with the labels, and the
	// 0 key has nothing else to go by.
	for i := range nodes {
		switch idName(nodes[i].id) {
		case "key1":
			nodes[i].id = "com.android.systemui:id/key2"
		case "key2":
			nodes[i].id = "com.android.systemui:id/key1"
		}
	}
	if b := classifyBouncer(nodes); b.pad != nil || b.unsure {
		t.Errorf("ids that disagree with the labels: %+v", b)
	}
}

// TestPinPadSafety covers lock screens Pinky must not tap.
func TestPinPadSafety(t *testing.T) {
	miui := keyguardNodes(t, "synthetic-miui14-pin.xml")
	pad := classifyBouncer(miui).pad
	if pad == nil {
		t.Fatal("no MIUI pad")
	}
	key := func(nodes []lockNode, text string) *lockNode {
		for i := range nodes {
			if nodes[i].text == text {
				return &nodes[i]
			}
		}
		t.Fatalf("no %q", text)
		return nil
	}
	clone := func() []lockNode { return append([]lockNode(nil), miui...) }

	t.Run("locked out", func(t *testing.T) {
		nodes := clone()
		for i := range nodes {
			nodes[i].enabled = false
		}
		if b := classifyBouncer(nodes); b.kind != bouncerPIN || b.enabled {
			t.Errorf("%+v", b)
		}
	})
	t.Run("a key under another window", func(t *testing.T) {
		nodes := keyguardNodes(t, "real-aosp10-emulator-pin.companion.json")
		var tr companion.Tree
		b, _ := os.ReadFile(filepath.Join("testdata", "keyguard", "real-aosp10-emulator-pin.companion.json"))
		if err := json.Unmarshal(b, &tr); err != nil {
			t.Fatal(err)
		}
		// An overlay (another app's floating button) over the 5 key.
		tr.Windows = append([]companion.Window{{Type: companion.WindowAccessibilityOverlay, Bounds: companion.Bounds{500, 1100, 600, 1200}}}, tr.Windows...)
		nodes = asOEM(companionLockNodes(&tr))
		if b := classifyBouncer(nodes); b.pad != nil || !b.unsure {
			t.Errorf("%+v", b)
		}
	})
	t.Run("two pads", func(t *testing.T) {
		nodes := clone()
		for _, n := range miui {
			if nodeDigit(&n) >= 0 {
				n.bounds.Y -= 1150
				nodes = append(nodes, n)
			}
		}
		if b := classifyBouncer(nodes); b.pad != nil || !b.unsure {
			t.Errorf("%+v", b)
		}
	})
	t.Run("a calendar with digits", func(t *testing.T) {
		nodes := clone()
		for d := 1; d <= 9; d++ {
			x, y := 60+(d%7)*140, 700+(d/7)*80
			nodes = append(nodes, lockNode{pkg: "com.android.keyguard", text: string(rune('0' + d)), enabled: true,
				bounds: screen.Rect{X: x, Y: y, W: 60, H: 60}})
		}
		b := classifyBouncer(nodes)
		if b.pad == nil || !b.pad.same(pad) {
			t.Errorf("%+v", b)
		}
	})
	t.Run("two rows of five", func(t *testing.T) {
		var nodes []lockNode
		for d := 0; d < 10; d++ {
			nodes = append(nodes, lockNode{pkg: "com.android.systemui", text: string(rune('0' + d)), clickable: true, enabled: true,
				bounds: screen.Rect{X: 40 + (d%5)*200, Y: 1500 + (d/5)*200, W: 180, H: 180}})
		}
		if b := classifyBouncer(nodes); b.pad != nil || !b.unsure {
			t.Errorf("%+v", b)
		}
	})
	t.Run("a key whose text and description disagree", func(t *testing.T) {
		nodes := clone()
		key(nodes, "5").desc = "6"
		if b := classifyBouncer(nodes); b.pad != nil || !b.unsure {
			t.Errorf("%+v", b)
		}
	})
	t.Run("a pad in an app", func(t *testing.T) {
		nodes := clone()
		for i := range nodes {
			nodes[i].pkg = "com.example.calculator"
		}
		if b := classifyBouncer(nodes); b.kind != bouncerNone || b.unsure {
			t.Errorf("%+v", b)
		}
	})
	t.Run("a missing digit", func(t *testing.T) {
		nodes := clone()
		key(nodes, "8").text = ""
		if b := classifyBouncer(nodes); b.kind != bouncerNone || b.unsure {
			t.Errorf("%+v", b)
		}
	})
	t.Run("sliding in", func(t *testing.T) {
		moved, jitter := clone(), clone()
		for i := range moved {
			moved[i].bounds.Y += 40
			jitter[i].bounds.Y += 2
		}
		if pad.same(classifyBouncer(moved).pad) {
			t.Error("a pad 40 px lower is the same")
		}
		if !pad.same(classifyBouncer(jitter).pad) {
			t.Error("a pad 2 px lower is not the same")
		}
	})
}

// TestPinPadLayouts reads pads in other sizes and scripts.
func TestPinPadLayouts(t *testing.T) {
	keypad := func(x0, y0, cw, ch int, label func(d int) string) []lockNode {
		var nodes []lockNode
		order := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, -1, 0, -1}
		for i, d := range order {
			if d < 0 {
				continue
			}
			r := screen.Rect{X: x0 + (i%3)*cw + 10, Y: y0 + (i/3)*ch + 10, W: cw - 20, H: ch - 20}
			nodes = append(nodes, lockNode{pkg: "com.android.systemui", desc: label(d), clickable: true, enabled: true, bounds: r})
		}
		return nodes
	}
	ascii := func(d int) string { return string(rune('0' + d)) }
	t.Run("tablet in landscape", func(t *testing.T) {
		nodes := keypad(1700, 500, 220, 200, ascii)
		if b := classifyBouncer(nodes); b.pad == nil {
			t.Errorf("%+v", b)
		}
	})
	t.Run("Arabic-Indic digits", func(t *testing.T) {
		nodes := keypad(90, 1300, 300, 220, func(d int) string { return string(rune(0x0660 + d)) })
		b := classifyBouncer(nodes)
		if b.pad == nil {
			t.Fatalf("%+v", b)
		}
		checkPadKeys(t, nodes, b.pad)
	})
	t.Run("keys with letters", func(t *testing.T) {
		letters := []string{"+", "", "ABC", "DEF", "GHI", "JKL", "MNO", "PQRS", "TUV", "WXYZ"}
		for _, sep := range []string{" ", "\n", ", "} {
			nodes := keypad(90, 1300, 300, 220, func(d int) string { return strings.TrimSpace(ascii(d) + sep + letters[d]) })
			if b := classifyBouncer(nodes); b.pad == nil {
				t.Errorf("separator %q: %+v", sep, b)
			}
		}
	})
	t.Run("confirm keys", func(t *testing.T) {
		for _, c := range []struct {
			n    lockNode
			want bool
		}{
			{lockNode{text: "OK"}, true},
			{lockNode{text: "确定"}, true},
			{lockNode{desc: "✓"}, true},
			{lockNode{id: "com.android.systemui:id/btn_letter_ok"}, true},
			{lockNode{id: "com.android.systemui:id/key_enter_text"}, true},
			{lockNode{desc: "Done"}, true},
			{lockNode{}, false}, // an icon without a label
			{lockNode{text: "Enter PIN"}, false},
			{lockNode{text: "Emergency call"}, false},
			{lockNode{desc: "Delete"}, false},
			{lockNode{id: "com.android.systemui:id/keyguard_message_area"}, false},
			{lockNode{id: "com.android.systemui:id/emergency_call_button"}, false},
			{lockNode{id: "com.android.systemui:id/enter_pin_hint"}, false},
		} {
			if got := isConfirmKey(&c.n); got != c.want {
				t.Errorf("isConfirmKey(%+v) = %v", c.n, got)
			}
		}
		// Where the OK key may be: in the pad's bottom row or below it, not
		// a dialog's OK above the pad, and only one.
		nodes := keypad(90, 1300, 300, 220, ascii)
		ok := lockNode{pkg: "com.android.systemui", text: "OK", clickable: true, enabled: true,
			bounds: screen.Rect{X: 700, Y: 1970, W: 280, H: 200}}
		dialog := ok
		dialog.bounds.Y = 600
		if b := classifyBouncer(append(append([]lockNode(nil), nodes...), ok, dialog)); b.pad == nil || b.pad.confirm != ok.bounds {
			t.Errorf("OK key: %+v", b.pad)
		}
		other := ok
		other.bounds.X = 100
		if b := classifyBouncer(append(append([]lockNode(nil), nodes...), ok, other)); b.pad == nil || !b.pad.confirm.Empty() {
			t.Errorf("two OK keys: %+v", b.pad)
		}
	})
}

func TestKeyLabels(t *testing.T) {
	for s, want := range map[string]int{
		"1": 1, "2 ABC": 2, "2\nABC": 2, "2, ABC": 2, "7 PQRS": 7, "9WXYZ": 9, "0 +": 0, "0+": 0, " 4 ": 4,
		"٣": 3, "۵": 5, "８": 8, "Five": 5, "zero": 0,
		"": -1, "10": -1, "12": -1, "10:30": -1, "433": -1, "5 min ago": -1, "2 new messages": -1,
		"100%": -1, "7.5": -1, "1 2 3": -1, "Enter": -1, "A1": -1, "1/4": -1, "Sat 7 Nov": -1, "*": -1, "#": -1,
	} {
		if got := labelDigit(s); got != want {
			t.Errorf("labelDigit(%q) = %d, want %d", s, got, want)
		}
	}
	for id, want := range map[string]int{
		"com.android.systemui:id/key5": 5, "com.android.systemui:id/VivoPinkey0": 0, "x:id/digit_7": 7, "x:id/num3": 3,
		"com.android.systemui:id/key_enter": -1, "com.android.systemui:id/digit_text": -1, "com.android.systemui:id/row1": -1,
		"com.android.systemui:id/password1": -1, "x:id/key10": -1, "": -1,
	} {
		if got := idDigit(id); got != want {
			t.Errorf("idDigit(%q) = %d, want %d", id, got, want)
		}
	}
	for name, want := range map[string]string{"key_enter": "key enter", "keyEnter": "key enter", "btn_letter_ok": "btn letter ok", "OKButton": "okbutton"} {
		if got := strings.Join(idWords(name), " "); got != want {
			t.Errorf("idWords(%q) = %q, want %q", name, got, want)
		}
	}
}
