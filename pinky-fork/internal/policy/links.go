package policy

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/DIYgod/pinky/internal/screen"
)

// RuleUnsafeLinks is the built-in rule that refuses links Pinky never
// opens (LinkRefused).
const RuleUnsafeLinks = "Unsafe links"

var schemeRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*):(.)`)

// LinkScheme returns a link's scheme in lower case ("https", "tel"), or ""
// when s does not start with one.
func LinkScheme(s string) string {
	m := schemeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}

// IsLink reports whether s, as phone_launch gets it, is a link rather than
// an app's name: a scheme and "//" ("https://…", "myapp://…"), or a scheme
// that goes without them ("tel:", "mailto:", "javascript:").
func IsLink(s string) bool {
	s = strings.TrimSpace(s)
	m := schemeRe.FindStringSubmatch(s)
	if m == nil || strings.ContainsAny(s, "\n\r") || m[2] == " " || m[2] == "\t" {
		return false
	}
	scheme := strings.ToLower(m[1])
	rest := s[len(m[1])+1:]
	if strings.HasPrefix(rest, "//") {
		return true
	}
	_, refused := refusedSchemes[scheme]
	return refused || linkSchemeKinds[scheme] != ""
}

// refusedSchemes are the schemes Pinky never opens, whatever the rules say,
// with why.
var refusedSchemes = map[string]string{}

func refuse(why string, schemes ...string) {
	for _, s := range schemes {
		refusedSchemes[s] = why
	}
}

func init() {
	refuse("they read files and data on the phone rather than open a page or an app",
		"file", "content", "data", "blob")
	refuse("they name an app and what it should do, which the rules cannot judge; launch the app by its name instead",
		"intent", "android-app")
	refuse("they run code in the page that is open",
		"javascript", "vbscript")
	refuse("they run shortcuts, which can do anything without a step Pinky could judge; launch Shortcuts by its name instead",
		"shortcuts", "shortcuts-production", "workflow")
	refuse("they install apps from outside the App Store",
		"itms-services")
}

// LinkRefused reports whether Pinky refuses to open a link whatever the
// rules say, and why, in a sentence for the agent.
func LinkRefused(link string) (reason string, refused bool) {
	scheme := LinkScheme(link)
	why, ok := refusedSchemes[scheme]
	if !ok {
		return "", false
	}
	return fmt.Sprintf("Pinky does not open %s: links: %s.", scheme, why), true
}

// refusedLinkApps are the apps Pinky never opens a link in, by package or
// bundle id, with why: Shortcuts runs a shortcut from a link under more
// schemes than refusedSchemes can list ("shortcuts-production",
// "workflow<id>").
var refusedLinkApps = map[string]string{
	"com.apple.shortcuts": "they run shortcuts, which can do anything without a step Pinky could judge; launch Shortcuts by its name instead",
}

// linkAppRefused reports whether Pinky refuses a link because of the app
// that opens it, and why.
func linkAppRefused(link string, app screen.App) (reason string, refused bool) {
	why, ok := refusedLinkApps[app.ID]
	if !ok {
		return "", false
	}
	return fmt.Sprintf("Pinky does not open %s: links in %s: %s.", LinkScheme(link), appName(app), why), true
}

// linkSchemeKinds are the schemes that do more than open a page or an
// app's screen: a tap on what they stand for (a phone number, a Send or a
// Pay button) meets the rules for that kind, and so does the link.
var linkSchemeKinds = map[string]Kind{}

func init() {
	for _, s := range []string{"tel", "telprompt", "callto", "sip", "sips",
		"facetime", "facetime-audio", "facetime-prompt", "facetime-audio-prompt"} {
		linkSchemeKinds[s] = Call
	}
	for _, s := range []string{"sms", "smsto", "mms", "mmsto", "mailto", "imessage"} {
		linkSchemeKinds[s] = SendMessage
	}
	for _, s := range []string{
		// Alipay, WeChat Pay's and UnionPay's wallets, QQ Wallet.
		"alipay", "alipays", "alipayqr", "alipayhk", "upwallet", "uppaywallet", "mqqwallet",
		// India's UPI and its apps.
		"upi", "paytmmp", "phonepe", "tez", "gpay", "bhim", "credpay",
		// PayPal, Venmo, Cash App.
		"paypal", "venmo", "cashme", "cashapp",
		// Crypto payment requests and wallet connections.
		"bitcoin", "bitcoincash", "ethereum", "lightning", "litecoin", "dogecoin",
		"monero", "solana", "ripple", "tron", "wc",
	} {
		linkSchemeKinds[s] = Payment
	}
}

// payPathRe finds a payment in a WeChat link: weixin://wxpay/…,
// weixin://app/<id>/pay/….
var payPathRe = regexp.MustCompile(`(?i)(^|[/?&=._-])(wx)?pay([/?&=._-]|$)`)

// linkKinds returns what opening a link does besides opening a link: a
// call, a message or a payment, judged as a tap on the same thing would be.
func linkKinds(link string) []Kind {
	scheme := LinkScheme(link)
	if k := linkSchemeKinds[scheme]; k != "" {
		return []Kind{k}
	}
	if scheme == "weixin" || scheme == "wechat" {
		rest := strings.TrimSpace(link)[len(scheme)+1:]
		if payPathRe.MatchString(rest) {
			return []Kind{Payment}
		}
	}
	return nil
}
