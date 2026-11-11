package gateway

import (
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShotName(t *testing.T) {
	ok := map[string]string{
		"onboarding-1":          "onboarding-1.png",
		"onboarding-1.png":      "onboarding-1.png",
		"bugs/login.PNG":        "bugs/login.PNG",
		"photo.jpg":             "photo.jpg",
		"photo.jpeg":            "photo.jpeg",
		" Pinky/shots/a.png ":   "shots/a.png",
		"2026-11-09 login page": "2026-11-09 login page.png",
	}
	for in, want := range ok {
		if got, why := shotName(in); why != "" || got != want {
			t.Errorf("shotName(%q) = %q, %q; want %q", in, got, why, want)
		}
	}
	for _, in := range []string{"", "../a.png", ".a.png", "a.gif", "a.webp", "shots/", "/tmp/a.png", "a:b.png"} {
		if got, why := shotName(in); why == "" {
			t.Errorf("shotName(%q) = %q, accepted", in, got)
		}
	}
}

func decodeFile(t *testing.T, p string, dec func(*os.File) (image.Image, error)) image.Image {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := dec(f)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return img
}

func TestObserveSave(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t, func(o *Options) { o.DataDir = t.TempDir() })
	s, _ := connect(t, g, "Report a bug")
	dir := g.FilesDir("pixel")

	// A screenshot as a file, covered as the agent's are, without an image
	// in the result.
	dev.Show("messages")
	res := s.Observe(ctx, ObserveRequest{Save: "bugs/messages"})
	mustStatus(t, res, StatusSettled)
	want := filepath.Join(dir, "bugs", "messages.png")
	if res.Screenshot != nil || len(res.Saved) != 1 || res.Saved[0].Path != want || res.Saved[0].Name != "bugs/messages.png" ||
		!strings.Contains(res.Message, "Saved the screenshot as bugs/messages.png (1080×2400, ") ||
		!strings.Contains(res.Message, "One-time codes on the screen are covered in the screenshot.") {
		t.Fatalf("save: %+v", res)
	}
	img := decodeFile(t, want, func(f *os.File) (image.Image, error) { return png.Decode(f) })
	if b := img.Bounds(); b.Dx() != 1080 || b.Dy() != 2400 {
		t.Fatalf("saved at %v, not the screen's full size", b)
	}
	if !covered(img.At(codeRow.X, codeRow.Y)) {
		t.Fatal("the saved screenshot shows the one-time code")
	}
	if fi, err := os.Stat(want); err != nil || fi.Size() != res.Saved[0].Size {
		t.Fatalf("size %d, %v", res.Saved[0].Size, err)
	}

	// Again under the same name, with the image too: the file is replaced.
	dev.Show("settings")
	res = s.Observe(ctx, ObserveRequest{Save: "bugs/messages.png", Screenshot: true})
	if res.Screenshot == nil || len(res.Saved) != 1 || !strings.Contains(res.Message, "Saved the screenshot over bugs/messages.png") {
		t.Fatalf("replace: %+v", res)
	}
	if img := decodeFile(t, want, func(f *os.File) (image.Image, error) { return png.Decode(f) }); covered(img.At(codeRow.X, codeRow.Y)) {
		t.Fatal("the file was not replaced")
	}
	res = s.Observe(ctx, ObserveRequest{Save: "settings.jpg"})
	if len(res.Saved) != 1 {
		t.Fatalf("jpg: %+v", res)
	}
	decodeFile(t, filepath.Join(dir, "settings.jpg"), func(f *os.File) (image.Image, error) { return jpeg.Decode(f) })

	// Nothing of a blocked app, nor of a locked phone.
	dev.Show("wallet")
	res = s.Observe(ctx, ObserveRequest{Save: "wallet.png"})
	if len(res.Saved) != 0 || !strings.Contains(res.Message, "Nothing was saved.") {
		t.Fatalf("blocked app saved: %+v", res)
	}
	dev.Show("settings")
	dev.SetLocked(true)
	res = s.Observe(ctx, ObserveRequest{Save: "locked.png"})
	if len(res.Saved) != 0 || !strings.Contains(res.Message, "Nothing was saved.") {
		t.Fatalf("locked phone saved: %+v", res)
	}
	dev.SetLocked(false)
	for _, name := range []string{"wallet.png", "locked.png"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s written: %v", name, err)
		}
	}

	// Names that cannot be a file in the folder write nothing.
	for _, name := range []string{"../out.png", ".hidden.png", "clip.gif", "login v1.2"} {
		res := s.Observe(ctx, ObserveRequest{Save: name})
		if res.Error != ErrInvalidArgument {
			t.Errorf("save %q: %+v", name, res)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "out.png")); !os.IsNotExist(err) {
		t.Fatalf("wrote out of the folder: %v", err)
	}

	var texts []string
	for _, st := range taskSteps(g, s) {
		if st.Tool == "phone_observe" {
			texts = append(texts, st.Text)
		}
	}
	if got := strings.Join(texts, "\n"); got != "Saved a screenshot as “bugs/messages.png”\nSaved a screenshot as “bugs/messages.png”\nSaved a screenshot as “settings.jpg”" {
		t.Fatalf("steps %q", texts)
	}
}

// Saving touches nothing on the phone, so read-only and watch-only agents
// and read-only mode may.
func TestObserveSaveReadOnly(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t, func(o *Options) { o.DataDir = t.TempDir() })
	g.SetReadOnly("pixel", true)
	s := g.OpenSession(agent)
	t.Cleanup(s.Close)
	mustStatus(t, s.Connect(ctx, ConnectRequest{Task: "Look around", ReadOnly: true}), StatusSettled)
	dev.Show("settings")
	if res := s.Observe(ctx, ObserveRequest{Save: "settings.png"}); len(res.Saved) != 1 {
		t.Fatalf("read-only save: %+v", res)
	}
	if res := s.Release(ctx, ReleaseRequest{Export: true}); len(res.Saved) != 2 {
		t.Fatalf("read-only export: %+v", res)
	}

	g.SetReadOnly("pixel", false)
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeWatchOnly})
	s2 := g.OpenSession(agent)
	t.Cleanup(s2.Close)
	mustStatus(t, s2.Connect(ctx, ConnectRequest{Task: "Watch"}), StatusSettled)
	if res := s2.Observe(ctx, ObserveRequest{Save: "watch.png"}); len(res.Saved) != 1 {
		t.Fatalf("watch-only save: %+v", res)
	}
}

func TestObserveSaveWithoutDataDir(t *testing.T) {
	g, _ := newTestGateway(t)
	s, _ := connect(t, g, "Report a bug")
	if res := s.Observe(context.Background(), ObserveRequest{Save: "a.png"}); res.Error != ErrUnsupported {
		t.Fatalf("%+v", res)
	}
	if res := s.Release(context.Background(), ReleaseRequest{Export: true}); res.Error != ErrUnsupported {
		t.Fatalf("%+v", res)
	}
}

// recordedTask connects to a phone whose screen Pinky records and takes
// two steps there. It returns the task's id.
func recordedTask(t *testing.T, readOnly bool, opts ...func(*Options)) (*Gateway, *Session, string) {
	t.Helper()
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v, append([]func(*Options){func(o *Options) { o.NoScreenshots = false }}, opts...)...)
	g.SetRecording(true)
	s := g.OpenSession(agent)
	t.Cleanup(s.Close)
	mustStatus(t, s.Connect(ctx, ConnectRequest{Task: "Reorder lunch", ReadOnly: readOnly}), StatusSettled)
	id := taskID(t, g, s)
	st := newStreamer(t, v.open(t))
	st.config()
	st.pictures(10, true)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusSettled)
	return g, s, id
}

// readExport reads the report a release saved, as HTML and as data.
func readExport(t *testing.T, res *Result) (string, exportTask) {
	t.Helper()
	if len(res.Saved) < 2 {
		t.Fatalf("saved %+v", res)
	}
	b, err := os.ReadFile(res.Saved[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tasks []exportTask `json:"tasks"`
	}
	if js, err := os.ReadFile(res.Saved[1].Path); err != nil || json.Unmarshal(js, &doc) != nil || len(doc.Tasks) != 1 {
		t.Fatalf("json %+v, %v", doc, err)
	}
	return string(b), doc.Tasks[0]
}

// The person gives the agent the task's screen recording: it goes with the
// report. The card offers nothing that would skip the next export's
// question.
func TestReleaseExportRecordingAllowed(t *testing.T) {
	ctx := context.Background()
	g, s, id := recordedTask(t, false)
	answer(t, g, AllowOnce, func(n NeedView) {
		if n.Rule != recordingsRule || n.Title != "Give Claude Code the screen recording of “Reorder lunch”?" ||
			n.Detail != recordingsDetail || n.DeviceName != "Pixel 9" || n.AllowTask || n.AlwaysAllow {
			t.Errorf("card %+v", n)
		}
	})
	res := s.Release(ctx, ReleaseRequest{Summary: "Done.", Export: true})
	mustStatus(t, res, StatusSettled)
	dir := g.FilesDir("pixel")
	if len(res.Saved) != 3 {
		t.Fatalf("saved %+v", res)
	}
	html, js, mp4 := res.Saved[0], res.Saved[1], res.Saved[2]
	base := strings.TrimSuffix(html.Name, ".html")
	if filepath.Dir(html.Path) != dir || !strings.HasPrefix(html.Name, "pinky-task-") || js.Name != base+".json" ||
		mp4.Name != base+"-"+id+".mp4" || filepath.Dir(mp4.Path) != dir || mp4.Size == 0 {
		t.Fatalf("names %+v", res.Saved)
	}
	if !strings.Contains(res.Message, "Released Pixel 9. Saved the report as "+html.Name+" (its steps, with the screenshots Pinky kept, and its screen recording), "+
		js.Name+" (the same as data) and "+mp4.Name+" (the recording, which the person allowed you).") || res.Note != "" {
		t.Fatalf("message %q", res.Message)
	}
	page, task := readExport(t, res)
	for _, want := range []string{"Reorder lunch", "Opened Dinely", "data:image/jpeg;base64,", "<video", mp4.Name, "Finished: done"} {
		if !strings.Contains(page, want) {
			t.Errorf("the report lacks %q", want)
		}
	}
	if task.ID != id || task.Recording == nil || task.Recording.File != mp4.Name {
		t.Fatalf("json %+v", task)
	}

	// The task's history says it, in plain words.
	steps := taskSteps(g, s)
	n := len(steps)
	if n < 3 || steps[n-3].Text != "Waiting for you: Give Claude Code the screen recording of “Reorder lunch”" || steps[n-3].Status != "allowed" ||
		steps[n-2].Text != exportVideoStepText || steps[n-1].Text != "Finished: done" {
		t.Fatalf("steps %+v", steps)
	}
}

// The person declines: the agent gets the report alone, with why and
// their note, and the release is no error.
func TestReleaseExportRecordingDeclined(t *testing.T) {
	ctx := context.Background()
	g, s, id := recordedTask(t, false)
	go func() {
		if err := g.Decide(waitNeed(t, g).ID, Deny, "Not this one."); err != nil {
			t.Error(err)
		}
	}()
	res := s.Release(ctx, ReleaseRequest{Summary: "Done.", Export: true})
	mustStatus(t, res, StatusSettled)
	if len(res.Saved) != 2 || res.Note != "Not this one." ||
		!strings.HasSuffix(res.Message, " You did not get the screen recording: the person declined, and it stays on Pinky's computer.") {
		t.Fatalf("%+v", res)
	}
	page, task := readExport(t, res)
	if strings.Contains(page, "<video") || task.Recording != nil {
		t.Fatal("the report has the recording")
	}
	dir := g.FilesDir("pixel")
	if mp4s, _ := filepath.Glob(filepath.Join(dir, "*.mp4")); len(mp4s) != 0 {
		t.Fatalf("recordings in the folder: %v", mp4s)
	}
	steps := taskSteps(g, s)
	n := len(steps)
	if n < 3 || steps[n-3].Status != string(StatusDenied) || steps[n-2].Text != exportStepText || steps[n-1].Text != "Finished: done" {
		t.Fatalf("steps %+v", steps)
	}
	// The task ended, and the person's own export still has the recording.
	g.mu.Lock()
	held := len(s.leases)
	g.mu.Unlock()
	if held != 0 {
		t.Fatal("the session still holds the phone")
	}
	g.Flush()
	if _, err := g.Recording(ctx, id); err != nil {
		t.Fatalf("recording: %v", err)
	}
}

// No answer in time counts as no.
func TestReleaseExportRecordingNoAnswer(t *testing.T) {
	g, s, _ := recordedTask(t, false, func(o *Options) { o.ApprovalTimeout = 50 * time.Millisecond })
	res := s.Release(context.Background(), ReleaseRequest{Export: true})
	mustStatus(t, res, StatusSettled)
	if len(res.Saved) != 2 || !strings.Contains(res.Message, "You did not get the screen recording: the person did not answer in time") {
		t.Fatalf("%+v", res)
	}
	if len(g.Needs()) != 0 {
		t.Fatal("the card is still open")
	}
}

// An answer that takes longer than a call waits leaves the task running:
// the agent repeats the release with the approval's id. The phone takes
// no other action meanwhile, and a takeover leaves the card open.
func TestReleaseExportRecordingPending(t *testing.T) {
	ctx := context.Background()
	g, s, _ := recordedTask(t, false, func(o *Options) { o.MaxBlock = 30 * time.Millisecond })
	req := ReleaseRequest{Summary: "Done.", Export: true}
	res := s.Release(ctx, req)
	if res.Status != StatusPending || res.ApprovalID == "" || res.Rule != recordingsRule {
		t.Fatalf("%+v", res)
	}
	id := res.ApprovalID
	if r := s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}); r.Status != StatusPending || r.ApprovalID != id {
		t.Fatalf("an action while the card is open: %+v", r)
	}
	// The same release again waits on the same card; another one is
	// another call.
	if r := s.Release(ctx, req); r.Status != StatusPending || r.ApprovalID != id || len(g.Needs()) != 1 {
		t.Fatalf("again: %+v", r)
	}
	if r := s.Release(ctx, ReleaseRequest{Summary: "Other.", Export: true, Call: Call{ApprovalID: id}}); r.Error != ErrInvalidArgument {
		t.Fatalf("other arguments: %+v", r)
	}
	if err := g.TakeOver("pixel"); err != nil {
		t.Fatal(err)
	}
	if n := g.Needs(); len(n) != 1 || n[0].ID != id {
		t.Fatalf("after a takeover: %+v", n)
	}
	if err := g.Decide(id, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	req.ApprovalID = id
	res = s.Release(ctx, req)
	mustStatus(t, res, StatusSettled)
	if len(res.Saved) != 3 || !strings.HasSuffix(res.Saved[2].Name, ".mp4") {
		t.Fatalf("%+v", res)
	}
}

// A read-only agent gets the same card. Take over on it answers no: nothing
// on the phone waits on it.
func TestReleaseExportRecordingReadOnly(t *testing.T) {
	g, s, _ := recordedTask(t, true)
	answer(t, g, TakeOver, func(n NeedView) {
		if n.Rule != recordingsRule {
			t.Errorf("card %+v", n)
		}
	})
	res := s.Release(context.Background(), ReleaseRequest{Export: true})
	mustStatus(t, res, StatusSettled)
	if len(res.Saved) != 2 || !strings.Contains(res.Message, "the person declined") {
		t.Fatalf("%+v", res)
	}
	g.mu.Lock()
	person := g.devices["pixel"].person
	g.mu.Unlock()
	if person {
		t.Fatal("the answer took over the phone")
	}
}

// A task without a recording exports its report, and says nothing of one.
func TestReleaseExportWithoutRecording(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Look around")
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusSettled)
	res := s.Release(ctx, ReleaseRequest{Export: true})
	if len(res.Saved) != 2 || strings.Contains(res.Message, "recording") {
		t.Fatalf("%+v", res)
	}
	// Another export the same second gets names of its own.
	s2, _ := connect(t, g, "Look again")
	res2 := s2.Release(ctx, ReleaseRequest{Export: true})
	if len(res2.Saved) != 2 || res2.Saved[0].Path == res.Saved[0].Path {
		t.Fatalf("%+v", res2)
	}
}

// A finance app the person allowed for the task is never saved: Pinky
// keeps no picture of it.
func TestObserveSaveAllowedFinanceApp(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t, func(o *Options) { o.DataDir = t.TempDir(); o.NoScreenshots = false })
	s, _ := connect(t, g, "Bookkeeping")
	go func() {
		g.Decide(waitNeed(t, g).ID, AllowTask, "")
	}()
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Google Wallet"}), StatusSettled)
	if dev.Current() != "wallet" {
		t.Fatalf("on %s", dev.Current())
	}
	res := s.Observe(ctx, ObserveRequest{Save: "wallet.png"})
	if len(res.Saved) != 0 || !strings.Contains(res.Message, "Nothing was saved.") {
		t.Fatalf("saved: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(g.FilesDir("pixel"), "wallet.png")); !os.IsNotExist(err) {
		t.Fatalf("written: %v", err)
	}
}
