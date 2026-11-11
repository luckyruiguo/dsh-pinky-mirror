package ui

import (
	"image"
	"os/exec"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/video"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestTileStill: an iPhone's tile shows a still while no task runs on it
// and the person does not hold it; an Android phone's tile stays live.
func TestTileStill(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	iphone := gateway.DeviceView{Info: driver.Info{ID: "iphone", Platform: driver.IOS, State: driver.StateOnline}, Status: gateway.DeviceIdle}
	android := gateway.DeviceView{Info: driver.Info{ID: "pixel", Platform: driver.Android, State: driver.StateOnline}, Status: gateway.DeviceIdle}
	working := &gateway.TaskView{ID: "t1", State: gateway.TaskWorking}
	paused := &gateway.TaskView{ID: "t2", State: gateway.TaskPaused}
	done := &gateway.TaskView{ID: "t3", State: gateway.TaskDone}
	inControl := iphone
	inControl.Status = gateway.DeviceInControl
	for _, tc := range []struct {
		name string
		d    gateway.DeviceView
		t    *gateway.TaskView
		want bool
	}{
		{"idle iPhone", iphone, nil, true},
		{"iPhone after its task", iphone, done, true},
		{"iPhone with a task", iphone, working, false},
		{"iPhone with a paused task", iphone, paused, false},
		{"iPhone the person holds", inControl, nil, false},
		{"idle Android phone", android, nil, false},
	} {
		if got := ta.tileStill(tc.d, tc.t); got != tc.want {
			t.Errorf("%s: tileStill = %v, want %v", tc.name, got, tc.want)
		}
	}
	ta.needs = []gateway.NeedView{{ID: "n1", Kind: gateway.NeedHandoff, DeviceID: "iphone"}}
	if ta.tileStill(iphone, nil) {
		t.Error("an iPhone handed to the person shows a still")
	}
}

// TestAllPhonesIPhoneStill: All phones shows an iPhone live while its task
// runs, then holds its last picture and lets its stream go, so that the
// phone can lock; its own page opens the stream again. The Android
// phone's tile stays live throughout.
func TestAllPhonesIPhoneStill(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs the ffmpeg command")
	}
	if err := video.Load(); err != nil {
		t.Skip(err)
	}
	const iphone, pixel = "iphone-17", "pixel-9"
	ta := newTestApp(t, fakeapi.ThreePhones)
	ta.sync()
	ta.app.videoErr = nil
	t.Cleanup(ta.closeMirrors)
	streaming := func(id string) bool {
		for _, d := range ta.api.Devices() {
			if d.Info.ID == id {
				return d.Streaming
			}
		}
		return false
	}
	ta.selectPhone(allPhones)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()

	// The iPhone's task runs: its tile is live, past stillAfter.
	if tk := ta.taskOf(iphone); tk == nil || !running(tk.State) {
		t.Fatalf("iPhone 17's task: %+v", tk)
	}
	ta.frameUntil(t, tt, "both tiles' pictures", func() bool {
		mi, mp := ta.mirrors[iphone], ta.mirrors[pixel]
		return mi != nil && mi.bitmap != nil && mp != nil && mp.bitmap != nil
	})
	ta.frameUntil(t, tt, "a while after the first picture", func() bool {
		return time.Since(ta.mirrors[iphone].firstAt) > 2*stillAfter
	})
	if m := ta.mirrors[iphone]; m.state != "playing" || !streaming(iphone) {
		t.Fatalf("iPhone 17's tile while its task runs: %s, streaming %v", m.state, streaming(iphone))
	}

	// The task ends: the tile keeps the picture and the stream closes.
	if err := ta.api.Stop(iphone); err != nil {
		t.Fatal(err)
	}
	m := ta.mirrors[iphone]
	ta.frameUntil(t, tt, "iPhone 17's still", func() bool {
		return m.state == "still" && m.player == nil && !streaming(iphone)
	})
	if ta.mirrors[iphone] != m || m.bitmap == nil {
		t.Fatalf("iPhone 17's still has no picture")
	}
	if mp := ta.mirrors[pixel]; mp.state != "playing" || !streaming(pixel) {
		t.Errorf("Pixel 9's tile: %s, streaming %v", mp.state, streaming(pixel))
	}
	for range 5 {
		tt.Frame()
		ta.sync()
	}
	if m.state != "still" || streaming(iphone) {
		t.Fatalf("iPhone 17's still opened its stream again: %s", m.state)
	}

	// Its own page is live again, and back on All phones it holds.
	ta.selectPhone(iphone)
	ta.frameUntil(t, tt, "iPhone 17's page live", func() bool {
		return m.state == "playing" && m.player != nil && streaming(iphone)
	})
	ta.selectPhone(allPhones)
	ta.frameUntil(t, tt, "iPhone 17's still again", func() bool {
		return ta.mirrors[iphone] == m && m.state == "still" && !streaming(iphone)
	})

	// Another page lets go of the streams; the still stays for All phones.
	ta.selectPhone(pixel)
	ta.page = pageActivity
	ta.frameUntil(t, tt, "the streams closed", func() bool { return !streaming(iphone) && !streaming(pixel) })
	if ta.mirrors[iphone] != m || m.state != "still" {
		t.Errorf("iPhone 17's still after another page: %v", ta.mirrors[iphone])
	}
}

// TestStillKeepsItsPicture: a tile's mirror opened again whose stream
// fails, or sends no picture for stillGiveUp, keeps the picture it has
// rather than the stream.
func TestStillKeepsItsPicture(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	pic := mg.NewBitmap(image.NewRGBA(image.Rect(0, 0, 2, 4)))
	for _, tc := range []struct {
		name  string
		state string
		ago   time.Duration
		want  string
	}{
		{"waiting", "connecting", time.Second, "connecting"},
		{"no picture", "connecting", stillGiveUp + time.Second, "still"},
		{"failed", "failed", time.Second, "still"},
	} {
		m := &mirror{a: ta.app, id: "pixel-9", state: tc.state, bitmap: pic,
			openedAt: time.Now().Add(-tc.ago), retry: time.Now().Add(time.Hour)}
		ta.mirrors["pixel-9"] = m
		if got := ta.stillFor("pixel-9"); got != m || m.state != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, m.state, tc.want)
		}
		if tc.want == "still" && m.bitmap != pic {
			t.Errorf("%s: the picture went", tc.name)
		}
	}
}
