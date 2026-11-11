package ui

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// vitalsApp is a test app with three phones, the Pixel charging at 82%
// with 41.3 GB of 128 GB free, the iPhone at 15% off its charger and
// almost full.
func vitalsApp(t *testing.T) *testApp {
	t.Helper()
	ta := newTestApp(t, fakeapi.ThreePhones)
	ta.api.SetVitals("pixel-9", gateway.PhoneVitals{
		Battery: &driver.Battery{Level: 82, Charging: true, Plugged: true, TempC: 31},
		Storage: &driver.Storage{Total: 128_000_000_000, Free: 41_300_000_000},
	})
	ta.api.SetVitals("iphone-17", gateway.PhoneVitals{
		Battery: &driver.Battery{Level: 15},
		Storage: &driver.Storage{Total: 256_000_000_000, Free: 9_800_000_000},
	})
	ta.sync()
	return ta
}

// TestPhoneAbout: a connected phone's header shows its battery, which
// opens the phone's model, system, battery and storage. The client asks
// again only after a minute.
func TestPhoneAbout(t *testing.T) {
	ta := vitalsApp(t)
	ta.selectPhone("pixel-9")
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.frameUntil(t, tt, "the battery", func() bool { return hasText(tt, "82%") })
	if err := tt.Click(locales.PhoneAboutButton("Pixel 9")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"Android 16", "Battery", "82% · Charging · 31 °C", "Storage", "41.3 GB free of 128 GB"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in the panel: %q", want, tt.Texts())
		}
	}

	reads := ta.api.VitalsReads("pixel-9")
	for range 5 {
		ta.sync()
		tt.Frame()
	}
	if n := ta.api.VitalsReads("pixel-9"); n != reads {
		t.Errorf("%d reads within a minute, want none", n-reads)
	}
	ta.vit.at["pixel-9"] = time.Now().Add(-vitalsEvery)
	ta.frameUntil(t, tt, "a read a minute later", func() bool { return ta.api.VitalsReads("pixel-9") > reads })

	// The iPhone has no temperature, and too little room.
	ta.selectPhone("iphone-17")
	ta.frameUntil(t, tt, "the iPhone's battery", func() bool { return hasText(tt, "15%") })
	if err := tt.Click(locales.PhoneAboutButton("iPhone 17")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"iOS 27.1", "9.8 GB free of 256 GB"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in the iPhone's panel: %q", want, tt.Texts())
		}
	}
	if hasText(tt, "°C") || hasText(tt, "Charging") {
		t.Errorf("the iPhone's battery: %q", tt.Texts())
	}
}

// TestPhoneAboutOnlyConnected: All phones' tiles show the battery of the
// connected phones; an offline phone and a paused one show none, and the
// client does not ask about them.
func TestPhoneAboutOnlyConnected(t *testing.T) {
	ta := vitalsApp(t)
	ta.selectPhone(allPhones)
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.frameUntil(t, tt, "the tiles' batteries", func() bool { return hasText(tt, "82%") && hasText(tt, "15%") })
	if n := ta.api.VitalsReads("galaxy-s25"); n != 0 {
		t.Errorf("asked %d times about the offline phone", n)
	}

	if err := ta.api.PauseDevice("pixel-9"); err != nil {
		t.Fatal(err)
	}
	ta.sync()
	tt.Frame()
	reads := ta.api.VitalsReads("pixel-9")
	ta.selectPhone("pixel-9")
	for range 3 {
		ta.sync()
		tt.Frame()
	}
	if hasText(tt, "82%") {
		t.Errorf("the paused phone shows its battery: %q", tt.Texts())
	}
	if n := ta.api.VitalsReads("pixel-9"); n != reads {
		t.Errorf("asked %d times about the paused phone", n-reads)
	}

	// Resumed: it shows its battery at once.
	if err := ta.api.ResumeDevice("pixel-9"); err != nil {
		t.Fatal(err)
	}
	ta.frameUntil(t, tt, "the battery after resuming", func() bool { return hasText(tt, "82%") })
}

// TestPhoneAboutFits: the panel's lines fit its width in every language.
func TestPhoneAboutFits(t *testing.T) {
	for _, lang := range testLanguages() {
		t.Run(lang, func(t *testing.T) {
			ta := vitalsApp(t)
			ta.selectPhone("pixel-9")
			tt := mg.NewTester(ta.view, 1280, 820)
			inLanguage(t, lang, tt)
			ta.frameUntil(t, tt, "the battery", func() bool { return ta.vit.got["pixel-9"].Battery != nil })
			if err := tt.Click(locales.PhoneAboutButton("Pixel 9")); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			// One line is as high as the label beside it.
			line, ok := tt.Find(locales.PhoneAboutBattery())
			if !ok {
				t.Fatalf("no battery label: %q", tt.Texts())
			}
			for _, s := range []string{locales.PhoneAboutStorageFree("41.3 GB", "128 GB"), batteryText(*ta.vit.got["pixel-9"].Battery)} {
				r, ok := tt.Find(s)
				if !ok {
					t.Fatalf("no %q: %q", s, tt.Texts())
				}
				if r.H > line.H*1.5 {
					t.Errorf("%q wraps: %v high, a line is %v", s, r.H, line.H)
				}
			}
		})
	}
}

// TestPhoneAboutScreenshots draws the header's battery, its panel and All
// phones' tiles in light and dark, in every language, into
// $PINKY_SCREENSHOTS.
func TestPhoneAboutScreenshots(t *testing.T) {
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
	for _, lang := range testLanguages() {
		for _, dark := range []bool{false, true} {
			theme := "light"
			if dark {
				theme = "dark"
			}
			ta := vitalsApp(t)
			ta.selectPhone("pixel-9")
			tt := mg.NewTester(ta.view, 1280, 820)
			tt.SetScale(2)
			tt.SetDark(dark)
			inLanguage(t, lang, tt)
			ta.frameUntil(t, tt, "the battery", func() bool { return ta.vit.got["pixel-9"].Battery != nil })
			if err := tt.Click(locales.PhoneAboutButton("Pixel 9")); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			shot(tt, "phone-about-"+theme+"-"+langShort(lang)+".png")
			ta.more["about:pixel-9"] = false
			ta.selectPhone(allPhones)
			ta.frameUntil(t, tt, "the tiles", func() bool { return ta.vit.got["iphone-17"].Battery != nil })
			shot(tt, "phone-tiles-"+theme+"-"+langShort(lang)+".png")
		}
	}
}
