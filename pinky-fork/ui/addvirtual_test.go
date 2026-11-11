package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// addVirtualApp opens Add phone on a Mac, on a fake whose virtual phones
// wait for BootVirtual once started.
func addVirtualApp(t *testing.T) (*testApp, *mg.Tester) {
	old := guideOS
	guideOS = "darwin"
	t.Cleanup(func() { guideOS = old })
	ta := newTestAppWith(t, fakeapi.Options{Scenario: fakeapi.OnePhone, Speed: 50, HoldSetup: true})
	ta.sync()
	ta.page = pageAddPhone
	ta.add.reset(ta.app)
	return ta, mg.NewTester(ta.view, 1280, 820)
}

// TestAddVirtualPhone starts a Simulator from Add phone › Virtual phone:
// it shows Starting… until it runs, then Open takes the person to it; Shut
// down takes it offline, and its page starts it again.
func TestAddVirtualPhone(t *testing.T) {
	ta, tt := addVirtualApp(t)
	tt.Frame()
	mustHave(t, tt, "Virtual phone", "An iOS Simulator or an Android emulator on this Mac")
	if err := clickInView(tt, "Virtual phone"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the list", func() bool { tt.Frame(); return hasText(tt, "iPhone 17 Pro") })
	mustHave(t, tt, "Virtual phones", "Only you can start them; agents can't.",
		"iOS Simulators", "iPhone 17 Pro · iOS 26.5", "iPad Air", "Pinky builds its helper for Simulators",
		"Android emulators", "Pixel 9 · Android 16", "Can't start: Its system image is not installed.")
	if r, _ := tt.Find("iOS Simulators"); func() bool { a, _ := tt.Find("Android emulators"); return a.Y < r.Y }() {
		t.Error("Android emulators show before iOS Simulators")
	}

	// The first Start is the iPhone's: iOS comes first.
	if err := clickInView(tt, "Start"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "Starting…", func() bool { tt.Frame(); return hasText(tt, "Starting…") })
	if starts := waitStarts(t, ta, 1); !slices.Equal(starts, []string{"ios/5F1E7A2C-SIM-17"}) {
		t.Fatalf("starts %q", starts)
	}
	ta.api.BootVirtual()
	ta.waitFor(t, "the running Simulator", func() bool { tt.Frame(); return hasText(tt, "iPhone 17 Pro · iOS 26.5 · Running") })
	ta.waitFor(t, "Open", func() bool { tt.Frame(); _, ok := tt.Find("Open"); return ok })
	if err := clickInView(tt, "Open"); err != nil {
		t.Fatal(err)
	}
	ta.sync()
	if ta.page != pagePhones || ta.sel != "5F1E7A2C-SIM-17" {
		t.Fatalf("Open went to page %v, phone %q", ta.page, ta.sel)
	}
	tt.Frame()
	mustHave(t, tt, "iPhone · Simulator")

	// Shut down from Add phone: the phone goes offline and can start again.
	ta.page = pageAddPhone
	ta.add.step = "virtual"
	tt.Frame()
	if err := clickInView(tt, "Shut down"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the Simulator shut down", func() bool {
		tt.Frame()
		d := ta.device("5F1E7A2C-SIM-17")
		return d != nil && d.Info.State == driver.StateOffline && !hasText(tt, "Running")
	})
	if _, stops := ta.api.VirtualCalls(); !slices.Equal(stops, []string{"ios/5F1E7A2C-SIM-17"}) {
		t.Fatalf("stops %q", stops)
	}

	// Its page says it isn't running and starts it.
	ta.selectPhone("5F1E7A2C-SIM-17")
	tt.Frame()
	mustHave(t, tt, "iPhone · Simulator · offline", "It isn't running.")
	if hasText(tt, "Reconnect") {
		t.Error("the page of a virtual phone offers to reconnect it")
	}
	if err := tt.Click("Start"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "Starting… on its page", func() bool { tt.Frame(); return hasText(tt, "Starting…") })
	if starts := waitStarts(t, ta, 2); len(starts) != 2 || starts[1] != "ios/5F1E7A2C-SIM-17" {
		t.Fatalf("starts %q", starts)
	}
	ta.api.BootVirtual()
	ta.waitFor(t, "the phone back", func() bool {
		d := ta.device("5F1E7A2C-SIM-17")
		return d != nil && d.Info.State == driver.StateOnline
	})
}

// waitStarts waits until the fake has had n StartVirtual calls and returns
// them. Start shows Starting… as soon as it is clicked and calls
// StartVirtual a moment later, off the UI thread, so Starting… alone does
// not mean the call came: a BootVirtual or FailVirtual before it would act
// on nothing.
func waitStarts(t *testing.T, ta *testApp, n int) []string {
	t.Helper()
	var starts []string
	ta.waitFor(t, fmt.Sprintf("StartVirtual call %d", n), func() bool {
		starts, _ = ta.api.VirtualCalls()
		return len(starts) >= n
	})
	return starts
}

// TestAddVirtualFails: what failed shows under the phone's row.
func TestAddVirtualFails(t *testing.T) {
	ta, tt := addVirtualApp(t)
	ta.api.SetVirtualError(errors.New("Unable to boot the Simulator."))
	ta.add.step = "virtual"
	ta.waitFor(t, "the list", func() bool { tt.Frame(); return hasText(tt, "iPhone 17 Pro") })
	if err := clickInView(tt, "Start"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the error", func() bool {
		tt.Frame()
		return hasText(tt, "Couldn't start iPhone 17 Pro: Unable to boot the Simulator.")
	})
	if _, ok := tt.Find("Start"); !ok {
		t.Error("no Start to try again")
	}
}

// TestAddVirtualQuitWhileStarting: a virtual phone that quits while it
// boots, as an emulator that crashed, stops showing Starting… and says
// why, in Add phone and on its page, until it starts again.
func TestAddVirtualQuitWhileStarting(t *testing.T) {
	ta, tt := addVirtualApp(t)
	ta.add.step = "virtual"
	ta.waitFor(t, "the list", func() bool { tt.Frame(); return hasText(tt, "iPhone 17 Pro") })
	if err := clickInView(tt, "Start"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "Starting…", func() bool { tt.Frame(); return hasText(tt, "Starting…") })
	waitStarts(t, ta, 1)
	const why = "signal: abort trap: detected a hanging thread 'QEMU2 main loop'"
	ta.api.FailVirtual(why)
	ta.waitFor(t, "the reason", func() bool {
		tt.Frame()
		return hasText(tt, "Couldn't start iPhone 17 Pro: "+why) && !hasText(tt, "Starting…")
	})

	// Its page, once it ran and quit while it started again.
	ta.api.BootVirtual() // nothing starts: no change
	if err := clickInView(tt, "Start"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "Starting… again", func() bool { tt.Frame(); return hasText(tt, "Starting…") })
	if hasText(tt, "Couldn't start") {
		t.Error("the old reason shows while it starts again")
	}
	waitStarts(t, ta, 2)
	ta.api.BootVirtual()
	ta.waitFor(t, "the phone", func() bool { d := ta.device("5F1E7A2C-SIM-17"); return d != nil && d.Info.State == driver.StateOnline })
	if err := ta.api.StopVirtual(t.Context(), driver.IOS, "5F1E7A2C-SIM-17"); err != nil {
		t.Fatal(err)
	}
	// Shut down outside Pinky soon after it started: the page shows Start
	// once a listing says it no longer runs, not Starting… for minutes.
	ta.selectPhone("5F1E7A2C-SIM-17")
	ta.waitFor(t, "its page", func() bool {
		tt.Frame()
		_, ok := tt.Find("Start")
		return hasText(tt, "It isn't running.") && ok && !hasText(tt, "Starting…")
	})
	if err := tt.Click("Start"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "Starting… on its page", func() bool { tt.Frame(); return hasText(tt, "Starting…") })
	waitStarts(t, ta, 3)
	ta.api.FailVirtual(why)
	ta.waitFor(t, "the reason on its page", func() bool {
		tt.Frame()
		return hasText(tt, "Couldn't start iPhone 17 Pro: "+why) && !hasText(tt, "Starting…")
	})
	if _, ok := tt.Find("Start"); !ok {
		t.Error("no Start to try again")
	}
}

// TestAddVirtualMissing says what the computer lacks for each platform.
func TestAddVirtualMissing(t *testing.T) {
	ta, tt := addVirtualApp(t)
	ta.api.SetVirtualMissing(driver.VirtualPhones{Platform: driver.IOS, Missing: "xcode"})
	ta.api.SetVirtualMissing(driver.VirtualPhones{Platform: driver.Android, Missing: "emulator", Where: "/Users/me/Library/Android/sdk"})
	ta.add.step = "virtual"
	ta.waitFor(t, "the list", func() bool { tt.Frame(); return hasText(tt, "iOS Simulators need Xcode") })
	mustHave(t, tt, "The Android SDK in /Users/me/Library/Android/sdk has no emulator.")
	if hasText(tt, "Pinky builds its helper") {
		t.Error("the helper note shows without Xcode")
	}

	ta.api.SetVirtualMissing(driver.VirtualPhones{Platform: driver.Android, Missing: "android_sdk"})
	ta.waitFor(t, "no SDK", func() bool { tt.Frame(); return hasText(tt, "Android emulators need the Android SDK") })
}

func TestVirtualMissingWords(t *testing.T) {
	for _, c := range []struct {
		vp   driver.VirtualPhones
		want string
	}{
		{driver.VirtualPhones{Platform: driver.IOS}, "No Simulators yet."},
		{driver.VirtualPhones{Platform: driver.Android}, "No emulators yet."},
		{driver.VirtualPhones{Platform: driver.Android, Detail: "adb failed"}, "Couldn't list them: adb failed"},
		{driver.VirtualPhones{Platform: driver.IOS, Missing: "something new", Detail: "Simulators need more."}, "Simulators need more."},
		{driver.VirtualPhones{Platform: driver.IOS, Phones: []driver.VirtualPhone{{ID: "x"}}}, ""},
	} {
		got := virtualMissing(c.vp)
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("virtualMissing(%+v) = %q, want %q", c.vp, got, c.want)
		}
	}
}

// TestVirtualLines: a virtual phone's header and settings lines say what
// it is in place of its connection.
func TestVirtualLines(t *testing.T) {
	sim := gateway.DeviceView{Info: driver.Info{Platform: driver.IOS, Model: "iPad Air 13-inch (M3)", OSVersion: "26.5",
		Connection: "usb", Virtual: true, State: driver.StateOnline}, Status: gateway.DeviceIdle}
	emu := gateway.DeviceView{Info: driver.Info{Platform: driver.Android, Model: "Pixel 9", OSVersion: "16",
		Connection: "usb", Virtual: true, State: driver.StateOnline}, Status: gateway.DeviceIdle}
	off := emu
	off.Info.State, off.Status = driver.StateOffline, gateway.DeviceOffline
	for _, c := range []struct{ got, want string }{
		{connectionLine(sim), "iPad · Simulator"},
		{connectionLine(emu), "Android · Emulator"},
		{connectionLine(off), "Android · Emulator · offline"},
		{osLine(sim), "iOS 26.5 · Simulator"},
		{osLine(emu), "Android 16 · Emulator"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	if notes := removeNotes(&emu, driver.Android); len(notes) != 1 || !strings.Contains(notes[0], "Add phone › Virtual phone") {
		t.Errorf("remove notes %q", notes)
	}
}

// TestSimulatorHelperPage: while Pinky builds its helper for a booted
// Simulator, the phone's page says so with nothing to fix; when the build
// failed, it says why and Try again builds it again. No iPhone steps show.
func TestSimulatorHelperPage(t *testing.T) {
	ta, tt := addVirtualApp(t)
	const udid = "5F1E7A2C-SIM-17"
	if err := ta.api.StartVirtual(t.Context(), driver.IOS, udid); err != nil {
		t.Fatal(err)
	}
	ta.api.BootVirtual()
	ta.api.SetIPhone(udid, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupPreparing,
		Detail: "Pinky is building WebDriverAgent for this Simulator with Xcode."})
	ta.waitFor(t, "the Simulator", func() bool { return ta.device(udid) != nil })
	ta.selectPhone(udid)
	tt.Frame()
	mustHave(t, tt, "Pinky is building its helper for Simulators with Xcode.")
	for _, no := range []string{"See what's wrong", "Show the steps", "Try again"} {
		if hasText(tt, no) {
			t.Errorf("%q shows while the helper builds", no)
		}
	}

	ta.api.SetIPhone(udid, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupSimulatorHelper,
		Detail: "xcodebuild failed: no iOS 26.5 SDK."})
	ta.waitFor(t, "the failed build", func() bool { tt.Frame(); return hasText(tt, "Pinky couldn't build its helper for Simulators") })
	mustHave(t, tt, "xcodebuild failed: no iOS 26.5 SDK.")
	if hasText(tt, "Show the steps") {
		t.Error("a Simulator shows an iPhone's steps")
	}
	if err := tt.Click("Try again"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the retry", func() bool {
		starts, _ := ta.api.VirtualCalls()
		return len(starts) == 2 && starts[1] == "ios/"+udid
	})
}
