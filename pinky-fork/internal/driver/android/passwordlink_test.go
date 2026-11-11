package android

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

const originOSFingerprint = "synthetic-originos-fingerprint.xml"

// linkTapText returns the text of the node the password link's tap lands
// on, the smallest one under it.
func linkTapText(nodes []lockNode, link screen.Rect) string {
	p := link.Center()
	best := -1
	for i := range nodes {
		n := &nodes[i]
		if n.bounds.Contains(p) && (n.text != "" || n.desc != "") && (best < 0 || area(n.bounds) < area(nodes[best].bounds)) {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return nodes[best].text + nodes[best].desc
}

// relabel returns a copy of nodes with texts replaced.
func relabel(nodes []lockNode, words map[string]string) []lockNode {
	out := append([]lockNode(nil), nodes...)
	for i := range out {
		if w, ok := words[out[i].text]; ok {
			out[i].text = w
		}
	}
	return out
}

// TestPasswordLinkOriginOS reads vivo's fingerprint page (OriginOS) and
// finds its 使用密码 link, never the emergency call or cancel links.
func TestPasswordLinkOriginOS(t *testing.T) {
	zh := keyguardNodes(t, originOSFingerprint)
	en := relabel(zh, map[string]string{"请验证指纹": "Verify fingerprint", "紧急拨号": "Emergency call", "使用密码": "Use password", "取消": "Cancel"})
	tw := relabel(zh, map[string]string{"请验证指纹": "請驗證指紋", "紧急拨号": "緊急撥號", "使用密码": "使用密碼", "取消": "取消"})
	pin := relabel(en, map[string]string{"Use password": "Use PIN"})
	for name, nodes := range map[string][]lockNode{"zh": zh, "en": en, "zh-TW": tw, "Use PIN": pin} {
		b := classifyBouncer(nodes)
		if b.kind != bouncerNone || b.pad != nil || b.unsure || b.passwordLink.Empty() {
			t.Errorf("%s: classifyBouncer = %+v", name, b)
			continue
		}
		got := linkTapText(nodes, b.passwordLink)
		if !isPasswordLink(&lockNode{text: got}) {
			t.Errorf("%s: the tap at %v lands on %q", name, b.passwordLink.Center(), got)
		}
	}
}

// TestPasswordLinkSafety: no tap where the link is not known with
// confidence.
func TestPasswordLinkSafety(t *testing.T) {
	base := keyguardNodes(t, originOSFingerprint)
	find := func(nodes []lockNode, text string) int {
		for i := range nodes {
			if nodes[i].text == text {
				return i
			}
		}
		t.Fatalf("no %q", text)
		return -1
	}
	clone := func() []lockNode { return append([]lockNode(nil), base...) }
	cases := map[string]func([]lockNode) []lockNode{
		"no fingerprint prompt": func(n []lockNode) []lockNode {
			n[find(n, "请验证指纹")].text = "10:42"
			return n
		},
		"two links": func(n []lockNode) []lockNode {
			n[find(n, "取消")].text = "密码"
			return n
		},
		"link disabled": func(n []lockNode) []lockNode {
			n[find(n, "使用密码")].enabled = false
			return n
		},
		"link under a window above": func(n []lockNode) []lockNode {
			i := find(n, "使用密码")
			n[i].above = []screen.Rect{{X: 0, Y: 2000, W: 1080, H: 400}}
			return n
		},
		"link in an app": func(n []lockNode) []lockNode {
			n[find(n, "使用密码")].pkg = "com.example.bank"
			return n
		},
		"words that only contain the label": func(n []lockNode) []lockNode {
			n[find(n, "使用密码")].text = "忘记密码"
			return n
		},
	}
	for name, edit := range cases {
		if b := classifyBouncer(edit(clone())); !b.passwordLink.Empty() {
			t.Errorf("%s: tapping %v", name, b.passwordLink.Center())
		}
	}

	// The alternate bouncer's ids count as a fingerprint prompt too.
	n := clone()
	n[find(n, "请验证指纹")].text = ""
	n = append(n, lockNode{pkg: "com.android.systemui", id: "com.android.systemui:id/alternate_bouncer", enabled: true, bounds: screen.Rect{W: 1080, H: 2400}})
	if b := classifyBouncer(n); b.passwordLink.Empty() || !b.fingerprint {
		t.Errorf("alternate bouncer with a password link: %+v", b)
	}

	// A PIN pad on screen wins over any link: the link is never tapped.
	pad := append(keyguardNodes(t, "synthetic-funtouchos-pin.xml"), base...)
	if b := classifyBouncer(pad); b.pad == nil || !b.passwordLink.Empty() {
		t.Errorf("a pad and the link: pad %v, link %v", b.pad != nil, b.passwordLink)
	}
}

// TestPasswordLinkOtherDumps: no other lock screen dump shows a link.
func TestPasswordLinkOtherDumps(t *testing.T) {
	files, err := os.ReadDir(filepath.Join("testdata", "keyguard"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name := f.Name()
		if name == originOSFingerprint || !strings.HasSuffix(name, ".xml") && !strings.HasSuffix(name, ".json") {
			continue
		}
		for _, nodes := range [][]lockNode{keyguardNodes(t, name), asOEM(keyguardNodes(t, name))} {
			if b := classifyBouncer(nodes); !b.passwordLink.Empty() {
				t.Errorf("%s: a password link at %v", name, b.passwordLink)
			}
		}
	}
}
