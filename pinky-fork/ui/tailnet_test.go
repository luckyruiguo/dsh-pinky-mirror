package ui

import (
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestAddTailnet: Add phone › Android links to the Tailscale steps, which
// show this computer's tailnet and the phone on it. Connecting finds the
// phone's adb not listening and says how to set it up; the port Wireless
// debugging shows then connects it, and the phone shows.
func TestAddTailnet(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.sync()
	ta.page = pageAddPhone
	ta.add.reset(ta.app)
	ta.add.step = "android"
	// Tall enough for the steps of every system (Windows adds the USB
	// driver's), so the link is in view.
	tt := mg.NewTester(ta.view, 1280, 1700)
	tt.Frame()
	if err := tt.Click("Phone not on this network? Use Tailscale"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the tailnet", func() bool { tt.Frame(); return ta.tn.have })
	tt.Frame()
	for _, want := range []string{"Connect through Tailscale", "Connected to someone@example.com as Someone's MacBook.", "pixel-8a", "100.101.2.2 · online"} {
		if !hasText(tt, want) {
			t.Fatalf("no %q on the page: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Connect"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the answer", func() bool {
		tt.Frame()
		return !ta.tn.connecting["100.101.2.2"] && ta.tn.connErr["100.101.2.2"] != ""
	})
	tt.Frame()
	if !hasText(tt, "pixel-8a doesn't take connections from Pinky yet") || !hasText(tt, "Use away from home") {
		t.Fatalf("no set-up steps: %q", tt.Texts())
	}
	ta.tn.port["100.101.2.2"] = "37123"
	tt.Frame()
	if err := tt.Click("Connect at this port"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the phone", func() bool {
		return ta.page == pagePhones && strings.HasPrefix(ta.sel, "pixel-8a-tailnet-")
	})
	if d := ta.device(ta.sel); d == nil || connectionLine(*d) != "Android · Tailscale" {
		t.Fatalf("header line of %+v", d)
	}
}

// TestAddTailnetHere: without Tailscale running here, the first step says
// what to do and offers the app or its download. On Linux, where Tailscale
// has no app to open, it gives the command that turns it on.
func TestAddTailnetHere(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.sync()
	ta.page, ta.add.step = pageAddPhone, "tailnet"
	tt := mg.NewTester(ta.view, 1280, 1000)
	stopped, login, open := "Tailscale is turned off on this computer. Open it and turn it on.", "Sign in to Tailscale on this computer.", "Open Tailscale"
	if runtime.GOOS == "linux" {
		stopped, login, open = "Turn Tailscale on: run sudo tailscale up in a terminal.", "Turn Tailscale on: run sudo tailscale up in a terminal.", ""
	}
	for _, c := range []struct {
		state, text, button string
	}{
		{driver.TailnetNotInstalled, "Install Tailscale on this computer and sign in.", "Get Tailscale"},
		{driver.TailnetStopped, stopped, open},
		{driver.TailnetNeedsLogin, login, open},
	} {
		ta.api.SetTailnet(driver.TailnetStatus{State: c.state})
		ta.tn.at = ta.tn.at.AddDate(0, 0, -1)
		ta.waitFor(t, c.state, func() bool { tt.Frame(); return !ta.tn.busy && ta.tn.st.State == c.state })
		tt.Frame()
		if !hasText(tt, c.text) {
			t.Errorf("%s: no %q: %q", c.state, c.text, tt.Texts())
		}
		if _, ok := tt.Find(c.button); c.button != "" && !ok {
			t.Errorf("%s: no %q button", c.state, c.button)
		}
		if _, ok := tt.Find("Open Tailscale"); c.button == "" && ok {
			t.Errorf("%s: an Open Tailscale button on Linux", c.state)
		}
	}
}

// TestUseAwayFromHome turns Use away from home on for a phone in Settings ›
// Phones, through its dialog, and off again; a phone connected through
// Tailscale keeps the computer awake while the setting says so.
func TestUseAwayFromHome(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	var awake []bool
	ta.awayAwake = func(on bool) { awake = append(awake, on) }
	ta.sync()
	ta.page, ta.set.sec = pageSettings, secPhones
	tt := mg.NewTester(ta.view, 1280, 1000)
	ta.waitFor(t, "the row", func() bool { tt.Frame(); return hasText(tt, "Use away from home") })
	tt.Frame()
	if !hasText(tt, "Reach Pixel 9 through Tailscale when it's away from this computer's network.") {
		t.Fatalf("row: %q", tt.Texts())
	}
	if !hasText(tt, "Keep this computer awake for phones away") {
		t.Errorf("no keep-awake switch: %q", tt.Texts())
	}
	if err := tt.Click("Turn on…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !ta.tn.askOpen || !hasText(tt, "Use Pixel 9 away from home?") || !hasText(tt, "tap Cancel") {
		t.Fatalf("dialog: %q", tt.Texts())
	}
	if err := tt.Click("Turn on"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "on", func() bool { tt.Frame(); return hasText(tt, "On. When Pixel 9 is away from this computer's network") })
	if !hasText(tt, "Pixel 9 can now be used away from home") {
		t.Errorf("no toast: %q", tt.Texts())
	}
	if len(awake) != 0 {
		t.Errorf("awake %v with no phone away", awake)
	}
	// A phone connected through Tailscale: the computer stays awake, and
	// not once the setting is off.
	if _, err := ta.api.ConnectTailnet(ta.ctx, "100.101.2.2:37123"); err != nil {
		t.Fatal(err)
	}
	ta.sync()
	if len(awake) != 1 || !awake[0] {
		t.Fatalf("awake %v with a phone away", awake)
	}
	ta.settings.KeepAwakeAway = false
	ta.sync()
	if len(awake) != 2 || awake[1] {
		t.Fatalf("awake %v with the setting off", awake)
	}
	// Off again.
	ta.settings.KeepAwakeAway = true
	ta.sync()
	tt.Frame()
	if err := tt.Click("Turn off"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "off", func() bool { tt.Frame(); return hasText(tt, "Use away from home is off for Pixel 9") })
}

// TestAwayNotOnTailnet: a phone whose Tailscale is off, maybe for another
// VPN, or on another tailnet says which in Settings › Phones, with Set up
// Tailscale, which opens the Tailscale steps, where the phone's step says
// that Android runs one VPN at a time too.
func TestAwayNotOnTailnet(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.sync()
	id := ta.devices[0].Info.ID
	oneVPN := "Android runs one VPN at a time: while another VPN or proxy app is on, Tailscale is off. Turn that one off, then turn Tailscale on."
	tt := mg.NewTester(ta.view, 1280, 1000)
	for _, c := range []struct {
		missing, text string
		vpn           bool
	}{
		{driver.AwayNoTailscaleThere, "Tailscale isn't connected on Pixel 9. Install it there, open it and sign in to the same account as on this computer.", true},
		{driver.AwayOtherTailnet, "Pixel 9's Tailscale is signed in to another tailnet than this computer's. Sign in to the same account on both.", false},
	} {
		ta.api.SetPhoneAway(id, driver.Away{Port: 5555, Missing: c.missing})
		ta.tn.init()
		ta.tn.awayAt[id] = time.Time{}
		ta.page, ta.set.sec = pageSettings, secPhones
		ta.waitFor(t, c.missing, func() bool { tt.Frame(); return hasText(tt, c.text) })
		if hasText(tt, oneVPN) != c.vpn {
			t.Errorf("%s: one VPN at a time shows %v: %q", c.missing, !c.vpn, tt.Texts())
		}
		if err := tt.Click("Set up Tailscale"); err != nil {
			t.Fatalf("%s: %v", c.missing, err)
		}
		tt.Frame()
		if ta.page != pageAddPhone || ta.add.step != "tailnet" {
			t.Fatalf("%s: Set up Tailscale opened %v %q", c.missing, ta.page, ta.add.step)
		}
	}
	// The phone's step, while no Android phone is on the tailnet.
	ta.api.SetTailnet(driver.TailnetStatus{State: driver.TailnetRunning, Tailnet: "someone@example.com", Self: "Someone's MacBook"})
	ta.tn.at = time.Time{}
	ta.waitFor(t, "the phone's step", func() bool { tt.Frame(); return hasText(tt, "Waiting for an Android phone on your tailnet…") })
	if !hasText(tt, oneVPN) {
		t.Errorf("the phone's step: %q", tt.Texts())
	}
}

// TestTailnetDiagnostics: the diagnostics say whether Tailscale runs here,
// what the Android devices on the tailnet are to Pinky, and each Android
// phone's Use away from home, without the tailnet's or the devices' names.
func TestTailnetDiagnostics(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.sync()
	var phones []driver.Info
	for _, d := range ta.devices {
		phones = append(phones, d.Info)
	}
	id := phones[0].ID
	ta.api.SetPhoneAway(id, driver.Away{Port: 5555, Missing: driver.AwayNoTailscaleThere})
	got := tailnetDiagnostics(ta.ctx, ta.api, phones)
	want := []string{
		"Tailscale: running · tailnet name not shown · Android devices on it: 1",
		"Tailscale: Android device 1: online · not one of Pinky's phones",
		"Use away from home on Pixel 9: off · missing tailscale_phone",
	}
	if !slices.Equal(got, want) {
		t.Errorf("lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, l := range got {
		if strings.Contains(l, "example.com") || strings.Contains(l, "pixel-8a") || strings.Contains(l, "MacBook") {
			t.Errorf("a name in %q", l)
		}
	}
	ta.api.SetTailnet(driver.TailnetStatus{State: driver.TailnetStopped})
	if got := tailnetDiagnostics(ta.ctx, ta.api, nil); !slices.Equal(got, []string{"Tailscale: stopped"}) {
		t.Errorf("stopped: %q", got)
	}
}

// TestUseWiFiInstead moves an Android 10 phone on a cable to Wi-Fi from the
// ⋯ menu under its mirror, through the dialog that says what it opens; a
// restart then leaves it offline with what to do, and Settings › Phones
// stops it.
func TestUseWiFiInstead(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	id := ta.api.AddAndroid10("Pixel")
	ta.sync()
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 1000)
	ta.waitFor(t, "the Wi-Fi switch", func() bool { tt.Frame(); _, ok := ta.phoneWiFi.got[id]; return ok })
	if err := tt.Click(locales.PhoneMoreControls()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Use Wi-Fi instead…"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	for _, want := range []string{"Use Pixel over Wi-Fi?", "192.168.1.42", "tap Cancel, never Allow", "use it only on Wi-Fi you trust"} {
		if !hasText(tt, want) {
			t.Fatalf("dialog lacks %q: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Use Wi-Fi"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "on", func() bool {
		tt.Frame()
		return hasText(tt, "Pixel is connected over Wi-Fi at 192.168.1.42. You can unplug it now.")
	})

	// It restarts off the cable: its page says to plug it in once.
	ta.api.RestartOnWiFi(id)
	ta.sync()
	tt.Frame()
	if !hasText(tt, "Pixel's Wi-Fi connection stopped, as it does when the phone restarts. Plug it in once, then choose Use Wi-Fi again in the ⋯ menu under its picture.") {
		t.Fatalf("offline page: %q", tt.Texts())
	}

	// Settings › Phones says the same, and stops it.
	ta.page, ta.set.sec = pageSettings, secPhones
	ta.phoneWiFi.at[id] = ta.phoneWiFi.at[id].AddDate(0, 0, -1)
	ta.waitFor(t, "the row", func() bool { tt.Frame(); return hasText(tt, "Pixel's Wi-Fi connection stopped") })
}

// TestWiFiRow: Settings › Phones offers the switch for an Android 10 phone
// on a cable, not for a newer one.
func TestWiFiRow(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	id := ta.api.AddAndroid10("Pixel")
	ta.sync()
	ta.page, ta.set.sec = pageSettings, secPhones
	tt := mg.NewTester(ta.view, 1280, 1400)
	ta.waitFor(t, "the row", func() bool { tt.Frame(); return hasText(tt, "Wi-Fi connection") })
	tt.Frame()
	if !hasText(tt, "Pixel has no Wireless debugging. Keep it on this computer's Wi-Fi, and Pinky can reach it without the cable.") {
		t.Fatalf("row: %q", tt.Texts())
	}
	if hasText(tt, "Pixel 9 has no Wireless debugging") {
		t.Error("offered to Android 16")
	}
	if err := tt.Click("Use Wi-Fi instead…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Use Wi-Fi"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "on", func() bool { tt.Frame(); return hasText(tt, "On: Pinky reaches Pixel over Wi-Fi at 192.168.1.42") })
	if err := tt.Click("Stop the Wi-Fi connection"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "off", func() bool { tt.Frame(); return hasText(tt, "Stopped Pixel's Wi-Fi connection") })
	if st, _ := ta.api.PhoneWiFi(ta.ctx, id); st.On {
		t.Fatalf("still on: %+v", st)
	}
}
