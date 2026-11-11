package ui

import (
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// installApp is helperApp on a Mac with Xcode: Pinky installs the helper
// from its window.
func installApp(t *testing.T) (*testApp, string) {
	t.Helper()
	ta, id, _ := helperApp(t)
	ta.api.SetHelperInstall(true)
	return ta, id
}

// installed waits until the install from the window is done, and checks
// that it said so and that the phone is online with a helper good for a
// week.
func installed(t *testing.T, ta *testApp, id, name string) {
	t.Helper()
	// Without frames, which show the toasts and let go of them.
	ta.waitFor(t, "the install to end", func() bool { return ta.helper.runs[id] == nil })
	if !slices.Contains(ta.toasts, locales.HelperInstalledToast(name)) {
		t.Errorf("no word that it is installed: %q", ta.toasts)
	}
	ta.waitFor(t, "the phone online", func() bool {
		d := ta.iphoneView(t)
		return d.Info.State == driver.StateOnline && time.Until(d.Info.HelperExpires) > 6*24*time.Hour
	})
}

// TestIPhoneHelperInstallFromPage: on a Mac with Xcode, an iPhone that
// needs the helper offers to install it from its page, in place of the
// link to how; while it installs, the page says what it does, in bold when
// the phone has to be unlocked; once done, the phone is online.
func TestIPhoneHelperInstallFromPage(t *testing.T) {
	ta, id := installApp(t)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper, Detail: "The WebDriverAgent helper is not installed on this iPhone."})
	ta.api.HoldHelper(true)
	ta.sync()
	name := ta.iphoneView(t).Info.Name
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	for _, want := range []string{locales.CheckHelperMissing(), locales.HelperInstall(), locales.HelperInstallNote(), locales.PhoneCheckup()} {
		if !hasText(tt, want) {
			t.Errorf("the page lacks %q: %q", want, tt.Texts())
		}
	}
	if hasText(tt, locales.CheckHelperHow()) || hasText(tt, locales.PhoneCheckupFix()) {
		t.Errorf("the page still says how to install it by hand: %q", tt.Texts())
	}

	if err := tt.Click(locales.HelperInstall()); err != nil {
		t.Fatal(err)
	}
	ta.frameUntil(t, tt, "Xcode building", func() bool { return hasText(tt, locales.HelperStepBuilding()) })
	if !hasText(tt, locales.CheckHelperTitle()) || !hasText(tt, locales.HelperCancel()) || hasText(tt, locales.HelperInstall()) {
		t.Errorf("while building: %q", tt.Texts())
	}
	// Xcode waits for the phone to be unlocked: said in bold.
	ta.helper.runs[id].step = driver.HelperUnlock
	tt.Frame()
	if !hasText(tt, locales.HelperStepUnlock(name)) {
		t.Errorf("no word to unlock the phone: %q", tt.Texts())
	}
	ta.api.HoldHelper(false)
	installed(t, ta, id, name)
	if runs := ta.api.HelperRuns(); len(runs) != 1 || runs[0] != (fakeapi.HelperRun{DeviceID: id}) {
		t.Errorf("installs %+v", runs)
	}
	tt.Frame()
	if hasText(tt, locales.PhoneNeedsSetup(name)) {
		t.Errorf("the page still needs setup: %q", tt.Texts())
	}
}

// TestIPhoneHelperInstallFails: a failed install says why in words, with
// Xcode's own below and the log a click away, and installs again on the
// next click.
func TestIPhoneHelperInstallFails(t *testing.T) {
	ta, id := installApp(t)
	var revealed string
	ta.reveal = func(p string) { revealed = p }
	ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper})
	const why = "the developer certificate is not trusted on the phone"
	const log = "/tmp/pinky/WebDriverAgent/helper-install.log"
	ta.api.SetHelperError(&driver.HelperInstallError{Fix: driver.HelperFixTrust, Err: errors.New(why), Hint: "Trust it on the phone.", Log: log})
	ta.sync()
	name := ta.iphoneView(t).Info.Name
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if err := tt.Click(locales.HelperInstall()); err != nil {
		t.Fatal(err)
	}
	ta.frameUntil(t, tt, "the failure", func() bool { r := ta.helper.runs[id]; return r != nil && !r.running })
	for _, want := range []string{locales.HelperErrTrust(name), why, locales.HelperShowLog(), locales.HelperInstall()} {
		if !hasText(tt, want) {
			t.Errorf("the page lacks %q: %q", want, tt.Texts())
		}
	}
	if hasText(tt, "Trust it on the phone.") {
		t.Errorf("the command line's hint shows: %q", tt.Texts())
	}
	if err := tt.Click(locales.HelperShowLog()); err != nil || revealed != log {
		t.Errorf("Show the log: %v, revealed %q", err, revealed)
	}

	ta.api.SetHelperError(nil)
	if err := tt.Click(locales.HelperInstall()); err != nil {
		t.Fatal(err)
	}
	installed(t, ta, id, name)
	if len(ta.api.HelperRuns()) != 2 {
		t.Errorf("installs %+v", ta.api.HelperRuns())
	}

	// An agent holds the phone: Pinky does not take it away.
	if got, _, _ := helperErrorWords(fmt.Errorf("installing: %w", gateway.ErrPhoneInUse), name); got != locales.HelperErrInUse(name) {
		t.Errorf("in use: %q", got)
	}
}

// TestIPhoneHelperInstallTeams: when the Mac signs for several teams, the
// install asks which, and goes on with the one chosen.
func TestIPhoneHelperInstallTeams(t *testing.T) {
	ta, id := installApp(t)
	teams := []driver.HelperTeam{{ID: "ABCDE12345", Name: "Sam Lee (Personal Team)"}, {ID: "FGHIJ67890"}}
	ta.api.SetHelperTeams(teams)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper})
	ta.sync()
	name := ta.iphoneView(t).Info.Name
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if err := tt.Click(locales.HelperInstall()); err != nil {
		t.Fatal(err)
	}
	ta.frameUntil(t, tt, "the teams", func() bool { return hasText(tt, locales.HelperChooseTeam()) })
	first := locales.HelperTeam(teams[0].Name, teams[0].ID)
	if !hasText(tt, first) || !hasText(tt, teams[1].ID) {
		t.Fatalf("the teams: %q", tt.Texts())
	}
	if err := tt.Click(teams[1].ID); err != nil {
		t.Fatal(err)
	}
	installed(t, ta, id, name)
	if runs := ta.api.HelperRuns(); len(runs) != 2 || runs[1].Team != teams[1].ID {
		t.Errorf("installs %+v", runs)
	}
}

// TestIPhoneHelperInstallAgain: two days before the helper's signature runs
// out, the reminder above the phone installs it again; the checkup offers
// it once, from the helper's item.
func TestIPhoneHelperInstallAgain(t *testing.T) {
	ta, id := installApp(t)
	exp := time.Now().Add(30 * time.Hour)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateOnline, HelperExpires: exp})
	ta.api.SetCheckup(id, driver.Checkup{System: "iOS 18.6", Checks: []driver.Check{
		{ID: driver.CheckHelper, Status: driver.CheckWarn, Fix: driver.FixInstallHelper, Detail: "WebDriverAgent answers; its provisioning profile runs out soon"},
		{ID: driver.CheckInput, Status: driver.CheckWarn, Fix: driver.FixInstallHelper},
	}})
	ta.sync()
	d := ta.iphoneView(t)
	soon := locales.CheckHelperSoonHere(d.Info.Name, exp.Local())
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if !hasText(tt, soon) || !hasText(tt, locales.HelperInstallAgain()) || hasText(tt, locales.CheckHelperHow()) {
		t.Fatalf("the reminder: %q", tt.Texts())
	}

	ta.openCheckup(d)
	ta.frameUntil(t, tt, "the checkup", func() bool { r := ta.checkupOf(id); return r != nil && r.have && !r.running })
	// One button: its label and its text.
	if !hasText(tt, soon) || countText(tt, locales.HelperInstallAgain()) != 2 || hasText(tt, locales.CheckHelperHow()) {
		t.Errorf("the checkup: %q", tt.Texts())
	}
	ta.chk.showing = ""
	tt.Frame()

	ta.api.HoldHelper(true)
	if err := tt.Click(locales.HelperInstallAgain()); err != nil {
		t.Fatal(err)
	}
	ta.frameUntil(t, tt, "Xcode building", func() bool { return hasText(tt, locales.HelperStepBuilding()) })
	ta.api.HoldHelper(false)
	installed(t, ta, id, d.Info.Name)
	tt.Frame()
	if hasText(tt, soon) || hasText(tt, locales.HelperInstallAgain()) {
		t.Errorf("the reminder stays: %q", tt.Texts())
	}
}

// TestIPhoneHelperRanOutHere: once the helper ran out, the page installs
// it again, in words that leave out the command.
func TestIPhoneHelperRanOutHere(t *testing.T) {
	ta, id := installApp(t)
	exp := time.Now().Add(-3 * time.Hour)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper, HelperExpires: exp})
	ta.sync()
	d := ta.iphoneView(t)
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if !hasText(tt, locales.CheckHelperExpiredHere(d.Info.Name, exp.Local())) || !hasText(tt, locales.HelperInstallAgain()) || hasText(tt, locales.CheckHelperHow()) {
		t.Fatalf("the page: %q", tt.Texts())
	}
	if err := tt.Click(locales.HelperInstallAgain()); err != nil {
		t.Fatal(err)
	}
	installed(t, ta, id, d.Info.Name)
}

// TestAddOlderIPhoneHelper: Add phone › older iPhone › helper installs it
// on the iPhone Xcode finds, Pinky having found none, and goes back to the
// steps once done; without Xcode it says how. iPhone Mirroring is not
// there yet, and says so.
func TestAddOlderIPhoneHelper(t *testing.T) {
	ta := newTestAppWith(t, fakeapi.Options{Scenario: fakeapi.OnePhone, Speed: 50})
	ta.sync()
	ta.page = pageAddPhone
	ta.add.reset(ta.app)
	ta.add.step, ta.add.old = "iphone", true
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if err := tt.Click(locales.AddPhoneOlderMirroring()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasText(tt, locales.AddPhoneOlderMirrorNotYet()) {
		t.Errorf("iPhone Mirroring: %q", tt.Texts())
	}
	if err := tt.Click(locales.AddPhoneOlderHelper()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasText(tt, locales.AddPhoneOlderHelperHow()) || !hasText(tt, locales.CheckHelperHow()) || hasText(tt, locales.HelperInstall()) {
		t.Errorf("without Xcode: %q", tt.Texts())
	}

	ta.api.SetHelperInstall(true)
	tt.Frame()
	if hasText(tt, locales.AddPhoneOlderHelperHow()) || !hasText(tt, locales.HelperInstallNote()) {
		t.Errorf("with Xcode: %q", tt.Texts())
	}
	if err := tt.Click(locales.HelperInstall()); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the install to end", func() bool { return len(ta.helper.runs) == 0 })
	if ta.add.old || ta.add.oldPick != "" {
		t.Errorf("still on the older iPhone's page: old %v, pick %q", ta.add.old, ta.add.oldPick)
	}
	if runs := ta.api.HelperRuns(); len(runs) != 1 || runs[0].DeviceID != "" {
		t.Errorf("installs %+v", runs)
	}
	if !slices.ContainsFunc(ta.toasts, func(s string) bool { return strings.Contains(s, locales.HelperThisIPhone()) }) {
		t.Errorf("toasts %q", ta.toasts)
	}
}

// TestIPhoneHelperInstallScreenshots draws installing the helper from the
// window into $PINKY_SCREENSHOTS, in each of the client's languages: the
// page of an iPhone without it, the install under way, a failure, the
// reminder, the checkup, and Add phone's older iPhone:
//
//	PINKY_SCREENSHOTS=dir go test ./ui -run TestIPhoneHelperInstallScreenshots
func TestIPhoneHelperInstallScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS to a folder to write the screenshots")
	}
	dark := os.Getenv("PINKY_SCREENSHOTS_DARK") != ""
	ta, id := installApp(t)
	shoot := func(name, lang string) {
		t.Helper()
		st := mg.NewTester(ta.view, 1280, 820)
		inLanguage(t, lang, st)
		st.SetScale(2)
		st.SetDark(dark)
		st.Frame()
		st.Frame()
		f, err := os.Create(filepath.Join(dir, name+"-"+lang+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, st.Image()); err != nil {
			t.Fatal(err)
		}
	}
	for _, lang := range testLanguages() {
		inLanguage(t, lang)
		ta.helper.runs = nil
		ta.page, ta.chk.showing = pagePhones, ""
		ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper})
		ta.sync()
		ta.selectPhone(id)
		shoot("helper-install", lang)

		ta.helper.runs = map[string]*helperRun{id: {running: true, step: driver.HelperUnlock, cancel: func() {}}}
		shoot("helper-installing", lang)
		ta.helper.runs[id] = &helperRun{teams: []driver.HelperTeam{{ID: "ABCDE12345", Name: "Sam Lee (Personal Team)"}, {ID: "FGHIJ67890"}}}
		shoot("helper-teams", lang)
		name := ta.iphoneView(t).Info.Name
		text, detail, log := helperErrorWords(&driver.HelperInstallError{Fix: driver.HelperFixPasscode,
			Err: errors.New("the iPhone asked for its passcode to run automation, and nobody entered it"), Log: "/Users/sam/Library/Application Support/Pinky/WebDriverAgent/helper-install.log"}, name)
		ta.helper.runs[id] = &helperRun{err: text, detail: detail, log: log}
		shoot("helper-failed", lang)

		ta.helper.runs = nil
		exp := time.Now().Add(30 * time.Hour)
		ta.api.SetIPhone(id, driver.Info{State: driver.StateOnline, HelperExpires: exp})
		ta.api.SetCheckup(id, driver.Checkup{System: "iOS 18.6", Checks: []driver.Check{
			{ID: driver.CheckDeveloperMode, Status: driver.CheckPass},
			{ID: driver.CheckHelper, Status: driver.CheckWarn, Fix: driver.FixInstallHelper},
			{ID: driver.CheckInput, Status: driver.CheckPass},
		}})
		ta.sync()
		shoot("helper-again", lang)
		tt := mg.NewTester(ta.view, 1280, 820)
		ta.openCheckup(ta.iphoneView(t))
		ta.frameUntil(t, tt, "the checkup", func() bool { r := ta.checkupOf(id); return r != nil && r.have && !r.running })
		shoot("helper-again-checkup", lang)

		ta.chk.showing = ""
		ta.page = pageAddPhone
		ta.add.reset(ta.app)
		ta.add.known[id] = true
		ta.add.step, ta.add.old, ta.add.oldPick = "iphone", true, "helper"
		shoot("helper-add-older", lang)
	}
}
