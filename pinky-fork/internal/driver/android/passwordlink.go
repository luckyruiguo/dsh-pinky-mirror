package android

import (
	"strings"

	"github.com/DIYgod/pinky/internal/screen"
)

// Lock screens that open on a fingerprint page of their own, such as
// vivo's OriginOS: a title asking for the fingerprint ("请验证指纹"), the
// sensor's ring, and a row of links at the bottom (紧急拨号, 使用密码,
// 取消). Neither `wm dismiss-keyguard` nor a swipe brings up the PIN pad
// there (a vivo V2172A on OriginOS, Android 15); the "use password" link
// does. Its resource id is not known, so the link is read from its
// words.
//
// The link is tapped only when the lock screen also asks for a
// fingerprint, it is the one place on the screen with such words, it
// takes input and no window above covers it, and it shows in the same
// place in two reads in a row. The tap enters nothing: the PIN goes in
// only once the pad that comes up is read with confidence.

// passwordLinkLabels are the link's words, lower case and without
// spaces: Simplified and Traditional Chinese, English and Japanese.
var passwordLinkLabels = map[string]bool{
	"使用密码": true, "密码": true, "输入密码": true, "密码解锁": true, "使用密码解锁": true,
	"使用pin码": true, "使用pin": true, "pin码": true, "输入pin码": true,
	"使用密碼": true, "密碼": true, "輸入密碼": true, "密碼解鎖": true, "使用pin碼": true, "pin碼": true,
	"usepassword": true, "password": true, "enterpassword": true,
	"usepin": true, "enterpin": true, "usepincode": true,
	"パスワードを使用": true, "パスワード": true, "パスワードを入力": true,
}

// fingerprintWords mark a lock screen that asks for a fingerprint, in a
// title such as "请验证指纹" or "Verify fingerprint".
var fingerprintWords = []string{"指纹", "指紋", "fingerprint"}

func linkLabel(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}

func isPasswordLink(n *lockNode) bool {
	return passwordLinkLabels[linkLabel(n.text)] || passwordLinkLabels[linkLabel(n.desc)]
}

func asksFingerprint(n *lockNode) bool {
	for _, s := range []string{n.text, n.desc} {
		s = strings.ToLower(s)
		for _, w := range fingerprintWords {
			if strings.Contains(s, w) {
				return true
			}
		}
	}
	return false
}

// findPasswordLink returns where to tap the lock screen's "use password"
// link, or an empty rectangle when it shows none, or more than one, or
// when the lock screen does not ask for a fingerprint (fingerprint, from
// the alternate bouncer's ids, or words such as 指纹 on the lock screen).
func findPasswordLink(nodes []lockNode, fingerprint bool) screen.Rect {
	var places []padKey
	for i := range nodes {
		n := &nodes[i]
		if !isSystemUI(n.pkg) || n.password || n.bounds.Empty() {
			continue
		}
		if asksFingerprint(n) && !isPasswordLink(n) {
			fingerprint = true
		}
		if isPasswordLink(n) {
			places = addPadKey(places, n, -1, -1)
		}
	}
	if !fingerprint || len(places) != 1 {
		return screen.Rect{}
	}
	k := places[0]
	if !k.enabled || k.rect.X < 0 || k.rect.Y < 0 || covered(k.rect.Center(), k.above) {
		return screen.Rect{}
	}
	return k.rect
}
