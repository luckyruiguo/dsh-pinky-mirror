package ui

import (
	"context"
	"errors"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/telemetry"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// reports is a telemetry backend that keeps what it is sent.
type reports struct {
	mu      sync.Mutex
	usage   []telemetry.Usage
	crashes []telemetry.CrashReport
}

func (r *reports) Usage(_ context.Context, u telemetry.Usage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage = append(r.usage, u)
	return nil
}

func (r *reports) Crash(_ context.Context, c telemetry.CrashReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.crashes = append(r.crashes, c)
	return nil
}

type telApp struct {
	*testApp
	tel     *telemetry.Client
	dir     string
	mu      sync.Mutex
	changes []Settings
}

// newTelApp is the client on the fake with a telemetry client that sends
// to b (nil: a build with nowhere to send).
func newTelApp(t *testing.T, b telemetry.Backend) *telApp {
	t.Helper()
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv(telemetry.EndpointEnv, "")
	dir := t.TempDir()
	cr := telemetry.CaptureCrashes(telemetry.CrashOptions{Dir: dir, Process: "app"})
	t.Cleanup(cr.Stop)
	opt := telemetry.Options{Dir: dir, Version: "1.2.3", Backend: b, Crashes: cr, Usage: true}
	if b == nil {
		opt.Version = telemetry.DevVersion
	}
	tel := telemetry.Open(opt)
	ta := &telApp{testApp: newTestApp(t, fakeapi.OnePhone), tel: tel, dir: dir}
	ta.app.opt.Telemetry = tel
	ta.app.settings.UsageStats = true
	ta.app.opt.OnSettings = func(_, s Settings) {
		ta.mu.Lock()
		ta.changes = append(ta.changes, s)
		ta.mu.Unlock()
	}
	ta.sync()
	return ta
}

func (ta *telApp) lastChange() (Settings, bool) {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	if len(ta.changes) == 0 {
		return Settings{}, false
	}
	return ta.changes[len(ta.changes)-1], true
}

// eventually syncs until cond holds.
func (ta *telApp) eventually(t *testing.T, tt *mg.Tester, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 250; i++ {
		ta.sync()
		if tt != nil {
			tt.Frame()
		}
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestNoNoteAboutUsageStatistics(t *testing.T) {
	ta := newTelApp(t, &reports{})
	tt := mg.NewTester(ta.view, 1280, 820)
	if hasText(tt, "usage statistics") || !ta.settings.UsageStats || !ta.tel.Usage() || !DefaultSettings().UsageStats {
		t.Fatalf("a note, or statistics off by default: %q", tt.Texts())
	}
}

func TestPrivacySettings(t *testing.T) {
	ta := newTelApp(t, &reports{})
	ta.tel.Count("runs.started")
	ta.page, ta.set.sec = pageSettings, secPrivacy
	tt := mg.NewTester(ta.view, 1280, 820)
	for _, want := range []string{"Share anonymous usage statistics and crash reports", "What's shared", "Send feedback…"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Show what's shared"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"What Pinky shares", `"install_id"`, `"runs.started": 1`, "never include what is on your phones' screens"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Close"); err != nil {
		t.Fatal(err)
	}
	if hasText(tt, "What Pinky shares") {
		t.Fatal("the dialog stayed open")
	}

	if err := tt.Click("Share anonymous usage statistics and crash reports"); err != nil {
		t.Fatal(err)
	}
	ta.eventually(t, tt, "usage statistics off", func() bool { return !ta.tel.Usage() })
	if s, _ := ta.lastChange(); s.UsageStats {
		t.Fatal("the switch did not save")
	}
	if n := len(ta.tel.Report().Counts); n != 0 {
		t.Fatalf("counts kept after turning off: %v", ta.tel.Report().Counts)
	}

	t.Setenv("DO_NOT_TRACK", "1")
	tt.Frame()
	if !hasText(tt, "Off: DO_NOT_TRACK is set on this computer.") {
		t.Fatalf("DO_NOT_TRACK not shown: %q", tt.Texts())
	}
}

// openedIssue returns the query of the GitHub issue the view opened.
func openedIssue(t *testing.T, tt *mg.Tester) url.Values {
	t.Helper()
	opened := tt.OpenedURLs()
	if len(opened) != 1 {
		t.Fatalf("opened %q", opened)
	}
	u, err := url.Parse(opened[0])
	if err != nil || u.Host != "github.com" || u.Path != "/"+telemetry.IssueRepo+"/issues/new" {
		t.Fatalf("opened %q", opened[0])
	}
	return u.Query()
}

func TestFeedbackOpensAGitHubIssue(t *testing.T) {
	ta := newTelApp(t, &reports{})
	ta.page, ta.set.sec = pageSettings, secAdvanced
	tt := mg.NewTester(ta.view, 1280, 820)
	if err := tt.Click("Send feedback…"); err != nil {
		t.Fatal(err)
	}
	ta.eventually(t, tt, "the diagnostics", func() bool { return !ta.t.fb.reading })
	for _, want := range []string{"What happened, or what would make Pinky better?", "Issues are public."} {
		if !hasText(tt, want) {
			t.Fatalf("no %q in %q", want, tt.Texts())
		}
	}
	tt.Type("The mirror froze after I unlocked the phone.")
	if err := tt.Click("Review diagnostics"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasText(tt, "Phones (1):") || !hasText(tt, "Pinky 1.2.3") {
		t.Fatalf("diagnostics: %q", tt.Texts())
	}
	if err := tt.Click("Open on GitHub"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	q := openedIssue(t, tt)
	if q.Get("template") != telemetry.IssueTemplate || q.Get("title") != "The mirror froze after I unlocked the phone." ||
		q.Get("message") != "The mirror froze after I unlocked the phone." || !strings.HasPrefix(q.Get("version"), "Pinky 1.2.3 · ") ||
		!strings.Contains(q.Get("diagnostics"), "Phones (1):") {
		t.Fatalf("issue %v", q)
	}
	for _, d := range ta.devices {
		if strings.Contains(q.Get("diagnostics"), d.Info.ID) {
			t.Fatalf("the diagnostics name the phone %q", d.Info.ID)
		}
	}
	if ta.t.fb.open || ta.t.fb.message != "" {
		t.Fatalf("the dialog stayed open with its draft: %+v", ta.t.fb)
	}
}

func TestFeedbackWithoutDiagnostics(t *testing.T) {
	ta := newTelApp(t, nil) // a development build: feedback needs no backend
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.openFeedback()
	ta.eventually(t, tt, "the diagnostics", func() bool { return !ta.t.fb.reading })
	if err := tt.Click("Title"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Dark icon")
	if err := tt.Click("Message"); err != nil {
		t.Fatal(err)
	}
	tt.Type("Please add one.")
	if err := tt.Click("Attach diagnostics: versions, settings, phone models and Pinky's recent logs"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Copy feedback"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); !strings.HasPrefix(got, "Dark icon\n\nPlease add one.") || strings.Contains(got, "Phones (") {
		t.Fatalf("clipboard %q", got)
	}
	if err := tt.Click("Open on GitHub"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	q := openedIssue(t, tt)
	if q.Get("title") != "Dark icon" || q.Get("message") != "Please add one." || q.Has("diagnostics") || q.Get("version") == "" {
		t.Fatalf("issue %v", q)
	}
}

func TestFeedbackWhenTheBrowserFails(t *testing.T) {
	ta := newTelApp(t, &reports{})
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.FailOpenURL(errors.New("no browser"))
	ta.openFeedback()
	ta.eventually(t, tt, "the diagnostics", func() bool { return !ta.t.fb.reading })
	tt.Type("Hi")
	if err := tt.Click("Open on GitHub"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasText(tt, "Couldn't open the browser") || !ta.t.fb.open || ta.t.fb.message != "Hi" {
		t.Fatalf("after a failed open: %q, %+v", tt.Texts(), ta.t.fb)
	}
}

func TestMenusOfferFeedback(t *testing.T) {
	ta := newTelApp(t, &reports{})
	help := ta.helpMenu()
	if help == nil || len(help.Submenu) != 1 || help.Submenu[0].Label != "Send Feedback…" {
		t.Fatalf("help menu %+v", help)
	}
	help.Submenu[0].Click(help.Submenu[0], (*mygo.Window)(nil))
	if !ta.t.fb.open {
		t.Fatal("Send Feedback… did not open the dialog")
	}
	plain := newTestApp(t, fakeapi.OnePhone)
	if plain.helpMenu() != nil {
		t.Fatal("a Help menu without telemetry")
	}
	tt := mg.NewTester(plain.view, 1280, 820)
	plain.page = pageSettings
	tt.Frame()
	if hasText(tt, "Send feedback…") {
		t.Fatal("Send feedback without telemetry")
	}
}

// TestTelemetryScreenshots renders Send Feedback, What Pinky shares and
// Settings › Privacy in light and dark into $PINKY_SCREENSHOTS.
func TestTelemetryScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS to a folder to write the screenshots")
	}
	shoot := func(ta *telApp, name string, setup func()) {
		t.Helper()
		for _, dark := range []bool{false, true} {
			tt := mg.NewTester(ta.view, 1280, 820)
			tt.SetScale(2)
			tt.SetDark(dark)
			if setup != nil {
				setup()
			}
			ta.eventually(t, tt, "posted work", func() bool { return !ta.t.fb.reading })
			tt.Frame()
			suffix := "-light.png"
			if dark {
				suffix = "-dark.png"
			}
			f, err := os.Create(filepath.Join(dir, name+suffix))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
	ta := newTelApp(t, &reports{})
	ta.tel.Count("runs.started")
	ta.tel.Count("tools.phone_tap")
	shoot(ta, "feedback", func() {
		if !ta.t.fb.open {
			ta.openFeedback()
			ta.t.fb.title = "Mirror froze"
			ta.t.fb.message = "The mirror froze after I unlocked the phone from the computer."
			ta.t.fb.review = true
		}
	})
	ta.t.fb.open = false
	ta.page, ta.set.sec = pageSettings, secPrivacy
	shoot(ta, "settings-privacy", nil)
	shoot(ta, "whats-shared", func() { ta.openShared() })
}
