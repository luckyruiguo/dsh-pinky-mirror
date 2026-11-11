package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBackend records what would be sent.
type fakeBackend struct {
	mu      sync.Mutex
	usage   []Usage
	crashes []CrashReport
	err     error
}

func (f *fakeBackend) Usage(_ context.Context, u Usage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.usage = append(f.usage, u)
	return nil
}

func (f *fakeBackend) Crash(_ context.Context, r CrashReport) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.crashes = append(f.crashes, r)
	return nil
}

func (f *fakeBackend) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeBackend) usages() []Usage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Usage(nil), f.usage...)
}

// clock is a time that tests move.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func openTest(t *testing.T, dir string, b Backend, clk *clock, usage bool) *Client {
	t.Helper()
	t.Setenv("DO_NOT_TRACK", "")
	return Open(Options{Dir: dir, Version: "1.2.3", Backend: b, Now: clk.now, Usage: usage})
}

func TestKey(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{Key("runs", "agent", "Claude Code"), "runs.agent.claude_code"},
		{Key("needs", "approval", "rule", "Installs and links"), "needs.approval.rule.installs_and_links"},
		{Key("A b", "C-D", "", "  "), "a_b.c_d"},
		{Key("phones", "android", "os", "17"), "phones.android.os.17"},
		{cleanName("tools.phone_tap"), "tools.phone_tap"},
		{cleanName("../../etc/passwd"), "etc_passwd"},
		{Key("名前"), ""},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}

func TestCountingFollowsTheSetting(t *testing.T) {
	dir := t.TempDir()
	clk := newClock()
	c := openTest(t, dir, &fakeBackend{}, clk, false)
	c.Count("runs.started")
	c.SeePhone("android.usb", "serial-1")
	if n := len(c.Report().Counts); n != 0 {
		t.Fatalf("counted with usage statistics off: %v", c.Report().Counts)
	}
	c.SetUsage(true)
	c.Count("runs.started")
	c.Add("tools.phone_tap", 3)
	c.Max("phones.max_online", 2)
	c.Max("phones.max_online", 1)
	c.SeePhone("android.usb", "serial-1")
	got := c.Report().Counts
	want := map[string]int64{"runs.started": 1, "tools.phone_tap": 3, "phones.max_online": 2, "phones.android.usb": 1}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	// Turning it off forgets what was counted, on disk too.
	c.SetUsage(false)
	if n := len(c.Report().Counts); n != 0 {
		t.Fatalf("counts kept after turning off: %v", c.Report().Counts)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "telemetry.json"))
	if strings.Contains(string(data), "runs.started") {
		t.Fatalf("telemetry.json kept counts:\n%s", data)
	}
}

func TestUsageGoesOutOnceADay(t *testing.T) {
	b := &fakeBackend{}
	clk := newClock()
	c := openTest(t, t.TempDir(), b, clk, true)
	ctx := context.Background()
	c.Count("runs.started")

	clk.advance(time.Hour)
	c.sendUsage(ctx)
	if n := len(b.usages()); n != 0 {
		t.Fatalf("sent before a day passed: %d", n)
	}
	clk.advance(24 * time.Hour)
	c.sendUsage(ctx)
	us := b.usages()
	if len(us) != 1 {
		t.Fatalf("sent %d reports, want 1", len(us))
	}
	u := us[0]
	if u.Counts["runs.started"] != 1 || u.Version != "1.2.3" || u.InstallID != c.InstallID() || u.OS == "" || u.Arch == "" {
		t.Fatalf("report: %+v", u)
	}
	if !u.PeriodStart.Before(u.PeriodEnd) {
		t.Fatalf("period %v – %v", u.PeriodStart, u.PeriodEnd)
	}
	if n := len(c.Report().Counts); n != 0 {
		t.Fatalf("counts kept after sending: %v", c.Report().Counts)
	}
	// A new period starts with the next count, and waits a day again.
	c.Count("runs.started")
	clk.advance(time.Hour)
	c.sendUsage(ctx)
	if n := len(b.usages()); n != 1 {
		t.Fatalf("sent again within a day: %d", n)
	}
}

func TestFailedSendsBackOff(t *testing.T) {
	b := &fakeBackend{}
	clk := newClock()
	c := openTest(t, t.TempDir(), b, clk, true)
	c.Count("runs.started")
	clk.advance(25 * time.Hour)
	b.setErr(errors.New("network down"))
	ctx := context.Background()
	c.sendUsage(ctx)
	c.sendUsage(ctx)
	if c.st.Failures != 1 {
		t.Fatalf("failures %d, want 1 (the second try should wait)", c.st.Failures)
	}
	clk.advance(61 * time.Minute)
	c.sendUsage(ctx)
	if c.st.Failures != 2 || !c.st.NextTry.Equal(clk.now().Add(2*time.Hour)) {
		t.Fatalf("failures %d, next try %v", c.st.Failures, c.st.NextTry)
	}
	// What was counted meanwhile stays; a refused report is dropped.
	c.Count("runs.started")
	clk.advance(3 * time.Hour)
	b.setErr(&permanentError{errors.New("400 Bad Request")})
	c.sendUsage(ctx)
	if n := len(c.Report().Counts); n != 0 || c.st.Failures != 0 {
		t.Fatalf("after a refusal: counts %v, failures %d", c.Report().Counts, c.st.Failures)
	}
	if backoff(1) != time.Hour || backoff(3) != 4*time.Hour || backoff(20) != 24*time.Hour {
		t.Fatalf("backoff %v %v %v", backoff(1), backoff(3), backoff(20))
	}
}

func TestDoNotTrack(t *testing.T) {
	b := &fakeBackend{}
	clk := newClock()
	dir := t.TempDir()
	c := openTest(t, dir, b, clk, true)
	c.Count("runs.started")
	c.Close()

	t.Setenv("DO_NOT_TRACK", "1")
	c = Open(Options{Dir: dir, Version: "1.2.3", Backend: b, Now: clk.now, Usage: true})
	if c.Usage() {
		t.Fatal("Usage() with DO_NOT_TRACK=1")
	}
	clk.advance(48 * time.Hour)
	c.Count("runs.done")
	c.sendUsage(context.Background())
	if n := len(b.usages()); n != 0 {
		t.Fatalf("sent with DO_NOT_TRACK=1: %+v", b.usages())
	}
	for _, v := range []string{"0", "false", "no", ""} {
		t.Setenv("DO_NOT_TRACK", v)
		if DoNotTrack() {
			t.Errorf("DO_NOT_TRACK=%q read as set", v)
		}
	}
}

func TestStateKeepsAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	clk := newClock()
	c := openTest(t, dir, &fakeBackend{}, clk, true)
	id := c.InstallID()
	c.Count("runs.started")
	c.SeePhone("ios.usb", "00008110-001E30C9262A801E")
	c.Close()
	data, err := os.ReadFile(filepath.Join(dir, "telemetry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "001E30C9262A801E") {
		t.Fatalf("a phone id was written:\n%s", data)
	}
	if info, _ := os.Stat(filepath.Join(dir, "telemetry.json")); info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("telemetry.json mode %v", info.Mode())
	}

	c = openTest(t, dir, &fakeBackend{}, clk, true)
	if c.InstallID() != id || c.Report().Counts["runs.started"] != 1 || c.Report().Counts["phones.ios.usb"] != 1 {
		t.Fatalf("after reopening: id %s (was %s), counts %v", c.InstallID(), id, c.Report().Counts)
	}
	c.SeePhone("ios.usb", "00008110-001E30C9262A801E")
	if c.Report().Counts["phones.ios.usb"] != 1 {
		t.Fatal("the same phone counted twice")
	}

	// Opened with the setting off, a run's leftovers are forgotten.
	c.Close()
	c = openTest(t, dir, &fakeBackend{}, clk, false)
	if n := len(c.Report().Counts); n != 0 {
		t.Fatalf("counts with the setting off: %v", c.Report().Counts)
	}

	// A broken file starts over.
	os.WriteFile(filepath.Join(dir, "telemetry.json"), []byte("{not json"), 0o600)
	c = openTest(t, dir, &fakeBackend{}, clk, true)
	if c.InstallID() == "" || c.InstallID() == id {
		t.Fatalf("install id after a broken file: %q", c.InstallID())
	}
}

func TestKeysAreCapped(t *testing.T) {
	c := openTest(t, t.TempDir(), &fakeBackend{}, newClock(), true)
	for i := 0; i < maxKeys+50; i++ {
		c.Count(Key("x", strings.Repeat("a", i%30+1), string(rune('a'+i%26)), string(rune('a'+i/26%26))))
	}
	if n := len(c.Report().Counts); n > maxKeys {
		t.Fatalf("%d keys, cap %d", n, maxKeys)
	}
}

func TestEndpoint(t *testing.T) {
	old := Endpoint
	t.Cleanup(func() { Endpoint = old })
	Endpoint = "https://reports.example/"
	t.Setenv(EndpointEnv, "")
	if got := endpoint(DevVersion); got != "" {
		t.Errorf("a development build sends to %q", got)
	}
	if got := endpoint("0.2.0"); got != "https://reports.example" {
		t.Errorf("release: %q", got)
	}
	t.Setenv(EndpointEnv, "http://127.0.0.1:9/")
	if got := endpoint(DevVersion); got != "http://127.0.0.1:9" {
		t.Errorf("override: %q", got)
	}
	t.Setenv(EndpointEnv, "-")
	if got := endpoint("0.2.0"); got != "" {
		t.Errorf(`"-" still sends to %q`, got)
	}
	if c := Open(Options{Version: "0.2.0"}); c.CanSend() {
		t.Error("CanSend with the endpoint off")
	}
}

func TestHTTPBackend(t *testing.T) {
	type got struct {
		path, ua, ctype string
		body            []byte
	}
	var mu sync.Mutex
	var reqs []got
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, got{r.URL.Path, r.Header.Get("User-Agent"), r.Header.Get("Content-Type"), body})
		s := status
		mu.Unlock()
		if r.Method != http.MethodPost {
			s = http.StatusMethodNotAllowed
		}
		w.WriteHeader(s)
		io.WriteString(w, "nope")
	}))
	defer srv.Close()
	t.Setenv(EndpointEnv, srv.URL)
	t.Setenv("DO_NOT_TRACK", "")
	clk := newClock()
	c := Open(Options{Dir: t.TempDir(), Version: DevVersion, Now: clk.now, Usage: true})
	if !c.CanSend() {
		t.Fatal("no backend with PINKY_TELEMETRY_URL set")
	}
	c.Count("runs.started")
	clk.advance(25 * time.Hour)
	c.sendUsage(context.Background())
	if err := c.backend.Crash(context.Background(), CrashReport{ID: "app-1-2", Reason: "panic: x"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(reqs) != 2 || reqs[0].path != PathUsage || reqs[1].path != PathCrash {
		t.Fatalf("requests: %+v", reqs)
	}
	if !strings.HasPrefix(reqs[0].ua, "Pinky/0.0.0-dev (") || reqs[0].ctype != "application/json" {
		t.Fatalf("headers: %q %q", reqs[0].ua, reqs[0].ctype)
	}
	var u Usage
	if err := json.Unmarshal(reqs[0].body, &u); err != nil || u.Counts["runs.started"] != 1 {
		t.Fatalf("usage body %s: %v", reqs[0].body, err)
	}
	var cr CrashReport
	if err := json.Unmarshal(reqs[1].body, &cr); err != nil || cr.ID != "app-1-2" || cr.Reason != "panic: x" {
		t.Fatalf("crash body %s: %v", reqs[1].body, err)
	}
	mu.Unlock()

	h := &HTTP{URL: srv.URL}
	for code, permanent := range map[int]bool{400: true, 413: true, 429: false, 500: false, 503: false} {
		mu.Lock()
		status = code
		mu.Unlock()
		err := h.Crash(context.Background(), CrashReport{ID: "x"})
		if err == nil || Permanent(err) != permanent {
			t.Errorf("HTTP %d: err %v, permanent %v", code, err, Permanent(err))
		}
	}
	srv.Close()
	if err := h.Crash(context.Background(), CrashReport{ID: "x"}); err == nil || Permanent(err) {
		t.Errorf("server gone: %v", err)
	}
}

func TestRunSendsWhatIsDue(t *testing.T) {
	b := &fakeBackend{}
	clk := newClock()
	t.Setenv("DO_NOT_TRACK", "")
	dir := t.TempDir()
	c := Open(Options{Dir: dir, Version: "1.2.3", Backend: b, Now: clk.now, Usage: true, Tick: 10 * time.Millisecond})
	c.Count("runs.started")
	clk.advance(25 * time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(b.usages()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if len(b.usages()) != 1 {
		t.Fatalf("Run sent %d reports", len(b.usages()))
	}
	data, _ := os.ReadFile(filepath.Join(dir, "telemetry.json"))
	var st state
	if err := json.Unmarshal(data, &st); err != nil || st.LastSent.IsZero() || len(st.Counts) != 0 {
		t.Fatalf("state after Run: %s (%v)", data, err)
	}
}

func TestNilClient(t *testing.T) {
	var c *Client
	c.Count("x")
	c.SetUsage(true)
	c.Close()
	c.Run(context.Background())
	if c.Usage() || c.CanSend() || c.Crashes() != nil {
		t.Fatal("a nil client claims something")
	}
	Count("x") // no default client
}
