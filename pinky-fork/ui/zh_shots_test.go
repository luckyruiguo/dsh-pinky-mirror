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
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/secret"
	"github.com/DIYgod/pinky/internal/update"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestChineseScreenshots draws every screen in Simplified Chinese into
// $PINKY_SCREENSHOTS_ZH, to look for text that is cut or overflows. Tests
// have no video, so the mirrors show stills of the fake's test source and
// of a demo phone's drawn app. PINKY_SCREENSHOTS_DARK=1 draws the dark
// appearance too.
func TestChineseScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS_ZH")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS_ZH to a folder to write the Chinese screenshots")
	}
	inLanguage(t, "zh-Hans")
	_, devs := demo.World()
	drawn, err := devs[0].Screenshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	still := func(ta *testApp, key, id string, img image.Image, agent bool) {
		b := img.Bounds()
		ta.mirrors[key] = &mirror{a: ta.app, id: id, agent: agent, state: "playing", bitmap: mg.NewBitmap(img),
			picW: b.Dx(), picH: b.Dy(), used: ta.buildSeq, retry: time.Now().Add(time.Hour)}
	}
	source := func(ta *testApp, id string) image.Image {
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
	themes := []bool{false}
	if os.Getenv("PINKY_SCREENSHOTS_DARK") != "" {
		themes = append(themes, true)
	}
	write := func(name string, dark bool, img image.Image) {
		file := name + "-zh-light.png"
		if dark {
			file = name + "-zh-dark.png"
		}
		f, err := os.Create(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	// shoot draws view at w × h; before runs before each frame, to put the
	// stills back that a build swept.
	shoot := func(ta *testApp, name string, view func(*mg.Context), w, h int, before func()) {
		t.Helper()
		for _, dark := range themes {
			ta.sync()
			if before != nil {
				before()
			}
			tt := mg.NewTester(view, w, h)
			inLanguage(t, "zh-Hans", tt)
			tt.SetScale(2)
			tt.SetDark(dark)
			tt.Frame()
			if before != nil {
				before()
			}
			tt.Frame()
			write(name, dark, tt.Image())
		}
	}
	main := func(ta *testApp, name string, before func()) {
		t.Helper()
		shoot(ta, name, ta.view, 1280, 820, before)
	}
	// The floating panel at the size it takes for what it shows.
	pw, ph := panelW, func(ta *testApp) int { _, h := ta.panelSize(); return h }
	pixel := func(ta *testApp) func() {
		img := source(ta, "pixel-9")
		return func() { still(ta, "pixel-9", "pixel-9", img, false) }
	}

	// One phone, the payment waiting.
	ta := newTestAppTimeout(t, fakeapi.OnePhone, 10*time.Minute)
	ta.waitFor(t, "the approval", func() bool { _, ok := ta.approval(); return ok })
	main(ta, "phone-one", pixel(ta))
	ta.more["pixel-9"] = true
	main(ta, "phone-more", pixel(ta))
	ta.more["pixel-9"] = false
	n, _ := ta.approval()
	ta.noting[n.ID] = true
	main(ta, "phone-note", pixel(ta))
	shoot(ta, "panel-one", ta.panelView, pw, ph(ta), nil)

	// Three phones, and All phones.
	ta = newTestAppTimeout(t, fakeapi.ThreePhones, 10*time.Minute)
	ta.waitFor(t, "two approvals", func() bool { return len(ta.approvals()) == 2 })
	main(ta, "phone-three", pixel(ta))
	ta.sel = allPhones
	main(ta, "all-phones", nil)
	shoot(ta, "panel-three", ta.panelView, pw, ph(ta), nil)
	shoot(ta, "flyout", ta.flyoutView, 320, ta.flyoutHeight(), nil)
	ta.remember(ta.approvals()[0], "allow_once", "")
	shoot(ta, "panel-allowed", ta.panelView, pw, ph(ta), nil)

	// The person in control, a handoff, a new agent.
	ta = newTestAppTimeout(t, fakeapi.Takeover, 10*time.Minute)
	ta.waitFor(t, "the takeover", func() bool { return len(ta.devices) > 0 && ta.devices[0].Status == "in_control" })
	main(ta, "takeover", pixel(ta))
	ta = newTestAppTimeout(t, fakeapi.Handoff, 10*time.Minute)
	ta.waitFor(t, "the handoff", func() bool { return len(ta.needs) > 0 })
	main(ta, "handoff", pixel(ta))
	shoot(ta, "panel-handoff", ta.panelView, pw, ph(ta), nil)
	ta = newTestAppTimeout(t, fakeapi.NewAgent, 10*time.Minute)
	ta.waitFor(t, "the new agent", func() bool { return len(ta.needs) > 0 })
	main(ta, "new-agent", pixel(ta))
	ta.page, ta.set.sec = pageSettings, secAgents
	main(ta, "settings-agents-request", nil)

	// A locked phone: the PIN form, then Touch ID.
	ta = newTestApp(t, fakeapi.Locked)
	ta.waitFor(t, "the unlock card", func() bool { return len(ta.needs) == 1 && ta.pin.ready })
	main(ta, "locked", pixel(ta))
	ta.pin.editing, ta.pin.where, ta.pin.value = "pixel-9", "card", "2468"
	main(ta, "unlock-pin", pixel(ta))
	ta.pin.editing, ta.pin.value = "", ""
	ta.opt.Secrets.Save(secret.PINAccount("pixel-9"), "2468")
	ta.pin.saved["pixel-9"] = true
	main(ta, "unlock-touch-id", pixel(ta))

	// Settings, every section, with an Always allow answer kept.
	ta.api.AlwaysAllow(gateway.Exemption{Rule: "Payments", With: []string{"Commits"}, AppID: "com.dinely.app", AppName: "Dinely"})
	ta.always.list, ta.always.loaded, ta.always.lastLoad = ta.api.Exemptions(), true, time.Now()
	for _, s := range []struct {
		sec  settingsSection
		name string
	}{{secSafety, "settings-safety"}, {secPhones, "settings-phones"}, {secAgents, "settings-agents"},
		{secPrivacy, "settings-privacy"}, {secAdvanced, "settings-advanced"}} {
		ta.page, ta.set.sec = pageSettings, s.sec
		main(ta, s.name, nil)
	}
	ta.opt.AgentToken = "demo-0f3c9a7e51b24d6c8e2a"
	ta.settings.RemoteAgents = true
	ta.page, ta.set.sec = pageSettings, secAgents
	main(ta, "settings-remote", nil)

	// The companion app, the agent's screen.
	ta = newTestApp(t, fakeapi.Companion)
	ta.sync()
	img := source(ta, "pixel-9")
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "the companion state", func() bool { tt.Frame(); return ta.comp.state["pixel-9"] != "" })
	main(ta, "companion", func() { still(ta, "pixel-9", "pixel-9", img, false) })
	ta = newTestAppWith(t, fakeapi.Options{Scenario: fakeapi.Background, Speed: 0.5, ApprovalTimeout: time.Minute})
	ta.waitFor(t, "the task", func() bool { return ta.anyRunning() && len(ta.tasks[0].Steps) >= 2 })
	main(ta, "background", func() { still(ta, "agent:pixel-9", "pixel-9", drawn, true) })

	// Add phone: the choice, Android, pairing.
	ta = newTestAppWith(t, fakeapi.Options{Scenario: fakeapi.NoPhones, Speed: 1, PairDelay: -1})
	ta.sync()
	ta.page = pageAddPhone
	ta.add.reset(ta.app)
	main(ta, "add-phone", nil)
	ta.add.step = "android"
	main(ta, "add-android", nil)
	ta.add.step = "iphone"
	main(ta, "add-iphone", nil)
	ta.add.step = "qr"
	tt = mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "the QR code", func() bool { tt.Frame(); return ta.add.pair.code != nil })
	main(ta, "pair-qr", nil)
	ta.stopPairing()
	ta.add.pair.form, ta.add.pair.addr, ta.add.pair.pin = true, "192.168.1.20:37123", "000000"
	ta.add.pair.formErr = pairError(fmt.Errorf("android: pairing with 192.168.1.20:37123 failed: %w", driver.ErrPairRejected), false)
	main(ta, "pair-code", nil)
	// A QR pairing that waited 30 s for the phone, one the Mac keeps
	// off the local network, and the connect step of a phone that paired
	// but did not show up.
	ta.add.pair.form, ta.add.pair.formErr = false, ""
	ta.startPairing()
	ta.waitFor(t, "the QR code", func() bool { tt.Frame(); return ta.add.pair.code != nil })
	ta.add.pair.since = time.Now().Add(-31 * time.Second)
	main(ta, "pair-stuck", nil)
	ta.api.SetPairingProgress(driver.PairingProgress{NotAllowed: true, SearchErr: "mdns: not allowed to use the local network"})
	main(ta, "pair-not-allowed", nil)
	ta.api.SetPairingProgress(driver.PairingProgress{})
	ta.stopPairing()
	ta.add.pair.form, ta.add.pair.connect, ta.add.pair.connAddr = true, true, "192.168.1.20:41234"
	main(ta, "pair-connect", nil)
	ta.add.pair.form, ta.add.pair.connect, ta.add.pair.connAddr = false, false, ""

	// Activity, a task, its recording.
	ta = newTestApp(t, fakeapi.OnePhone)
	ta.page = pageActivity
	tt = mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "Activity", func() bool { tt.Frame(); return ta.act.loaded })
	main(ta, "activity", nil)
	ta.act.sel = "h-lunch"
	main(ta, "activity-task", nil)
	ta.waitFor(t, "the recording", func() bool { tt.Frame(); return ta.recordingOf("h-lunch") != nil })
	rec := *ta.recordingOf("h-lunch")
	ta.act.replay = true
	pl := &recPlayer{a: ta.app, task: "h-lunch", rec: rec, state: "paused", pos: rec.Marks[4].At + time.Second}
	b := drawn.Bounds()
	pl.bitmap, pl.picW, pl.picH = mg.NewBitmap(drawn), b.Dx(), b.Dy()
	ta.rec.pl = pl
	main(ta, "recording", nil)
}

// TestChineseUpdateScreenshots draws the update screens in Simplified
// Chinese into $PINKY_SCREENSHOTS_ZH: the update card, its notes, the
// download, the companion's automatic update, What's new, Settings ›
// Advanced › Updates and the confirmation.
func TestChineseUpdateScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS_ZH")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS_ZH to a folder to write the Chinese screenshots")
	}
	inLanguage(t, "zh-Hans")
	ut := newUpdateTest(t, fakeapi.Companion)
	ut.check(t)
	shot := func(name string) {
		t.Helper()
		tt := mg.NewTester(ut.view, 1280, 820)
		inLanguage(t, "zh-Hans", tt)
		tt.SetScale(2)
		tt.Frame()
		tt.Frame()
		f, err := os.Create(filepath.Join(dir, name+"-zh-light.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	shot("update-card")
	ut.upd.notesOpen = true
	shot("update-notes")
	ut.upd.notesOpen = false
	ut.upd.confirm = true
	shot("update-confirm")
	ut.upd.confirm = false
	ut.upd.phase, ut.upd.done, ut.upd.total = updDownloading, 18<<20, 41<<20
	shot("update-downloading")
	ut.upd.phase, ut.upd.err = updFailed, updateError(update.ErrStalled)
	shot("update-failed")
	ut.upd.phase, ut.upd.err = updIdle, ""
	ut.page, ut.set.sec = pageSettings, secAdvanced
	shot("settings-updates")
	ut.page = pagePhones
	ut.comp.state["pixel-9"], ut.comp.checked["pixel-9"] = driver.CompanionOutdated, time.Now()
	shot("companion-outdated")
	ut.upd.comp = map[string]*compAuto{"pixel-9": {running: true}}
	shot("companion-updating")
	ut.upd.comp = nil
	ut.comp.state["pixel-9"] = driver.CompanionOn
	ut.upd.rel = nil
	ut.u.Version, ut.opt.Version, ut.opt.Changelog = "2026.1107.2", "2026.1107.2", testChangelog
	ut.u.Change(func(s *update.State) { s.Launched("2026.1107.1"); s.Launched("2026.1107.2") })
	ut.u.Change(func(s *update.State) {
		s.Changes = []update.Change{{Kind: "companion", Device: "Pixel 9", From: "0.1.0 (75)", To: "0.1.0 (130)", At: time.Now()}}
	})
	shot("whats-new-card")
	ut.upd.newsOpen = true
	shot("whats-new")
}

// TestChineseTelemetryScreenshots draws Settings › Privacy, What Pinky
// shares and Send feedback in Simplified Chinese into $PINKY_SCREENSHOTS_ZH.
func TestChineseTelemetryScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS_ZH")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS_ZH to a folder to write the Chinese screenshots")
	}
	inLanguage(t, "zh-Hans")
	ta := newTelApp(t, &reports{})
	ta.tel.Count("runs.started")
	ta.tel.Count("tools.phone_tap")
	shoot := func(name string, setup func()) {
		t.Helper()
		tt := mg.NewTester(ta.view, 1280, 820)
		inLanguage(t, "zh-Hans", tt)
		tt.SetScale(2)
		if setup != nil {
			setup()
		}
		ta.eventually(t, tt, "posted work", func() bool { return !ta.t.fb.reading })
		tt.Frame()
		f, err := os.Create(filepath.Join(dir, name+"-zh-light.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	shoot("feedback", func() {
		if !ta.t.fb.open {
			ta.openFeedback()
			ta.t.fb.title = "Mirror froze"
			ta.t.fb.message = "The mirror froze after I unlocked the phone from the computer."
			ta.t.fb.review = true
		}
	})
	ta.t.fb.open = false
	ta.page, ta.set.sec = pageSettings, secPrivacy
	shoot("settings-privacy-usage", nil)
	shoot("whats-shared", func() { ta.openShared() })
}
