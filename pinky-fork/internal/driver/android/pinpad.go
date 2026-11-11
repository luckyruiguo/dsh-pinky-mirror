package android

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// PIN pads that AOSP's resource ids do not name: OEM skins and SystemUI's
// Compose bouncer. Such a pad is read from what it shows: ten keys, each
// labelled with one digit in its text or its description ("2", "2 ABC",
// "2\nABC", or the word "Two" as on vivo's pads), laid out like a phone
// keypad (three evenly spaced columns, the digits 1 to 9 in three rows and
// one key below the middle column). The digits' order is not checked,
// since some lock screens scramble it. A key without a label (ColorOS
// leaves the 0 key's description out) may take its digit from its
// resource id (key0, VivoPinkey0), but only when every key with both a
// label and such an id agrees with it: LineageOS's scrambled pad keeps
// the ids in place and moves the labels.
//
// Pinky taps those keys only when it reads exactly one such pad, all of it
// on one window and none of it covered by another window, and the same in
// two reads in a row. A lock screen it cannot read that well gets nothing:
// a wrong entry costs one of the phone's attempts.

// pinPad is a PIN pad read from the screen.
type pinPad struct {
	keys    [10]screen.Rect // each digit's key, by digit
	enabled bool            // every key takes input
	// confirm is the key that submits the PIN (OK, Enter, a check mark),
	// empty when the pad shows none: such pads take the PIN by themselves.
	confirm        screen.Rect
	confirmEnabled bool
}

// same reports whether q has its keys where p has them, within a few
// pixels: the pad is not moving.
func (p *pinPad) same(q *pinPad) bool {
	if p == nil || q == nil {
		return false
	}
	for d := range p.keys {
		a, b := p.keys[d].Center(), q.keys[d].Center()
		if abs(a.X-b.X) > 3 || abs(a.Y-b.Y) > 3 {
			return false
		}
	}
	return true
}

// Reading a pad.
const (
	// maxPadCombos bounds how many ways of picking one place per digit are
	// tried when the screen shows some digits more than once (a clock, a
	// calendar, a notification count).
	maxPadCombos = 4096
	minPINDigits = 4 // the shortest PIN Android takes
)

// padKey is one place on the screen that shows or names a digit: a node
// and the nodes nested in it or around it, such as a key and its label.
type padKey struct {
	area      screen.Rect // all the nodes together
	rect      screen.Rect // the key: its smallest clickable node, else its smallest node
	clickable bool        // rect is a clickable node's
	enabled   bool        // every node is enabled
	win       int
	above     []screen.Rect
	// label is the digit the nodes' labels show and id the one their
	// resource ids name: -1 for none, -2 when they disagree.
	label, id int
}

// addPadKey adds n to the place it is nested in or around in keys, or as
// a new place. Keys that merely touch or overlap at their edges stay
// apart.
func addPadKey(keys []padKey, n *lockNode, label, id int) []padKey {
	merge := func(a, b int) int {
		switch {
		case a == -1:
			return b
		case b == -1 || a == b:
			return a
		}
		return -2
	}
	for i := range keys {
		k := &keys[i]
		if k.win != n.win || !k.area.Contains(n.bounds.Center()) && !n.bounds.Contains(k.rect.Center()) {
			continue
		}
		k.area = union(k.area, n.bounds)
		k.enabled = k.enabled && n.enabled
		k.label, k.id = merge(k.label, label), merge(k.id, id)
		smaller := area(n.bounds) < area(k.rect)
		if n.clickable && (!k.clickable || smaller) || !n.clickable && !k.clickable && smaller {
			k.rect, k.clickable = n.bounds, n.clickable
		}
		return keys
	}
	return append(keys, padKey{area: n.bounds, rect: n.bounds, clickable: n.clickable,
		enabled: n.enabled, win: n.win, above: n.above, label: label, id: id})
}

// findPinPad looks among the lock screen's nodes for a PIN pad without
// AOSP's ids. It returns the pad when exactly one set of ten places, one
// per digit, forms a keypad. unsure means the screen shows every digit but
// not as exactly one keypad (or in too many places to tell), so a pad may
// be showing that Pinky cannot read well enough to tap.
func findPinPad(nodes []lockNode) (pad *pinPad, unsure bool) {
	var all []padKey
	for i := range nodes {
		n := &nodes[i]
		if !isSystemUI(n.pkg) || n.password || n.bounds.Empty() {
			continue
		}
		if label, id := nodeDigit(n), idDigit(n.id); label != -1 || id >= 0 {
			all = addPadKey(all, n, label, id)
		}
	}
	// Resource ids may stand in for a missing label only when the keys
	// that have both agree. A place whose labels disagree may be the key
	// that is missing.
	agree, disagree := 0, 0
	conflict := false
	for _, k := range all {
		conflict = conflict || k.label == -2
		if k.label >= 0 && k.id >= 0 {
			if k.label == k.id {
				agree++
			} else {
				disagree++
			}
		}
	}
	trustIDs := disagree == 0 && agree >= 8
	var places [10][]padKey
	for _, k := range all {
		d := k.label
		if d == -1 && trustIDs {
			d = k.id
		}
		if d >= 0 {
			places[d] = append(places[d], k)
		}
	}
	for d := range places {
		if len(places[d]) == 0 {
			return nil, conflict
		}
	}
	combos := 1
	for d := range places {
		if combos *= len(places[d]); combos > maxPadCombos {
			return nil, true
		}
	}
	var idx [10]int
	var pick, found [10]*padKey
	var grid gridInfo
	valid := 0
	for {
		for d := range pick {
			pick[d] = &places[d][idx[d]]
		}
		if g, ok := keypadGrid(pick); ok {
			if valid++; valid > 1 {
				return nil, true
			}
			found, grid = pick, g
		}
		d := 0
		for ; d < len(idx); d++ {
			if idx[d]++; idx[d] < len(places[d]) {
				break
			}
			idx[d] = 0
		}
		if d == len(idx) {
			break
		}
	}
	if valid == 0 {
		return nil, true
	}
	pad = &pinPad{enabled: true}
	for d, k := range found {
		pad.keys[d] = k.rect
		pad.enabled = pad.enabled && k.enabled
	}
	pad.confirm, pad.confirmEnabled = findConfirm(nodes, found, grid)
	return pad, false
}

// gridInfo is where a keypad's columns and rows are.
type gridInfo struct {
	cols               [3]int // column centers, left to right
	rows               [4]int // row centers, top to bottom
	colPitch, rowPitch int
}

// keypadGrid checks that ten places, one per digit, sit like a phone
// keypad's keys: three evenly spaced columns and four evenly spaced rows,
// three keys in each of the first three rows and one below the middle
// column; no key larger than its cell, none over another's center, all on
// one window and none under a window above it.
func keypadGrid(keys [10]*padKey) (gridInfo, bool) {
	var g gridInfo
	var xs, ys [10]int
	for i, k := range keys {
		if k.win != keys[0].win || k.rect.X < 0 || k.rect.Y < 0 {
			return g, false
		}
		c := k.rect.Center()
		xs[i], ys[i] = c.X, c.Y
	}
	colOf, cols, ok := axisGroups(xs[:], 3)
	if !ok {
		return g, false
	}
	rowOf, rows, ok := axisGroups(ys[:], 4)
	if !ok {
		return g, false
	}
	var cell [4][3]bool
	for i := range keys {
		r, c := rowOf[i], colOf[i]
		if cell[r][c] || r == 3 && c != 1 {
			return g, false
		}
		cell[r][c] = true
	}
	copy(g.cols[:], cols)
	copy(g.rows[:], rows)
	g.colPitch = (cols[2] - cols[0]) / 2
	g.rowPitch = (rows[3] - rows[0]) / 3
	if g.colPitch <= 0 || g.rowPitch <= 0 {
		return g, false
	}
	for i, k := range keys {
		if k.rect.W > g.colPitch*6/5 || k.rect.H > g.rowPitch*6/5 {
			return g, false
		}
		c := k.rect.Center()
		for j, o := range keys {
			if i != j && o.rect.Contains(c) {
				return g, false
			}
		}
		if covered(c, k.above) {
			return g, false
		}
	}
	return g, true
}

// axisGroups sorts positions along one axis into n groups, split where
// neighbours are farther apart than a quarter of the expected pitch. Each
// group must be tight and the groups evenly spaced. It returns each
// position's group and the groups' centers.
func axisGroups(pos []int, n int) (of []int, centers []int, ok bool) {
	sorted := append([]int(nil), pos...)
	sort.Ints(sorted)
	span := sorted[len(sorted)-1] - sorted[0]
	if span <= 0 {
		return nil, nil, false
	}
	tol := span / (4 * (n - 1))
	var last []int // the largest position of each group
	start := 0
	for i := 1; i <= len(sorted); i++ {
		if i < len(sorted) && sorted[i]-sorted[i-1] <= tol {
			continue
		}
		if sorted[i-1]-sorted[start] > tol {
			return nil, nil, false
		}
		sum := 0
		for _, v := range sorted[start:i] {
			sum += v
		}
		centers = append(centers, sum/(i-start))
		last = append(last, sorted[i-1])
		start = i
	}
	if len(centers) != n {
		return nil, nil, false
	}
	pitch := float64(centers[n-1]-centers[0]) / float64(n-1)
	for i := 1; i < n; i++ {
		gap := float64(centers[i] - centers[i-1])
		if gap < 0.8*pitch || gap > 1.2*pitch {
			return nil, nil, false
		}
	}
	of = make([]int, len(pos))
	for i, v := range pos {
		for of[i] < n-1 && v > last[of[i]] {
			of[i]++
		}
	}
	return of, centers, true
}

// findConfirm finds the pad's confirm key: one place labelled OK, Enter,
// a check mark or the like (or with such a resource id), in the pad's
// bottom row or just below it, on the pad's window. It returns an empty
// rectangle when there is none, or more than one.
func findConfirm(nodes []lockNode, keys [10]*padKey, g gridInfo) (screen.Rect, bool) {
	var places []padKey
	for i := range nodes {
		n := &nodes[i]
		if n.win != keys[0].win || !isSystemUI(n.pkg) || n.password || n.bounds.Empty() ||
			nodeDigit(n) != -1 || !isConfirmKey(n) {
			continue
		}
		c := n.bounds.Center()
		if c.X < g.cols[0]-g.colPitch || c.X > g.cols[2]+g.colPitch ||
			c.Y < g.rows[3]-g.rowPitch/2 || c.Y > g.rows[3]+g.rowPitch*3/2 {
			continue
		}
		places = addPadKey(places, n, -1, -1)
	}
	if len(places) != 1 {
		return screen.Rect{}, false
	}
	k := places[0]
	c := k.rect.Center()
	for _, d := range keys {
		if d.rect.Contains(c) {
			return screen.Rect{}, false
		}
	}
	if covered(c, k.above) {
		return screen.Rect{}, false
	}
	return k.rect, k.enabled
}

// confirmLabels are the labels of confirm keys, lower-cased: AOSP's
// "Enter", OEM pads' "OK" and "确定", and their common translations.
var confirmLabels = map[string]bool{
	"ok": true, "enter": true, "done": true, "confirm": true, "go": true, "submit": true,
	"✓": true, "✔": true, "☑": true,
	"确定": true, "确认": true, "完成": true, "確定": true, "確認": true, "完了": true, "決定": true,
	"확인": true, "완료": true,
	"ок": true, "ввод": true, "готово": true,
	"eingabe": true, "bestätigen": true, "fertig": true,
	"entrée": true, "valider": true, "confirmer": true,
	"intro": true, "aceptar": true, "confirmar": true, "listo": true,
	"invio": true, "conferma": true, "concluído": true,
}

// confirmWords and notConfirmWords are words of confirm keys' resource
// ids (key_enter, key_enter_text, btn_ok), and of other ids that may have
// them (an "Enter PIN" message, the delete key).
var (
	confirmWords    = map[string]bool{"enter": true, "ok": true, "confirm": true, "done": true, "submit": true}
	notConfirmWords = map[string]bool{"emergency": true, "cancel": true, "delete": true, "del": true,
		"back": true, "backspace": true, "clear": true, "message": true, "hint": true, "title": true,
		"prompt": true, "area": true, "info": true}
)

// isConfirmKey reports whether n is labelled or named as a confirm key.
func isConfirmKey(n *lockNode) bool {
	for _, s := range []string{n.text, n.desc} {
		if confirmLabels[strings.ToLower(strings.TrimSpace(s))] {
			return true
		}
	}
	words := idWords(idName(n.id))
	confirm := false
	for _, w := range words {
		if notConfirmWords[w] {
			return false
		}
		confirm = confirm || confirmWords[w]
	}
	return confirm
}

// idWords splits a resource id's name into lower-case words at
// underscores, digits and camel case: "key_enter" and "keyEnter" are
// [key enter].
func idWords(name string) []string {
	var out []string
	var cur []rune
	prevLower := false
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range name {
		switch {
		case !unicode.IsLetter(r):
			flush()
			prevLower = false
			continue
		case unicode.IsUpper(r) && prevLower:
			flush()
		}
		cur = append(cur, r)
		prevLower = unicode.IsLower(r)
	}
	flush()
	return out
}

// nodeDigit returns the digit a node shows as a key, from its text or its
// description: -1 when it shows none, -2 when the two disagree.
func nodeDigit(n *lockNode) int {
	t, d := labelDigit(n.text), labelDigit(n.desc)
	switch {
	case t >= 0 && d >= 0 && t != d:
		return -2
	case t >= 0:
		return t
	}
	return d
}

// labelDigit returns the digit a key's label shows (keyDigit), or the
// digit an English word names ("Five", vivo's keys' descriptions), or -1.
func labelDigit(s string) int {
	if d := keyDigit(s); d >= 0 {
		return d
	}
	return digitWords[strings.ToLower(strings.TrimSpace(s))] - 1
}

var digitWords = map[string]int{"zero": 1, "one": 2, "two": 3, "three": 4, "four": 5,
	"five": 6, "six": 7, "seven": 8, "eight": 9, "nine": 10}

// idDigit returns the digit a digit key's resource id ends with (key5,
// VivoPinkey5, digit_5, num5), or -1.
func idDigit(id string) int {
	name := strings.ToLower(idName(id))
	if name == "" {
		return -1
	}
	last := name[len(name)-1]
	rest := strings.TrimRight(name[:len(name)-1], "_")
	if last < '0' || last > '9' || rest == "" || rest[len(rest)-1] >= '0' && rest[len(rest)-1] <= '9' {
		return -1
	}
	for _, w := range []string{"key", "digit", "num", "btn", "button"} {
		if strings.HasSuffix(rest, w) {
			return int(last - '0')
		}
	}
	return -1
}

// keyDigit returns the digit of a keypad key's label: one digit, alone or
// followed by its keypad letters ("2", "2 ABC", "2\nABC", "2, ABC",
// "0 +"); -1 for anything else, such as "10:30", "12" or "5 min ago".
// Digits of other scripts count (Arabic-Indic digits on Arabic phones).
func keyDigit(s string) int {
	s = strings.TrimSpace(s)
	r, size := utf8.DecodeRuneInString(s)
	d := digitValue(r)
	if d < 0 {
		return -1
	}
	letters := 0
	for _, r := range s[size:] {
		switch {
		case unicode.IsSpace(r) || strings.ContainsRune(",.·+", r):
		case unicode.IsLetter(r):
			if letters++; letters > 4 {
				return -1
			}
		default:
			return -1
		}
	}
	return d
}

// digitZeros are the zeros of the decimal digits phones show: ASCII,
// Arabic-Indic, Extended Arabic-Indic (Persian, Urdu), Devanagari,
// Bengali, Thai, Myanmar and full-width digits.
var digitZeros = []rune{'0', '\u0660', '\u06f0', '\u0966', '\u09e6', '\u0e50', '\u1040', '\uff10'}

func digitValue(r rune) int {
	for _, z := range digitZeros {
		if r >= z && r <= z+9 {
			return int(r - z)
		}
	}
	return -1
}

// covered reports whether p is under one of the rectangles that windows
// above cover.
func covered(p screen.Point, above []screen.Rect) bool {
	for _, a := range above {
		if a.Contains(p) {
			return true
		}
	}
	return false
}

func intersects(a, b screen.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

func union(a, b screen.Rect) screen.Rect {
	x0, y0 := min(a.X, b.X), min(a.Y, b.Y)
	x1, y1 := max(a.X+a.W, b.X+b.W), max(a.Y+a.H, b.Y+b.H)
	return screen.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

func area(r screen.Rect) int { return r.W * r.H }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Entering a PIN by taps.
const (
	pinTapGap  = 150 * time.Millisecond // between the PIN's digits
	pinTapHold = 40 * time.Millisecond  // how long each key is held
	// autoAccept is how long a pad gets to take the PIN by itself before
	// Pinky taps its confirm key (after checking the phone is still
	// locked).
	autoAccept = 600 * time.Millisecond
)

// tapPIN enters pin on a pad read from the screen by tapping its keys,
// then, if the phone is still locked, taps the pad's confirm key when one
// shows. It returns nil once the lock screen is gone and wraps
// driver.ErrLocked otherwise, also when it stopped partway: the digits
// already entered would spoil another try.
func (d *Device) tapPIN(ctx context.Context, pad *pinPad, pin string) error {
	d.cfg.logf("android: %s: entering the PIN on a PIN pad read from its keys' labels (confirm key: %v)",
		d.info.ID, !pad.confirm.Empty())
	partly := func(err error) error {
		return fmt.Errorf("android: entering the PIN stopped partway (%v); not retried: %w", err, driver.ErrLocked)
	}
	for i := 0; i < len(pin); i++ {
		if i >= minPINDigits {
			// A pad that checks the PIN as it is typed may have taken it:
			// the rest would land on the screen behind the lock screen.
			locked, err := d.Locked(ctx)
			if err != nil {
				return partly(err)
			}
			if !locked {
				d.markAwake()
				d.cfg.logf("android: %s: unlocked before the last digit", d.info.ID)
				return nil
			}
		}
		if i > 0 {
			if err := sleepCtx(ctx, pinTapGap); err != nil {
				return partly(err)
			}
		}
		p := pad.keys[pin[i]-'0'].Center()
		if err := d.withInput(ctx, false, func(l *live) error { return l.tap(ctx, p, pinTapHold) }); err != nil {
			if i == 0 {
				return err
			}
			return partly(err)
		}
	}
	// From here on the whole PIN is in: an error must not lead to a retry.
	unknown := func(err error) error {
		return fmt.Errorf("android: cannot tell whether the phone took the PIN (%v); not retried: %w", err, driver.ErrLocked)
	}
	if unlocked, err := d.waitUnlocked(ctx, autoAccept); err != nil || unlocked {
		if err != nil {
			return unknown(err)
		}
		return nil
	}
	// Tap the confirm key only on the same pad, read again: it may show
	// only once the PIN is long enough, and a pad that moved or went is
	// not the one the PIN went to.
	b, err := d.readBouncer(ctx)
	switch {
	case err != nil:
		if ctx.Err() != nil {
			return unknown(ctx.Err())
		}
		d.cfg.logf("android: %s: reading the PIN pad again: %v", d.info.ID, err)
	case b.pad == nil || !b.pad.same(pad):
		d.cfg.logf("android: %s: the PIN pad changed after the PIN; not confirming", d.info.ID)
	case b.pad.confirm.Empty() || !b.pad.confirmEnabled:
		// A pad without a confirm key takes the PIN by itself.
	default:
		locked, err := d.Locked(ctx)
		if err == nil && !locked {
			d.markAwake()
			return nil
		}
		c := b.pad.confirm.Center()
		if err := d.withInput(ctx, false, func(l *live) error { return l.tap(ctx, c, pinTapHold) }); err != nil {
			return unknown(err)
		}
	}
	unlocked, err := d.waitUnlocked(ctx, unlockVerify)
	if err != nil {
		return unknown(err)
	}
	if unlocked {
		return nil
	}
	d.cfg.logf("android: %s: still locked after entering the PIN", d.info.ID)
	return fmt.Errorf("android: the phone is still locked after the PIN was entered (wrong PIN?); not retried: %w", driver.ErrLocked)
}

// waitUnlocked polls the lock screen for up to max and reports whether it
// went away.
func (d *Device) waitUnlocked(ctx context.Context, max time.Duration) (bool, error) {
	deadline := time.Now().Add(max)
	for {
		if err := sleepCtx(ctx, lockPoll); err != nil {
			return false, err
		}
		locked, err := d.Locked(ctx)
		if err == nil && !locked {
			d.markAwake()
			d.cfg.logf("android: %s: unlocked", d.info.ID)
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, err
		}
	}
}
