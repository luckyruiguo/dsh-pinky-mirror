package telemetry

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Redactor takes out of logs and crash reports what could identify the
// person: their home folder and user name, phone ids and the names they
// gave their phones, e-mail and IP addresses, long hex strings such as
// tokens, and the values of log fields that may name apps, tasks or files.
// What it leaves is shown to the person before anything is sent.
type Redactor struct {
	literal []pair
	phones  map[string]string // id → placeholder
}

// pair replaces old with new; whole words only when word is set, so a
// short user name does not eat into other words.
type pair struct {
	old, new string
	word     *regexp.Regexp
}

// NewRedactor returns a redactor for this computer's home folder and user.
func NewRedactor() *Redactor {
	r := &Redactor{phones: map[string]string{}}
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		r.add(home, "~", false)
		if alt := filepath.ToSlash(home); alt != home {
			r.add(alt, "~", false)
		}
	}
	if u, err := user.Current(); err == nil {
		for _, name := range []string{u.Username, u.Name} {
			// "DOMAIN\\name" on Windows: the name alone is what shows up.
			if i := strings.LastIndexAny(name, `\/`); i >= 0 {
				name = name[i+1:]
			}
			if len(name) >= 3 {
				r.add(name, "<user>", true)
			}
		}
	}
	return r
}

// Phone takes a phone's id out, and its name unless that is just its model
// ("Pixel 9"), which diagnostics show anyway. Each phone gets a placeholder
// of its own, "<phone-1>", so a log still tells phones apart.
func (r *Redactor) Phone(id, name, model string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	tag, ok := r.phones[id]
	if !ok {
		tag = fmt.Sprintf("<phone-%d>", len(r.phones)+1)
		r.phones[id] = tag
		if len(id) >= 4 {
			r.add(id, tag, true)
		}
	}
	name = strings.TrimSpace(name)
	if name != "" && !strings.EqualFold(name, strings.TrimSpace(model)) && len(name) >= 3 {
		r.add(name, strings.TrimSuffix(tag, ">")+" name>", true)
	}
	return tag
}

func (r *Redactor) add(old, new string, word bool) {
	for _, p := range r.literal {
		if p.old == old {
			return
		}
	}
	p := pair{old: old, new: new}
	if word {
		// \b only works next to letters and digits; elsewhere the name's
		// own edge is its boundary.
		pre, post := "", ""
		if isWordByte(old[0]) {
			pre = `\b`
		}
		if isWordByte(old[len(old)-1]) {
			post = `\b`
		}
		p.word = regexp.MustCompile(pre + regexp.QuoteMeta(old) + post)
	}
	r.literal = append(r.literal, p)
	// Longest first, so a phone's name is replaced before a shorter id
	// inside it.
	sort.SliceStable(r.literal, func(i, j int) bool { return len(r.literal[i].old) > len(r.literal[j].old) })
}

var (
	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// Log fields whose values may name an app, a task, a file or what a
	// screen showed.
	reField  = regexp.MustCompile(`\b(app|apps|task|title|name|path|file|files|dir|text|label|query|url|note|intent|summary)=("(?:[^"\\]|\\.)*"|[^\s]+)`)
	reDevice = regexp.MustCompile(`\b(?:device|serial|udid)=("(?:[^"\\]|\\.)*"|[^\s]+)`)
	reIPv4   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	reIPv6   = regexp.MustCompile(`\b(?:[0-9a-fA-F]{1,4}:){3,7}[0-9a-fA-F]{1,4}\b`)
	reMAC    = regexp.MustCompile(`\b(?:[0-9a-fA-F]{2}[:-]){5}[0-9a-fA-F]{2}\b`)
	reUDID   = regexp.MustCompile(`\b[0-9A-Fa-f]{8}-[0-9A-Fa-f]{16}\b`)
	reUUID   = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	reHex    = regexp.MustCompile(`\b[0-9a-fA-F]{24,}\b`)
	// adb's names for phones over Wi-Fi carry the serial.
	reADB = regexp.MustCompile(`adb-[A-Za-z0-9]+-[A-Za-z0-9]+\._adb-tls-(?:connect|pairing)\._tcp\.?`)
)

// Learn finds the phone ids in a log's device= fields, so that phones no
// longer connected are taken out too.
func (r *Redactor) Learn(s string) {
	for _, m := range reDevice.FindAllStringSubmatch(s, -1) {
		r.Phone(strings.Trim(m[1], `"`), "", "")
	}
}

// keep are words a redactor leaves alone, such as Pinky's module path in a
// stack, which holds its author's name.
var keep = []string{"github.com/DIYgod/pinky"}

// String returns s without what could identify the person.
func (r *Redactor) String(s string) string {
	if r == nil {
		return s
	}
	for i, k := range keep {
		s = strings.ReplaceAll(s, k, fmt.Sprintf("\x00keep%d\x00", i))
	}
	s = reADB.ReplaceAllString(s, "<adb-wifi-phone>")
	for _, p := range r.literal {
		if p.word != nil {
			s = p.word.ReplaceAllLiteralString(s, p.new)
		} else {
			s = strings.ReplaceAll(s, p.old, p.new)
		}
	}
	s = reField.ReplaceAllString(s, "$1=<redacted>")
	s = reEmail.ReplaceAllString(s, "<email>")
	s = reUUID.ReplaceAllString(s, "<uuid>")
	s = reUDID.ReplaceAllString(s, "<udid>")
	s = reMAC.ReplaceAllString(s, "<mac>")
	s = reIPv6.ReplaceAllString(s, "<ip>")
	s = reIPv4.ReplaceAllStringFunc(s, func(ip string) string {
		switch ip {
		case "127.0.0.1", "0.0.0.0":
			return ip
		}
		return "<ip>"
	})
	s = reHex.ReplaceAllString(s, "<hex>")
	for i, k := range keep {
		s = strings.ReplaceAll(s, fmt.Sprintf("\x00keep%d\x00", i), k)
	}
	return s
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
