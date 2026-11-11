package ios

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// runRecords remembers on the computer, by UDID, the helper runners Pinky
// started and has not yet seen end on the phone. A Pinky that crashed or
// was killed leaves its runner going; the next one finds WebDriverAgent
// already answering and takes it over (startWDAFor), and the record tells
// it that the runner is its own to end when the device closes, unlike one
// Xcode or another tool started. Each record is a file named after the
// phone, holding the runner app's folder and when the run started.
type runRecords struct{ dir string }

func (r runRecords) file(udid string) string {
	return filepath.Join(r.dir, strings.Map(func(c rune) rune {
		if c == '/' || c == '\\' || c == ':' {
			return '_'
		}
		return c
	}, udid))
}

// mark records a runner started from the app at appPath, returning what it
// wrote for unmark; "" when nothing was recorded.
func (r runRecords) mark(udid, appPath string) string {
	if r.dir == "" || udid == "" || appPath == "" {
		return ""
	}
	body := appPath + "\n" + strconv.FormatInt(time.Now().UnixNano(), 10) + "\n"
	if os.MkdirAll(r.dir, 0o700) != nil || os.WriteFile(r.file(udid), []byte(body), 0o600) != nil {
		return ""
	}
	return body
}

// unmark forgets the record, if it is still the one mark wrote (a newer
// run of the same phone keeps its own); body "" forgets any.
func (r runRecords) unmark(udid, body string) {
	if r.dir == "" || udid == "" {
		return
	}
	if body != "" {
		if b, err := os.ReadFile(r.file(udid)); err != nil || string(b) != body {
			return
		}
	}
	os.Remove(r.file(udid))
}

// started returns the runner app's folder of the run recorded for the
// phone, "" when there is none.
func (r runRecords) started(udid string) string {
	if r.dir == "" || udid == "" {
		return ""
	}
	b, err := os.ReadFile(r.file(udid))
	if err != nil {
		return ""
	}
	path, _, _ := strings.Cut(string(b), "\n")
	return path
}
