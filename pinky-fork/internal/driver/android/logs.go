package android

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
)

// App logs and crash reports (driver.AppLogger, docs/notes/app-logs.md).
// Only the app's own lines leave the phone: logcat is asked for the app's
// user id alone where it can be (--uid, Android 11 and later), and every
// line's uid is checked again here; crash reports come from the drop box
// (data_app_crash, data_app_native_crash, data_app_anr) by the entry's UID,
// with an ANR cut down to the app's own threads, and from the crash log
// buffer for Java crashes the drop box left out.

// Limits of reading, against runaway output.
const (
	logcatMaxLines   = 100_000 // lines kept when a query sets no Max
	logcatLineMax    = 64 << 10
	dropboxEntryMax  = 1 << 20 // bytes of one drop box entry kept
	dropboxSeparator = "========================================"
)

// dropboxTags are the drop box tags of an app's crashes, and the kind of
// report each one is.
var dropboxTags = map[string]string{
	"data_app_crash":        "crash",
	"data_app_native_crash": "native crash",
	"data_app_anr":          "ANR",
}

// AppLogs implements driver.AppLogger.
func (d *Device) AppLogs(ctx context.Context, q driver.LogQuery) (driver.LogResult, error) {
	_, uid, err := d.appUID(ctx, q.App)
	if err != nil {
		return driver.LogResult{}, err
	}
	res, err := d.logcat(ctx, uid, "main,system,crash", q)
	if err != nil {
		return driver.LogResult{}, wrapErr(err)
	}
	res.From = q.Since
	return res, nil
}

// logcat reads the app's lines of the given buffers.
func (d *Device) logcat(ctx context.Context, uid int, buffers string, q driver.LogQuery) (driver.LogResult, error) {
	byUID := d.props.SDK == 0 || d.props.SDK >= 30
	for {
		conn, err := d.client.Dial(ctx, d.info.ID, "shell:"+logcatCommand(uid, buffers, q, byUID))
		if err != nil {
			return driver.LogResult{}, err
		}
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		res, unknown, err := readLogcat(conn, uid, q)
		stop()
		conn.Close()
		if err := ctx.Err(); err != nil {
			return driver.LogResult{}, err
		}
		if unknown && byUID {
			// A logcat without --uid: read every line and keep the app's.
			byUID = false
			continue
		}
		return res, err
	}
}

// logcatCommand is the logcat command line for an app's lines: a dump of
// the buffers in epoch time with each line's uid, from q.Since, at q.Level
// or more severe.
func logcatCommand(uid int, buffers string, q driver.LogQuery, byUID bool) string {
	cmd := "logcat -d -b " + buffers + " -v epoch -v uid"
	if byUID {
		cmd += " --uid=" + strconv.Itoa(uid)
	}
	if !q.Since.IsZero() {
		ms := q.Since.UnixMilli()
		cmd += fmt.Sprintf(" -t %d.%03d", ms/1000, ms%1000)
	}
	if q.Level.Rank() > 0 {
		cmd += " " + adb.Quote("*:"+q.Level.Letter())
	}
	return cmd + " 2>&1"
}

// readLogcat reads logcat's output and keeps the newest q.Max lines of the
// app's uid that the query keeps. unknown reports a logcat that did not
// know an option.
func readLogcat(r io.Reader, uid int, q driver.LogQuery) (res driver.LogResult, unknown bool, err error) {
	most := q.Max
	if most <= 0 {
		most = logcatMaxLines
	}
	ring := make([]driver.LogLine, 0, min(most, 1024))
	start, total := 0, 0
	first := true
	err = eachLine(r, func(s string) bool {
		if first {
			first = false
			if strings.Contains(strings.ToLower(s), "unrecognized option") {
				unknown = true
				return false
			}
		}
		u, l, ok := parseLogcatLine(s)
		if !ok || u != uid || l.Time.Before(q.Since) || !q.Keep(l) {
			return true
		}
		total++
		if len(ring) < most {
			ring = append(ring, l)
			return true
		}
		ring[start] = l
		start = (start + 1) % most
		return true
	})
	if unknown || err != nil {
		return res, unknown, err
	}
	res.Lines = append(ring[start:], ring[:start]...)
	res.Dropped = total - len(res.Lines)
	return res, false, nil
}

// parseLogcatLine reads one line of `logcat -v epoch -v uid`:
//
//	1794218329.176 10213  8726  8728 I pinky.crashtest: Wrote stack traces
//
// The uid is a number, or a name for the system's own ("root", "system"),
// which are never an app's; "u0_a213" is read as 10213.
func parseLogcatLine(s string) (uid int, l driver.LogLine, ok bool) {
	var f [5]string
	rest := s
	for i := range f {
		rest = strings.TrimLeft(rest, " ")
		j := strings.IndexByte(rest, ' ')
		if j < 0 {
			return 0, l, false
		}
		f[i], rest = rest[:j], rest[j+1:]
	}
	sec, frac, _ := strings.Cut(f[0], ".")
	secs, err1 := strconv.ParseInt(sec, 10, 64)
	ms, err2 := strconv.Atoi((frac + "000")[:3])
	if err1 != nil || err2 != nil {
		return 0, l, false
	}
	uid, ok = parseUID(f[1])
	if !ok {
		return 0, l, false
	}
	pid, err := strconv.Atoi(f[2])
	if err != nil {
		return 0, l, false
	}
	level, ok := logcatLevels[f[4]]
	if !ok {
		return 0, l, false
	}
	tag, text, found := strings.Cut(rest, ": ")
	if !found {
		tag, text = strings.TrimSuffix(rest, ":"), ""
	}
	return uid, driver.LogLine{Time: time.UnixMilli(secs*1000 + int64(ms)), PID: pid, Level: level,
		Tag: strings.TrimSpace(tag), Text: text}, true
}

var logcatLevels = map[string]driver.LogLevel{
	"V": driver.LevelVerbose, "D": driver.LevelDebug, "I": driver.LevelInfo,
	"W": driver.LevelWarn, "E": driver.LevelError, "F": driver.LevelFatal, "A": driver.LevelFatal,
}

// parseUID reads a uid as logcat writes it: a number or "u<user>_a<app>".
func parseUID(s string) (int, bool) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	var user, app int
	if _, err := fmt.Sscanf(s, "u%d_a%d", &user, &app); err == nil && fmt.Sprintf("u%d_a%d", user, app) == s {
		return user*100000 + 10000 + app, true
	}
	return 0, false
}

// AppCrashes implements driver.AppLogger.
func (d *Device) AppCrashes(ctx context.Context, q driver.LogQuery) ([]driver.CrashReport, error) {
	pkg, uid, err := d.appUID(ctx, q.App)
	if err != nil {
		return nil, err
	}
	var cmd strings.Builder
	cmd.WriteString("date +%z")
	for _, tag := range []string{"data_app_crash", "data_app_native_crash", "data_app_anr"} {
		cmd.WriteString("; dumpsys dropbox --print " + tag)
	}
	conn, err := d.client.Dial(ctx, d.info.ID, "shell:"+cmd.String())
	if err != nil {
		return nil, wrapErr(err)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	reports, err := readDropbox(conn, pkg, uid, q.Since)
	stop()
	conn.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, wrapErr(err)
	}
	// Java crashes the drop box left out (it drops entries that come too
	// fast), from the crash buffer.
	buf, err := d.logcat(ctx, uid, "crash", driver.LogQuery{Since: q.Since})
	if err != nil {
		return nil, wrapErr(err)
	}
	seen := map[int]bool{}
	for _, r := range reports {
		if r.Kind == "crash" {
			seen[r.PID] = true
		}
	}
	for _, r := range crashBufferReports(buf.Lines) {
		if !seen[r.PID] {
			reports = append(reports, r)
		}
	}
	sort.SliceStable(reports, func(i, j int) bool { return reports[i].Time.After(reports[j].Time) })
	if q.Max > 0 && len(reports) > q.Max {
		reports = reports[:q.Max]
	}
	return reports, nil
}

var dropboxHeading = regexp.MustCompile(`^(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d) (\S+) \(`)

// dropboxEntry is one drop box entry being read.
type dropboxEntry struct {
	tag    string
	at     time.Time
	fields map[string]string // the heading fields, "Process", "UID", ...
	body   []string          // the lines after them
	size   int
	inBody bool
}

// readDropbox reads `date +%z` and then `dumpsys dropbox --print <tag>`
// for each tag, and returns the reports of the app's uid since a time.
func readDropbox(r io.Reader, pkg string, uid int, since time.Time) ([]driver.CrashReport, error) {
	zone := time.Local
	var out []driver.CrashReport
	var cur *dropboxEntry
	finish := func() {
		if cur != nil {
			if r, ok := dropboxReport(cur, pkg, uid); ok && !r.Time.Before(since) {
				out = append(out, r)
			}
		}
		cur = nil
	}
	first := true
	err := eachLine(r, func(s string) bool {
		if first {
			first = false
			if t, err := time.Parse("-0700", strings.TrimSpace(s)); err == nil {
				zone = t.Location()
				return true
			}
		}
		switch {
		case s == dropboxSeparator:
			finish()
			cur = &dropboxEntry{fields: map[string]string{}}
			return true
		case strings.HasPrefix(s, "Drop box contents: "):
			// The next tag's dump begins.
			finish()
			return true
		}
		if cur == nil {
			return true
		}
		if cur.tag == "" {
			m := dropboxHeading.FindStringSubmatch(s)
			if m == nil || dropboxTags[m[2]] == "" {
				cur = nil
				return true
			}
			cur.tag = m[2]
			cur.at, _ = time.ParseInLocation("2006-01-02 15:04:05", m[1], zone)
			if cur.at.Before(since.Truncate(time.Second)) {
				cur = nil
			}
			return true
		}
		if !cur.inBody {
			if s == "" {
				cur.inBody = true
				return true
			}
			if k, v, ok := strings.Cut(s, ": "); ok {
				if _, dup := cur.fields[k]; !dup {
					cur.fields[k] = strings.TrimSpace(v)
				}
				return true
			}
			cur.inBody = true
		}
		if cur.size+len(s)+1 <= dropboxEntryMax {
			cur.body = append(cur.body, s)
			cur.size += len(s) + 1
		}
		return true
	})
	finish()
	return out, err
}

// eachLine calls f with each line of r, without its line ending, until f
// returns false. A line longer than logcatLineMax is cut.
func eachLine(r io.Reader, f func(string) bool) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var long []byte
	for {
		b, err := br.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			if len(long) < logcatLineMax {
				long = append(long, b[:min(len(b), logcatLineMax-len(long))]...)
			}
			continue
		}
		if len(b) > 0 || len(long) > 0 {
			if len(long) > 0 {
				b = append(long, b[:min(len(b), max(logcatLineMax-len(long), 0))]...)
				long = long[:0]
			}
			if !f(strings.TrimRight(string(b), "\r\n")) {
				return nil
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// dropboxReport makes a report of an entry of the app, by its UID (or, on
// phones whose entries have none, its package).
func dropboxReport(e *dropboxEntry, pkg string, uid int) (driver.CrashReport, bool) {
	if u, ok := e.fields["UID"]; ok {
		if n, err := strconv.Atoi(u); err != nil || n != uid {
			return driver.CrashReport{}, false
		}
	} else if p, _, _ := strings.Cut(e.fields["Package"], " "); p != pkg || uid >= 100000 {
		return driver.CrashReport{}, false
	}
	r := driver.CrashReport{Time: e.at, Kind: dropboxTags[e.tag], Process: e.fields["Process"]}
	if t, err := time.Parse("2006-01-02 15:04:05.000-0700", e.fields["Timestamp"]); err == nil {
		r.Time = t
	}
	r.PID, _ = strconv.Atoi(e.fields["PID"])
	switch e.tag {
	case "data_app_anr":
		r.Summary, r.Text = anrReport(e.body, r.PID)
		if r.Summary == "" {
			r.Summary = e.fields["Subject"]
		}
	case "data_app_native_crash":
		r.Text = strings.TrimSpace(strings.Join(e.body, "\n"))
		r.Summary = nativeSummary(e.body)
	default:
		r.Text = strings.TrimSpace(strings.Join(e.body, "\n"))
		for _, l := range e.body {
			if l = strings.TrimSpace(l); l != "" {
				r.Summary = l
				break
			}
		}
	}
	return r, true
}

// anrReport cuts an ANR entry down to what is the app's own: the reason
// (Subject) and the app's threads (the "----- pid <pid> at" section), not
// the CPU use of other processes or their threads.
func anrReport(body []string, pid int) (summary, text string) {
	start := fmt.Sprintf("----- pid %d at ", pid)
	end := fmt.Sprintf("----- end %d -----", pid)
	var b strings.Builder
	in := false
	for _, l := range body {
		switch {
		case summary == "" && strings.HasPrefix(l, "Subject: "):
			summary = strings.TrimPrefix(l, "Subject: ")
		case !in && pid > 0 && strings.HasPrefix(l, start):
			in = true
			b.WriteString(l + "\n")
		case in:
			b.WriteString(l + "\n")
			if l == end {
				return summary, strings.TrimSpace(b.String())
			}
		}
	}
	return summary, strings.TrimSpace(b.String())
}

// nativeSummary is the signal line of a native crash, with the abort
// message when there is one.
func nativeSummary(body []string) string {
	var sig, abort string
	for _, l := range body {
		switch {
		case sig == "" && strings.HasPrefix(l, "signal "):
			sig = l
		case abort == "" && strings.HasPrefix(l, "Abort message: "):
			abort = l
		}
	}
	if abort != "" {
		return strings.TrimSpace(sig + " · " + abort)
	}
	return sig
}

// crashBufferReports groups the app's crash buffer lines into Java crashes:
// AndroidRuntime's "FATAL EXCEPTION" and the lines of the same process
// after it.
func crashBufferReports(lines []driver.LogLine) []driver.CrashReport {
	var out []driver.CrashReport
	var cur *driver.CrashReport
	var b strings.Builder
	flush := func() {
		if cur != nil {
			cur.Text = strings.TrimSpace(b.String())
			out = append(out, *cur)
		}
		cur = nil
		b.Reset()
	}
	for _, l := range lines {
		if l.Tag != "AndroidRuntime" {
			continue
		}
		if strings.HasPrefix(l.Text, "FATAL EXCEPTION") {
			flush()
			cur = &driver.CrashReport{Time: l.Time, Kind: "crash", PID: l.PID}
		}
		if cur == nil || l.PID != cur.PID {
			continue
		}
		b.WriteString(l.Text + "\n")
		switch {
		case strings.HasPrefix(l.Text, "Process: "):
			p, _, _ := strings.Cut(strings.TrimPrefix(l.Text, "Process: "), ",")
			cur.Process = p
		case cur.Summary == "" && !strings.HasPrefix(l.Text, "FATAL EXCEPTION") && !strings.HasPrefix(l.Text, "\t"):
			cur.Summary = l.Text
		}
	}
	flush()
	return out
}

// DevBuild implements driver.AppLogger: an app is a development build when
// it is debuggable.
func (d *Device) DevBuild(ctx context.Context, app driver.App) (bool, error) {
	pkg, _, err := d.appUID(ctx, app)
	if err != nil {
		return false, err
	}
	out, err := d.client.Shell(ctx, d.info.ID, "dumpsys package "+adb.Quote(pkg))
	if err != nil {
		return false, wrapErr(err)
	}
	return debuggable(string(out), pkg), nil
}

var pkgFlags = regexp.MustCompile(`^\s*(?:pkg)?[Ff]lags=\[([^\]]*)\]`)

// debuggable reads `dumpsys package <pkg>`: the flags of the package's own
// entry, "Package [pkg] (...)", say DEBUGGABLE.
func debuggable(out, pkg string) bool {
	in := false
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "Package [") {
			in = strings.HasPrefix(t, "Package ["+pkg+"]")
			continue
		}
		if in {
			if m := pkgFlags.FindStringSubmatch(l); m != nil {
				return strings.Contains(" "+m[1]+" ", " DEBUGGABLE ")
			}
		}
	}
	return false
}

// appUID finds an app's package and its user id: the package's, in the
// clone's user for an app clone. Pinky Companion is never read, and an app
// that shares its user id with others is not: their lines could not be
// told apart.
func (d *Device) appUID(ctx context.Context, app driver.App) (pkg string, uid int, err error) {
	pkg, user := app.ID, 0
	if p, u, ok := driver.ParseCloneID(app.ID); ok {
		pkg, user = p, u
	}
	if pkg == companion.Release.Package || pkg == companion.Dev.Package {
		return "", 0, fmt.Errorf("%w: Pinky does not read its own app's logs", driver.ErrNotFound)
	}
	out, err := d.client.Shell(ctx, d.info.ID, "pm list packages -U")
	if err != nil {
		return "", 0, wrapErr(err)
	}
	uids := packageUIDs(string(out))
	n, ok := uids[pkg]
	if !ok {
		return "", 0, fmt.Errorf("%w: %s is not installed", driver.ErrNotFound, pkg)
	}
	for p, u := range uids {
		if u == n && p != pkg {
			return "", 0, fmt.Errorf("%w: %s shares its user id with %s, so its logs cannot be told apart from theirs", driver.ErrUnsupported, pkg, p)
		}
	}
	return pkg, user*100000 + n%100000, nil
}

// packageUIDs reads `pm list packages -U`: "package:<pkg> uid:<uid>".
func packageUIDs(out string) map[string]int {
	m := map[string]int{}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		p, ok := strings.CutPrefix(l, "package:")
		if !ok {
			continue
		}
		p, u, ok := strings.Cut(p, " uid:")
		if !ok {
			continue
		}
		u, _, _ = strings.Cut(u, ",")
		if n, err := strconv.Atoi(strings.TrimSpace(u)); err == nil {
			m[p] = n
		}
	}
	return m
}

var _ driver.AppLogger = (*Device)(nil)
