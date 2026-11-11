package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

// How crash capture works: each process that captures crashes (the app and
// the daemon) creates crashes/<process>-<start>-<pid>.run, writes a JSON
// header line (version, system, start time), holds a lock on the file and
// hands it to runtime/debug.SetCrashOutput, so the Go runtime appends any
// unrecovered panic or fatal error after the header. A clean exit removes
// the file. At the next start, a .run file whose lock is free belongs to a
// process that is gone: with text after the header it crashed, and becomes
// a crash report (crashes/<id>.json) that goes out while usage statistics
// are on; with none, it ended without quitting (killed, a power cut, a
// hang the person force-quit), which only counts in usage statistics.

// CrashReport is a crash of an earlier run.
type CrashReport struct {
	ID        string    `json:"id"`
	Process   string    `json:"process"` // "app" or "daemon"
	Version   string    `json:"version"`
	OS        string    `json:"os"`
	OSVersion string    `json:"os_version,omitempty"`
	Arch      string    `json:"arch"`
	GoVersion string    `json:"go_version,omitempty"`
	Started   time.Time `json:"started"`
	Crashed   time.Time `json:"crashed"`
	// Reason is the crash's first line, such as "panic: runtime error:
	// index out of range [3] with length 3".
	Reason string `json:"reason"`
	// Stack is what the runtime printed, with the home folder, e-mail and
	// IP addresses and similar taken out (Redactor).
	Stack string `json:"stack"`
}

// crashHeader is the first line of a .run file.
type crashHeader struct {
	Process   string    `json:"process"`
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
	OS        string    `json:"os"`
	OSVersion string    `json:"os_version,omitempty"`
	Arch      string    `json:"arch"`
	GoVersion string    `json:"go_version"`
}

// CrashOptions configure CaptureCrashes.
type CrashOptions struct {
	// Dir is Pinky's data folder; crash files go in Dir/crashes.
	Dir     string
	Process string // "app" or "daemon"
	Version string
	Log     *slog.Logger
	Now     func() time.Time
}

// Crashes is this process's crash capture, with what earlier runs left.
type Crashes struct {
	dir  string
	log  *slog.Logger
	file *os.File
	path string
	once sync.Once

	found   []CrashReport // reports made from earlier runs at this start
	unclean []string      // processes of earlier runs that ended without quitting
}

// Keep at most this many reports, for this long.
const (
	maxPendingCrashes = 10
	keepCrashes       = 30 * 24 * time.Hour
)

// CaptureCrashes looks at what earlier runs left in opt.Dir/crashes and
// starts writing this process's crash output there. Call Stop on a clean
// exit. It never fails: without a crash folder, crashes are not captured.
func CaptureCrashes(opt CrashOptions) *Crashes {
	if opt.Log == nil {
		opt.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Process == "" {
		opt.Process = "app"
	}
	c := &Crashes{dir: filepath.Join(opt.Dir, "crashes"), log: opt.Log}
	if opt.Dir == "" {
		return c
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		c.log.Warn("telemetry: crash folder", "err", err)
		return c
	}
	c.scan(opt.Now())
	c.start(opt)
	return c
}

// Found returns the crash reports this start made of earlier runs.
func (c *Crashes) Found() []CrashReport {
	if c == nil {
		return nil
	}
	return c.found
}

// Unclean returns the processes ("app", "daemon") of earlier runs that
// ended without quitting and without a crash.
func (c *Crashes) Unclean() []string {
	if c == nil {
		return nil
	}
	return c.unclean
}

// Dir is the crash folder.
func (c *Crashes) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}

// Stop ends the capture on a clean exit and removes this run's file.
func (c *Crashes) Stop() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		if c.file == nil {
			return
		}
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
		c.file.Close()
		os.Remove(c.path)
	})
}

// Exit ends the capture as the process's function returns, deferred
// first thing so that it runs last:
//
//	defer func() { crashes.Exit(recover()) }()
//
// On a clean exit (r is nil) it is Stop. A panic, though, runs deferred
// calls on its way up, and a deferred Stop removed this run's file before
// the runtime wrote the panic to it: the crash left no report (Settings ›
// Safety quit Pinky so in November 2026). With a panic, Exit leaves the
// file and panics again with r, which the runtime writes to the file with
// the stack of the first panic still under it.
func (c *Crashes) Exit(r any) {
	if r != nil {
		panic(r)
	}
	c.Stop()
}

func (c *Crashes) start(opt CrashOptions) {
	sys := System()
	now := opt.Now()
	h := crashHeader{Process: opt.Process, Version: opt.Version, PID: os.Getpid(), Started: now,
		OS: sys.OS, OSVersion: sys.OSVersion, Arch: sys.Arch, GoVersion: sys.GoVersion}
	name := fmt.Sprintf("%s-%d-%d.run", cleanPart(opt.Process), now.UnixMilli(), os.Getpid())
	path := filepath.Join(c.dir, name)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		c.log.Warn("telemetry: crash file", "err", err)
		return
	}
	if !tryLock(f) {
		f.Close()
		os.Remove(path)
		c.log.Warn("telemetry: crash file cannot be locked; crashes are not captured")
		return
	}
	line, _ := json.Marshal(h)
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		os.Remove(path)
		return
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		f.Close()
		os.Remove(path)
		c.log.Warn("telemetry: crash output", "err", err)
		return
	}
	c.file, c.path = f, path
}

// scan turns the files of earlier runs into reports, and drops old ones.
func (c *Crashes) scan(now time.Time) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		path := filepath.Join(c.dir, name)
		switch {
		case strings.HasSuffix(name, ".run"):
			c.scanRun(path, strings.TrimSuffix(name, ".run"))
		case strings.HasSuffix(name, ".tmp"), strings.HasSuffix(name, ".unclean"):
			if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > time.Hour {
				os.Remove(path)
			}
		}
	}
	pruneCrashes(c.dir, now)
}

func (c *Crashes) scanRun(path, id string) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return
	}
	if !tryLock(f) {
		f.Close() // its process still runs
		return
	}
	info, _ := f.Stat()
	data, _ := io.ReadAll(io.LimitReader(f, 4<<20))
	f.Close()
	os.Remove(path)

	head, rest, _ := bytes.Cut(data, []byte("\n"))
	var h crashHeader
	if err := json.Unmarshal(head, &h); err != nil {
		return // not ours, or cut short before the header was written
	}
	if len(bytes.TrimSpace(rest)) == 0 {
		// The client counts and removes the marker (countCrashes), even
		// when another process scanned the file.
		if mf, err := os.OpenFile(filepath.Join(c.dir, id+".unclean"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
			mf.Close()
		}
		c.unclean = append(c.unclean, nonEmpty(h.Process, "app"))
		return
	}
	crashed := h.Started
	if info != nil {
		crashed = info.ModTime()
	}
	r := CrashReport{ID: id, Process: nonEmpty(h.Process, "app"), Version: h.Version, OS: h.OS, OSVersion: h.OSVersion,
		Arch: h.Arch, GoVersion: h.GoVersion, Started: h.Started, Crashed: crashed}
	r.Stack, r.Reason = crashText(string(rest), NewRedactor())
	if strings.HasPrefix(r.Reason, "goroutine ") {
		// A fatal error, whose message the runtime printed before the
		// copy to the crash file began.
		r.Reason = fatalReason(r.Stack, filepath.Join(filepath.Dir(c.dir), h.Process+".log"), crashed)
	}
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return
	}
	// O_EXCL: another Pinky starting at the same moment may have read the
	// same file; one report is enough.
	rf, err := os.OpenFile(filepath.Join(c.dir, id+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return
	}
	_, werr := rf.Write(append(out, '\n'))
	if cerr := rf.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		c.log.Warn("telemetry: save crash report", "err", werr)
		return
	}
	c.log.Warn("telemetry: an earlier run crashed", "process", r.Process, "version", r.Version, "reason", r.Reason)
	c.found = append(c.found, r)
}

// crashText cleans what the runtime printed and finds its first line.
func crashText(s string, red *Redactor) (stack, reason string) {
	s = strings.TrimSpace(strings.ToValidUTF8(s, "?"))
	if len(s) > maxStack {
		s = s[:maxStack] + "\n… (cut)"
	}
	s = red.String(s)
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			reason = line
			break
		}
	}
	if len(reason) > 300 {
		reason = reason[:300] + "…"
	}
	return s, reason
}

// fatalReason names a fatal error whose message the crash file lacks: the
// runtime prints "fatal error: …" before it starts copying to the crash
// output. A daemon started by `pinky mcp` writes its standard error to
// daemon.log, where the line is when the log changed at the crash;
// otherwise the reason is the function that failed, from the stack.
func fatalReason(stack, stderrLog string, crashed time.Time) string {
	if info, err := os.Stat(stderrLog); err == nil && info.ModTime().Sub(crashed).Abs() < time.Minute {
		if tail, err := readTail(stderrLog, 64<<10, 400); err == nil {
			lines := strings.Split(tail, "\n")
			for i := len(lines) - 1; i >= 0; i-- {
				if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "fatal error: ") {
					_, reason := crashText(l, NewRedactor())
					return reason
				}
			}
		}
	}
	lines := strings.Split(stack, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "goroutine ") && i+1 < len(lines) {
			fn := strings.TrimSpace(lines[i+1])
			if j := strings.LastIndexByte(fn, '('); j > 0 {
				fn = fn[:j]
			}
			return "fatal error in " + fn
		}
	}
	return "fatal error"
}

// pruneCrashes keeps the newest reports, none older than keepCrashes.
func pruneCrashes(dir string, now time.Time) {
	reports := readCrashes(dir)
	for i, r := range reports {
		if i >= maxPendingCrashes || now.Sub(r.Crashed) > keepCrashes {
			os.Remove(filepath.Join(dir, r.ID+".json"))
		}
	}
}

// readCrashes returns the reports in dir, newest first.
func readCrashes(dir string) []CrashReport {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []CrashReport
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r CrashReport
		if json.Unmarshal(data, &r) != nil || r.ID+".json" != e.Name() {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Crashed.After(out[j].Crashed) })
	return out
}

// --- on the client ---

// countCrashes adds to the usage counters the crashes and unclean exits
// that this or another process found: reports not counted before, and
// the markers of unclean exits, which it removes.
func (c *Client) countCrashes(cr *Crashes) {
	dir := cr.Dir()
	if dir == "" {
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".unclean"); ok {
			os.Remove(filepath.Join(dir, e.Name()))
			process, _, _ := strings.Cut(id, "-")
			c.Count(Key("exits", "unclean", process))
		}
	}
	reports := readCrashes(dir)
	c.mu.Lock()
	counted := map[string]bool{}
	for _, id := range c.st.Counted {
		counted[id] = true
	}
	var keep, fresh []string
	for _, r := range reports {
		keep = append(keep, r.ID)
		if !counted[r.ID] {
			fresh = append(fresh, r.Process)
		}
	}
	if len(fresh) > 0 || len(keep) != len(c.st.Counted) {
		c.st.Counted = keep
		c.dirty = true
	}
	c.mu.Unlock()
	for _, p := range fresh {
		c.Count(Key("crashes", p))
	}
}

// Crashes returns the crash reports waiting to be sent or dismissed,
// newest first.
func (c *Client) Crashes() []CrashReport {
	if c == nil || c.opt.Crashes == nil {
		return nil
	}
	return readCrashes(c.opt.Crashes.Dir())
}

// DiscardCrash forgets a crash report without sending it.
func (c *Client) DiscardCrash(id string) {
	if c == nil || c.opt.Crashes == nil || !validID(id) {
		return
	}
	os.Remove(filepath.Join(c.opt.Crashes.Dir(), id+".json"))
}

// SendCrash sends a crash report and forgets it. A report the backend
// refuses is forgotten too. Run sends them while usage statistics are on.
func (c *Client) SendCrash(ctx context.Context, id string) error {
	if c == nil || c.backend == nil {
		return ErrNoBackend
	}
	if c.opt.Crashes == nil || !validID(id) {
		return errors.New("no such crash report")
	}
	var r *CrashReport
	for _, x := range c.Crashes() {
		if x.ID == id {
			r = &x
			break
		}
	}
	if r == nil {
		return errors.New("no such crash report")
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := c.backend.Crash(sctx, *r)
	if err == nil || Permanent(err) {
		c.DiscardCrash(id)
	}
	return err
}

// sendCrashes sends the waiting crash reports while usage statistics are
// on; after a failure it waits an hour. With them off, reports stay on this
// computer (for feedback's diagnostics) until they are 30 days old.
func (c *Client) sendCrashes(ctx context.Context) {
	c.mu.Lock()
	auto := c.usage && !c.closed && c.backend != nil && !DoNotTrack()
	wait := c.now().Before(c.crashRetry)
	c.mu.Unlock()
	if !auto || wait {
		return
	}
	for _, r := range c.Crashes() {
		if err := c.SendCrash(ctx, r.ID); err != nil && !Permanent(err) {
			c.mu.Lock()
			c.crashRetry = c.now().Add(time.Hour)
			c.mu.Unlock()
			c.log.Info("telemetry: crash report not sent; trying later", "err", err)
			return
		}
	}
}

// validID keeps report ids to the names crash capture makes.
func validID(id string) bool {
	if id == "" || len(id) > 100 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
