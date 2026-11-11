package android

import (
	"errors"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

func TestParseScreenOn(t *testing.T) {
	cases := []struct {
		out    string
		on, ok bool
	}{
		// Android 16 with a background display: display 0 off, display 17 on,
		// and the phone as a whole awake.
		{"    mDisplayId=0\n    mDisplayId=17\n  mDisplayId=0\n  mScreenState=OFF\n  mDisplayId=17\n  mScreenState=ON\n  mWakefulness=Awake\n", false, true},
		{"  mDisplayId=0\n  mScreenState=ON\n  mDisplayId=17\n  mScreenState=OFF\n", true, true},
		{"  mScreenState=ON\n  mWakefulness=Awake\n", true, true}, // Android 10: one controller
		{"  mScreenState=DOZE\n", false, true},
		{"  mScreenState=ON_SUSPEND\n", false, true},
		{"  mWakefulness=Dozing\n", false, true},
		{"  mWakefulness=Awake\n", true, true},
		{"nothing here", false, false},
	}
	for _, c := range cases {
		on, ok := parseScreenOn(c.out)
		if on != c.on || ok != c.ok {
			t.Errorf("parseScreenOn(%q) = %v, %v; want %v, %v", c.out, on, ok, c.on, c.ok)
		}
	}
	if w, h, ok := parseCurSize("    init=1080x2400 420dpi mMinSizeOfResizeableTaskDp=220 cur=2400x1080 app=2400x1017 rng=1080x1080-2400x2400\n"); !ok || w != 2400 || h != 1080 {
		t.Errorf("parseCurSize = %d, %d, %v", w, h, ok)
	}
}

func TestCheckPIN(t *testing.T) {
	for pin, digits := range map[string]bool{"1234": true, "0000000000000000": true, "hunter2!": false, "abcd": false} {
		got, err := checkPIN(pin)
		if err != nil || got != digits {
			t.Errorf("checkPIN(%q) = %v, %v", pin, got, err)
		}
	}
	for _, pin := range []string{"", "123", "12345678901234567", "\u5bc6\u7801", "12\n34"} {
		if _, err := checkPIN(pin); err == nil {
			t.Errorf("checkPIN(%q) accepted", pin)
		}
	}
}

func TestClassifyBouncer(t *testing.T) {
	sysui := "com.android.systemui"
	keys := func(enabled bool) []lockNode {
		var out []lockNode
		for _, d := range "1234567890" {
			out = append(out, lockNode{pkg: sysui, id: sysui + ":id/digit_text", text: string(d), enabled: enabled})
		}
		return out
	}
	clock := []lockNode{{pkg: sysui, id: sysui + ":id/clock", text: "09\n01", enabled: true}, {pkg: sysui, text: "7", enabled: true}}
	cases := []struct {
		name  string
		nodes []lockNode
		want  bouncer
	}{
		{"lock screen with a clock", clock, bouncer{kind: bouncerNone}},
		{"Android 16 PIN pad", append(keys(true), lockNode{pkg: sysui, id: sysui + ":id/pinEntry", password: true, focused: true, enabled: true}), bouncer{kind: bouncerPIN, enabled: true}},
		{"locked out", append(keys(false), lockNode{pkg: sysui, id: sysui + ":id/pinEntry", password: true, enabled: true}), bouncer{kind: bouncerPIN, enabled: false}},
		{"PIN pad without ids", append(keys(true), lockNode{pkg: sysui, password: true, focused: true, enabled: true}), bouncer{kind: bouncerPIN, enabled: true}},
		{"password", []lockNode{{pkg: sysui, id: sysui + ":id/passwordEntry", password: true, focused: true, enabled: true}}, bouncer{kind: bouncerPassword, enabled: true}},
		{"password without ids", []lockNode{{pkg: sysui, password: true, focused: true, enabled: true}}, bouncer{kind: bouncerPassword, enabled: true}},
		{"pattern", []lockNode{{pkg: sysui, id: sysui + ":id/lockPatternView", enabled: true}}, bouncer{kind: bouncerPattern, enabled: true}},
		{"an app's password field", []lockNode{{pkg: "com.example.bank", password: true, focused: true, enabled: true}}, bouncer{kind: bouncerNone}},
	}
	for _, c := range cases {
		if got := classifyBouncer(c.nodes); got != c.want {
			t.Errorf("%s: classifyBouncer = %+v, want %+v", c.name, got, c.want)
		}
	}
	_ = errors.Is(nil, driver.ErrLocked)
}

// TestClassifyFingerprintBouncer tells the alternate bouncer of an
// under-display fingerprint sensor (the Mi 10 Pro's) from a PIN pad.
func TestClassifyFingerprintBouncer(t *testing.T) {
	sys := "com.android.systemui"
	b := classifyBouncer([]lockNode{
		{pkg: sys, id: sys + ":id/udfps_touch_overlay", enabled: true},
		{pkg: sys, id: sys + ":id/alternate_bouncer", enabled: true},
		{pkg: sys, id: sys + ":id/alternate_bouncer_udfps_icon_view", enabled: true},
	})
	if b.kind != bouncerNone || !b.fingerprint {
		t.Errorf("the fingerprint screen: %+v", b)
	}
	pad := []lockNode{{pkg: sys, id: sys + ":id/pinEntry", password: true, focused: true, enabled: true}}
	for i := 0; i < 10; i++ {
		pad = append(pad, lockNode{pkg: sys, id: sys + ":id/key" + string(rune('0'+i)), text: string(rune('0' + i)), enabled: true})
	}
	if b := classifyBouncer(pad); b.kind != bouncerPIN || b.fingerprint {
		t.Errorf("the PIN pad: %+v", b)
	}
}
