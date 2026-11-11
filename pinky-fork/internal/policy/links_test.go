package policy

import (
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

func TestIsLink(t *testing.T) {
	for s, want := range map[string]bool{
		"https://example.com":              true,
		"HTTP://example.com/a?b=c":         true,
		"myapp://item/42":                  true,
		"weixin://dl/scan":                 true,
		"tel:+15551234":                    true,
		"mailto:a@example.com":             true,
		"sms:+15551234?body=hi":            true,
		"javascript:alert(1)":              true,
		"data:text/html,hi":                true,
		"intent:#Intent;package=com.x;end": true,
		"bitcoin:bc1qxyz":                  true,
		"  https://example.com  ":          true,
		"WeChat":                           false,
		"com.tencent.mm":                   false,
		"com.tencent.mm@999":               false,
		"Data: usage":                      false,
		"Notes: today":                     false,
		"myapp:item":                       false, // an unknown scheme needs "//"
		"":                                 false,
		"https://a\nb":                     false,
	} {
		if got := IsLink(s); got != want {
			t.Errorf("IsLink(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestLinkScheme(t *testing.T) {
	for s, want := range map[string]string{
		"HTTPS://x":        "https",
		"facetime-audio:1": "facetime-audio",
		"com.apple.x":      "",
		"x":                "",
	} {
		if got := LinkScheme(s); got != want {
			t.Errorf("LinkScheme(%q) = %q, want %q", s, got, want)
		}
	}
}

func linkInput(link string, app screen.App) Input {
	return Input{Op: OpLaunch, Launch: app, LaunchURL: true, Link: link}
}

// TestUnsafeLinksAreRefused checks the links Pinky never opens: whatever
// the preset, whatever app would open them, and also when the person
// allowed that app for the task.
func TestUnsafeLinksAreRefused(t *testing.T) {
	links := []string{
		"file:///sdcard/Download/a.pdf",
		"content://com.android.contacts/contacts",
		"intent://scan/#Intent;scheme=zxing;package=com.google.zxing.client.android;end",
		"intent:#Intent;component=com.android.settings/.Settings;end",
		"android-app://com.android.settings",
		"javascript:alert(1)",
		"JavaScript:alert(1)",
		"data:text/html,<h1>hi</h1>",
		"shortcuts://run-shortcut?name=Send",
		"itms-services://?action=download-manifest&url=https://x/m.plist",
	}
	browser := screen.App{ID: "com.android.chrome", Name: "Chrome"}
	for _, e := range []*Engine{commitsEngine(t), Default()} {
		for _, link := range links {
			in := linkInput(link, browser)
			in.AllowBlocked = true
			v := e.Evaluate(in)
			if v.Decision != Deny || v.Rule != RuleUnsafeLinks || v.Scope {
				t.Errorf("preset %d, %s: got %v %q, want deny %q", e.Preset(), link, v.Decision, v.Rule, RuleUnsafeLinks)
			}
			if !strings.Contains(v.Reason, LinkScheme(link)+":") {
				t.Errorf("%s: reason %q does not name the scheme", link, v.Reason)
			}
		}
	}
	if _, refused := LinkRefused("https://example.com"); refused {
		t.Error("https refused")
	}
	// Shortcuts runs shortcuts under schemes no list can hold: a link that
	// opens in it is refused by the app.
	shortcuts := screen.App{ID: "com.apple.shortcuts", Name: "Shortcuts"}
	for _, link := range []string{"workflow1b9af7://run-workflow?name=Pay", "shortcuts-production://run-shortcut?name=Send"} {
		in := linkInput(link, shortcuts)
		in.AllowBlocked = true
		if v := Default().Evaluate(in); v.Decision != Deny || v.Rule != RuleUnsafeLinks || !strings.Contains(v.Reason, "Shortcuts") {
			t.Errorf("%s: got %v %q %q", link, v.Decision, v.Rule, v.Reason)
		}
	}
	if v := Default().Evaluate(Input{Op: OpLaunch, Launch: shortcuts}); v.Decision == Deny {
		t.Errorf("launching Shortcuts by its name: %v %q", v.Decision, v.Rule)
	}
}

// TestLinksMeetTheRulesOfATap checks that a link for a call, a message or
// a payment meets the rules a tap on the same thing would, and that the
// app that opens a link is judged as a launch of it.
func TestLinksMeetTheRulesOfATap(t *testing.T) {
	phone := screen.App{ID: "com.google.android.dialer", Name: "Phone"}
	messages := screen.App{ID: "com.google.android.apps.messaging", Name: "Messages"}
	alipay := screen.App{ID: "com.eg.android.AlipayGphone", Name: "支付宝"}
	wechat := screen.App{ID: "com.tencent.mm", Name: "WeChat"}
	chrome := screen.App{ID: "com.android.chrome", Name: "Chrome"}
	cases := []struct {
		name string
		in   Input
		// preset 1, and the default preset 2
		want1, want2 Decision
		rule1, rule2 string
		kind         Kind
	}{
		{"web page", linkInput("https://example.com", chrome), Ask, Allow, "Installs and links", "", OpenURL},
		{"app link", linkInput("dinely://order/42", dinely), Ask, Allow, "Installs and links", "", OpenURL},
		{"call", linkInput("tel:+15551234", phone), Ask, Allow, "Installs and links", "", Call},
		{"facetime", linkInput("facetime:a@example.com", phone), Ask, Allow, "Installs and links", "", Call},
		{"message", linkInput("sms:+15551234?body=hi", messages), Ask, Allow, "Messages", "", SendMessage},
		{"mail", linkInput("mailto:a@example.com", messages), Ask, Allow, "Messages", "", SendMessage},
		{"upi payment", linkInput("upi://pay?pa=a@upi&am=100", dinely), Ask, Ask, "Payments", "Payments", Payment},
		{"wechat pay", linkInput("weixin://wxpay/bizpayurl?pr=abc", wechat), Ask, Ask, "Payments", "Payments", Payment},
		{"wechat app pay", linkInput("weixin://app/wx123/pay/?prepayid=1", wechat), Ask, Ask, "Payments", "Payments", Payment},
		{"wechat scan", linkInput("weixin://dl/scan", wechat), Ask, Allow, "Installs and links", "", OpenURL},
		{"opened by a finance app", linkInput("alipays://platformapi/startapp?appId=1", alipay), Deny, Deny, "Finance apps", "Finance apps", Payment},
		{"opened by a finance app the person allowed", func() Input {
			in := linkInput("alipays://platformapi/startapp?appId=1", alipay)
			in.AllowBlocked = true
			return in
		}(), Ask, Ask, "Payments", "Payments", Payment},
		{"opened by a bank", linkInput("https://chase.com/pay", chase), Deny, Deny, "Finance apps", "Finance apps", OpenURL},
	}
	for _, e := range []*Engine{commitsEngine(t), Default()} {
		for _, c := range cases {
			want, rule := c.want1, c.rule1
			if e.Preset() == PresetPayments {
				want, rule = c.want2, c.rule2
			}
			v := e.Evaluate(c.in)
			if v.Decision != want || v.Rule != rule {
				t.Errorf("preset %d, %s: got %v %q (kinds %v), want %v %q", e.Preset(), c.name, v.Decision, v.Rule, v.Kinds, want, rule)
			}
			if !v.Has(c.kind) || !v.Has(OpenURL) {
				t.Errorf("preset %d, %s: kinds %v miss %s or %s", e.Preset(), c.name, v.Kinds, c.kind, OpenURL)
			}
		}
	}
	// The tap a call link stands for meets the same rule.
	if v := commitsEngine(t).Evaluate(tap(dinely, "Call restaurant")); v.Decision != Ask || !v.Has(Call) {
		t.Errorf("tap on Call: %v %v", v.Decision, v.Kinds)
	}
}

// TestLinkAppScopes checks that the app that opens a link must be in the
// agent's scope and the task's app list, as a launch of it must.
func TestLinkAppScopes(t *testing.T) {
	e := Default()
	in := linkInput("https://example.com", screen.App{ID: "com.android.chrome", Name: "Chrome"})
	in.TaskApps = []string{"Dinely"}
	if v := e.Evaluate(in); v.Decision != Deny || v.Rule != RuleTaskApps || !v.Scope {
		t.Errorf("task apps: got %v %q scope %v", v.Decision, v.Rule, v.Scope)
	}
	in = linkInput("dinely://order/42", dinely)
	in.TaskApps = []string{"Dinely"}
	if v := e.Evaluate(in); v.Decision != Allow {
		t.Errorf("link into a task app: got %v %q", v.Decision, v.Rule)
	}
	in.TaskApps = nil
	in.ScopeApps = []string{"WhatsApp"}
	if v := e.Evaluate(in); v.Decision != Deny || v.Rule != RuleAgentScope || !v.Scope {
		t.Errorf("agent scope: got %v %q scope %v", v.Decision, v.Rule, v.Scope)
	}
}
