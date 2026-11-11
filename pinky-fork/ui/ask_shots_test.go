package ui

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/pilot"
)

// TestAskScreenshots writes Ask Pinky's box (before a key is saved, ready,
// working and ended) and its part of Settings › Agents, in each language
// and theme, to the folder PINKY_SCREENSHOTS names. The model is the fake
// one of ask_test.go.
func TestAskScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS to a folder to write the screenshots")
	}
	shot := func(tt *mg.Tester, name string) {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	// What the model would say, in the language it was asked in.
	tasks := map[string][2]string{
		"en": {"Check the battery level", "The battery is at 82% and charging."},
		"zh": {"看看电量还剩多少", "电量还剩 82%，正在充电。"},
		"ja": {"バッテリー残量を確認して", "バッテリー残量は 82% で、充電中です。"},
	}
	for _, lang := range testLanguages() {
		for _, dark := range []bool{false, true} {
			theme := "light"
			if dark {
				theme = "dark"
			}
			name := func(what string) string { return "ask-" + what + "-" + theme + "-" + langShort(lang) + ".png" }
			setUp := func(tt *mg.Tester) {
				tt.SetScale(2)
				tt.SetDark(dark)
				inLanguage(t, lang, tt)
			}

			// Before an API key is saved.
			ta, _, tt := newAskApp(t, true, false)
			setUp(tt)
			ta.frameUntil(t, tt, "the keychain", func() bool { _, known := ta.askReady(); return known })
			shot(tt, name("setup"))

			ta, fp, tt := newAskApp(t, true, true)
			setUp(tt)
			ta.frameUntil(t, tt, "the key", func() bool { ready, _ := ta.askReady(); return ready })
			shot(tt, name("ready"))

			id := ta.devices[0].Info.ID
			words := tasks[langShort(lang)]
			now := time.Now()
			fp.mu.Lock()
			fp.status[id] = pilot.Status{Device: id, Task: words[0], Config: ta.settings.PilotConfig(),
				Started: now.Add(-2 * time.Minute), Steps: 12, Cost: 0.18, Priced: true}
			fp.mu.Unlock()
			tt.Frame()
			shot(tt, name("working"))

			fp.mu.Lock()
			s := fp.status[id]
			s.Ended, s.End, s.Summary = now, pilot.Done, words[1]
			fp.status[id] = s
			fp.mu.Unlock()
			tt.Frame()
			shot(tt, name("ended"))

			// Its part of Settings › Agents, in a window tall enough to show
			// all of it.
			tall := mg.NewTester(ta.view, 1280, 1500)
			setUp(tall)
			ta.openAskSettings()
			tall.Frame()
			tall.Frame()
			shot(tall, name("settings"))
			ta.ask.editing = askEditKey
			tall.Frame()
			tall.Frame()
			shot(tall, name("settings-key"))
		}
	}
}
