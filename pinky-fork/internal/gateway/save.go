package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/screen"
)

// Files for the agent: phone_observe { save } writes a screenshot, and
// phone_release { export } the task's report, into the phone's folder on
// this computer (FilesDir), where an agent on this computer reads them and
// one on another computer downloads them through a one-time link
// (internal/mcp). They stay on this computer otherwise. What they show is
// what the agent may see: a screenshot is taken and covered as for
// phone_observe { screenshot: true }, and the report's screenshots are
// those of the task's history, covered alike. Saving them asks nothing,
// also for read-only agents and in read-only mode: the phone is not
// touched. The task's screen recording is different: it does not cover
// one-time codes or blocked apps' notifications, and catches a blocked app
// only when a read sees it. So it goes with the report only when the person
// allows it on a card of its own (recordingsRule), asked at every export.

// SavedFile is a file a call wrote for the agent on Pinky's computer.
type SavedFile struct {
	Path string // where it is, absolute
	Name string // its name in the phone's folder
	Size int64
}

// shotExts are the kinds of picture a screenshot is saved as.
var shotExts = []string{".png", ".jpg", ".jpeg"}

// shotName cleans the name a screenshot is saved as, a path inside the
// phone's folder: the rules of uploads, and .png when it names no kind.
func shotName(p string) (name, why string) {
	name, why = uploadName(p)
	if why != "" {
		return "", why
	}
	ext := strings.ToLower(path.Ext(name))
	switch {
	case ext == "":
		name += ".png"
	case !contains(shotExts, ext):
		return "", "a screenshot is saved as .png or .jpg"
	}
	return name, ""
}

// saveFile writes rel in dir, the phone's folder on this computer, through
// a temporary file, so a failed write leaves nothing half written and a
// link in the way is replaced, not followed. A file of that name is
// replaced, and replaced says so.
func saveFile(dir, rel string, write func(io.Writer) error) (f SavedFile, replaced bool, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return f, false, err
	}
	old, why, err := uploadTarget(dir, rel)
	switch {
	case err != nil:
		return f, false, err
	case why != "":
		return f, false, fmt.Errorf("%s cannot be saved: %s", rel, why)
	}
	dst, err := localPath(dir, rel)
	if err != nil {
		return f, false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".pinky-*")
	if err != nil {
		return f, false, err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	err = write(w)
	if err == nil {
		err = w.Flush()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return f, false, err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return f, false, err
	}
	fi, err := os.Stat(dst)
	if err != nil {
		return f, false, err
	}
	return SavedFile{Path: dst, Name: rel, Size: fi.Size()}, old != nil, nil
}

// saveShot saves a screenshot the agent may see as rel in the phone's
// folder, at full resolution: a PNG, or a JPEG for a .jpg name. It adds the
// step that says so to the task, and returns the message for the agent.
func (g *Gateway) saveShot(l *lease, rel string, img image.Image) (SavedFile, string, error) {
	enc := func(w io.Writer) error { return png.Encode(w, img) }
	if ext := strings.ToLower(path.Ext(rel)); ext == ".jpg" || ext == ".jpeg" {
		enc = func(w io.Writer) error { return jpeg.Encode(w, img, &jpeg.Options{Quality: 92}) }
	}
	f, replaced, err := saveFile(g.FilesDir(l.dev.id), rel, enc)
	if err != nil {
		return SavedFile{}, "", err
	}
	g.addStep(l, StepView{Tool: "phone_observe", Args: argsJSON(map[string]any{"save": rel}),
		Text: "Saved a screenshot as " + quoteName(rel), Status: string(StatusSettled), At: time.Now()})
	b := img.Bounds()
	verb := "Saved the screenshot as"
	if replaced {
		verb = "Saved the screenshot over"
	}
	return f, fmt.Sprintf("%s %s (%d×%d, %s).", verb, rel, b.Dx(), b.Dy(), SizeText(f.Size)), nil
}

// exportStepText is the step phone_release { export } adds to a task, and
// exportVideoStepText the one when the person gave the agent its screen
// recording too.
const (
	exportStepText      = "Saved the task's report for the agent"
	exportVideoStepText = "Saved the task's report and screen recording for the agent"
)

// recordingsRule is the card that asks the person whether an agent gets
// the screen recordings of the tasks it releases with export. It asks at
// every export: it carries no policy snapshot, so no preset, Always allow
// or Allow for this task lets it through (canAlwaysAllow, canAllowTask).
// Read-only agents get the same card. A takeover leaves it open, as
// nothing on the phone waits on it.
const recordingsRule = "Recordings"

// recordingsDetail is the second line of the Recordings card: what the
// person agrees to.
const recordingsDetail = "Unlike screenshots, recordings do not cover one-time codes or blocked apps' notifications."

// askRecordings asks the person whether the agent gets the screen
// recordings of the tasks of ls (sorted, one card for all) with their
// reports. It returns their answer, or a result to return instead: the
// answer is still pending, the call was cancelled, or the person stopped
// the task.
func (g *Gateway) askRecordings(ctx context.Context, s *Session, ls []*lease, req ReleaseRequest, outcome string) (approval.Outcome, *Result) {
	l := ls[0]
	var key strings.Builder
	fmt.Fprintf(&key, "%s\x00%s\x00%s", req.Device, outcome, req.Summary)
	for _, x := range ls {
		fmt.Fprintf(&key, "\x00%s", x.task.id)
	}
	b := approval.Binding{Session: s.id, Device: l.dev.id, Lease: l.id, Tool: "phone_release", Args: digest(key.String())}
	id := req.ApprovalID
	if id != "" {
		if res := g.checkApproval(id, b); res != nil {
			if res.Error != "" {
				return approval.Outcome{}, res
			}
			id = ""
		}
	}
	if id == "" {
		// The same release while its card is open waits on that card.
		for _, info := range g.approvals.List(true) {
			if info.Binding == b && info.Rule == recordingsRule {
				id = info.ID
			}
		}
	}
	var info approval.Info
	if id == "" {
		title := fmt.Sprintf("Give %s the screen recording of “%s”?", s.agent.Name, l.task.title)
		if len(ls) > 1 {
			title = fmt.Sprintf("Give %s the screen recordings of %d tasks?", s.agent.Name, len(ls))
		}
		info = g.approvals.Open(approval.Request{Kind: approval.KindAction, Binding: b, Rule: recordingsRule, Title: title,
			Detail: recordingsDetail, Agent: s.agent.Name, TaskID: l.task.id, DeviceName: l.dev.name()})
		g.mu.Lock()
		ended, by := l.ended, l.endedBy
		g.mu.Unlock()
		if ended {
			g.approvals.Withdraw(info.ID, by)
			return approval.Outcome{}, statusResult(StatusStopped, "The lease ended (%s). Nothing was saved.", by)
		}
		args := argsJSON(map[string]any{"outcome": outcome, "summary": req.Summary, "export": true})
		g.openStep(l, info, "phone_release", args, "Waiting for you: "+strings.TrimSuffix(title, "?"), screen.Rect{}, screen.Point{}, "")
	} else {
		info, _ = g.approvals.Get(id)
	}
	out, err := g.waitApproval(ctx, l, info.ID, info, req.Progress)
	if errors.Is(err, errTaken) {
		return out, takenResult(info.ID)
	}
	if err != nil {
		return out, g.cancelled(l, info.ID)
	}
	switch out.State {
	case approval.Open:
		return out, g.pendingResult(l, info)
	case approval.Withdrawn:
		g.closeStep(info.ID, string(StatusDenied), 0)
		return out, g.withdrawnResult(l, out)
	}
	g.recordDecision(info, out)
	status := "allowed"
	if !out.Decision.Allows() {
		status = string(StatusDenied)
	}
	g.closeStep(info.ID, status, out.Waited)
	return out, nil
}

// recordingsWithheld tells the agent why it did not get the screen
// recordings the person was asked about.
func recordingsWithheld(out approval.Outcome, n int) string {
	what, it := "the screen recording", "it stays"
	if n > 1 {
		what, it = "the screen recordings", "they stay"
	}
	why := "the person declined"
	if out.TimedOut {
		why = "the person did not answer in time"
	}
	return fmt.Sprintf(" You did not get %s: %s, and %s on Pinky's computer.", what, why, it)
}

// taskExport is a task phone_release { export } saves the report of.
type taskExport struct {
	task, device, dir string
	video             bool // the person gave the agent its screen recording
}

// exportForAgent saves the reports of tasks that just ended in their
// phones' folders on this computer, once their screenshots and steps are
// written, and tells res where. It saves a task's screen recording with
// its report only where the person allowed it (video), and waits for it to
// be finished then.
func (g *Gateway) exportForAgent(ctx context.Context, exports []taskExport, res *Result) {
	videos := false
	for _, e := range exports {
		videos = videos || e.video
	}
	if videos {
		g.Flush()
	} else {
		g.shots.wait()
		g.rec.flush()
	}
	now := time.Now()
	for _, e := range exports {
		on := ""
		if len(exports) > 1 {
			on = " of the task on " + e.device
		}
		fail := func(err error) {
			res.Message += fmt.Sprintf(" Saving the report%s failed: %v.", on, err)
		}
		if err := os.MkdirAll(e.dir, 0o700); err != nil {
			fail(err)
			continue
		}
		base := uniqueBase(e.dir, "pinky-task-"+now.Format("2006-01-02-150405"))
		out, err := g.exportTo(ctx, []string{e.task}, e.dir, base, now, e.video)
		if err != nil {
			fail(err)
			continue
		}
		files := make([]SavedFile, 0, 3)
		for _, p := range append([]string{out.html, out.json}, out.videos...) {
			fi, err := os.Stat(p)
			if err != nil {
				continue
			}
			files = append(files, SavedFile{Path: p, Name: filepath.Base(p), Size: fi.Size()})
		}
		res.Saved = append(res.Saved, files...)
		if len(out.videos) > 0 {
			res.Message += fmt.Sprintf(" Saved the report%s as %s (its steps, with the screenshots Pinky kept, and its screen recording), %s (the same as data) and %s (the recording, which the person allowed you).",
				on, filepath.Base(out.html), filepath.Base(out.json), filepath.Base(out.videos[0]))
			continue
		}
		res.Message += fmt.Sprintf(" Saved the report%s as %s (its steps, with the screenshots Pinky kept) and %s (the same as data).",
			on, filepath.Base(out.html), filepath.Base(out.json))
		if e.video {
			res.Message += " Its screen recording could not be saved."
		}
	}
}
