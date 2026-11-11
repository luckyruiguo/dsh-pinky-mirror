package policy

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/DIYgod/pinky/internal/screen"
)

// Buttons that show only an icon: a paper plane that sends, a trash can
// that deletes, a card that pays. Their element has no words of its own,
// or only what the driver fell back on: the last part of the resource id
// on Android ("btn_send"), the accessibility identifier on iOS
// ("sendButton"), or an SF Symbol's name ("paperplane.fill"). What the
// app calls the button then says what it does.
//
// A label with words of its own decides by itself, so a row called
// "Orders" stays a plain tap whatever its id. Ids name nouns as often as
// actions ("order_status", "post_image"), so the words that count are
// fewer than for labels, some count only next to a word that names a
// button, and words that name a place or a state keep an id out.

// iconCommitWords commit wherever they are in an id.
var iconCommitWords = words(
	"send", "pay", "delete", "trash", "checkout", "purchase", "buy", "submit", "confirm",
	"transfer", "publish", "tweet", "retweet", "repost", "donate", "subscribe", "withdraw",
	"erase", "paperplane", "logout", "signout", "sell", "swap", "trade", "convert", "repay",
	"borrow", "stake", "unstake", "deposit", "remove", "bin",
)

// iconMoneyWords make an icon's commit a payment by themselves, as
// moneyWords do a label's.
var iconMoneyWords = words(
	"pay", "buy", "purchase", "checkout", "sell", "swap", "trade", "repay", "borrow", "stake", "unstake",
	"withdraw", "transfer", "deposit", "donate",
)

// iconCommitPhrases commit when an id has them in a row.
var iconCommitPhrases = phrases("place order", "order now", "book now", "reserve now", "sign out", "log out")

// iconButtonVerbs commit only in an id that also names a button
// ("post_button", "btnBook"): alone they are as often nouns ("post_title").
var iconButtonVerbs = words("post", "book", "reserve", "order")

// iconButtonWords name a button in an id.
var iconButtonWords = words("btn", "button", "bt", "fab", "action", "menu")

// iconNegatives name a place, a state or a piece of text rather than an
// action: "payment_methods", "send_status", "order_history".
var iconNegatives = words(
	"status", "state", "history", "histories", "detail", "details", "list", "count", "counter",
	"badge", "time", "timestamp", "date", "method", "methods", "settings", "setting", "info",
	"summary", "title", "hint", "progress", "indicator", "preview", "receipt", "receipts",
	"options", "option", "help", "tab", "label", "error", "placeholder", "carousel", "section", "header",
	"banner", "recycler", "pager", "feed", "address", "rate", "rates", "price", "prices", "chart", "fee", "fees",
)

// iconInstallWords install an app from an icon in an app store.
var iconInstallWords = words("install", "download", "uninstall", "reinstall")

// symbolRe matches SF Symbol names and other dotted names: "trash.fill".
var symbolRe = regexp.MustCompile(`^[a-z0-9]+(?:\.[a-z0-9]+)+$`)

// iconSymbols are SF Symbol names of one word that iOS may give an icon
// button as its label. "Trash", capitalized, is a mailbox.
var iconSymbols = words("trash", "paperplane")

// iconNames returns what an app calls an element that has no words of its
// own: its id's last part, and its label when that is an id or a symbol's
// name. It returns nothing for an element whose label has words.
func iconNames(e *screen.Element) []string {
	if e == nil {
		return nil
	}
	id := strings.TrimSpace(e.ID)
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	label := strings.TrimSpace(e.Label)
	var out []string
	switch {
	case len(labelWords(label)) == 0:
	case idLike(label, id):
		out = append(out, label)
	default:
		return nil
	}
	if id != "" && id != label {
		out = append(out, id)
	}
	return out
}

// idLike reports whether a label reads as a name in code rather than words
// for people: the element's id, "send_button", "sendButton",
// "paperplane.fill".
func idLike(label, id string) bool {
	if strings.ContainsAny(label, " \t") {
		return false
	}
	if label == id || strings.Contains(label, "_") || symbolRe.MatchString(label) || iconSymbols[label] {
		return true
	}
	rs := []rune(label)
	for i := 1; i < len(rs); i++ {
		if unicode.IsLower(rs[i-1]) && unicode.IsUpper(rs[i]) {
			return true
		}
	}
	return false
}

// idWords splits a name in code into lower-case words: "btnSendMessage",
// "IBSend", "fab_pay2" and "trash.fill" give [btn send message], [ib send],
// [fab pay 2] and [trash fill].
func idWords(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(cur) > 0 {
			prev := rs[i-1]
			next := rune(0)
			if i+1 < len(rs) {
				next = rs[i+1]
			}
			switch {
			case unicode.IsDigit(r) != unicode.IsDigit(prev):
				flush()
			case unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsUpper(prev) && unicode.IsLower(next)):
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

// isCommitIcon reports whether an element without words of its own
// commits, by what the app calls it.
func isCommitIcon(e *screen.Element) bool {
	for _, name := range iconNames(e) {
		ws := idWords(name)
		if anyWord(ws, iconNegatives) {
			continue
		}
		if anyWord(ws, iconCommitWords) || containsPhrase(ws, iconCommitPhrases) ||
			(anyWord(ws, iconButtonVerbs) && anyWord(ws, iconButtonWords)) {
			return true
		}
	}
	return false
}

// isMoneyIcon reports whether an element without words of its own moves
// money, by what the app calls it ("btn_sell", "swapButton").
func isMoneyIcon(e *screen.Element) bool {
	for _, name := range iconNames(e) {
		ws := idWords(name)
		if !anyWord(ws, iconNegatives) && anyWord(ws, iconMoneyWords) {
			return true
		}
	}
	return false
}

// isInstallIcon reports whether an element without words of its own
// installs an app, by what the app calls it.
func isInstallIcon(e *screen.Element) bool {
	for _, name := range iconNames(e) {
		ws := idWords(name)
		if !anyWord(ws, iconNegatives) && anyWord(ws, iconInstallWords) {
			return true
		}
	}
	return false
}

// containsPhrase reports whether ws has one of the phrases in a row.
func containsPhrase(ws []string, ps [][]string) bool {
	for i := range ws {
		if startsWithPhrase(ws[i:], ps) {
			return true
		}
	}
	return false
}
