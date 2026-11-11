package policy

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/DIYgod/pinky/internal/screen"
)

// maxCommitWords bounds how long a Latin label may be and still count as a
// button: "Place order · $12.80" yes, a paragraph that happens to start with
// "Send" no.
const maxCommitWords = 6

// maxCJKRunes is the same bound for labels without spaces.
const maxCJKRunes = 14

// excludedStarts are phrases that start with a commit verb but are not
// commits, such as sort menus.
var excludedStarts = phrases("order by", "ordenar por", "sort by", "book mark", "post office", "send to back")

// tokenize lower-cases s and splits it into words of letters and digits,
// keeping in-word apostrophes and hyphens.
func tokenize(s string) []string {
	s = strings.ToLower(s)
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r):
			b.WriteRune(r)
		case (r == '\'' || r == '’' || r == '-') && b.Len() > 0 && i+1 < len(rs) && unicode.IsLetter(rs[i+1]):
			if r == '’' {
				r = '\''
			}
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// labelWords returns the words of a label without numbers, so prices and
// counts do not count towards its length.
func labelWords(label string) []string {
	var out []string
	for _, t := range tokenize(label) {
		if isNumber(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func isNumber(t string) bool {
	for _, r := range t {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return t != ""
}

// hasCJK reports whether s contains Han, Hiragana, Katakana or Hangul.
func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

// cjkCore strips whitespace, punctuation, symbols and digits from a CJK
// label: "去结算(3)" becomes "去结算", "立即支付 ¥12.80" becomes "立即支付".
func cjkCore(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsDigit(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func startsWithPhrase(ws []string, ps [][]string) bool {
	for _, p := range ps {
		if len(p) <= len(ws) && equalWords(ws[:len(p)], p) {
			return true
		}
	}
	return false
}

func equalWords(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func anyWord(ws []string, set map[string]bool) bool {
	for _, w := range ws {
		if set[w] {
			return true
		}
	}
	return false
}

func hasPrefixAny(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func containsAny(s string, subs []string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// isCommitLabel reports whether a button label is a commit, in any
// supported language.
func isCommitLabel(label string) bool {
	if label == "" {
		return false
	}
	if hasCJK(label) {
		core := cjkCore(label)
		if core == "" || len([]rune(core)) > maxCJKRunes || containsAny(core, cjkNegatives) {
			return false
		}
		if hasPrefixAny(core, cjkCommitPrefixes) {
			return true
		}
		// "同意并支付", "確認して購入": a commit after a conjunction.
		for _, p := range cjkCommitPrefixes {
			for _, joint := range []string{"并", "並", "して", "하고", "및"} {
				if strings.Contains(core, joint+p) {
					return true
				}
			}
		}
		// Mixed labels such as "Send 发送" fall through to the word check.
	}
	ws := labelWords(label)
	if len(ws) == 0 || len(ws) > maxCommitWords || anyWord(ws, negativeWords) {
		return false
	}
	if startsWithPhrase(ws, excludedStarts) {
		return false
	}
	if commitVerbs[ws[0]] || startsWithPhrase(ws, commitPhrases) {
		return true
	}
	// "Yes, delete", "OK, send it".
	if affirmativeWords[ws[0]] && len(ws) >= 2 && (commitVerbs[ws[1]] || startsWithPhrase(ws[1:], commitPhrases)) {
		return true
	}
	// "Slide to pay", "Swipe to confirm", "Tap to send".
	if len(ws) >= 3 && slideVerbs[ws[0]] && (ws[1] == "to" || ws[1] == "pour" || ws[1] == "para" || ws[1] == "zum") &&
		(commitVerbs[ws[2]] || startsWithPhrase(ws[2:], commitPhrases)) {
		return true
	}
	return false
}

// isAffirmative reports whether a label confirms a dialog: OK, Yes, 确定.
func isAffirmative(label string) bool {
	if hasCJK(label) {
		core := cjkCore(label)
		for _, a := range cjkAffirmatives {
			if core == a {
				return true
			}
		}
		return false
	}
	ws := labelWords(label)
	return len(ws) >= 1 && len(ws) <= 2 && affirmativeWords[ws[0]]
}

// mentionsCommit reports whether a sentence talks about a commit: it has a
// commit verb anywhere, as in "Delete this message?".
func mentionsCommit(text string) bool {
	if hasCJK(text) {
		core := cjkCore(text)
		for _, p := range cjkCommitPrefixes {
			if strings.Contains(core, p) {
				return true
			}
		}
	}
	ws := labelWords(text)
	for i := range ws {
		if commitVerbs[ws[i]] || startsWithPhrase(ws[i:], commitPhrases) {
			return true
		}
	}
	return false
}

// movesMoney reports whether a commit's label moves money by itself: it
// pays, buys, sells, swaps, trades, lends, borrows or moves money in or out
// ("Sell all", "Confirm swap", "立即还款"), with or without an amount.
func movesMoney(label string) bool {
	if hasCJK(label) && containsAny(cjkCore(label), cjkMoneyKeywords) {
		return true
	}
	ws := labelWords(label)
	return anyWord(ws, moneyWords) || containsPhrase(ws, moneyPhrases)
}

// ocrParts splits a line that OCR read into what a tap on it may have
// meant: the whole line, and, when the line is CJK or short, each piece of
// it, since OCR puts the buttons of one row on one line ("还款 分期还款
// 去分期", "Cancel Delete"). A long line is a sentence, and a commit verb
// inside it no button.
func ocrParts(text string) []string {
	parts := []string{text}
	fields := strings.Fields(text)
	if len(fields) > 1 && (hasCJK(text) || len(labelWords(text)) <= 4) {
		parts = append(parts, fields...)
	}
	return parts
}

func isQuestion(s string) bool {
	s = strings.TrimSpace(s)
	return strings.ContainsAny(s, "?？") || strings.HasSuffix(s, "吗") || strings.HasSuffix(s, "嗎") ||
		strings.HasSuffix(s, "か") || strings.HasSuffix(s, "까")
}

// screenAsksCommit reports whether the screen shows a confirmation about a
// commit: a dialog event, or a question in the element list, that mentions
// one. The target itself does not count.
func screenAsksCommit(s *screen.Screen, target *screen.Element) bool {
	if s == nil {
		return false
	}
	for _, ev := range s.Events {
		if ev.Kind == screen.EventDialog && mentionsCommit(ev.Text) {
			return true
		}
	}
	for i := range s.Elements {
		e := &s.Elements[i]
		if target != nil && e.Ref == target.Ref && e.Label == target.Label && e.Bounds == target.Bounds {
			continue
		}
		if isQuestion(e.Label) && mentionsCommit(e.Label) {
			return true
		}
	}
	return false
}

func isInstallLabel(label string) bool {
	if hasCJK(label) {
		return hasPrefixAny(cjkCore(label), cjkInstallPrefixes)
	}
	ws := labelWords(label)
	if len(ws) == 0 {
		// A price alone, such as "$4.99", buys an app in a store.
		return hasCurrency(label)
	}
	return len(ws) <= maxCommitWords && installVerbs[ws[0]]
}

func isCallTarget(e *screen.Element) bool {
	if e == nil {
		return false
	}
	id := strings.ToLower(e.ID)
	for _, s := range []string{"call_button", "callbutton", "call_action", "dial_button", "dialbutton",
		"dialpad_floating_action_button", "voice_call", "video_call", "start_call", "make_call"} {
		if strings.Contains(id, s) {
			return true
		}
	}
	label := e.Label
	if hasCJK(label) {
		return hasPrefixAny(cjkCore(label), cjkCallPrefixes)
	}
	ws := labelWords(label)
	if len(ws) == 0 || len(ws) > maxCommitWords {
		return false
	}
	return callWords[ws[0]] || startsWithPhrase(ws, callPhrases)
}

func isSettingLabel(label string) bool {
	ws := labelWords(label)
	return len(ws) >= 1 && len(ws) <= maxCommitWords && settingVerbs[ws[0]]
}

// cryptoTickers are coins and stable coins whose amounts count as money,
// as a wallet shows them: "0.5 ETH", "1,200 USDC".
const cryptoTickers = `BTC|ETH|USDT|USDC|USDE|DAI|BNB|SOL|XRP|ADA|DOGE|TRX|TON|AVAX|DOT|MATIC|POL|LTC|BCH|SUI|APT|ARB|OP|ENA|WBTC|WETH|STETH|EURC|FDUSD|PYUSD|SATS`

var currencyRe = regexp.MustCompile(
	`(?:US\$|HK\$|NT\$|A\$|C\$|S\$|R\$|\$|€|£|¥|￥|₩|₹|₽|₺|₫|฿|₱|₪|CHF|USD|EUR|GBP|CNY|RMB|JPY|KRW|HKD|TWD|INR|RUB|AUD|CAD|SGD|BRL|MXN)\s?\d` +
		`|\d(?:[\d.,\s]*\d)?\s?(?:€|元|円|원|₽|zł|kr|Kč|руб|USD|EUR|GBP|CNY|RMB|JPY|KRW|HKD|TWD|INR|RUB|AUD|CAD|SGD|BRL|MXN)` +
		`|\b(?:` + cryptoTickers + `)\s?\d|\d(?:[\d.,\s]*\d)?\s?(?:` + cryptoTickers + `)\b`)

// hasCurrency reports whether s shows an amount of money.
func hasCurrency(s string) bool { return currencyRe.MatchString(s) }

func screenHasCurrency(s *screen.Screen) bool {
	if s == nil {
		return false
	}
	for _, e := range s.Elements {
		if hasCurrency(e.Label) {
			return true
		}
	}
	return false
}

var domainRe = regexp.MustCompile(`(?i)^(?:[a-z0-9-]+\.)+[a-z]{2,}(?:[/?#]\S*)?$`)

// looksLikeURL reports whether a link label is a web address.
func looksLikeURL(s string) bool {
	s = strings.TrimSpace(s)
	l := strings.ToLower(s)
	if strings.Contains(l, "://") || strings.HasPrefix(l, "www.") {
		return true
	}
	return !strings.ContainsAny(s, " \t") && domainRe.MatchString(s)
}

// IsSearchField reports whether an input is a search box, by its label or
// resource id.
func IsSearchField(e *screen.Element) bool {
	if e == nil {
		return false
	}
	text := strings.ToLower(e.Label + " " + e.ID)
	return containsAny(text, searchWords)
}

// IsSearchBox reports whether a text field typed into is a search box:
// target, the field, reads as one (IsSearchField), or, when the screen
// shows no field with focus (target nil), the field is one the app draws
// itself and the screen shows a search box at its top
// (impliedSearchBox).
func IsSearchBox(target *screen.Element, s *screen.Screen) bool {
	if target != nil {
		return IsSearchField(target)
	}
	return impliedSearchBox(s)
}

// impliedSearchBox reports whether the field that has focus, which s does
// not show, is most likely the app's search box. Apps that draw their own
// text field read as no field at all: Xianyu's search box, empty, is only
// the text of its placeholder, "搜索宝贝/帖子/用户" (GitHub issue #76). It
// takes all of: the keyboard up, so something takes text; no element that
// reads as a text field, which could be the one with focus; and a search
// box's placeholder or label, not a button, in the top quarter of the
// screen, where apps put their search box. A chat's own compose field at
// the bottom with a search button at the top does not count.
func impliedSearchBox(s *screen.Screen) bool {
	if s == nil || !s.Keyboard || s.Height <= 0 {
		return false
	}
	found := false
	for i := range s.Elements {
		e := &s.Elements[i]
		if e.Role == screen.RoleInput || e.Has(screen.StateEditable) || e.Has(screen.StateFocused) {
			return false
		}
		if e.Role == screen.RoleButton || e.Role == screen.RoleLink || e.Bounds.Empty() {
			continue
		}
		if c := e.Bounds.Center(); c.Y < s.Height/4 && IsSearchField(e) {
			found = true
		}
	}
	return found
}

// IsCredentialField reports whether typing into e would hand a password or a
// one-time code to the agent.
//
// The label counts as people read it: a code word anywhere in it. The
// identifier, which agents do not see, counts by whole words (see
// identifierWords), since apps join words there: a note's body named
// "footprintTextView" or "barcodeNotes" must not turn into a code field
// for the "otp" or "code" inside a longer word (GitHub issue #47). A
// password word counts anywhere in both.
func IsCredentialField(e *screen.Element) bool {
	if e == nil {
		return false
	}
	if e.Has(screen.StatePassword) {
		return true
	}
	label, id := strings.ToLower(e.Label), strings.ToLower(e.ID)
	if containsAny(label, passwordWords) || containsAny(id, passwordWords) {
		return true
	}
	idw := identifierWords(e.ID)
	if containsAny(label, otpNegatives) || idHasAny(id, idw, otpNegatives) {
		return false
	}
	if containsAny(label, otpWords) || idHasAny(id, idw, otpWords) {
		return true
	}
	if anyWord(tokenize(label), otpTokens) || anyWord(idw, otpTokens) {
		return true
	}
	for _, w := range idw {
		if isIDCodeWord(w) {
			return true
		}
	}
	return false
}

// identifierWords splits an identifier into lower-case words: at anything
// but letters and digits, between letters and digits, and where camelCase
// starts a word. "OTPCodeField" gives otp, code, field; "et_verify_code2"
// gives et, verify, code, 2.
func identifierWords(id string) []string {
	var out []string
	var w []rune
	flush := func() {
		if len(w) > 0 {
			out = append(out, string(w))
			w = w[:0]
		}
	}
	rs := []rune(id)
	for i, r := range rs {
		letter := unicode.IsLetter(r) || unicode.Is(unicode.Mn, r)
		if !letter && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if i > 0 && len(w) > 0 {
			prev := rs[i-1]
			switch {
			case unicode.IsDigit(r) != unicode.IsDigit(prev):
				flush()
			case unicode.IsUpper(r) && unicode.IsLower(prev):
				flush()
			case unicode.IsUpper(r) && unicode.IsUpper(prev) && i+1 < len(rs) && unicode.IsLower(rs[i+1]):
				// The last capital of an acronym starts the next word:
				// "OTPCode".
				flush()
			}
		}
		w = append(w, unicode.ToLower(r))
	}
	flush()
	return out
}

// idHasAny reports whether an identifier holds one of the phrases: as
// whole words of idw, or, for CJK phrases, which have no word breaks,
// anywhere in the lower-cased identifier id.
func idHasAny(id string, idw []string, phrases []string) bool {
	if len(idw) == 0 {
		return false
	}
	joined := " " + strings.Join(idw, " ") + " "
	for _, p := range phrases {
		if hasCJK(p) {
			if strings.Contains(id, strings.ReplaceAll(p, " ", "")) {
				return true
			}
			continue
		}
		if pw := identifierWords(p); len(pw) > 0 && strings.Contains(joined, " "+strings.Join(pw, " ")+" ") {
			return true
		}
	}
	return false
}

// isIDCodeWord reports whether an identifier word written without breaks
// names a code field: "otpinput", "verifycode", "etsmscode", "vcode". A
// word that only ends in "code" ("barcode", "unicode", "zipcode") does not.
func isIDCodeWord(w string) bool {
	if strings.HasPrefix(w, "otp") || strings.HasPrefix(w, "verify") || strings.HasPrefix(w, "verification") {
		return true
	}
	q, ok := strings.CutSuffix(w, "code")
	if !ok || q == "" {
		return false
	}
	if q == "v" || strings.Contains(q, "verif") {
		return true
	}
	for _, k := range idCodeQualifiers {
		if strings.HasSuffix(q, k) {
			return true
		}
	}
	return false
}

func isInput(e *screen.Element) bool {
	return e != nil && (e.Role == screen.RoleInput || e.Has(screen.StateEditable))
}

// commitParts reports whether an element's label commits, and whether
// that commit moves money by itself. Text that only OCR read is judged by
// its pieces too (ocrParts).
func commitParts(t *screen.Element) (commit, money bool) {
	parts := []string{t.Label}
	if t.Source == screen.SourceOCR {
		parts = ocrParts(t.Label)
	}
	for _, p := range parts {
		if isCommitLabel(p) {
			commit = true
			money = money || movesMoney(p)
		}
	}
	return commit, money
}

// detect returns the policy kinds of an action.
func detect(in Input, cats []string) []Kind {
	var kinds []Kind
	add := func(k Kind) {
		for _, x := range kinds {
			if x == k {
				return
			}
		}
		kinds = append(kinds, k)
	}
	has := func(c string) bool {
		for _, x := range cats {
			if x == c {
				return true
			}
		}
		return false
	}
	t := in.Target
	label := ""
	if t != nil {
		label = t.Label
	}
	switch in.Op {
	case OpTap:
		// A tab only moves between pages, whatever it is called ("Trade",
		// "Swap" in an exchange's tab bar).
		if t == nil || isInput(t) || t.Role == screen.RoleList || t.Role == screen.RoleTab {
			break
		}
		commit, money := commitParts(t)
		if commit || (isAffirmative(label) && screenAsksCommit(in.Screen, t)) || isCommitIcon(t) {
			add(Commit)
		}
		if has(CatStores) && (isInstallLabel(label) || isAffirmative(label) || isInstallIcon(t)) {
			add(InstallApp)
		}
		if isCallTarget(t) {
			add(Call)
		}
		if t.Role == screen.RoleLink && looksLikeURL(label) && !has(CatBrowsers) {
			add(OpenURL)
		}
		if has(CatSettings) && (t.Role == screen.RoleSwitch || t.Role == screen.RoleCheckbox || isSettingLabel(label)) {
			add(ChangeSetting)
		}
		// A payment: a commit that moves money by itself, or any commit on
		// a screen that shows an amount.
		if (hasKind(kinds, Commit) || hasKind(kinds, InstallApp)) &&
			(money || isMoneyIcon(t) || hasCurrency(label) || screenHasCurrency(in.Screen)) {
			add(Payment)
		}
	case OpSwipe:
		// Sliders such as "Slide to pay", also by their id ("slide_to_pay");
		// a list that scrolls is no slider.
		if t == nil {
			break
		}
		slider := !t.Has(screen.StateScrollable) && t.Role != screen.RoleList
		if commit, money := commitParts(t); commit || (slider && isCommitIcon(t)) {
			add(Commit)
			if money || (slider && isMoneyIcon(t)) || hasCurrency(label) || screenHasCurrency(in.Screen) {
				add(Payment)
			}
		}
	case OpType:
		// A line break may press Enter, which sends in many chat apps.
		submit := in.Submit || strings.ContainsAny(in.Text, "\n\r")
		if submit && (has(CatMessaging) || has(CatSocial)) && !IsSearchField(t) {
			add(SendMessage)
		}
	case OpKey:
		if in.Key == "enter" && (has(CatMessaging) || has(CatSocial)) && !IsSearchField(t) {
			add(SendMessage)
		}
	case OpLaunch:
		if in.LaunchURL {
			add(OpenURL)
			for _, k := range linkKinds(in.Link) {
				add(k)
			}
		}
	}
	return kinds
}

func hasKind(ks []Kind, k Kind) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}
