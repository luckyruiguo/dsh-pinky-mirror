package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/history"
	"github.com/DIYgod/pinky/internal/recording"
)

// History export (round three, decision 9): an HTML report that opens in
// any browser without anything else (screenshots inline), a JSON file with
// the same tasks for tools, and each task's recording copied next to them.

// exportTask is one task in the JSON file and the report.
type exportTask struct {
	ID        string           `json:"id"`
	Device    string           `json:"device"`
	DeviceID  string           `json:"device_id"`
	Agent     string           `json:"agent"`
	Title     string           `json:"title"`
	State     string           `json:"state"`
	Outcome   string           `json:"outcome,omitempty"`
	Summary   string           `json:"summary,omitempty"`
	Started   time.Time        `json:"started"`
	Ended     *time.Time       `json:"ended,omitempty"`
	Steps     []exportStep     `json:"steps"`
	Approvals []exportApproval `json:"approvals,omitempty"`
	Recording *exportRecording `json:"recording,omitempty"`
}

type exportStep struct {
	Index   int             `json:"index"`
	Text    string          `json:"text"`
	Tool    string          `json:"tool,omitempty"`
	Args    json.RawMessage `json:"args,omitempty"`
	Intent  string          `json:"intent,omitempty"`
	Status  string          `json:"status"`
	Started time.Time       `json:"started"`
	Elapsed int64           `json:"elapsed_ms"`
	// Before and After are screenshot ids; the report shows them.
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	// Screen is the screen after the step as the agent read it.
	Screen string `json:"screen,omitempty"`
}

type exportApproval struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	Rule     string     `json:"rule,omitempty"`
	Title    string     `json:"title"`
	Decision string     `json:"decision,omitempty"`
	Note     string     `json:"note,omitempty"`
	Step     int        `json:"step"`
	Created  time.Time  `json:"created"`
	Decided  *time.Time `json:"decided,omitempty"`
	Waited   int64      `json:"waited_ms"`
}

type exportRecording struct {
	File   string       `json:"file"` // next to the report
	Codec  string       `json:"codec"`
	Length int64        `json:"length_ms"`
	Marks  []exportMark `json:"marks"`
}

type exportMark struct {
	At   int64  `json:"at_ms"`
	Step int    `json:"step"`
	Text string `json:"text"`
}

// ExportHistory writes a self-contained HTML report and a JSON file of the
// tasks (all finished tasks when taskIDs is empty) into dir, with each
// task's recording copied next to them, and returns the HTML file's path.
func (g *Gateway) ExportHistory(ctx context.Context, taskIDs []string, dir string) (string, error) {
	if dir == "" {
		return "", errors.New("export: no folder")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	g.Flush()
	now := time.Now()
	out, err := g.exportTo(ctx, taskIDs, dir, uniqueBase(dir, "pinky-history-"+now.Format("2006-01-02-150405")), now, true)
	if err != nil {
		return "", err
	}
	return out.html, nil
}

// exported are the files an export wrote: the report, the JSON file and
// the recordings, by path.
type exported struct {
	html, json string
	videos     []string
}

// exportTo writes the report and JSON file of the tasks into dir, named
// base.html and base.json, and with videos each task's recording next to
// them. Callers flush first.
func (g *Gateway) exportTo(ctx context.Context, taskIDs []string, dir, base string, now time.Time, videos bool) (exported, error) {
	var out exported
	tasks, err := g.exportTasks(ctx, taskIDs)
	if err != nil {
		return out, err
	}
	shots := map[string]string{} // media id -> data URL
	for i := range tasks {
		t := &tasks[i]
		for _, st := range t.Steps {
			for _, id := range []string{st.Before, st.After} {
				if id == "" || shots[id] != "" || g.store == nil {
					continue
				}
				if p, err := g.store.MediaPath(ctx, id); err == nil {
					if b, err := os.ReadFile(p); err == nil {
						shots[id] = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b)
					}
				}
			}
		}
		if !videos {
			t.Recording = nil
		}
		if t.Recording != nil {
			name := base + "-" + t.ID + ".mp4"
			if err := copyFile(filepath.Join(dir, name), t.Recording.File); err != nil {
				g.log.Warn("export: recording", "task", t.ID, "err", err)
				t.Recording = nil
				continue
			}
			t.Recording.File = name
			out.videos = append(out.videos, filepath.Join(dir, name))
		}
	}
	data, err := json.MarshalIndent(map[string]any{"exported": now, "tasks": tasks}, "", "  ")
	if err != nil {
		return out, err
	}
	out.json = filepath.Join(dir, base+".json")
	if err := os.WriteFile(out.json, append(data, '\n'), 0o600); err != nil {
		return out, err
	}
	out.html = filepath.Join(dir, base+".html")
	f, err := os.OpenFile(out.html, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return out, err
	}
	err = writeReport(f, now, tasks, shots)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return out, err
}

// exportTasks gathers the tasks to export, oldest first.
func (g *Gateway) exportTasks(ctx context.Context, ids []string) ([]exportTask, error) {
	if g.store == nil {
		var out []exportTask
		for _, v := range g.memoryHistory(HistoryQuery{Limit: 1 << 30}) {
			if len(ids) > 0 && !contains(ids, v.ID) || len(ids) == 0 && v.Ended.IsZero() {
				continue
			}
			t := exportTask{ID: v.ID, Device: v.DeviceName, DeviceID: v.DeviceID, Agent: v.Agent, Title: v.Title,
				State: string(v.State), Started: v.Started}
			if !v.Ended.IsZero() {
				e := v.Ended
				t.Ended = &e
			}
			for _, st := range v.Steps {
				t.Steps = append(t.Steps, exportStep{Index: st.Index, Text: st.Text, Tool: st.Tool, Args: rawArgs(st.Args),
					Status: st.Status, Started: st.At, Elapsed: st.Elapsed.Milliseconds()})
			}
			out = append(out, t)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
		return out, nil
	}
	var recs []history.Task
	if len(ids) == 0 {
		all, err := g.store.Tasks(ctx, history.Query{Limit: 1 << 30})
		if err != nil {
			return nil, err
		}
		for _, t := range all {
			if !t.Ended.IsZero() {
				recs = append(recs, t)
			}
		}
	} else {
		for _, id := range ids {
			t, ok, err := g.store.Task(ctx, id)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("export: no task %s", id)
			}
			recs = append(recs, t)
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Started.Before(recs[j].Started) })
	out := make([]exportTask, 0, len(recs))
	for _, r := range recs {
		t := exportTask{ID: r.ID, Device: r.DeviceName, DeviceID: r.DeviceID, Agent: r.Agent, Title: r.Title, State: r.State,
			Outcome: r.Outcome, Summary: r.Summary, Started: r.Started}
		if !r.Ended.IsZero() {
			e := r.Ended
			t.Ended = &e
		}
		steps, err := g.store.Steps(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		views := make([]StepView, 0, len(steps))
		for _, st := range steps {
			t.Steps = append(t.Steps, exportStep{Index: st.Index, Text: st.Text, Tool: st.Tool, Args: rawArgs(st.Args),
				Intent: st.Intent, Status: st.Status, Started: st.Started, Elapsed: st.Elapsed.Milliseconds(),
				Before: st.Before, After: st.After, Screen: st.ScreenText})
			views = append(views, StepView{Index: st.Index, Text: st.Text, At: st.Started})
		}
		aps, err := g.store.Approvals(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		for _, a := range aps {
			e := exportApproval{ID: a.ID, Kind: a.Kind, Rule: a.Rule, Title: a.Title, Decision: a.Decision, Note: a.Note,
				Step: a.StepIndex, Created: a.Created, Waited: a.Waited.Milliseconds()}
			if !a.Decided.IsZero() {
				d := a.Decided
				e.Decided = &d
			}
			t.Approvals = append(t.Approvals, e)
		}
		if rec, err := g.store.Recording(ctx, r.ID); err == nil {
			view := RecordingView{TaskID: r.ID, Path: rec.Path, Start: rec.Started, Length: rec.Length}
			if info, err := recording.Probe(rec.Path); err == nil {
				view.Length = info.Length
			}
			er := &exportRecording{File: rec.Path, Codec: rec.Codec, Length: view.Length.Milliseconds()}
			for _, m := range marksFor(view, views) {
				er.Marks = append(er.Marks, exportMark{At: m.At.Milliseconds(), Step: m.Step, Text: m.Text})
			}
			t.Recording = er
		}
		out = append(out, t)
	}
	return out, nil
}

func rawArgs(s string) json.RawMessage {
	if s == "" || s == "{}" || !json.Valid([]byte(s)) {
		return nil
	}
	return json.RawMessage(s)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// uniqueBase returns base, or base-2, base-3, ..., whichever has no files
// in dir yet.
func uniqueBase(dir, base string) string {
	name := base
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name+".html")); errors.Is(err, os.ErrNotExist) {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// reportTask is a task as the report template shows it.
type reportTask struct {
	exportTask
	When     string
	Duration string
	Steps    []reportStep
}

type reportStep struct {
	exportStep
	// Line is the step's first line, and Sub the rest, which the client
	// also shows smaller: "Always allowed: “Payments” in Dinely".
	Line, Sub           string
	Offset              string
	Took                string
	BeforeURL, AfterURL template.URL
}

func writeReport(w io.Writer, now time.Time, tasks []exportTask, shots map[string]string) error {
	var rt []reportTask
	for _, t := range tasks {
		r := reportTask{exportTask: t, When: t.Started.Format("Mon 2 Jan 2006, 15:04")}
		if t.Ended != nil {
			r.Duration = shortDuration(t.Ended.Sub(t.Started))
		}
		prevAfter := ""
		for _, st := range t.Steps {
			rs := reportStep{exportStep: st, Offset: "+" + shortDuration(st.Started.Sub(t.Started)),
				Took: shortDuration(time.Duration(st.Elapsed) * time.Millisecond)}
			rs.Line, rs.Sub, _ = strings.Cut(st.Text, "\n")
			// A step usually starts from the picture the one before ended
			// with: show that once.
			if st.Before != "" && st.Before != prevAfter {
				rs.BeforeURL = template.URL(shots[st.Before])
			}
			if st.After != "" {
				rs.AfterURL = template.URL(shots[st.After])
				prevAfter = st.After
			}
			r.Steps = append(r.Steps, rs)
		}
		rt = append(rt, r)
	}
	return reportTemplate.Execute(w, map[string]any{
		"Exported": now.Format("Mon 2 Jan 2006, 15:04"), "Tasks": rt, "Count": len(rt),
	})
}

func shortDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "0 s"
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1f s", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%d min %d s", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}

var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"ms":    func(v int64) string { return shortDuration(time.Duration(v) * time.Millisecond) },
	"sec":   func(v int64) string { return fmt.Sprintf("%.3f", float64(v)/1000) },
	"lower": strings.ToLower,
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Pinky history</title>
<style>
:root { --bg: #fbfaf8; --card: #ffffff; --ink: #1d1b19; --ink2: #5f5a54; --line: #e7e2dc; --ok: #2f7d4f; --warn: #a3561b; --bad: #b3261e; }
@media (prefers-color-scheme: dark) { :root { --bg: #171614; --card: #201f1c; --ink: #f1ede7; --ink2: #aaa39a; --line: #36332e; --ok: #6cc28f; --warn: #e3a066; --bad: #f08a80; } }
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--ink); font: 15px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; }
main { max-width: 980px; margin: 0 auto; padding: 32px 16px 64px; }
h1 { font-size: 26px; margin: 0 0 4px; }
.sub { color: var(--ink2); margin: 0 0 28px; }
section.task { background: var(--card); border: 1px solid var(--line); border-radius: 12px; padding: 20px; margin: 0 0 24px; }
h2 { font-size: 19px; margin: 0 0 4px; }
.meta { color: var(--ink2); font-size: 14px; margin: 0 0 12px; }
.summary { margin: 0 0 12px; }
ol.steps { list-style: none; padding: 0; margin: 0; }
ol.steps li { border-top: 1px solid var(--line); padding: 10px 0; display: grid; grid-template-columns: 72px 1fr; gap: 4px 12px; }
.at { color: var(--ink2); font-size: 13px; font-variant-numeric: tabular-nums; padding-top: 1px; }
.text { overflow-wrap: anywhere; }
.status { display: inline-block; font-size: 12px; border: 1px solid var(--line); border-radius: 99px; padding: 0 8px; margin-left: 6px; color: var(--ink2); }
.status.settled { color: var(--ok); }
.status.approval_denied, .status.blocked_by_policy, .status.stopped, .status.wrong_pin { color: var(--bad); }
.status.uncertain, .status.stale { color: var(--warn); }
.detail, .intent { color: var(--ink2); font-size: 14px; }
.detail { white-space: pre-line; }
.shots { grid-column: 2; display: flex; gap: 8px; flex-wrap: wrap; }
.shots figure { margin: 0; }
.shots img { width: 150px; border-radius: 8px; border: 1px solid var(--line); cursor: zoom-in; display: block; }
.shots img.big { width: min(420px, 100%); cursor: zoom-out; }
.shots figcaption { font-size: 12px; color: var(--ink2); }
video { width: min(360px, 100%); border-radius: 10px; background: #000; display: block; margin: 0 0 8px; }
.marks { font-size: 14px; margin: 0 0 12px; padding-left: 18px; }
.marks a { color: inherit; }
details { margin-top: 12px; color: var(--ink2); font-size: 14px; }
table { border-collapse: collapse; width: 100%; }
td, th { text-align: left; border-top: 1px solid var(--line); padding: 4px 8px 4px 0; vertical-align: top; }
</style>
</head>
<body>
<main>
<h1>Pinky history</h1>
<p class="sub">{{.Count}} task{{if ne .Count 1}}s{{end}} · exported {{.Exported}}</p>
{{range .Tasks}}
<section class="task" id="{{.ID}}">
<h2>{{.Title}}</h2>
<p class="meta">{{.Agent}} on {{.Device}} · {{.When}}{{if .Duration}} · {{.Duration}}{{end}} · {{.State}}{{if and .Outcome (ne .Outcome .State)}} ({{.Outcome}}){{end}}</p>
{{if .Summary}}<p class="summary">{{.Summary}}</p>{{end}}
{{if .Recording}}
<video controls preload="metadata" src="{{.Recording.File}}" id="v-{{.ID}}"></video>
{{if eq .Recording.Codec "hevc"}}<p class="meta">This recording is HEVC, which some browsers do not play; open the file in a video player.</p>{{end}}
{{if .Recording.Marks}}<ol class="marks">{{$id := .ID}}{{range .Recording.Marks}}<li><a href="#" data-video="v-{{$id}}" data-at="{{sec .At}}">{{ms .At}}</a> {{.Text}}</li>{{end}}</ol>{{end}}
{{end}}
<ol class="steps">
{{range .Steps}}
<li>
<span class="at">{{.Offset}}</span>
<div><span class="text">{{.Line}}</span><span class="status {{lower .Status}}">{{.Status}}</span>{{if .Sub}}<div class="detail">{{.Sub}}</div>{{end}}{{if .Intent}}<div class="intent">{{.Intent}}</div>{{end}}</div>
{{if or .BeforeURL .AfterURL}}
<div class="shots">
{{with .BeforeURL}}<figure><img src="{{.}}" alt="Before"><figcaption>Before</figcaption></figure>{{end}}
{{with .AfterURL}}<figure><img src="{{.}}" alt="After"><figcaption>After</figcaption></figure>{{end}}
</div>
{{end}}
</li>
{{end}}
</ol>
{{if .Approvals}}
<details><summary>Approvals ({{len .Approvals}})</summary>
<table><tr><th>Asked</th><th>Rule</th><th>Answer</th><th>Note</th></tr>
{{range .Approvals}}<tr><td>{{.Title}}</td><td>{{.Rule}}</td><td>{{.Decision}}</td><td>{{.Note}}</td></tr>{{end}}
</table></details>
{{end}}
</section>
{{end}}
</main>
<script>
document.addEventListener("click", function (e) {
  var t = e.target;
  if (t.tagName === "IMG" && t.closest(".shots")) { t.classList.toggle("big"); }
  if (t.dataset && t.dataset.video) {
    e.preventDefault();
    var v = document.getElementById(t.dataset.video);
    if (v) { v.currentTime = parseFloat(t.dataset.at); v.play(); }
  }
});
</script>
</body>
</html>
`))
