package history

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPrune checks what retention deletes: a finished task older than the
// cutoff, with the screenshot only it showed, and a recording; a task the
// caller asked to keep; a newer task; and a screenshot a newer task still
// shows. A cancelled context deletes nothing.
func TestPrune(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	old := time.Now().Add(-40 * 24 * time.Hour).Truncate(time.Millisecond)
	recent := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	shared := saveShot(t, s)
	onlyOld := saveShot(t, s)
	keptShot := saveShot(t, s)

	saveEnded(t, s, "old", old)
	saveEnded(t, s, "kept", old)
	saveEnded(t, s, "new", recent)
	saveStepShot(t, s, "old", 0, onlyOld)
	saveStepShot(t, s, "old", 1, shared)
	saveStepShot(t, s, "new", 0, shared)
	saveStepShot(t, s, "kept", 0, keptShot)
	recPath := saveRecFile(t, s, "old", old)

	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	out, err := s.Prune(ctx, cutoff, []string{"kept"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tasks != 1 || out.Steps != 2 || out.Media != 1 || out.Recordings != 1 {
		t.Fatalf("pruned %+v, want 1 task, 2 steps, 1 screenshot, 1 recording", out)
	}
	if _, ok, err := s.Task(ctx, "old"); err != nil || ok {
		t.Fatalf("old task still there: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.Task(ctx, "kept"); err != nil || !ok {
		t.Fatalf("kept task: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.Task(ctx, "new"); err != nil || !ok {
		t.Fatalf("new task: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(recPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old recording: %v", err)
	}
	if _, err := s.MediaPath(ctx, onlyOld); !errors.Is(err, ErrNoMedia) {
		t.Fatalf("screenshot only the old task showed: %v", err)
	}
	if _, err := s.MediaPath(ctx, shared); err != nil {
		t.Fatalf("screenshot a newer task still shows: %v", err)
	}
	if _, err := s.MediaPath(ctx, keptShot); err != nil {
		t.Fatalf("screenshot of a task retention was told to keep: %v", err)
	}

	gone, err := s.Prune(context.Background(), cutoff, []string{"kept"})
	if err != nil || gone.Tasks != 0 {
		t.Fatalf("second prune: %+v %v", gone, err)
	}
	// Already cancelled: the read fails before anything is deleted.
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Prune(cancelCtx, time.Now().Add(time.Hour), nil); err == nil {
		t.Fatal("prune with a cancelled context succeeded")
	}
	if _, ok, err := s.Task(ctx, "new"); err != nil || !ok {
		t.Fatalf("a failed prune removed a task: ok=%v err=%v", ok, err)
	}
}

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func saveShot(t *testing.T, s *Store) string {
	t.Helper()
	m, err := s.SaveScreenshot(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func saveEnded(t *testing.T, s *Store, id string, at time.Time) {
	t.Helper()
	err := s.SaveTask(context.Background(), Task{
		ID: id, Title: id, State: "done", Started: at.Add(-time.Minute), Ended: at,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func saveStepShot(t *testing.T, s *Store, task string, index int, media string) {
	t.Helper()
	err := s.SaveStep(context.Background(), Step{
		TaskID: task, Index: index, Tool: "phone_tap", Text: "Tapped",
		Status: "settled", Started: time.Now(), Before: media,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func saveRecFile(t *testing.T, s *Store, task string, at time.Time) string {
	t.Helper()
	path, err := s.RecordingPath(task, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a real recording"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = s.SaveRecording(context.Background(), Recording{
		TaskID: task, Path: path, Started: at, Length: time.Second, Codec: "h264", Width: 8, Height: 8, Size: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}
