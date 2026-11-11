package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// phone_logs (docs/notes/app-logs.md): one app's own log lines and crash
// reports (driver.AppLogger), for an agent that tests the app. Logs show
// more than the screen, so:
//
//   - Only apps inside the agent's scope and the task's app list; never for
//     watch-only agents. Read-only agents and read-only mode may read them:
//     reading changes nothing on the phone.
//   - Never blocked apps, finance apps, password managers, the phone's own
//     system apps or Pinky's own apps on the phone (policy.LogsRefused).
//   - The first read of an app in a task asks the person (rule "App logs");
//     "Allow for this task" covers that app's logs and crash reports until
//     the task ends. A development build (debuggable, or signed for
//     debugging) does not ask: it is the app the agent is testing.
//   - History keeps the step ("Read 120 log lines of Example"), never the
//     lines.

// LogsRequest is phone_logs.
type LogsRequest struct {
	Call
	App  string // the app's name or id, as for phone_launch
	Kind string // logs (default) or crashes
	// Since is where to start: an RFC 3339 time, a time of day today
	// ("14:05"), or a duration back ("10m"). Empty means when the task
	// started.
	Since  string
	Filter string // keep lines whose tag or text contains it
	Level  string // keep lines of this level or more severe
	Limit  int    // lines or reports; 0 means the default
}

// Kinds of phone_logs.
const (
	LogsKindLines   = "logs"
	LogsKindCrashes = "crashes"
)

// Bounds of a phone_logs result.
const (
	LogsDefault    = 200
	LogsMax        = 500
	CrashesDefault = 3
	CrashesMax     = 10
	// logsMaxBytes bounds the text of the lines or reports in one result.
	logsMaxBytes = 64 * 1024
	// logLineMax bounds one line's text; a longer one is cut.
	logLineMax = 2000
)

// Logs implements phone_logs.
func (s *Session) Logs(ctx context.Context, req LogsRequest) *Result {
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	switch kind {
	case "", "log", "lines":
		kind = LogsKindLines
	case "crash", "crashes":
		kind = LogsKindCrashes
	default:
		return errResult(ErrInvalidArgument, "kind must be logs or crashes.")
	}
	level := driver.LogLevel(strings.ToLower(strings.TrimSpace(req.Level)))
	if level == "warning" {
		level = driver.LevelWarn
	}
	if level != "" && level.Rank() < 0 {
		return errResult(ErrInvalidArgument, "level must be verbose, debug, info, warn, error or fatal.")
	}
	limit, most := LogsDefault, LogsMax
	if kind == LogsKindCrashes {
		limit, most = CrashesDefault, CrashesMax
	}
	switch {
	case req.Limit < 0:
		return errResult(ErrInvalidArgument, "limit must be positive.")
	case req.Limit > most:
		limit = most
	case req.Limit > 0:
		limit = req.Limit
	}
	name := strings.TrimSpace(req.App)
	if name == "" {
		return errResult(ErrInvalidArgument, "app is required: the name or id of the app whose logs to read, as phone_apps lists it.")
	}
	l, res := s.acquire(req.Device)
	if res != nil {
		return res
	}
	defer s.done(l)
	g := s.g
	ctx, cancel := g.callCtx(ctx, l)
	defer cancel()
	if res := g.gate(l, req.ApprovalID, false); res != nil {
		return g.finish(l, res)
	}
	now := time.Now()
	g.mu.Lock()
	started, taskApps := l.task.started, append([]string(nil), l.apps...)
	g.mu.Unlock()
	since, err := parseSince(req.Since, now, started)
	if err != nil {
		return g.finish(l, errResult(ErrInvalidArgument, "%v", err))
	}
	d := l.dev
	dev := g.driverOf(d)
	if dev == nil {
		return g.finish(l, errResult(ErrDeviceOffline, "%s is offline.", d.name()))
	}
	lg, ok := dev.(driver.AppLogger)
	if !ok {
		return g.finish(l, errResult(ErrUnsupported, "Pinky cannot read app logs on %s.", d.name()))
	}
	scope := g.scopeOf(s)
	if scope.Mode == ScopeWatchOnly {
		return g.finish(l, errResult(ErrScopeDenied, "This agent may only watch the screen: the person gave it watch-only access, and logs show more than the screen."))
	}
	apps, err := dev.Apps(ctx)
	if err != nil {
		return g.finish(l, g.filesError(l, err))
	}
	m := findApp(apps, name)
	if !m.ok {
		if len(m.similar) > 0 {
			return g.finish(l, errResult(ErrNotFound, "No installed app is named %q. Similar: %s. Call phone_logs again with the exact name or id of the one you mean.",
				name, appList(m.similar)))
		}
		return g.finish(l, errResult(ErrNotFound, "No installed app matches %q; call phone_apps to see what is installed.", name))
	}
	app := m.app
	orig := app.Original()
	sa := screen.App{ID: orig.ID, Name: orig.Name}
	label := appLabel(screen.App{ID: app.ID, Name: app.Name})
	args := argsJSON(map[string]any{"app": app.ID, "kind": kind, "since": req.Since, "filter": req.Filter, "level": string(level), "limit": limit})
	what := "read the logs of " + label
	start := time.Now()
	refuse := func(res *Result) *Result {
		text := what
		switch {
		case res.Status == StatusBlocked:
			text = "Blocked by “" + res.Rule + "”: " + what
		case res.Error == ErrScopeDenied:
			text = "Outside the scope: " + what
		}
		g.recordLogs(l, args, text, res, start)
		return g.finish(l, res)
	}
	switch {
	case scope.Mode == ScopeApps && !appListed(sa, scope.Apps):
		return refuse(errResult(ErrScopeDenied, "%s is outside this agent's scope.", label))
	case len(taskApps) > 0 && !appListed(sa, taskApps):
		return refuse(errResult(ErrScopeDenied, "%s is not in this task's app list (%s).", label, strings.Join(taskApps, ", ")))
	}
	if rule, refused := g.logsRefused(orig); refused {
		return refuse(&Result{Status: StatusBlocked, Rule: rule,
			Message: fmt.Sprintf("Pinky never gives agents the logs of %s: no logs of blocked apps, finance apps, password managers, the phone's own apps or Pinky's own apps.", label)})
	}
	if dev, err := lg.DevBuild(ctx, app); err != nil || !dev {
		if res := g.askForLogs(ctx, l, req.Call, app, label, args); res != nil {
			return g.finish(l, res)
		}
	}
	q := driver.LogQuery{App: app, Since: since, Max: limit}
	if kind == LogsKindCrashes {
		reports, err := lg.AppCrashes(ctx, q)
		if err != nil {
			res := g.filesError(l, err)
			g.recordLogs(l, args, "Failed: "+what, res, start)
			return g.finish(l, res)
		}
		res := crashesResult(reports, label, since, now)
		g.recordLogs(l, args, fmt.Sprintf("Read %s of %s", count(len(reports), "crash report"), label), res, start)
		return g.finish(l, res)
	}
	q.Filter, q.Level = req.Filter, level
	g.mu.Lock()
	l.logsRead = true
	g.mu.Unlock()
	lr, err := lg.AppLogs(ctx, q)
	if err != nil {
		res := g.filesError(l, err)
		g.recordLogs(l, args, "Failed: "+what, res, start)
		return g.finish(l, res)
	}
	_, listens := dev.(driver.LogListener)
	res = linesResult(lr, label, since, now, listens)
	g.recordLogs(l, args, fmt.Sprintf("Read %s of %s", count(len(lr.Lines), "log line"), label), res, start)
	return g.finish(l, res)
}

// logsRefused says whether an app's logs never reach agents (see
// policy.LogsRefused), adding the phone's system apps and Pinky's own.
func (g *Gateway) logsRefused(app driver.App) (string, bool) {
	if rule, refused := g.policy.LogsRefused(screen.App{ID: app.ID, Name: app.Name}); refused {
		return rule, true
	}
	if app.System || pinkyApp(app.ID) {
		return policy.RuleAppLogs, true
	}
	return "", false
}

// pinkyApp reports whether an id is one of Pinky's own apps on the phone:
// Pinky Companion, or the WebDriverAgent helper on an iPhone.
func pinkyApp(id string) bool {
	return id == "app.pinky.companion" || strings.HasPrefix(id, "app.pinky.companion.") ||
		strings.HasSuffix(id, ".xctrunner")
}

// askForLogs asks the person before an agent reads an app's logs, unless
// they allowed it for the task, or always allow it for the agent on this
// phone (standing.go). It returns nil once allowed.
func (g *Gateway) askForLogs(ctx context.Context, l *lease, c Call, app driver.App, label, args string) *Result {
	g.mu.Lock()
	granted := l.logsGranted[app.ID]
	g.mu.Unlock()
	sa := screen.App{ID: app.ID, Name: app.Name}
	if granted || g.standingAllowed(l, policy.RuleAppLogs, sa) {
		return nil
	}
	s := l.session
	b := approval.Binding{Session: s.id, Device: l.dev.id, Lease: l.id, Tool: "phone_logs", Args: digest(app.ID)}
	id := c.ApprovalID
	if id != "" {
		if res := g.checkApproval(id, b); res != nil {
			if res.Error != "" {
				return res
			}
			id = ""
		}
	}
	var info approval.Info
	if id == "" {
		title := fmt.Sprintf("Read the logs of %s?", label)
		detail := s.agent.Name + " · " + l.task.title
		if i := strings.TrimSpace(c.Intent); i != "" {
			detail = s.agent.Name + " · " + i
		}
		info = g.approvals.Open(approval.Request{Kind: approval.KindAction, Binding: b, Rule: policy.RuleAppLogs, Title: title,
			Detail: detail, App: label, Agent: s.agent.Name, TaskID: l.task.id, DeviceName: l.dev.name(),
			Data: &standingAsk{rule: policy.RuleAppLogs, agent: s.agent.Name, device: l.dev.id, deviceName: l.dev.name(), app: sa}})
		if res := g.afterOpen(l, info); res != nil {
			return res
		}
		g.openStep(l, info, "phone_logs", args, "Waiting for you: "+strings.TrimSuffix(title, "?"), screen.Rect{}, screen.Point{}, "")
	} else {
		info, _ = g.approvals.Get(id)
	}
	out, err := g.waitApproval(ctx, l, info.ID, info, c.Progress)
	if errors.Is(err, errTaken) {
		return takenResult(info.ID)
	}
	if err != nil {
		return g.cancelled(l, info.ID)
	}
	switch {
	case out.State == approval.Open:
		return g.pendingResult(l, info)
	case out.State == approval.Withdrawn:
		g.closeStep(info.ID, string(StatusDenied), 0)
		return g.withdrawnResult(l, out)
	}
	g.recordDecision(info, out)
	if !out.Decision.Allows() {
		g.closeStep(info.ID, string(StatusDenied), out.Waited)
		return &Result{Status: StatusDenied, Rule: policy.RuleAppLogs, Note: denyNote(out),
			Message: fmt.Sprintf("The person did not allow reading the logs of %s.", label)}
	}
	g.closeStep(info.ID, "allowed", out.Waited)
	if out.Decision != approval.AllowOnce {
		g.mu.Lock()
		if l.logsGranted == nil {
			l.logsGranted = map[string]bool{}
		}
		l.logsGranted[app.ID] = true
		g.mu.Unlock()
	}
	return nil
}

// recordLogs adds a phone_logs step to the task: what was read, never the
// lines themselves.
func (g *Gateway) recordLogs(l *lease, args, text string, res *Result, start time.Time) {
	status := string(res.Status)
	if res.Error != "" {
		status = string(res.Error)
	}
	g.addStep(l, StepView{Tool: "phone_logs", Args: args, Text: text, Status: status, At: start, Elapsed: time.Since(start)})
}

// parseSince reads phone_logs' since: an RFC 3339 time, a date and time
// in the computer's time zone, a time of day (the last one that passed),
// or a duration back ("10m", "2h", "1d"). Empty means the task's start.
func parseSince(s string, now, taskStart time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		if taskStart.IsZero() {
			return now, nil
		}
		return taskStart, nil
	}
	bad := fmt.Errorf("since %q is not a time: use an RFC 3339 time (2026-11-09T14:05:00+08:00), a time of day (14:05) or a duration back (10m, 2h, 1d)", s)
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 0 {
			return time.Time{}, bad
		}
		return now.AddDate(0, 0, -days), nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			d = -d
		}
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		if t.After(now) {
			return time.Time{}, fmt.Errorf("since %s is in the future", s)
		}
		return t, nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			if t.After(now) {
				return time.Time{}, fmt.Errorf("since %s is in the future", s)
			}
			return t, nil
		}
	}
	for _, layout := range []string{"15:04:05", "15:04"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			y, mo, d := now.Date()
			at := time.Date(y, mo, d, t.Hour(), t.Minute(), t.Second(), 0, now.Location())
			if at.After(now) {
				at = at.AddDate(0, 0, -1)
			}
			return at, nil
		}
	}
	return time.Time{}, bad
}

// when says a time for a result's message: the time of day when it is
// today, else the date as well.
func when(t, now time.Time) string {
	t = t.In(now.Location())
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04:05")
	}
	return t.Format("2006-01-02 15:04:05")
}

// logLine writes one line as "15:04:05.123 4321 E Tag: text", its time in
// the computer's time zone, cutting a very long text.
func logLine(l driver.LogLine, loc *time.Location) string {
	text := l.Text
	if len(text) > logLineMax {
		cut := logLineMax
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = fmt.Sprintf("%s… (%d more bytes)", text[:cut], len(text)-cut)
	}
	text = strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\n    ")
	tag := l.Tag
	if tag != "" {
		tag += ": "
	}
	return fmt.Sprintf("%s %d %s %s%s", l.Time.In(loc).Format("15:04:05.000"), l.PID, l.Level.Letter(), tag, text)
}

// linesResult says an app's log lines, oldest first. When they are more
// than logsMaxBytes, the newest that fit are kept.
func linesResult(lr driver.LogResult, label string, since, now time.Time, listens bool) *Result {
	var kept []string
	size := 0
	for i := len(lr.Lines) - 1; i >= 0; i-- {
		s := logLine(lr.Lines[i], now.Location())
		if size+len(s)+1 > logsMaxBytes {
			break
		}
		size += len(s) + 1
		kept = append(kept, s)
	}
	cut := len(lr.Lines) - len(kept)
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	from := since
	if lr.From.After(since) {
		from = lr.From
	}
	res := &Result{Status: StatusSettled, Log: strings.Join(kept, "\n")}
	if len(kept) == 0 {
		res.Message = fmt.Sprintf("No log lines of %s since %s.", label, when(from, now))
	} else {
		res.Message = fmt.Sprintf("%s of %s since %s, oldest first.", count(len(kept), "log line"), label, when(from, now))
	}
	if left := lr.Dropped + cut; left > 0 {
		res.Message += fmt.Sprintf(" %s left out: these are the newest. Narrow with since, filter or level to see others.", count(left, "older line"))
	}
	if listens && lr.From.After(since) {
		res.Message += fmt.Sprintf(" This phone keeps no log history Pinky can read: Pinky listens to %s from %s, the first phone_logs call for it in this task. Make the app do what you want to see, then call phone_logs again.", label, when(lr.From, now))
	}
	return res
}

// crashesResult says an app's crash reports, newest first: every report's
// heading, and their texts whole while they fit in logsMaxBytes; then the
// first one cut short, or the first lines of later ones.
func crashesResult(reports []driver.CrashReport, label string, since, now time.Time) *Result {
	heads := make([]string, len(reports))
	room := logsMaxBytes
	for i, r := range reports {
		head := fmt.Sprintf("── %s · %s", r.Kind, r.Time.In(now.Location()).Format("2006-01-02 15:04:05"))
		if r.PID > 0 {
			head += fmt.Sprintf(" · pid %d", r.PID)
		}
		if r.Process != "" {
			head += " · " + r.Process
		}
		// The summary, unless the text starts with it (a Java crash does).
		if r.Summary != "" && !strings.HasPrefix(r.Text, r.Summary) {
			head += "\n" + r.Summary
		}
		heads[i] = head
		room -= len(head) + 2
	}
	// cut keeps the start of a text that fits in n bytes, ending at a line
	// when byLine is set, and says how much was left out.
	const note = 40 // "\n… (123456 more bytes left out)"
	cut := func(text string, n int, byLine bool) string {
		n -= note
		if n <= 0 {
			return ""
		}
		if byLine {
			n = strings.LastIndexByte(text[:n], '\n')
			if n <= 0 {
				return ""
			}
		}
		for n > 0 && !utf8.RuneStart(text[n]) {
			n--
		}
		return fmt.Sprintf("%s\n… (%d more bytes left out)", strings.TrimRight(text[:n], "\n"), len(text)-n)
	}
	// Each report keeps room for the first lines of the ones after it, so
	// every report shows what crashed and where.
	const keep = 1000
	texts := make([]string, len(reports))
	later := make([]int, len(reports)+1)
	for i := len(reports) - 1; i >= 0; i-- {
		texts[i] = strings.TrimSpace(reports[i].Text)
		later[i] = later[i+1]
		if texts[i] != "" {
			later[i] += min(len(texts[i])+1, keep)
		}
	}
	var b strings.Builder
	short := 0
	for i, text := range texts {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(heads[i])
		free := room - later[i+1] - 1
		var part string
		switch {
		case text == "":
		case len(text) <= free:
			part = text
		case i == 0 && free > keep:
			part = cut(text, free, false)
		default:
			short++
			part = cut(text, min(free, 2*keep), true)
		}
		if part != "" {
			b.WriteString("\n" + part)
			room -= len(part) + 1
		}
	}
	res := &Result{Status: StatusSettled, Log: b.String()}
	if len(reports) == 0 {
		res.Message = fmt.Sprintf("No crash reports of %s since %s.", label, when(since, now))
		return res
	}
	res.Message = fmt.Sprintf("%s of %s since %s, newest first.", count(len(reports), "crash report"), label, when(since, now))
	switch {
	case short == 1:
		res.Message += " One report shows only its first lines, to keep the result short."
	case short > 1:
		res.Message += fmt.Sprintf(" %d reports show only their first lines, to keep the result short.", short)
	}
	return res
}
