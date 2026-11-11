package gateway

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/fake"
	"github.com/DIYgod/pinky/internal/policy"
)

const crashTest = "dev.pinky.crashtest"

// newLogsGateway runs a gateway on a fake Android phone with the test app
// Crash Test, a finance app, Pinky Companion and Settings installed.
func newLogsGateway(t *testing.T) (*Gateway, *fake.Device) {
	t.Helper()
	dev := fake.NewDevice("pixel", "Pixel 9")
	dev.AddApp(driver.App{ID: crashTest, Name: "Crash Test"})
	dev.AddApp(driver.App{ID: "com.example.mybank", Name: "My Bank"})
	dev.AddApp(driver.App{ID: "app.pinky.companion", Name: "Pinky Companion"})
	dev.AddApp(driver.App{ID: "com.example.notes", Name: "Notes Two"})
	pol := testPolicy(t)
	pol.AddApps(policy.CatFinance, policy.CategoryEntries{IDs: []string{"com.example.mybank"}})
	g, err := New(Options{
		Providers: []driver.Provider{fake.NewProvider(dev)}, Policy: pol, TrustNewAgents: true, DataDir: t.TempDir(),
		ApprovalTimeout: 3 * time.Second, MaxBlock: 2 * time.Second, SettleMax: 200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond, NoScreenshots: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g, dev
}

// The first read of an app's logs in a task asks; Allow for this task
// covers its logs and crash reports until the task ends, and history keeps
// what was read, not the lines.
func TestPhoneLogsAsks(t *testing.T) {
	ctx := context.Background()
	g, dev := newLogsGateway(t)
	s, _ := connect(t, g, "Test Crash Test")
	now := time.Now()
	dev.AddLogs(crashTest,
		fake.LogLineAt(now.Add(-2*time.Hour), driver.LevelInfo, "CrashTest", "before the task"),
		fake.LogLineAt(now.Add(time.Millisecond), driver.LevelInfo, "CrashTest", "button pressed"),
		fake.LogLineAt(now.Add(2*time.Millisecond), driver.LevelError, "AndroidRuntime", "FATAL EXCEPTION: main"))

	done := make(chan *Result, 1)
	go func() { done <- s.Logs(ctx, LogsRequest{App: "Crash Test"}) }()
	n := waitNeedKind(t, g, NeedApproval)
	if n.Rule != policy.RuleAppLogs || n.Title != "Read the logs of Crash Test?" || !n.AllowTask || !n.AlwaysAllow {
		t.Fatalf("approval %+v", n)
	}
	if err := g.Decide(n.ID, Deny, ""); err != nil {
		t.Fatal(err)
	}
	res := <-done
	mustStatus(t, res, StatusDenied)
	if res.Log != "" || res.Rule != policy.RuleAppLogs {
		t.Fatalf("denied read: %+v", res)
	}

	go func() { done <- s.Logs(ctx, LogsRequest{App: crashTest}) }()
	n = waitNeedKind(t, g, NeedApproval)
	if err := g.Decide(n.ID, AllowTask, ""); err != nil {
		t.Fatal(err)
	}
	res = <-done
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Log, "E AndroidRuntime: FATAL EXCEPTION: main") || strings.Contains(res.Log, "before the task") ||
		strings.Index(res.Log, "button pressed") > strings.Index(res.Log, "FATAL") {
		t.Fatalf("log %q", res.Log)
	}
	if !strings.HasPrefix(res.Message, "2 log lines of Crash Test since ") {
		t.Fatalf("message %q", res.Message)
	}

	// Allowed for the task: no more questions, crash reports included.
	res = s.Logs(ctx, LogsRequest{App: "Crash Test", Since: "3h", Filter: "before"})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Log, "before the task") || strings.Count(res.Log, "\n") != 0 {
		t.Fatalf("filtered log %q", res.Log)
	}
	dev.AddCrash(crashTest, driver.CrashReport{Time: now.Add(time.Millisecond), Kind: "crash", Process: crashTest, PID: 4321,
		Summary: "java.lang.IllegalStateException: Crash button", Text: "at dev.pinky.crashtest.Main.onClick(Main.java:42)"})
	res = s.Logs(ctx, LogsRequest{App: "Crash Test", Kind: "crashes"})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Log, "── crash · ") || !strings.Contains(res.Log, "pid 4321 · "+crashTest) ||
		!strings.Contains(res.Log, "IllegalStateException") || !strings.Contains(res.Log, "Main.java:42") {
		t.Fatalf("crash log %q", res.Log)
	}
	if len(g.Needs()) != 0 {
		t.Fatalf("asked again: %+v", g.Needs())
	}

	var texts []string
	for _, st := range taskSteps(g, s) {
		if st.Tool != "phone_logs" {
			continue
		}
		texts = append(texts, st.Text)
		if strings.Contains(st.Text+st.Args, "FATAL") || strings.Contains(st.Text+st.Args, "Main.java") {
			t.Fatalf("history keeps log text: %+v", st)
		}
	}
	want := []string{"Waiting for you: Read the logs of Crash Test", "Waiting for you: Read the logs of Crash Test",
		"Read 2 log lines of Crash Test", "Read 1 log line of Crash Test", "Read 1 crash report of Crash Test"}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("steps %q, want %q", texts, want)
	}

	// A new task asks again.
	if res := s.Release(ctx, ReleaseRequest{Outcome: "done"}); res.IsError() {
		t.Fatalf("release: %+v", res)
	}
	s2, _ := connect(t, g, "Test again")
	go func() { done <- s2.Logs(ctx, LogsRequest{App: "Crash Test"}) }()
	n = waitNeedKind(t, g, NeedApproval)
	if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, <-done, StatusSettled)
	go func() { done <- s2.Logs(ctx, LogsRequest{App: "Crash Test"}) }()
	n = waitNeedKind(t, g, NeedApproval)
	if err := g.Decide(n.ID, Deny, ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, <-done, StatusDenied)
}

// A development build is the app the agent is testing: no question.
func TestPhoneLogsDevBuild(t *testing.T) {
	g, dev := newLogsGateway(t)
	dev.SetDevBuild(crashTest, true)
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeReadOnly})
	s, _ := connect(t, g, "Test Crash Test")
	dev.AddLogs(crashTest, fake.LogLineAt(time.Now(), driver.LevelDebug, "CrashTest", "hello"))
	res := s.Logs(context.Background(), LogsRequest{App: "Crash Test", Since: "1m"})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Log, "D CrashTest: hello") {
		t.Fatalf("log %q", res.Log)
	}
}

func TestPhoneLogsRefused(t *testing.T) {
	ctx := context.Background()
	g, dev := newLogsGateway(t)
	dev.SetDevBuild(crashTest, true)
	dev.SetDevBuild("app.pinky.companion", true)
	s, _ := connect(t, g, "Look around")
	for app, rule := range map[string]string{"My Bank": "Finance apps", "Settings": policy.RuleAppLogs, "Pinky Companion": policy.RuleAppLogs} {
		res := s.Logs(ctx, LogsRequest{App: app})
		if res.Status != StatusBlocked || res.Rule != rule || res.Log != "" {
			t.Errorf("%s: %+v", app, res)
		}
	}
	if res := s.Logs(ctx, LogsRequest{App: "Calculator"}); res.Error != ErrNotFound {
		t.Errorf("an app that is not installed: %+v", res)
	}
	if res := s.Logs(ctx, LogsRequest{App: "Notes"}); res.Error != ErrNotFound || !strings.Contains(res.Message, "Notes Two") {
		t.Errorf("an app named almost so: %+v", res)
	}
	if res := s.Logs(ctx, LogsRequest{}); res.Error != ErrInvalidArgument {
		t.Errorf("no app: %+v", res)
	}
	for _, req := range []LogsRequest{{App: crashTest, Kind: "traces"}, {App: crashTest, Level: "loud"},
		{App: crashTest, Limit: -1}, {App: crashTest, Since: "yesterday"}} {
		if res := s.Logs(ctx, req); res.Error != ErrInvalidArgument {
			t.Errorf("%+v: %+v", req, res)
		}
	}
	if res := s.Release(ctx, ReleaseRequest{Outcome: "done"}); res.IsError() {
		t.Fatal(res)
	}

	// Outside the task's apps or the agent's scope.
	s = g.OpenSession(agent)
	t.Cleanup(s.Close)
	mustStatus(t, s.Connect(ctx, ConnectRequest{Task: "Notes only", Apps: []string{"Notes Two"}}), StatusSettled)
	if res := s.Logs(ctx, LogsRequest{App: "Crash Test"}); res.Error != ErrScopeDenied {
		t.Errorf("outside the task's apps: %+v", res)
	}
	steps := taskSteps(g, s)
	if len(steps) == 0 || steps[len(steps)-1].Text != "Outside the scope: read the logs of Crash Test" {
		t.Errorf("steps %+v", steps)
	}
	s.Release(ctx, ReleaseRequest{Outcome: "done"})
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeApps, Apps: []string{"Notes Two"}})
	s, _ = connect(t, g, "Notes")
	if res := s.Logs(ctx, LogsRequest{App: "Crash Test"}); res.Error != ErrScopeDenied {
		t.Errorf("outside the agent's scope: %+v", res)
	}
	s.Release(ctx, ReleaseRequest{Outcome: "done"})
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeWatchOnly})
	s, _ = connect(t, g, "Watch")
	if res := s.Logs(ctx, LogsRequest{App: "Crash Test"}); res.Error != ErrScopeDenied {
		t.Errorf("watch-only: %+v", res)
	}
}

// Results stay short: at most limit lines and 64 KB, the newest kept, and
// the message says how many were left out.
func TestPhoneLogsLimits(t *testing.T) {
	ctx := context.Background()
	g, dev := newLogsGateway(t)
	dev.SetDevBuild(crashTest, true)
	s, _ := connect(t, g, "Test Crash Test")
	start := time.Now()
	for i := range 600 {
		level := driver.LevelInfo
		if i%100 == 0 {
			level = driver.LevelWarn
		}
		dev.AddLogs(crashTest, fake.LogLineAt(start.Add(time.Duration(i+1)*time.Millisecond), level, "CrashTest", fmt.Sprintf("line %d", i)))
	}
	res := s.Logs(ctx, LogsRequest{App: "Crash Test"})
	mustStatus(t, res, StatusSettled)
	lines := strings.Split(res.Log, "\n")
	if len(lines) != LogsDefault || !strings.HasSuffix(lines[0], "line 400") || !strings.HasSuffix(lines[len(lines)-1], "line 599") ||
		!strings.Contains(res.Message, "400 older lines left out") {
		t.Fatalf("default limit: %d lines, first %q, message %q", len(lines), lines[0], res.Message)
	}
	res = s.Logs(ctx, LogsRequest{App: "Crash Test", Limit: 10_000})
	if n := strings.Count(res.Log, "\n") + 1; n != LogsMax {
		t.Fatalf("limit above the most: %d lines", n)
	}
	res = s.Logs(ctx, LogsRequest{App: "Crash Test", Level: "warning"})
	if n := strings.Count(res.Log, "\n") + 1; n != 6 || strings.Contains(res.Message, "left out") {
		t.Fatalf("warnings: %d lines, %q", n, res.Message)
	}

	// Long lines: the newest that fit in 64 KB.
	long := strings.Repeat("x", 1500)
	for i := range 100 {
		dev.AddLogs(crashTest, fake.LogLineAt(start.Add(time.Second+time.Duration(i)*time.Millisecond), driver.LevelError, "Big", fmt.Sprintf("%d %s", i, long)))
	}
	res = s.Logs(ctx, LogsRequest{App: "Crash Test", Level: "error"})
	lines = strings.Split(res.Log, "\n")
	if len(res.Log) > logsMaxBytes || len(lines) >= 100 || !strings.Contains(lines[len(lines)-1], "E Big: 99 x") ||
		!strings.Contains(res.Message, fmt.Sprintf("%d older lines left out", 100-len(lines))) {
		t.Fatalf("64 KB: %d bytes, %d lines, %q", len(res.Log), len(lines), res.Message)
	}
	dev.AddLogs(crashTest, fake.LogLineAt(start.Add(2*time.Second), driver.LevelFatal, "Huge", strings.Repeat("é", 3000)+"\nsecond"))
	res = s.Logs(ctx, LogsRequest{App: "Crash Test", Level: "fatal"})
	if !strings.Contains(res.Log, "… (") || len(res.Log) > logLineMax+100 {
		t.Fatalf("a huge line: %d bytes", len(res.Log))
	}
}

// Crash reports come newest first; one too big for the result is cut, and
// later ones show their first lines.
func TestPhoneLogsCrashes(t *testing.T) {
	ctx := context.Background()
	g, dev := newLogsGateway(t)
	dev.SetDevBuild(crashTest, true)
	s, _ := connect(t, g, "Test Crash Test")
	now := time.Now()
	big := strings.Repeat("\tat dev.pinky.crashtest.Main.deep(Main.java:7)\n", 2000)
	dev.AddCrash(crashTest, driver.CrashReport{Time: now.Add(time.Second), Kind: "crash", Summary: "first", Text: big})
	dev.AddCrash(crashTest, driver.CrashReport{Time: now.Add(3 * time.Second), Kind: "ANR", Summary: "newest", Text: big})
	dev.AddCrash(crashTest, driver.CrashReport{Time: now.Add(2 * time.Second), Kind: "native crash", Summary: "middle", Text: "short"})
	dev.AddCrash(crashTest, driver.CrashReport{Time: now.Add(-time.Hour), Kind: "crash", Summary: "old"})
	res := s.Logs(ctx, LogsRequest{App: "Crash Test", Kind: "crashes"})
	mustStatus(t, res, StatusSettled)
	newest, middle, first := strings.Index(res.Log, "newest"), strings.Index(res.Log, "middle"), strings.Index(res.Log, "\nfirst")
	if newest < 0 || middle < newest || first < middle || strings.Contains(res.Log, "old") {
		t.Fatalf("order: %d %d %d", newest, middle, first)
	}
	if len(res.Log) > logsMaxBytes || !strings.Contains(res.Message, "3 crash reports of Crash Test") ||
		!strings.Contains(res.Message, "One report shows only its first lines") {
		t.Fatalf("%d bytes, %q", len(res.Log), res.Message)
	}
	if tail := res.Log[first:]; !strings.Contains(tail, "Main.java:7") || !strings.Contains(tail, "more bytes left out") {
		t.Fatalf("the cut report: %q", tail[:min(len(tail), 300)])
	}
	res = s.Logs(ctx, LogsRequest{App: "Crash Test", Kind: "crash", Limit: 1})
	if strings.Count(res.Log, "── ") != 1 || !strings.Contains(res.Log, "newest") {
		t.Fatalf("limit 1: %q", res.Log[:200])
	}
	res = s.Logs(ctx, LogsRequest{App: "Crash Test", Kind: "crashes", Since: "1m", Limit: 100})
	if strings.Count(res.Log, "── ") != 3 {
		t.Fatalf("limit 100: %d reports", strings.Count(res.Log, "── "))
	}
}

func TestPhoneLogsUnsupported(t *testing.T) {
	g := newIPhoneGateway(t)
	s, _ := connect(t, g, "Logs")
	if res := s.Logs(context.Background(), LogsRequest{App: "Settings"}); res.Error != ErrUnsupported {
		t.Fatalf("a phone without AppLogger: %+v", res)
	}
}

// A phone that keeps no log history says when Pinky started listening.
func TestLinesResultListening(t *testing.T) {
	now := time.Date(2026, 11, 9, 15, 0, 0, 0, time.Local)
	since := now.Add(-10 * time.Minute)
	res := linesResult(driver.LogResult{From: now.Add(-time.Minute)}, "Example", since, now, true)
	if !strings.HasPrefix(res.Message, "No log lines of Example since 14:59:00.") ||
		!strings.Contains(res.Message, "Pinky listens to Example from 14:59:00") {
		t.Fatalf("listening: %q", res.Message)
	}
	res = linesResult(driver.LogResult{From: since}, "Example", since, now, true)
	if strings.Contains(res.Message, "listens") {
		t.Fatalf("listening from before since: %q", res.Message)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 11, 9, 15, 4, 5, 0, time.Local)
	task := now.Add(-time.Hour)
	for in, want := range map[string]time.Time{
		"":                          task,
		"10m":                       now.Add(-10 * time.Minute),
		"-90s":                      now.Add(-90 * time.Second),
		"2d":                        now.AddDate(0, 0, -2),
		"14:05":                     time.Date(2026, 11, 9, 14, 5, 0, 0, time.Local),
		"16:00:30":                  time.Date(2026, 11, 8, 16, 0, 30, 0, time.Local),
		"2026-11-09 09:30":          time.Date(2026, 11, 9, 9, 30, 0, 0, time.Local),
		"2026-11-08":                time.Date(2026, 11, 8, 0, 0, 0, 0, time.Local),
		"2026-11-09T06:00:00Z":      time.Date(2026, 11, 9, 6, 0, 0, 0, time.UTC),
		"2026-11-09T14:00:00+08:00": time.Date(2026, 11, 9, 14, 0, 0, 0, time.FixedZone("", 8*3600)),
	} {
		got, err := parseSince(in, now, task)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v, want %v", in, got, err, want)
		}
	}
	if got, _ := parseSince("", now, time.Time{}); !got.Equal(now) {
		t.Errorf("no task start: %v", got)
	}
	for _, in := range []string{"yesterday", "2027-01-01", "xd", "2099-01-01T00:00:00Z"} {
		if _, err := parseSince(in, now, task); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}
