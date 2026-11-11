package ios

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	goios "github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/ostrace"
)

const (
	testDir   = "/private/var/containers/Bundle/Application/AAAA-1111/Example.app"
	testExe   = testDir + "/Example"
	otherExe  = "/private/var/containers/Bundle/Application/BBBB-2222/Other.app/Other"
	movedDir  = "/private/var/containers/Bundle/Application/CCCC-3333/Example.app"
	widgetExe = testDir + "/PlugIns/Widget.appex/Widget"
)

func TestLookupResult(t *testing.T) {
	m := map[string]any{"Status": "Complete", "LookupResult": map[string]any{"dev.pinky.example": map[string]any{
		"CFBundleIdentifier": "dev.pinky.example", "CFBundleExecutable": "Example", "Path": testDir + "/",
		"Entitlements": map[string]any{"get-task-allow": true, "application-identifier": "TEAM.dev.pinky.example"},
	}}}
	b, ok := lookupResult(m, "dev.pinky.example")
	if !ok || b.Path != testDir || b.Executable != "Example" || !b.Dev || b.ID != "dev.pinky.example" {
		t.Fatalf("%+v %v", b, ok)
	}
	m["LookupResult"].(map[string]any)["dev.pinky.example"].(map[string]any)["Entitlements"] = map[string]any{}
	if b, _ := lookupResult(m, "dev.pinky.example"); b.Dev {
		t.Error("a build without get-task-allow is a development build")
	}
	if _, ok := lookupResult(m, "com.someone.else"); ok {
		t.Error("found an app that is not there")
	}
	if _, ok := lookupResult(map[string]any{"Status": "Complete"}, "dev.pinky.example"); ok {
		t.Error("found an app in an empty answer")
	}
}

func TestLookupRefusesHelper(t *testing.T) {
	if _, err := lookupBundle(context.Background(), goios.DeviceEntry{}, "com.facebook.WebDriverAgentRunner.xctrunner"); err == nil {
		t.Fatal("looked up Pinky's helper")
	}
}

func TestInBundle(t *testing.T) {
	for _, c := range []struct {
		exe, dir string
		want     bool
	}{
		{testExe, testDir, true},
		{strings.TrimPrefix(testExe, "/private"), testDir, true},
		{testExe, strings.TrimPrefix(testDir, "/private"), true},
		{widgetExe, testDir, true},
		{otherExe, testDir, false},
		{testDir + "2/Example", testDir, false},
		{"", testDir, false},
		{testExe, "", false},
	} {
		if got := inBundle(c.exe, c.dir); got != c.want {
			t.Errorf("inBundle(%q, %q) = %v", c.exe, c.dir, got)
		}
	}
}

func TestEntryLine(t *testing.T) {
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		e     ostrace.LogEntry
		level driver.LogLevel
		tag   string
	}{
		{ostrace.LogEntry{Level: ostrace.LogLevelDefault, ImageName: testExe}, driver.LevelInfo, "Example"},
		{ostrace.LogEntry{Level: ostrace.LogLevelInfo, Label: &ostrace.LogLabel{Subsystem: "dev.pinky.example", Category: "net"}}, driver.LevelInfo, "dev.pinky.example:net"},
		{ostrace.LogEntry{Level: ostrace.LogLevelDebug, Label: &ostrace.LogLabel{Subsystem: "dev.pinky.example"}}, driver.LevelDebug, "dev.pinky.example"},
		{ostrace.LogEntry{Level: ostrace.LogLevelError, ImageName: "/usr/lib/libobjc.A.dylib"}, driver.LevelError, "libobjc.A.dylib"},
		{ostrace.LogEntry{Level: ostrace.LogLevelFault}, driver.LevelFatal, ""},
		{ostrace.LogEntry{Level: ostrace.LogLevelUserAction}, driver.LevelInfo, ""},
	} {
		c.e.PID, c.e.Timestamp, c.e.Message = 42, at, "hello"
		l := entryLine(c.e)
		if l.Level != c.level || l.Tag != c.tag || l.PID != 42 || !l.Time.Equal(at) || l.Text != "hello" {
			t.Errorf("%+v -> %+v", c.e, l)
		}
	}
	long := entryLine(ostrace.LogEntry{Message: strings.Repeat("x", logTextMax+10)})
	if len(long.Text) > logTextMax+len("…") {
		t.Errorf("a long line kept %d bytes", len(long.Text))
	}
}

// logPhoneFake lists processes and opens streams a test feeds.
type logPhoneFake struct {
	mu        sync.Mutex
	procs     map[int]string
	streams   map[int]*fakeLogStream
	opened    []int
	dir       string // what relocate finds
	relocated int
}

func newLogPhoneFake(procs map[int]string) *logPhoneFake {
	return &logPhoneFake{procs: procs, streams: map[int]*fakeLogStream{}}
}

func (p *logPhoneFake) source() logSource {
	return logSource{
		open: func(pid int) (logStream, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			s := &fakeLogStream{ch: make(chan ostrace.LogEntry, 16), closed: make(chan struct{})}
			p.streams[pid] = s
			p.opened = append(p.opened, pid)
			return s, nil
		},
		procs: func(context.Context) (map[int]string, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			m := map[int]string{}
			for k, v := range p.procs {
				m[k] = v
			}
			return m, nil
		},
		relocate: func(context.Context) (string, error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.relocated++
			return p.dir, nil
		},
	}
}

func (p *logPhoneFake) setProcs(m map[int]string) {
	p.mu.Lock()
	p.procs = m
	p.mu.Unlock()
}

func (p *logPhoneFake) stream(pid int) *fakeLogStream {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.streams[pid]
}

func (p *logPhoneFake) openedPIDs() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.opened)
}

type fakeLogStream struct {
	ch     chan ostrace.LogEntry
	closed chan struct{}
	once   sync.Once
}

func (s *fakeLogStream) ReadEntry() (ostrace.LogEntry, error) {
	select {
	case <-s.closed:
		return ostrace.LogEntry{}, io.EOF
	default:
	}
	select {
	case e := <-s.ch:
		return e, nil
	case <-s.closed:
		return ostrace.LogEntry{}, io.EOF
	}
}

func (s *fakeLogStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func (s *fakeLogStream) isClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

// waitUntil waits for cond, a second at most.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func lineCount(f *appFollower) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.lines)
}

func TestFollower(t *testing.T) {
	phone := newLogPhoneFake(map[int]string{10: testExe, 20: otherExe, 30: widgetExe})
	f := newFollower(testDir, phone.source())
	f.gone = 0
	defer f.stop()
	f.scan()
	if got := phone.openedPIDs(); !slices.Equal(sorted(got), []int{10, 30}) {
		t.Fatalf("opened %v, want the app's and its widget's", got)
	}
	f.scan()
	if got := phone.openedPIDs(); len(got) != 2 {
		t.Fatalf("opened again: %v", got)
	}

	base := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	s := phone.stream(10)
	for _, e := range []ostrace.LogEntry{
		{PID: 10, Timestamp: base, Level: ostrace.LogLevelDefault, Message: "started", Filename: testExe},
		{PID: 11, Timestamp: base, Message: "another process's line"},
		{PID: 10, Timestamp: base.Add(time.Second), Message: "from a binary outside the app", Filename: otherExe},
		{PID: 10, Timestamp: base.Add(2 * time.Second), Level: ostrace.LogLevelError, Message: "request failed", Label: &ostrace.LogLabel{Subsystem: "dev.pinky.example", Category: "net"}},
		{PID: 10, Timestamp: base.Add(3 * time.Second), Level: ostrace.LogLevelDebug, Message: "tapped Buy"},
	} {
		s.ch <- e
	}
	phone.stream(30).ch <- ostrace.LogEntry{PID: 30, Timestamp: base.Add(1500 * time.Millisecond), Message: "widget refreshed", Filename: widgetExe}
	waitUntil(t, "lines", func() bool { return lineCount(f) == 4 })

	res := f.query(driver.LogQuery{})
	var texts []string
	for _, l := range res.Lines {
		texts = append(texts, l.Text)
	}
	if want := []string{"started", "widget refreshed", "request failed", "tapped Buy"}; !slices.Equal(texts, want) {
		t.Fatalf("lines %q, want %q", texts, want)
	}
	if !res.From.Equal(f.start) || res.Dropped != 0 {
		t.Errorf("from %v dropped %d", res.From, res.Dropped)
	}
	if res := f.query(driver.LogQuery{Since: base.Add(time.Second)}); len(res.Lines) != 3 {
		t.Errorf("since: %+v", res.Lines)
	}
	if res := f.query(driver.LogQuery{Level: driver.LevelError}); len(res.Lines) != 1 || res.Lines[0].Tag != "dev.pinky.example:net" {
		t.Errorf("errors: %+v", res.Lines)
	}
	if res := f.query(driver.LogQuery{Filter: "buy"}); len(res.Lines) != 1 || res.Lines[0].Text != "tapped Buy" {
		t.Errorf("filter: %+v", res.Lines)
	}
	if res := f.query(driver.LogQuery{Max: 2}); len(res.Lines) != 2 || res.Dropped != 2 || res.Lines[1].Text != "tapped Buy" {
		t.Errorf("max: %+v dropped %d", res.Lines, res.Dropped)
	}

	// The app ends: its stream closes once it has been gone a while.
	phone.setProcs(map[int]string{20: otherExe, 30: widgetExe})
	f.scan()
	if s.isClosed() {
		t.Fatal("closed at once")
	}
	f.scan()
	waitUntil(t, "the stream to close", s.isClosed)
	waitUntil(t, "the stream to be dropped", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.streams[10] == nil
	})
	if lineCount(f) != 4 {
		t.Error("lines went with the process")
	}

	// A stream that ends by itself is opened again for a live process.
	phone.setProcs(map[int]string{10: testExe})
	f.scan()
	waitUntil(t, "pid 10 again", func() bool { return len(phone.openedPIDs()) == 3 })

	// The app is installed again: its bundle moves.
	phone.mu.Lock()
	phone.dir = movedDir
	phone.mu.Unlock()
	phone.setProcs(map[int]string{40: movedDir + "/Example"})
	f.scan()
	if phone.relocated != 0 {
		t.Fatal("looked the app up again too soon")
	}
	f.mu.Lock()
	f.lookedAt = time.Now().Add(-time.Minute)
	f.mu.Unlock()
	f.scan()
	if got := phone.openedPIDs(); phone.relocated != 1 || got[len(got)-1] != 40 {
		t.Fatalf("after a reinstall: relocated %d, opened %v", phone.relocated, got)
	}
	f.mu.Lock()
	if f.dir != movedDir {
		t.Errorf("dir %q", f.dir)
	}
	f.mu.Unlock()

	// Stop closes the streams and forgets the lines.
	f.stop()
	if !phone.stream(40).isClosed() {
		t.Error("a stream outlived stop")
	}
	if res := f.query(driver.LogQuery{}); len(res.Lines) != 0 {
		t.Errorf("lines after stop: %+v", res.Lines)
	}
	f.follow(50)
	if slices.Contains(phone.openedPIDs(), 50) {
		t.Error("followed a process after stop")
	}
}

func TestFollowerTrim(t *testing.T) {
	f := newFollower(testDir, newLogPhoneFake(nil).source())
	defer f.stop()
	base := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	f.mu.Lock()
	for i := range followMaxLines + 1 {
		f.addLocked(driver.LogLine{Time: base.Add(time.Duration(i) * time.Millisecond), Text: "x"})
	}
	n, trimmed := len(f.lines), f.trimmed
	f.mu.Unlock()
	if n != followMaxLines*3/4 {
		t.Fatalf("kept %d lines", n)
	}
	wantTrimmed := base.Add(time.Duration(followMaxLines-n) * time.Millisecond)
	if !trimmed.Equal(wantTrimmed) {
		t.Errorf("trimmed %v, want %v", trimmed, wantTrimmed)
	}
	f.start = base
	if res := f.query(driver.LogQuery{}); !res.From.Equal(trimmed) {
		t.Errorf("from %v, want the newest dropped line %v", res.From, trimmed)
	}
}

func TestAppLogsLaunched(t *testing.T) {
	var a appLogs
	phone := newLogPhoneFake(map[int]string{})
	a.launched("dev.pinky.example", 42) // no follower yet: nothing happens
	f := a.follower("dev.pinky.example", testDir, phone.source())
	if a.follower("dev.pinky.example", testDir, phone.source()) != f {
		t.Fatal("a second follower for one app")
	}
	a.launched("dev.pinky.example", 42)
	waitUntil(t, "the launched process's stream", func() bool { return slices.Contains(phone.openedPIDs(), 42) })
	a.stopAll()
	if !phone.stream(42).isClosed() {
		t.Error("stopAll left a stream open")
	}
	a.launched("dev.pinky.example", 43)
	time.Sleep(20 * time.Millisecond)
	if slices.Contains(phone.openedPIDs(), 43) {
		t.Error("followed a launch after stopAll")
	}
}

func TestOpenWithin(t *testing.T) {
	late := &fakeLogStream{closed: make(chan struct{})}
	release := make(chan struct{})
	_, err := openWithin(10*time.Millisecond, func() (logStream, error) {
		<-release
		return late, nil
	})
	if err == nil {
		t.Fatal("no timeout")
	}
	close(release)
	waitUntil(t, "the late stream to close", late.isClosed)
}

func sorted(s []int) []int {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/crashes/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var exampleBundle = appBundle{ID: "dev.pinky.example", Path: testDir, Executable: "Example"}

func TestCrashFileTime(t *testing.T) {
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{"Example-2026-11-09-101213.ips", true},
		{"Example-2026-11-09-101213.ips.synced", true},
		{"Example-2026-11-09-101213-0001.ips", true},
		{"Example Pro-2026-11-09-101213.ips", false},
		{"Example-Pro-2026-11-09-101213.ips", false},
		{"Example-2026-11-09-101213.txt", false},
		{"JetsamEvent-2026-11-09-101213.ips", false},
		{"Example-2026-11-09.ips", false},
	} {
		at, ok := crashFileTime(c.name, "Example")
		if ok != c.ok {
			t.Errorf("%s: %v", c.name, ok)
		}
		if ok && !at.Equal(time.Date(2026, 11, 9, 10, 12, 13, 0, time.UTC)) {
			t.Errorf("%s: %v", c.name, at)
		}
	}
}

func TestParseIPS(t *testing.T) {
	r, ok := parseIPS(readFixture(t, "swift.ips"), exampleBundle)
	if !ok {
		t.Fatal("swift.ips not read")
	}
	if want := time.Date(2026, 11, 9, 2, 12, 13, 0, time.UTC); !r.Time.Equal(want) || r.PID != 4321 || r.Process != "Example" || r.Kind != "crash" {
		t.Errorf("%v %d %s %s", r.Time, r.PID, r.Process, r.Kind)
	}
	if r.Summary != "Example/ContentView.swift:20: Fatal error: Crash button tapped" {
		t.Errorf("summary %q", r.Summary)
	}
	for _, want := range []string{
		"Exception Type:  EXC_BREAKPOINT (SIGTRAP)",
		"Termination:     SIGNAL, Code 0x5, Trace/BPT trap: 5",
		"OS Version:      iPhone OS 26.1 (23B85)",
		"Thread 0 Crashed:  Dispatch queue: com.apple.main-thread",
		"closure #1 in ContentView.body.getter + 120 (ContentView.swift:20)",
		"0x104a0b2c4", // the frame's address: the image's base plus its offset
		"SwiftUI.ButtonAction.callAsFunction()",
		"???",
		"Binary Images (the app's own):\n0x104a00000  Example  <5e1d1c0a11113c2b9d4e0123456789ab>",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("no %q in\n%s", want, r.Text)
		}
	}
	for _, never := range []string{"do-not-show-this-key", "deviceIdentifierForVendor", "__workq_kernreturn", "SwiftUI  <"} {
		if strings.Contains(r.Text, never) {
			t.Errorf("%q in\n%s", never, r.Text)
		}
	}

	r, ok = parseIPS(readFixture(t, "nsexception.ips"), exampleBundle)
	if !ok || r.Summary != "NSInvalidArgumentException: *** -[__NSArrayM insertObject:atIndex:]: object cannot be nil" || r.PID != 4400 {
		t.Fatalf("%v %+v", ok, r)
	}
	if !strings.Contains(r.Text, "Last Exception Backtrace:") || !strings.Contains(r.Text, "-[ViewController add:] + 60") {
		t.Errorf("no exception backtrace in\n%s", r.Text)
	}

	r, ok = parseIPS(readFixture(t, "watchdog.ips"), exampleBundle)
	if !ok || r.Kind != "watchdog" || !strings.HasPrefix(r.Summary, "scene-update watchdog transgression") ||
		!strings.Contains(r.Text, "FRONTBOARD, Code 0x8badf00d") || !strings.Contains(r.Text, "ContentView.hang()") {
		t.Fatalf("%v %+v", ok, r)
	}

	if _, ok := parseIPS(readFixture(t, "other.ips"), exampleBundle); ok {
		t.Error("read another app's report")
	}

	r, ok = parseIPS(readFixture(t, "legacy.ips"), exampleBundle)
	if !ok || r.Summary != "EXC_BAD_ACCESS (SIGSEGV)" || r.PID != 777 || !strings.Contains(r.Text, "Thread 0 Crashed:") {
		t.Fatalf("%v %+v", ok, r)
	}

	if _, ok := parseIPS([]byte(`{"bundleID":"dev.pinky.example","bug_type":"298","timestamp":"2026-11-09 10:00:00.00 +0800"}`+"\n{}"), exampleBundle); ok {
		t.Error("read a report that is no crash")
	}
	if _, ok := parseIPS([]byte("not a report"), exampleBundle); ok {
		t.Error("read garbage")
	}
}

type fakeFiles struct {
	dirs  map[string][]string
	files map[string][]byte
	read  []string
}

func (f *fakeFiles) List(dir string) ([]string, error) {
	names, ok := f.dirs[dir]
	if !ok {
		return nil, os.ErrNotExist
	}
	return names, nil
}

func (f *fakeFiles) ReadFile(name string, max int64) ([]byte, error) {
	f.read = append(f.read, name)
	b, ok := f.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return b[:min(int64(len(b)), max)], nil
}

func TestReadCrashes(t *testing.T) {
	files := &fakeFiles{
		dirs: map[string][]string{
			".": {
				"Example-2026-11-09-101213.ips",
				"Example-2026-11-09-123000.ips",
				"Example-2026-11-09-124000.ips", // another app with the same executable name
				"Other-2026-11-09-101213.ips",
				"JetsamEvent-2026-11-09-101213.ips",
				"Retired",
			},
			"Retired": {"Example-2026-11-09-110005.ips", "Example-2021-03-01-090000.ips"},
		},
		files: map[string][]byte{
			"Example-2026-11-09-101213.ips":         readFixture(t, "swift.ips"),
			"Example-2026-11-09-123000.ips":         readFixture(t, "watchdog.ips"),
			"Example-2026-11-09-124000.ips":         readFixture(t, "other.ips"),
			"Retired/Example-2026-11-09-110005.ips": readFixture(t, "nsexception.ips"),
			"Retired/Example-2021-03-01-090000.ips": readFixture(t, "legacy.ips"),
			"Other-2026-11-09-101213.ips":           []byte("not read"),
		},
	}
	since := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	reports, err := readCrashes(files, exampleBundle, driver.LogQuery{Since: since})
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, r := range reports {
		pids = append(pids, r.PID)
	}
	if !slices.Equal(pids, []int{4500, 4400, 4321}) {
		t.Fatalf("reports of pids %v, want newest first without the old one and the other app's", pids)
	}
	for _, name := range files.read {
		if strings.HasPrefix(name, "Other") || strings.Contains(name, "2021") || strings.HasPrefix(name, "Jetsam") {
			t.Errorf("read %s", name)
		}
	}

	files.read = nil
	reports, err = readCrashes(files, exampleBundle, driver.LogQuery{Since: since, Max: 1})
	if err != nil || len(reports) != 1 || reports[0].PID != 4500 {
		t.Fatalf("max 1: %+v %v", reports, err)
	}

	// Since 03:00 UTC, after the Swift crash (02:12 UTC): the later two.
	reports, _ = readCrashes(files, exampleBundle, driver.LogQuery{Since: time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC)})
	if len(reports) != 2 || reports[0].PID != 4500 || reports[1].PID != 4400 {
		t.Errorf("since 03:00 UTC: %+v", reports)
	}

	if _, err := readCrashes(&fakeFiles{}, exampleBundle, driver.LogQuery{}); err == nil {
		t.Error("no error when nothing could be listed")
	}
}
