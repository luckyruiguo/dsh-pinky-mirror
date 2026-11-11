package history

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestForgetDevice(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	for _, id := range []string{"pixel", "iphone"} {
		if err := s.SaveDevice(ctx, Device{ID: id, Name: id, LastSeen: now}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetDeviceBackground(ctx, id, true); err != nil {
			t.Fatal(err)
		}
		if err := s.SetDeviceConnection(ctx, id, "wda"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveTask(ctx, Task{ID: "t1", DeviceID: "pixel", DeviceName: "Pixel", Started: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetDevice(ctx, "pixel"); err != nil {
		t.Fatal(err)
	}
	var devices int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM devices WHERE id = 'pixel'`).Scan(&devices); err != nil || devices != 0 {
		t.Fatalf("pixel's record stayed: %d, %v", devices, err)
	}
	bg, err := s.BackgroundDevices(ctx)
	if err != nil || len(bg) != 1 || !bg["iphone"] {
		t.Fatalf("background %v, %v; want only the iPhone's", bg, err)
	}
	ways, err := s.DeviceConnections(ctx)
	if err != nil || len(ways) != 1 || ways["iphone"] != "wda" {
		t.Fatalf("connections %v, %v; want only the iPhone's", ways, err)
	}
	// Its history stays until asked.
	if _, ok, err := s.Task(ctx, "t1"); err != nil || !ok {
		t.Fatalf("the task went with the phone: %v, %v", ok, err)
	}
}

func TestRemovedDevices(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"mi", "pixel"} {
		if err := s.SetDeviceRemoved(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetDeviceRemoved(ctx, "mi", true); err != nil { // twice is fine
		t.Fatal(err)
	}
	if err := s.SetDeviceRemoved(ctx, "pixel", false); err != nil { // added back
		t.Fatal(err)
	}
	if err := s.SetDeviceRemoved(ctx, "never", false); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir) // survives a restart
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.RemovedDevices(ctx)
	if err != nil || !slices.Equal(got, []string{"mi"}) {
		t.Fatalf("removed %v, %v; want [mi]", got, err)
	}
}

func TestDeviceIDs(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"pixel", "iphone", "ipad"} {
		if err := s.SaveDevice(ctx, Device{ID: id, LastSeen: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetDeviceRemoved(ctx, "pixel", true); err != nil {
		t.Fatal(err)
	}
	got, err := s.DeviceIDs(ctx)
	if err != nil || !slices.Equal(got, []string{"ipad", "iphone"}) {
		t.Fatalf("DeviceIDs = %v, %v; want the phones seen and not removed", got, err)
	}
	if err := s.SaveDevice(ctx, Device{ID: "iphone", Name: "DIYgod's iPhone", Model: "iPhone 13 mini", LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	recs, err := s.SeenDevices(ctx)
	if err != nil || len(recs) != 2 || recs[1].ID != "iphone" || recs[1].Name != "DIYgod's iPhone" || recs[1].Model != "iPhone 13 mini" {
		t.Fatalf("SeenDevices = %+v, %v", recs, err)
	}
}

func TestDeleteDeviceHistory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	removedAt := start.Add(30 * time.Minute)
	shot := func() string {
		t.Helper()
		m, err := s.SaveScreenshot(ctx, image.NewRGBA(image.Rect(0, 0, 4, 4)))
		if err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	shared := shot() // shown by a step of each phone
	own := shot()
	other := shot()
	task := func(id, session, device string, started time.Time, before, after string) {
		t.Helper()
		if err := s.SaveTask(ctx, Task{ID: id, SessionID: session, DeviceID: device, DeviceName: device, State: "done",
			Started: started, Ended: started.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveStep(ctx, Step{TaskID: id, Index: 0, Tool: "phone_tap", Started: started, Before: before, After: after}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveApproval(ctx, Approval{ID: "a_" + id, TaskID: id, Created: started}); err != nil {
			t.Fatal(err)
		}
	}
	task("t_mi", "s_mi", "mi", start, own, shared)
	task("t_mi2", "s_both", "mi", start.Add(time.Minute), "", "")
	task("t_pixel", "s_both", "pixel", start, shared, other)
	task("t_later", "s_later", "mi", removedAt.Add(time.Minute), "", "") // started after the removal: stays
	rec, err := s.RecordingPath("t_mi", start)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(rec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte("mp4"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRecording(ctx, Recording{TaskID: "t_mi", Path: rec, Started: start}); err != nil {
		t.Fatal(err)
	}
	ended := start.Add(10 * time.Minute)
	for _, x := range []Session{
		{ID: "s_mi", Devices: []string{"mi"}, Started: start, Ended: ended},                         // only the Mi: goes
		{ID: "s_both", Devices: []string{"mi", "pixel"}, Started: start, Ended: ended},              // the Pixel's task stays
		{ID: "s_open", Devices: []string{"mi"}, Started: start},                                     // still open: stays
		{ID: "s_pixel", Devices: []string{"pixel"}, Started: start, Ended: ended},                   // another phone's: stays
		{ID: "s_later", Devices: []string{"mi"}, Started: removedAt.Add(time.Minute), Ended: ended}, // has t_later
	} {
		if err := s.SaveSession(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	paths := map[string]string{}
	for _, id := range []string{shared, own, other} {
		p, err := s.MediaPath(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		paths[id] = p
	}

	n, err := s.DeleteDeviceHistory(ctx, "mi", removedAt)
	if err != nil {
		t.Fatal(err)
	}
	if n.Tasks != 2 || n.Steps != 2 || n.Approvals != 2 || n.Recordings != 1 || n.Media != 1 || n.Sessions != 1 {
		t.Fatalf("deleted %+v; want 2 tasks, 2 steps, 2 approvals, 1 recording, 1 screenshot, 1 session", n)
	}
	for id, want := range map[string]bool{"t_mi": false, "t_mi2": false, "t_pixel": true, "t_later": true} {
		if _, ok, err := s.Task(ctx, id); err != nil || ok != want {
			t.Errorf("task %s kept %v, %v; want %v", id, ok, err, want)
		}
	}
	if _, err := os.Stat(rec); !os.IsNotExist(err) {
		t.Errorf("the recording's file stayed: %v", err)
	}
	if _, err := os.Stat(paths[own]); !os.IsNotExist(err) {
		t.Errorf("the Mi's own screenshot stayed: %v", err)
	}
	for _, id := range []string{shared, other} {
		if _, err := s.MediaPath(ctx, id); err != nil {
			t.Errorf("screenshot %s, which the Pixel's step shows, went: %v", id, err)
		}
	}
	sessions, err := s.strings(ctx, `SELECT id FROM sessions ORDER BY id`)
	if err != nil || !slices.Equal(sessions, []string{"s_both", "s_later", "s_open", "s_pixel"}) {
		t.Fatalf("sessions %v, %v", sessions, err)
	}
}
