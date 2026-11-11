package android

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/scrcpy"
	"github.com/DIYgod/pinky/internal/screen"
)

// Android key codes and meta states (android.view.KeyEvent).
const (
	keycodeUnknown    = 0 // KEYCODE_UNKNOWN: apps ignore it, but it counts as activity
	keycode0          = 7 // KEYCODE_0; the digits follow in order
	keycodeHome       = 3
	keycodeBack       = 4
	keycodeDpadUp     = 19
	keycodeDpadDown   = 20
	keycodeDpadLeft   = 21
	keycodeDpadRight  = 22
	keycodeA          = 29
	keycodeTab        = 61
	keycodeEnter      = 66
	keycodeDel        = 67
	keycodePageUp     = 92
	keycodePageDown   = 93
	keycodeEscape     = 111
	keycodeForwardDel = 112
	keycodeAppSwitch  = 187
	keycodeWakeup     = 224

	metaCtrlOn     = 0x1000
	metaCtrlLeftOn = 0x2000
)

var keycodes = map[driver.Key]int32{
	driver.KeyBack:          keycodeBack,
	driver.KeyHome:          keycodeHome,
	driver.KeyRecents:       keycodeAppSwitch,
	driver.KeyEnter:         keycodeEnter,
	driver.KeyTab:           keycodeTab,
	driver.KeyDelete:        keycodeDel,
	driver.KeyForwardDelete: keycodeForwardDel,
	driver.KeyEscape:        keycodeEscape,
	driver.KeyDpadUp:        keycodeDpadUp,
	driver.KeyDpadDown:      keycodeDpadDown,
	driver.KeyDpadLeft:      keycodeDpadLeft,
	driver.KeyDpadRight:     keycodeDpadRight,
	driver.KeyPageUp:        keycodePageUp,
	driver.KeyPageDown:      keycodePageDown,
}

// Touch timing.
const (
	tapHold      = 50 * time.Millisecond  // finger down time of a tap
	swipeStep    = 8 * time.Millisecond   // interval between move events
	defaultSwipe = 300 * time.Millisecond // swipe duration when none is given
	awakeTrust   = 10 * time.Second       // how long a screen-on check holds
)

// inputSession returns the session for an action, waking the screen first
// if it is off. Waking uses KEYCODE_WAKEUP, which only ever turns the
// screen on, never off.
func (d *Device) inputSession(ctx context.Context) (*live, error) {
	l, err := d.session(ctx)
	if err != nil {
		return nil, err
	}
	if err := d.wake(ctx, l); err != nil {
		return nil, err
	}
	return l, nil
}

// withInput runs one gesture on the main session, waking the screen first
// when wake is set. A gesture whose session was replaced before it began
// sent nothing, so it goes to the new session.
func (d *Device) withInput(ctx context.Context, wake bool, f func(l *live) error) error {
	for try := 0; ; try++ {
		var l *live
		var err error
		if wake {
			l, err = d.inputSession(ctx)
		} else {
			l, err = d.session(ctx)
		}
		if err == nil {
			err = l.input(func() error { return f(l) })
		}
		if errors.Is(err, errReplaced) && try < 3 {
			continue
		}
		return err
	}
}

func (d *Device) wake(ctx context.Context, l *live) error {
	if d.undark() {
		d.markAwake()
		return nil
	}
	d.stateMu.Lock()
	fresh := time.Since(d.awakeAt) < awakeTrust
	d.stateMu.Unlock()
	if fresh {
		return nil
	}
	on, err := d.ScreenOn(ctx)
	if err != nil {
		if errors.Is(err, driver.ErrOffline) || ctx.Err() != nil {
			return err
		}
		// A failed check should not block the action itself, and an
		// unknown state presses nothing.
		d.cfg.logf("android: %s: screen state: %v", d.info.ID, err)
		return nil
	}
	if !on {
		if err := d.pressWakeup(ctx, l); err != nil {
			return err
		}
	}
	d.markAwake()
	return nil
}

func (d *Device) markAwake() {
	d.stateMu.Lock()
	d.awakeAt = time.Now()
	d.stateMu.Unlock()
}

// longPressHold returns how long to hold a long press: the phone's
// long-press timeout plus a margin, at least 800 ms.
func (d *Device) longPressHold(ctx context.Context) time.Duration {
	d.stateMu.Lock()
	lp := d.longPress
	d.stateMu.Unlock()
	if lp == 0 {
		lp = 400 * time.Millisecond
		if out, err := d.client.Shell(ctx, d.info.ID, "settings get secure long_press_timeout"); err == nil {
			if ms, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && ms > 0 {
				lp = time.Duration(ms) * time.Millisecond
			}
		}
		d.stateMu.Lock()
		d.longPress = lp
		d.stateMu.Unlock()
	}
	return max(lp+400*time.Millisecond, 800*time.Millisecond)
}

// Tap implements driver.Device.
func (d *Device) Tap(ctx context.Context, p screen.Point, opt driver.TapOptions) error {
	d.used()
	hold := tapHold
	if opt.LongPress {
		hold = d.longPressHold(ctx)
	}
	err := d.withInput(ctx, true, func(l *live) error { return l.tap(ctx, p, hold) })
	if err == nil {
		d.markAwake()
		d.stateMu.Lock()
		d.tapPoint, d.tapAt = p, time.Now()
		d.stateMu.Unlock()
	}
	return err
}

// Swipe implements driver.Device.
func (d *Device) Swipe(ctx context.Context, from, to screen.Point, dur time.Duration) error {
	d.used()
	err := d.withInput(ctx, true, func(l *live) error { return l.swipe(ctx, from, to, dur) })
	if err == nil {
		d.markAwake()
	}
	return err
}

// Touch implements driver.Toucher, for the person's drags on the mirror.
func (d *Device) Touch(ctx context.Context, a driver.TouchAction, p screen.Point) error {
	d.used()
	return d.withInput(ctx, a == driver.TouchDown, func(l *live) error { return l.touch(ctx, a, p) })
}

// Key implements driver.Device.
func (d *Device) Key(ctx context.Context, k driver.Key) error {
	d.used()
	code, ok := keycodes[k]
	if !ok {
		return fmt.Errorf("android: key %q: %w", k, driver.ErrUnsupported)
	}
	return d.withInput(ctx, true, func(l *live) error { return l.key(code, 0) })
}

// Type implements driver.Device. With the companion app it writes the
// text with ACTION_SET_TEXT into the focused field (or the field under the
// last tap): any script, no keyboard, no clipboard. A view that refuses it
// gets the text through its input connection or as a paste (see
// TextSetter in the companion). Otherwise, or when the companion refuses,
// printable ASCII is typed as key events, unless the phone's keyboard may
// turn letters into a composition (pinyin and other CJK input methods)
// and is showing; that text, and anything that is not printable ASCII
// (CJK, emoji, newlines), is set as the clipboard and pasted, which
// replaces the clipboard's content. Without Append the field's text is
// selected first (Ctrl+A) and replaced. With the companion the field is
// read back afterwards: text that did not land, or reads otherwise, is an
// error and is not submitted.
func (d *Device) Type(ctx context.Context, text string, opt driver.TypeOptions) error {
	d.used()
	d.setInputNote("")
	if _, err := d.inputSession(ctx); err != nil {
		return err
	}
	enter := func() error {
		return d.withInput(ctx, false, func(l *live) error { return l.key(keycodeEnter, 0) })
	}
	c := d.companionClient(ctx)
	var refused error
	if c != nil {
		done, why, err := d.typeCompanion(ctx, c, text, opt.Append, opt.Submit, 0, d.recentTap, enter)
		if done {
			return err
		}
		refused = why
	}
	keys := plainASCII(text) && d.keysReachField(ctx)
	check := d.checkField(ctx, c, 0)
	o := opt
	o.Submit = false // after the check: text that did not land is not sent
	if err := d.withInput(ctx, false, func(l *live) error { return l.typeText(ctx, text, o, keys) }); err != nil {
		return err
	}
	if err := check.after(ctx, text, opt.Append, opt.Submit, keys, refused, !d.keyTyping(ctx)); err != nil {
		return err
	}
	if opt.Submit {
		return enter()
	}
	return nil
}

// keysReachField reports whether typed letters reach the app as they are:
// the keyboard commits them directly, or it is hidden, so no editor takes
// them (a calculator or a game handles keys itself).
func (d *Device) keysReachField(ctx context.Context) bool {
	if d.keyTyping(ctx) {
		return true
	}
	visible, err := d.imeVisible(ctx)
	return err == nil && !visible
}

// imeVisible reports whether a soft keyboard window is showing, on any
// display. It reads the window manager, which is much faster than
// `dumpsys input_method` (that also dumps the keyboard app: 1.7 s on the
// Pixel).
func (d *Device) imeVisible(ctx context.Context) (bool, error) {
	out, err := d.client.Shell(ctx, d.info.ID, `dumpsys window InputMethod | grep -E 'isVisible=|isOnScreen='`)
	if err != nil {
		return false, wrapErr(err)
	}
	return parseIMEWindowVisible(string(out)), nil
}

// Input methods whose keyboards commit letters typed on a hardware
// keyboard directly, without a composition.
var directIMEs = map[string]bool{
	"com.android.inputmethod.latin":         true, // AOSP keyboard, LineageOS
	"helium314.keyboard":                    true, // HeliBoard
	"org.dslul.openboard.inputmethod.latin": true, // OpenBoard
	"dev.patrickgold.florisboard":           true,
	"com.menny.android.anysoftkeyboard":     true,
}

// Input methods that compose letters into CJK text.
var composingIMEs = map[string]bool{
	"com.google.android.inputmethod.pinyin":   true,
	"com.google.android.inputmethod.japanese": true,
	"com.google.android.inputmethod.korean":   true,
	"com.sohu.inputmethod.sogou":              true,
	"com.sohu.inputmethod.sogouoem":           true,
	"com.baidu.input":                         true,
	"com.baidu.input_mi":                      true,
	"com.baidu.input_huawei":                  true,
	"com.baidu.input_oppo":                    true,
	"com.baidu.input_vivo":                    true,
	"com.iflytek.inputmethod":                 true,
	"com.tencent.wetype":                      true,
	"com.justsystems.atokmobile.service":      true,
	"jp.co.omronsoft.openwnn":                 true,
}

const keyTypingTTL = 30 * time.Second

// keyTyping reports whether the keyboard commits typed letters directly:
// the current input method is known to, or it is not known to compose
// them and the phone's language is not Chinese, Japanese or Korean.
func (d *Device) keyTyping(ctx context.Context) bool {
	d.stateMu.Lock()
	if time.Since(d.imeAt) < keyTypingTTL {
		v := d.imeKeys
		d.stateMu.Unlock()
		return v
	}
	d.stateMu.Unlock()
	out, err := d.client.Shell(ctx, d.info.ID,
		"settings get secure default_input_method; getprop persist.sys.locale; getprop ro.product.locale")
	if err != nil {
		return false // pasting always works
	}
	f := strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n")
	for len(f) < 3 {
		f = append(f, "")
	}
	locale := strings.TrimSpace(f[1])
	if locale == "" {
		locale = strings.TrimSpace(f[2])
	}
	keys := keyTypingFor(strings.TrimSpace(f[0]), locale)
	d.stateMu.Lock()
	d.imeKeys, d.imeAt = keys, time.Now()
	d.stateMu.Unlock()
	d.cfg.logf("android: %s: input method %q, locale %q: composing keyboard: %v", d.info.ID,
		strings.TrimSpace(f[0]), locale, !keys)
	return keys
}

// keyTypingFor decides keyTyping from the input method id
// ("package/class") and the system locale ("zh-Hans-CN", "en-US").
func keyTypingFor(ime, locale string) bool {
	pkg, _, _ := strings.Cut(ime, "/")
	if directIMEs[pkg] {
		return true
	}
	if composingIMEs[pkg] {
		return false
	}
	lang, _, _ := strings.Cut(strings.ToLower(locale), "-")
	lang, _, _ = strings.Cut(lang, "_")
	switch lang {
	case "zh", "ja", "ko":
		return false
	}
	return true
}

// SetClipboard implements driver.Clipboard.
func (d *Device) SetClipboard(ctx context.Context, text string) error {
	l, err := d.session(ctx)
	if err != nil {
		return err
	}
	if err := l.s.SetClipboard(ctx, text, false); err != nil {
		return l.mapErr(err)
	}
	return nil
}

// Clipboard implements driver.Clipboard. An empty or non-text clipboard
// reads as "".
func (d *Device) Clipboard(ctx context.Context) (string, error) {
	l, err := d.session(ctx)
	if err != nil {
		return "", err
	}
	text, err := l.s.Clipboard(ctx)
	if err != nil {
		return "", l.mapErr(err)
	}
	return text, nil
}

// Input on a session, shared by the main display and background displays.

func (l *live) mapErr(err error) error {
	if l.ended() {
		return l.failure()
	}
	return err
}

func (l *live) tap(ctx context.Context, p screen.Point, hold time.Duration) error {
	pos, err := l.position(ctx, p)
	if err != nil {
		return err
	}
	if err := l.send(scrcpy.InjectTouch(scrcpy.ActionDown, scrcpy.PointerGenericFinger, pos, 1, 0, 0)); err != nil {
		return err
	}
	// Always lift the finger, even when ctx ends during the hold.
	werr := sleepCtx(ctx, hold)
	if err := l.send(scrcpy.InjectTouch(scrcpy.ActionUp, scrcpy.PointerGenericFinger, pos, 0, 0, 0)); err != nil {
		return err
	}
	return werr
}

func (l *live) swipe(ctx context.Context, from, to screen.Point, dur time.Duration) error {
	if dur <= 0 {
		dur = defaultSwipe
	}
	a, err := l.position(ctx, from)
	if err != nil {
		return err
	}
	b, err := l.position(ctx, to)
	if err != nil {
		return err
	}
	finger := scrcpy.PointerGenericFinger
	if err := l.send(scrcpy.InjectTouch(scrcpy.ActionDown, finger, a, 1, 0, 0)); err != nil {
		return err
	}
	start := time.Now()
	var werr error
	for {
		if werr = sleepCtx(ctx, swipeStep); werr != nil {
			break
		}
		f := float64(time.Since(start)) / float64(dur)
		if f >= 1 {
			break
		}
		p := a
		p.X = a.X + int32(float64(b.X-a.X)*f)
		p.Y = a.Y + int32(float64(b.Y-a.Y)*f)
		if err := l.send(scrcpy.InjectTouch(scrcpy.ActionMove, finger, p, 1, 0, 0)); err != nil {
			return err
		}
	}
	if err := l.send(scrcpy.InjectTouch(scrcpy.ActionMove, finger, b, 1, 0, 0)); err != nil {
		return err
	}
	if err := l.send(scrcpy.InjectTouch(scrcpy.ActionUp, finger, b, 0, 0, 0)); err != nil {
		return err
	}
	return werr
}

func (l *live) touch(ctx context.Context, a driver.TouchAction, p screen.Point) error {
	pos, err := l.position(ctx, p)
	if err != nil {
		return err
	}
	var action uint8
	pressure := 1.0
	switch a {
	case driver.TouchDown:
		action = scrcpy.ActionDown
	case driver.TouchMove:
		action = scrcpy.ActionMove
	case driver.TouchUp:
		action, pressure = scrcpy.ActionUp, 0
	default:
		return fmt.Errorf("android: touch action %q: %w", a, driver.ErrUnsupported)
	}
	return l.send(scrcpy.InjectTouch(action, scrcpy.PointerGenericFinger, pos, pressure, 0, 0))
}

func (l *live) key(code, meta int32) error {
	if err := l.send(scrcpy.InjectKeycode(scrcpy.ActionDown, code, 0, meta)); err != nil {
		return err
	}
	return l.send(scrcpy.InjectKeycode(scrcpy.ActionUp, code, 0, meta))
}

// typeText types text into the focused field; keys says whether printable
// ASCII may go as key events (see Device.keyTyping).
func (l *live) typeText(ctx context.Context, text string, opt driver.TypeOptions, keys bool) error {
	if !opt.Append {
		// Select the field's text so the new text replaces it.
		if err := l.key(keycodeA, metaCtrlOn|metaCtrlLeftOn); err != nil {
			return err
		}
		if text == "" {
			if err := l.key(keycodeDel, 0); err != nil {
				return err
			}
		}
	}
	if text != "" {
		if keys && plainASCII(text) {
			if err := l.injectText(text); err != nil {
				return err
			}
		} else {
			if len(text) > scrcpy.MaxClipboardText {
				return fmt.Errorf("android: text too long to paste (%d bytes)", len(text))
			}
			if err := l.s.SetClipboard(ctx, text, true); err != nil {
				return l.mapErr(err)
			}
		}
	}
	if opt.Submit {
		return l.key(keycodeEnter, 0)
	}
	return nil
}

// injectText types printable ASCII as key events, in messages of at most
// scrcpy.MaxInjectText bytes.
func (l *live) injectText(text string) error {
	for _, part := range scrcpy.SplitText(text, scrcpy.MaxInjectText) {
		msg, err := scrcpy.InjectText(part)
		if err != nil {
			return err
		}
		if err := l.send(msg); err != nil {
			return err
		}
	}
	return nil
}

// plainASCII reports whether text is only printable ASCII, which the
// server can type with the virtual keyboard's key map. Control characters
// are excluded on purpose: a typed newline would press Enter.
func plainASCII(text string) bool {
	for i := 0; i < len(text); i++ {
		if c := text[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
