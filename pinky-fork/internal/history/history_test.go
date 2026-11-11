package history

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "with space")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Millisecond)
	if err := s.SaveDevice(ctx, Device{ID: "d1", Platform: "android", Name: "Pixel 9", LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAgent(ctx, Agent{Name: "Claude Code", Scope: "everything", CreatedAt: now, ApprovedAt: now}); err != nil {
		t.Fatal(err)
	}
	a, ok, err := s.Agent(ctx, "Claude Code")
	if err != nil || !ok || a.Scope != "everything" || !a.ApprovedAt.Equal(now) {
		t.Fatalf("agent: %+v %v %v", a, ok, err)
	}
	if _, ok, _ := s.Agent(ctx, "nobody"); ok {
		t.Fatal("unknown agent found")
	}
	if err := s.SaveSession(ctx, Session{ID: "s1", Agent: "Claude Code", Devices: []string{"d1"}, Started: now}); err != nil {
		t.Fatal(err)
	}
	task := Task{ID: "t1", SessionID: "s1", DeviceID: "d1", DeviceName: "Pixel 9", Agent: "Claude Code",
		Title: "Reorder last week's lunch", State: "working", Started: now}
	if err := s.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 1080, 2400))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	m, err := s.SaveScreenshot(ctx, img)
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.MediaPath(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(f)
	f.Close()
	if err != nil || cfg.Width != 540 || cfg.Height != 1200 {
		t.Fatalf("stored screenshot %dx%d, %v", cfg.Width, cfg.Height, err)
	}
	st := Step{TaskID: "t1", Index: 0, Tool: "phone_tap", Args: `{"ref":15}`, Text: "Tapped “Place order”",
		Status: "approval_pending", Started: now, Before: m.ID, Target: screen.Rect{X: 1, Y: 2, W: 3, H: 4}, Point: screen.Point{X: 2, Y: 4}}
	if err := s.SaveStep(ctx, st); err != nil {
		t.Fatal(err)
	}
	st.Status, st.Before, st.After = "settled", "", "m_after"
	if err := s.SaveStep(ctx, st); err != nil {
		t.Fatal(err)
	}
	steps, err := s.Steps(ctx, "t1")
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps: %v %v", steps, err)
	}
	if got := steps[0]; got.Status != "settled" || got.Before != m.ID || got.After != "m_after" || got.Target.W != 3 || got.Point.Y != 4 {
		t.Fatalf("updated step lost data: %+v", got)
	}
	// A screenshot taken in the background is set later, alone.
	if err := s.SetStepMedia(ctx, "t1", 0, "", "m_later"); err != nil {
		t.Fatal(err)
	}
	if steps, _ = s.Steps(ctx, "t1"); steps[0].After != "m_later" || steps[0].Before != m.ID || steps[0].Status != "settled" {
		t.Fatalf("SetStepMedia: %+v", steps[0])
	}
	if err := s.SaveApproval(ctx, Approval{ID: "ap_1", TaskID: "t1", StepIndex: 0, Kind: "action", Rule: "Payments", Created: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveApproval(ctx, Approval{ID: "ap_1", TaskID: "t1", StepIndex: 0, Kind: "action", Rule: "Payments", Decision: "allow_once",
		Created: now, Decided: now.Add(3 * time.Second), Waited: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	aps, err := s.Approvals(ctx, "t1")
	if err != nil || len(aps) != 1 || aps[0].Decision != "allow_once" || aps[0].Waited != 3*time.Second {
		t.Fatalf("approvals: %+v %v", aps, err)
	}

	for search, want := range map[string]int{"": 1, "lunch": 1, "place order": 1, "pixel": 1, "nothing like this": 0, "100%": 0} {
		got, err := s.Tasks(ctx, Query{Search: search})
		if err != nil || len(got) != want {
			t.Errorf("search %q: %d tasks, %v", search, len(got), err)
		}
	}
	if got, _ := s.Tasks(ctx, Query{Before: now}); len(got) != 0 {
		t.Errorf("before filter kept %d tasks", len(got))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: the data persists.
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, _ := s.Tasks(ctx, Query{}); len(got) != 1 || got[0].Title != task.Title {
		t.Fatalf("after reopen: %+v", got)
	}

	if err := s.Wipe(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Tasks(ctx, Query{}); len(got) != 0 {
		t.Fatalf("wipe kept tasks: %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("wipe kept the media file: %v", err)
	}
	if _, err := s.MediaPath(ctx, m.ID); err != ErrNoMedia {
		t.Fatalf("media after wipe: %v", err)
	}
	if _, ok, _ := s.Agent(ctx, "Claude Code"); !ok {
		t.Fatal("wipe removed agents, which are settings")
	}
}

func TestDownscale(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	src.Set(0, 0, color.NRGBA{R: 255, A: 255})
	src.Set(1, 0, color.NRGBA{R: 255, A: 255})
	src.Set(0, 1, color.NRGBA{A: 255})
	src.Set(1, 1, color.NRGBA{A: 255})
	got := Downscale(src, 2)
	if b := got.Bounds(); b.Dx() != 2 || b.Dy() != 1 {
		t.Fatalf("size %v", b)
	}
	r, _, _, _ := got.At(0, 0).RGBA()
	if r>>8 < 120 || r>>8 > 135 {
		t.Fatalf("average red %d", r>>8)
	}
	if same := Downscale(src, 10); same != image.Image(src) {
		t.Fatal("small image should be returned as is")
	}
}

func TestDeviceConnections(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeviceConnection(ctx, "iphone", "wda"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeviceConnection(ctx, "ipad", "coredevice"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeviceConnection(ctx, "ipad", ""); err != nil { // back to automatic
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir) // survives a restart
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.DeviceConnections(ctx)
	if err != nil || len(got) != 1 || got["iphone"] != "wda" {
		t.Fatalf("connections %v, %v", got, err)
	}
	// The background choice is kept apart: a phone with only a connection
	// choice has made none about background displays.
	if bg, err := s.BackgroundDevices(ctx); err != nil || len(bg) != 0 {
		t.Fatalf("background %v, %v", bg, err)
	}
}
