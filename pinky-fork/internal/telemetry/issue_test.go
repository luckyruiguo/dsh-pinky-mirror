package telemetry

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func issueQuery(t *testing.T, u string) url.Values {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "github.com" || parsed.Path != "/"+IssueRepo+"/issues/new" {
		t.Fatalf("address %s", u)
	}
	return parsed.Query()
}

func diagnosticsWith(lines int) *Diagnostics {
	d := &Diagnostics{Summary: "Pinky 1.2.3 · macOS 27.2 · arm64\n\nPhones (1):\n  android 17 · Mi 10 Pro · usb\n"}
	var log []string
	for i := 0; i < lines; i++ {
		log = append(log, fmt.Sprintf(`time=2026-11-07T15:%02d:00Z level=INFO msg="device online" device=<phone-1> line=%d`, i%60, i))
	}
	d.Logs = []LogTail{{Name: "pinky.log", Lines: log}}
	return d
}

func TestIssueURL(t *testing.T) {
	u, cut := IssueURL(Feedback{Message: "The mirror froze.\nIt happened twice.", Version: "Pinky 1.2.3 · macOS 27.2 · arm64",
		Diagnostics: diagnosticsWith(5)})
	q := issueQuery(t, u)
	if cut || q.Get("template") != IssueTemplate || q.Get("title") != "The mirror froze." ||
		q.Get("message") != "The mirror froze.\nIt happened twice." || q.Get("version") != "Pinky 1.2.3 · macOS 27.2 · arm64" {
		t.Fatalf("cut %v, query %v", cut, q)
	}
	if d := q.Get("diagnostics"); !strings.Contains(d, "Phones (1):") || !strings.Contains(d, "line=4") {
		t.Fatalf("diagnostics:\n%s", d)
	}

	// Without diagnostics, with a title of its own.
	u, cut = IssueURL(Feedback{Title: " Dark icon ", Message: "Please add one."})
	q = issueQuery(t, u)
	if cut || q.Get("title") != "Dark icon" || q.Has("diagnostics") || q.Has("version") {
		t.Fatalf("cut %v, query %v", cut, q)
	}
}

func TestIssueURLLeavesOutOldLogLines(t *testing.T) {
	f := Feedback{Message: "It froze.", Diagnostics: diagnosticsWith(300)}
	u, cut := IssueURL(f)
	if !cut || len(u) > maxIssueURL {
		t.Fatalf("cut %v, %d bytes", cut, len(u))
	}
	d := issueQuery(t, u).Get("diagnostics")
	if !strings.Contains(d, "Phones (1):") || !strings.Contains(d, "line=299") || strings.Contains(d, "line=0 ") {
		t.Fatalf("kept:\n%s", d)
	}
	// Copy has everything.
	if text := f.Text(); !strings.Contains(text, "line=0") || !strings.Contains(text, "line=299") {
		t.Fatal("Text lacks lines")
	}

	// A message too long for an address keeps as much as fits.
	long := strings.Repeat("word ", 4000)
	u, cut = IssueURL(Feedback{Message: long, Diagnostics: diagnosticsWith(10)})
	q := issueQuery(t, u)
	if !cut || len(u) > maxIssueURL || q.Has("diagnostics") || !strings.HasSuffix(q.Get("message"), " …") {
		t.Fatalf("cut %v, %d bytes, diagnostics %v", cut, len(u), q.Has("diagnostics"))
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("  Hello\nworld", 80); got != "Hello" {
		t.Errorf("%q", got)
	}
	if got := firstLine(strings.Repeat("é", 100), 10); got != strings.Repeat("é", 9)+"…" {
		t.Errorf("%q", got)
	}
}
