package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/history"
)

// ageTask moves a stored task, its steps and its media back in time.
func ageTask(t *testing.T, g *Gateway, id string, by time.Duration) {
	t.Helper()
	g.Flush()
	ctx := context.Background()
	rec, ok, err := g.store.Task(ctx, id)
	if err != nil || !ok {
		t.Fatalf("task %s: %v", id, err)
	}
	rec.Started = rec.Started.Add(-by)
	if !rec.Ended.IsZero() {
		rec.Ended = rec.Ended.Add(-by)
	}
	if err := g.store.SaveTask(ctx, rec); err != nil {
		t.Fatal(err)
	}
}

func mediaCount(t *testing.T, g *Gateway) int {
	t.Helper()
	n := 0
	filepath.Walk(filepath.Join(g.opt.DataDir, "media"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestRetention(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v, func(o *Options) { o.NoScreenshots = false })
	g.SetRecording(true)

	// An old task with screenshots and a recording.
	s, _ := connect(t, g, "Old lunch")
	old := taskID(t, g, s)
	st := newStreamer(t, v.open(t))
	st.config()
	st.pictures(10, true)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
	g.Flush()
	rec, err := g.Recording(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if mediaCount(t, g) == 0 {
		t.Fatal("no screenshots to delete")
	}
	ageTask(t, g, old, 40*24*time.Hour)

	// A running task that started long ago stays.
	s2, _ := connect(t, g, "Long task")
	running := taskID(t, g, s2)
	ageTask(t, g, running, 40*24*time.Hour)

	g.SetRetention(30)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, ok, _ := g.store.Task(ctx, old)
		if !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the old task was not deleted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok, _ := g.store.Task(ctx, running); !ok {
		t.Fatal("the running task was deleted")
	}
	if steps, _ := g.store.Steps(ctx, old); len(steps) != 0 {
		t.Fatalf("%d steps left", len(steps))
	}
	if _, err := os.Stat(rec.Path); !os.IsNotExist(err) {
		t.Fatalf("the recording is still there: %v", err)
	}
	if _, err := g.store.Recording(ctx, old); err != history.ErrNoRecording {
		t.Fatalf("recording row: %v", err)
	}
	if n := mediaCount(t, g); n != 0 {
		// The running task's first screen has no screenshot (no step shows
		// one yet), so nothing should be left.
		t.Fatalf("%d screenshots left", n)
	}
	if g.Retention() != 30 {
		t.Fatalf("retention %d", g.Retention())
	}
}

func TestExportHistory(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v, func(o *Options) { o.NoScreenshots = false })
	g.SetRecording(true)
	s, _ := connect(t, g, "Reorder <lunch> & pay")
	id := taskID(t, g, s)
	st := newStreamer(t, v.open(t))
	st.config()
	st.pictures(10, true)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusSettled)
	mustStatus(t, s.Release(ctx, ReleaseRequest{Summary: "Done."}), StatusSettled)
	g.Flush()

	dir := t.TempDir()
	path, err := g.ExportHistory(ctx, nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	for _, want := range []string{"Reorder &lt;lunch&gt; &amp; pay", "Opened Dinely", "data:image/jpeg;base64,", "<video"} {
		if !strings.Contains(html, want) {
			t.Errorf("the report lacks %q", want)
		}
	}
	if strings.Contains(html, "<lunch>") {
		t.Error("the task title is not escaped")
	}
	data, err := os.ReadFile(strings.TrimSuffix(path, ".html") + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tasks []exportTask `json:"tasks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Tasks) != 1 || doc.Tasks[0].ID != id || len(doc.Tasks[0].Steps) < 3 || doc.Tasks[0].Recording == nil {
		t.Fatalf("json %+v", doc.Tasks)
	}
	if _, err := os.Stat(filepath.Join(dir, doc.Tasks[0].Recording.File)); err != nil {
		t.Fatalf("the recording was not copied: %v", err)
	}
	if len(doc.Tasks[0].Recording.Marks) == 0 {
		t.Fatal("no marks")
	}
	// One task by id, into the same folder: another name.
	path2, err := g.ExportHistory(ctx, []string{id}, dir)
	if err != nil || path2 == path {
		t.Fatalf("second export %q, %v", path2, err)
	}
	if _, err := g.ExportHistory(ctx, []string{"t_nope"}, dir); err == nil {
		t.Fatal("exported an unknown task")
	}
}
