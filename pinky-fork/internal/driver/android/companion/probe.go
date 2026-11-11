package companion

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DIYgod/pinky/internal/driver"
)

// When the service does not answer after Pinky turned it on, the client
// reads what the phone shows about it, so that the error, the log and the
// person's diagnostics say why: docs/notes/companion.md, "When the service
// does not come on".

// probeMark starts each section of the probe's output.
const probeMark = "--pinky-probe:"

// probeCmd reads, in one shell: the setting as it is now, whether the
// phone was unlocked since it started, the app's process and socket, what
// the accessibility manager has bound, the app's last log lines, a crash
// of the app, and what the system itself said and keeps about the app:
// whether the package is stopped, Android's record of the service, whether
// Android counts the app as a bad process, and the system's log lines that
// name it (am_proc_start, am_kill with its reason, a phone maker's startup
// manager refusing it). A part may fail on some phones; the shell ends
// with true so that the others still come back.
func probeCmd(app App) string {
	part := func(name, cmd string) string { return "echo " + probeMark + name + "; " + cmd }
	return strings.Join([]string{
		part("setting", "settings get secure "+settingKey),
		part("user", "am get-started-user-state 0"),
		part("pid", "pidof "+app.Package),
		part("socket", "grep -c -a "+shellQuote("@"+app.Socket+"$")+" /proc/net/unix"),
		// Only the lines about services: the rest names other apps' windows.
		part("a11y", "dumpsys accessibility | grep -E "+shellQuote("Ui Automation|services:")),
		part("log", "logcat -d -v tag -s PinkyService:V PinkyServer:V | tail -n 1"),
		part("now", "date +%s"),
		part("crash", "logcat -d -b crash -v epoch -t 200 | grep -A1 "+shellQuote("Process: "+app.Package+",")),
		// "stopped=true": force-stopped, or never started since the
		// install; "notLaunched=true": never opened.
		part("package", "dumpsys package "+app.Package+" 2>/dev/null | grep -m1 -o -E 'stopped=[a-z]+|notLaunched=[a-z]+'"),
		// Android's ServiceRecord: its process (app=null when none) and
		// how often it restarted or crashed.
		part("record", "dumpsys activity services "+app.Package+" | grep -E 'ServiceRecord\\{|app=|restartCount=|crashCount='"),
		part("bad", "dumpsys activity processes | grep -F "+shellQuote("Bad process "+app.Package+" ")),
		// The system's lines about the app, not the app's own, nor
		// bookkeeping that says nothing about why it does not start
		// (SettingsShellCmd, and any line about enabled_accessibility_services
		// such as Huawei's SPMN, would also name the person's other
		// services; HwUidTcpMonitor counts traffic).
		part("system", "logcat -d -b events,system,main -v epoch -t 5000 | grep -F "+shellQuote(app.Package)+
			" | grep -v -F -e PinkyService -e PinkyServer -e PinkyConn -e SettingsShellCmd -e "+settingKey+
			" -e HwUidTcpMonitor -e ProcessStats -e 'artd ' -e am_compact | tail -n 12"),
		"true",
	}, "; ")
}

// startError says why the service did not answer within wait, from what
// the phone shows now. It runs before Enable restores the setting, which
// would change what the phone shows.
func (c *Client) startError(ctx context.Context, wait time.Duration) error {
	pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := c.run.Shell(pctx, probeCmd(c.app))
	if err != nil {
		return &driver.CompanionStartError{Detail: fmt.Sprintf("no answer within %s (reading the phone's state failed: %v)", wait, err)}
	}
	return parseProbe(out, c.app).startError(wait)
}

// probe is what probeCmd read. A section that did not come back leaves its
// fields at their zero values, and its "known" flag false.
type probe struct {
	settingKnown, listed bool // the setting lists the service
	locked               bool // the phone was not unlocked since it started
	pidKnown, running    bool // the app's process runs
	socketKnown, socket  bool // its socket listens
	a11yKnown            bool // the accessibility manager answered
	enabled              bool // it lists the service as enabled
	bound                bool // it has bound the service
	binding              bool // it is binding the service
	crashed              bool // it lists the service as crashed (Android 11 and later)
	uiAutomation         bool // a UiAutomation is connected
	log                  string
	crash                string   // the exception of the app's last crash in the last ten minutes
	crashAgo             int      // how many seconds ago it crashed
	stopped, notLaunched bool     // the package's stopped and notLaunched flags
	recorded             bool     // Android has a ServiceRecord for the service
	recordProc           bool     // the record has a process
	restarts, crashes    int      // the record's restartCount and crashCount
	bad                  bool     // Android counts the app as a bad process
	system               []string // the system's latest lines that name the app, newest last
}

// crashWindow is how far back a crash of the app counts: an earlier try
// may have made it crash.
const crashWindow = 600

// systemWindow is how far back the system's lines about the app count:
// the 10 s wait and a little before.
const systemWindow = 120

func parseProbe(out string, app App) probe {
	var p probe
	sections := map[string][]string{}
	name := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if rest, ok := strings.CutPrefix(line, probeMark); ok {
			name = rest
			sections[name] = []string{}
			continue
		}
		if name != "" && strings.TrimSpace(line) != "" {
			sections[name] = append(sections[name], line)
		}
	}
	ours := func(component string) bool {
		return normalizeComponent(component) == app.Package+"/"+ServiceClass
	}
	hasOurs := func(line string) bool {
		for _, f := range strings.FieldsFunc(line, func(r rune) bool { return strings.ContainsRune("{}, ", r) }) {
			if ours(f) {
				return true
			}
		}
		return false
	}
	if s := sections["setting"]; len(s) > 0 {
		p.settingKnown = true
		p.listed = slices.ContainsFunc(parseServices(s[0]), ours) // "null" when unset
	}
	p.locked = slices.ContainsFunc(sections["user"], func(l string) bool { return strings.Contains(l, "RUNNING_LOCKED") })
	if s, ok := sections["pid"]; ok {
		p.pidKnown, p.running = true, len(s) > 0
	}
	if s := sections["socket"]; len(s) > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(s[0])); err == nil {
			p.socketKnown, p.socket = true, n > 0
		}
	}
	for _, line := range sections["a11y"] {
		p.a11yKnown = true
		if strings.Contains(line, "Ui Automation[") {
			p.uiAutomation = true
		}
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "Bound services:"):
			if strings.Contains(l, "label="+app.Label+",") || strings.Contains(l, "label="+app.Label+"]") {
				p.bound = true
			}
		case strings.HasPrefix(l, "Enabled services:"):
			p.enabled = p.enabled || hasOurs(l)
		case strings.HasPrefix(l, "Binding services:"):
			p.binding = p.binding || hasOurs(l)
		case strings.HasPrefix(l, "Crashed services:"):
			p.crashed = p.crashed || hasOurs(l)
		}
	}
	// logcat starts with "--------- beginning of main" even when no line
	// matches.
	logLines := slices.DeleteFunc(slices.Clone(sections["log"]), isLogcatHeader)
	if len(logLines) > 0 {
		p.log = clip(strings.TrimSpace(logLines[len(logLines)-1]), 160)
	}
	var now float64
	if s := sections["now"]; len(s) > 0 {
		now, _ = strconv.ParseFloat(strings.TrimSpace(s[0]), 64)
	}
	crash := sections["crash"]
	for i, line := range crash {
		if !strings.Contains(line, "Process: "+app.Package+",") || i+1 >= len(crash) {
			continue
		}
		f := strings.Fields(line)
		at, err := strconv.ParseFloat(f[0], 64)
		if err != nil || now == 0 || now-at > crashWindow {
			continue
		}
		ex := crash[i+1]
		if _, msg, ok := strings.Cut(ex, "AndroidRuntime: "); ok {
			ex = msg
		}
		p.crash = clip(strings.TrimSpace(ex), 160)
		p.crashAgo = max(0, int(now-at))
	}
	for _, line := range sections["package"] {
		switch strings.TrimSpace(line) {
		case "stopped=true":
			p.stopped = true
		case "notLaunched=true":
			p.notLaunched = true
		}
	}
	for _, line := range sections["record"] {
		l := strings.TrimSpace(line)
		if strings.Contains(l, "ServiceRecord{") && strings.Contains(l, serviceName) {
			p.recorded = true
		}
		for _, f := range strings.Fields(l) {
			k, v, ok := strings.Cut(f, "=")
			if !ok {
				continue
			}
			switch k {
			case "app":
				p.recordProc = p.recordProc || v != "null"
			case "restartCount":
				p.restarts = max(p.restarts, atoi(v))
			case "crashCount":
				p.crashes = max(p.crashes, atoi(v))
			}
		}
	}
	p.bad = len(sections["bad"]) > 0
	for _, line := range sections["system"] {
		if isLogcatHeader(line) {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		at, err := strconv.ParseFloat(f[0], 64)
		if err != nil || now == 0 || now-at > systemWindow {
			continue
		}
		// epoch pid tid level tag: message
		msg := strings.Join(f[4:], " ")
		if f[3] != "" && len(f[3]) == 1 {
			msg = f[3] + "/" + msg
		}
		msg = clip(msg, 200)
		if !slices.Contains(p.system, msg) {
			p.system = append(p.system, msg)
		}
	}
	if n := len(p.system); n > 4 {
		p.system = p.system[n-4:]
	}
	return p
}

// isLogcatHeader reports whether a line is logcat's "--------- beginning
// of main" and the like.
func isLogcatHeader(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "--------- ")
}

// serviceName is the service's class without its package, as a
// ServiceRecord names it ("app.pinky.companion/.PinkyAccessibilityService").
var serviceName = ServiceClass[strings.LastIndex(ServiceClass, ".")+1:]

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimRight(s, ","))
	return n
}

// startError puts what the probe read into an error.
func (p probe) startError(wait time.Duration) *driver.CompanionStartError {
	e := &driver.CompanionStartError{}
	facts := []string{fmt.Sprintf("no answer within %s", wait)}
	add := func(cond bool, fact string) {
		if cond {
			facts = append(facts, fact)
		}
	}
	add(p.settingKnown && !p.listed, "something took it off enabled_accessibility_services again")
	if p.a11yKnown {
		switch {
		case p.bound:
			facts = append(facts, "Android has bound it")
		case p.binding:
			facts = append(facts, "Android is still binding it")
		case p.enabled:
			facts = append(facts, "Android lists it as enabled but has not bound it")
		default:
			facts = append(facts, "Android does not list it as enabled")
		}
		add(p.crashed, "Android lists it as crashed")
		add(p.uiAutomation, "another tool holds Android's UI automation")
	} else {
		facts = append(facts, "dumpsys accessibility showed nothing")
	}
	add(p.locked, "the phone was not unlocked since it started")
	add(p.pidKnown && p.running, "its app is running")
	add(p.pidKnown && !p.running, "its app is not running")
	add(p.socketKnown && p.socket, "its socket is listening")
	add(p.socketKnown && !p.socket, "its socket is not listening")
	add(p.stopped, "its package is stopped")
	add(p.notLaunched, "its app was never opened")
	add(p.bad, "Android counts its app as a bad process")
	if p.recorded {
		add(!p.recordProc, "Android's record of the service has no process")
		add(p.restarts > 0, fmt.Sprintf("Android restarted it %d times", p.restarts))
		add(p.crashes > 0, fmt.Sprintf("it crashed %d times", p.crashes))
	}
	add(p.crash != "", fmt.Sprintf("it crashed %ds ago: %s", p.crashAgo, p.crash))
	add(p.log != "", fmt.Sprintf("its last log line: %q", p.log))
	if len(p.system) > 0 {
		quoted := make([]string, len(p.system))
		for i, l := range p.system {
			quoted[i] = strconv.Quote(l)
		}
		facts = append(facts, "the system said: "+strings.Join(quoted, ", "))
	}
	e.Detail = strings.Join(facts, "; ")
	// Unlocking comes first: the service cannot start before, whatever
	// else holds it.
	switch {
	case p.bound:
	case p.locked:
		e.Reason = driver.CompanionStartNotUnlocked
	case p.uiAutomation:
		e.Reason = driver.CompanionStartUIAutomation
	}
	return e
}

// clip shortens s to at most n bytes, at a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
