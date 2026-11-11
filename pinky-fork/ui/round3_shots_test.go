package ui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/secret"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestRound3Screenshots renders round three's screens in light and dark
// into $PINKY_SCREENSHOTS, for the notes: pairing, the companion card,
// the agent's screen, unlocking, the recording player and Settings.
// Tests have no video, so the mirrors show stills of the fake's test
// source and of a demo phone's drawn app.
func TestRound3Screenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS to a folder to write the screenshots")
	}
	_, devs := demo.World()
	app, err := devs[0].Screenshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	still := func(ta *testApp, key, id string, img image.Image, agent bool) {
		b := img.Bounds()
		ta.mirrors[key] = &mirror{a: ta.app, id: id, agent: agent, state: "playing", bitmap: mg.NewBitmap(img),
			picW: b.Dx(), picH: b.Dy(), used: ta.buildSeq, retry: time.Now().Add(time.Hour)}
	}
	testSource := func(ta *testApp, id string) image.Image {
		path, err := ta.api.MediaPath(context.Background(), "shot-"+id)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	shoot := func(ta *testApp, name string, before func()) {
		t.Helper()
		for _, dark := range []bool{false, true} {
			if before != nil {
				before()
			}
			tt := mg.NewTester(ta.view, 1280, 820)
			tt.SetScale(2)
			tt.SetDark(dark)
			tt.Frame()
			if before != nil {
				before() // the first build swept the stills it did not show yet
			}
			tt.Frame()
			file := name + "-light.png"
			if dark {
				file = name + "-dark.png"
			}
			f, err := os.Create(filepath.Join(dir, file))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}

	// Pairing over Wi-Fi: the QR code, then the pairing-code form.
	ta := newTestAppWith(t, fakeapi.Options{Scenario: fakeapi.OnePhone, Speed: 1, PairDelay: -1})
	ta.sync()
	ta.page = pageAddPhone
	ta.add.reset(ta.app)
	ta.add.step = "qr"
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "the QR code", func() bool { tt.Frame(); return ta.add.pair.code != nil })
	shoot(ta, "pair-qr", nil)
	ta.stopPairing()
	ta.add.pair.form, ta.add.pair.addr, ta.add.pair.pin = true, "192.168.1.20:37123", "000000"
	ta.add.pair.formErr = pairError(fmt.Errorf("android: pairing with 192.168.1.20:37123 failed: %w", driver.ErrPairRejected), false)
	shoot(ta, "pair-code", nil)
	// A QR pairing that waited 30 s for the phone, one the Mac keeps
	// off the local network, and the connect step of a phone that paired
	// but did not show up.
	ta.add.pair.form, ta.add.pair.formErr = false, ""
	ta.startPairing()
	ta.waitFor(t, "the QR code", func() bool { tt.Frame(); return ta.add.pair.code != nil })
	ta.add.pair.since = time.Now().Add(-31 * time.Second)
	shoot(ta, "pair-stuck", nil)
	ta.api.SetPairingProgress(driver.PairingProgress{NotAllowed: true, SearchErr: "mdns: not allowed to use the local network"})
	shoot(ta, "pair-not-allowed", nil)
	ta.api.SetPairingProgress(driver.PairingProgress{})
	ta.stopPairing()
	ta.add.pair.form, ta.add.pair.connect, ta.add.pair.connAddr = true, true, "192.168.1.20:41234"
	shoot(ta, "pair-connect", nil)
	ta.add.pair.form, ta.add.pair.connect, ta.add.pair.connAddr = false, false, ""

	// The companion app's card on a phone without it.
	ta = newTestApp(t, fakeapi.Companion)
	ta.sync()
	pixelShot := testSource(ta, "pixel-9")
	tt = mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "the companion state", func() bool { tt.Frame(); return ta.comp.state["pixel-9"] != "" })
	shoot(ta, "companion", func() { still(ta, "pixel-9", "pixel-9", pixelShot, false) })

	// An agent working in the background: its screen in the mirror.
	ta = newTestAppWith(t, fakeapi.Options{Scenario: fakeapi.Background, Speed: 0.5, ApprovalTimeout: time.Minute})
	ta.waitFor(t, "the task", func() bool { return ta.anyRunning() && len(ta.tasks[0].Steps) >= 2 })
	shoot(ta, "background", func() { still(ta, "agent:pixel-9", "pixel-9", app, true) })

	// A locked phone whose PIN is not saved yet, with the form open.
	ta = newTestApp(t, fakeapi.Locked)
	ta.waitFor(t, "the unlock card", func() bool { return len(ta.needs) == 1 && ta.pin.ready })
	pixelShot = testSource(ta, "pixel-9")
	ta.pin.editing, ta.pin.where, ta.pin.value = "pixel-9", "card", "2468"
	shoot(ta, "unlock-pin", func() { still(ta, "pixel-9", "pixel-9", pixelShot, false) })
	ta.pin.editing, ta.pin.value = "", ""
	ta.opt.Secrets.Save(secret.PINAccount("pixel-9"), "2468")
	ta.pin.saved["pixel-9"] = true
	shoot(ta, "unlock-touch-id", func() { still(ta, "pixel-9", "pixel-9", pixelShot, false) })

	// Settings › Phones with a saved PIN and the background switch.
	ta.page, ta.set.sec = pageSettings, secPhones
	shoot(ta, "settings-phones", nil)

	// Settings › Agents › Remote agents, on.
	ta.opt.AgentToken = "demo-0f3c9a7e51b24d6c8e2a"
	ta.settings.RemoteAgents = true
	ta.page, ta.set.sec = pageSettings, secAgents
	shoot(ta, "settings-remote", nil)
	ta.page, ta.set.sec = pageSettings, secPrivacy
	shoot(ta, "settings-privacy", nil)

	// The recording player, paused at the payment.
	ta = newTestApp(t, fakeapi.OnePhone)
	ta.page = pageActivity
	tt = mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "Activity", func() bool { tt.Frame(); return ta.act.loaded })
	ta.act.sel = "h-lunch"
	ta.waitFor(t, "the recording", func() bool { tt.Frame(); return ta.recordingOf("h-lunch") != nil })
	rec := *ta.recordingOf("h-lunch")
	ta.act.replay = true
	pl := &recPlayer{a: ta.app, task: "h-lunch", rec: rec, state: "paused", pos: rec.Marks[4].At + time.Second}
	b := app.Bounds()
	pl.bitmap, pl.picW, pl.picH = mg.NewBitmap(app), b.Dx(), b.Dy()
	ta.rec.pl = pl
	shoot(ta, "recording", nil)
}
