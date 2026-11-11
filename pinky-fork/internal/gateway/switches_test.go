package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
)

// deviceSteps returns the texts of a task's phone_device steps, without
// the ones that waited for the person.
func deviceSteps(g *Gateway, s *Session) []string {
	var out []string
	for _, st := range taskSteps(g, s) {
		if st.Tool == "phone_device" && !strings.HasPrefix(st.Text, "Waiting for you: ") {
			out = append(out, st.Text)
		}
	}
	return out
}

func switchOf(res *Result, name string) (SwitchView, bool) {
	for _, sw := range res.Switches {
		if sw.Name == name {
			return sw, true
		}
	}
	return SwitchView{}, false
}

// release ends the session's task and waits until Pinky put back what it
// changed.
func release(t *testing.T, g *Gateway, s *Session) {
	t.Helper()
	mustStatus(t, s.Release(context.Background(), ReleaseRequest{}), StatusSettled)
	g.Flush()
}

func TestPhoneDeviceReads(t *testing.T) {
	x := newExtraDev("pixel", "Pixel 9")
	x.NoSwitch(driver.SwitchLocation, "android: an Android phone has no simulated location")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Check the switches")
	res := s.Device(context.Background(), DeviceRequest{})
	mustStatus(t, res, StatusSettled)
	if len(res.Switches) != 3 {
		t.Fatalf("switches %+v", res.Switches)
	}
	if sw, _ := switchOf(res, driver.SwitchRotation); sw.Value != driver.RotationPortrait || sw.Changed {
		t.Fatalf("rotation %+v", sw)
	}
	if sw, _ := switchOf(res, driver.SwitchAppearance); sw.Value != driver.AppearanceLight {
		t.Fatalf("appearance %+v", sw)
	}
	if sw, _ := switchOf(res, driver.SwitchLocation); sw.Unsupported != "An Android phone has no simulated location." {
		t.Fatalf("location %+v", sw)
	}
	if len(g.Needs()) != 0 || len(deviceSteps(g, s)) != 0 {
		t.Fatalf("a read asked or was recorded: %+v %q", g.Needs(), deviceSteps(g, s))
	}
}

// On a real phone every change asks; Allow for this task ends the asking,
// and the end of the task puts everything back, newest first.
func TestPhoneDeviceAsksOnPhoneAndPutsBack(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	if err := g.Policy().SetPreset(policy.PresetPayments); err != nil {
		t.Fatal(err)
	}
	s, _ := connect(t, g, "Test dark mode")

	done := make(chan *Result, 1)
	go func() { done <- s.Device(ctx, DeviceRequest{Appearance: "dark"}) }()
	n := waitNeedKind(t, g, NeedApproval)
	if n.Rule != settingsRule || n.Title != "Set the appearance to dark?" {
		t.Fatalf("approval %+v", n)
	}
	if err := g.Decide(n.ID, Deny, ""); err != nil {
		t.Fatal(err)
	}
	res := <-done
	mustStatus(t, res, StatusDenied)
	if v := x.SwitchValue(driver.SwitchAppearance); v != driver.AppearanceLight {
		t.Fatalf("appearance %q after Deny", v)
	}

	go func() {
		done <- s.Device(ctx, DeviceRequest{Call: Call{Intent: "See the app in dark mode"}, Appearance: "dark"})
	}()
	n = waitNeedKind(t, g, NeedApproval)
	if !strings.Contains(n.Detail, "See the app in dark mode") {
		t.Fatalf("detail %q", n.Detail)
	}
	if err := g.Decide(n.ID, AllowTask, ""); err != nil {
		t.Fatal(err)
	}
	res = <-done
	mustStatus(t, res, StatusSettled)
	if sw, _ := switchOf(res, driver.SwitchAppearance); sw.Value != "dark" || sw.Was != "light" || !sw.Changed {
		t.Fatalf("appearance %+v", sw)
	}
	if res.Screen == nil || !strings.Contains(res.Message, "puts the phone's settings back") {
		t.Fatalf("result %+v", res)
	}

	// Allowed for the task: the next change goes through.
	res = s.Device(ctx, DeviceRequest{Rotation: "landscape", Location: &Location{Latitude: 35.6812, Longitude: 139.7671}})
	mustStatus(t, res, StatusSettled)
	if len(g.Needs()) != 0 {
		t.Fatalf("asked again: %+v", g.Needs())
	}
	if x.SwitchValue(driver.SwitchRotation) != "landscape" || x.SwitchValue(driver.SwitchLocation) != "35.6812,139.7671" {
		t.Fatalf("switches %q %q", x.SwitchValue(driver.SwitchRotation), x.SwitchValue(driver.SwitchLocation))
	}
	// Already so: nothing to change or ask.
	res = s.Device(ctx, DeviceRequest{Rotation: "landscape"})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "already") {
		t.Fatalf("again: %+v", res)
	}

	release(t, g, s)
	for name, want := range map[string]string{driver.SwitchAppearance: "light", driver.SwitchRotation: "portrait", driver.SwitchLocation: ""} {
		if v := x.SwitchValue(name); v != want {
			t.Fatalf("%s %q after the task, want %q", name, v, want)
		}
	}
	want := []string{"Set the appearance to dark", "Set the screen's rotation to landscape", "Set the location to 35.6812, 139.7671",
		"Stopped the simulated location", "Put the screen's rotation back to portrait", "Put the appearance back to light"}
	if got := deviceSteps(g, s); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("steps\n%q\nwant\n%q", got, want)
	}
}

// Emulators and Simulators ask only when the person chose to approve more
// than payments.
func TestPhoneDeviceVirtual(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("emulator-5554", "Pixel 9 emulator")
	x.SetVirtual(true)
	g := newExtraGateway(t, x)
	if err := g.Policy().SetPreset(policy.PresetPayments); err != nil {
		t.Fatal(err)
	}
	s, _ := connect(t, g, "Test landscape")
	mustStatus(t, s.Device(ctx, DeviceRequest{Rotation: "landscape"}), StatusSettled)
	if x.SwitchValue(driver.SwitchRotation) != "landscape" {
		t.Fatal("did not turn")
	}

	if err := g.Policy().SetPreset(policy.PresetCommits); err != nil {
		t.Fatal(err)
	}
	done := make(chan *Result, 1)
	go func() { done <- s.Device(ctx, DeviceRequest{Appearance: "dark"}) }()
	n := waitNeedKind(t, g, NeedApproval)
	if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, <-done, StatusSettled)
	// Allow once: the next change asks again.
	go func() { done <- s.Device(ctx, DeviceRequest{Appearance: "light"}) }()
	n = waitNeedKind(t, g, NeedApproval)
	if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, <-done, StatusSettled)

	// Back to light by hand: nothing is left to put back for it.
	release(t, g, s)
	got := deviceSteps(g, s)
	if len(got) != 4 || got[3] != "Put the screen's rotation back to portrait" {
		t.Fatalf("steps %q", got)
	}
}

func TestPhoneDeviceReadOnly(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	x.SetVirtual(true)
	g := newExtraGateway(t, x)
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeReadOnly})
	s, _ := connect(t, g, "Look around")
	mustStatus(t, s.Device(ctx, DeviceRequest{}), StatusSettled)
	if res := s.Device(ctx, DeviceRequest{Appearance: "dark"}); res.Error != ErrScopeDenied {
		t.Fatalf("read-only agent: %+v", res)
	}
	res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Settings", Language: "ja"})
	if res.Error != ErrScopeDenied && res.Status != StatusBlocked {
		t.Fatalf("read-only agent's launch in Japanese: %+v", res)
	}
	if x.AppLocale("com.android.settings") != "" {
		t.Fatal("the language changed")
	}

	g.SetAgentScope(agent.Name, Scope{Mode: ScopeEverything})
	if err := g.SetReadOnly("pixel", true); err != nil {
		t.Fatal(err)
	}
	res = s.Device(ctx, DeviceRequest{Rotation: "landscape"})
	if res.Status != StatusBlocked || res.Rule != policy.RuleReadOnly {
		t.Fatalf("read-only mode: %+v", res)
	}
	res = s.Act(ctx, Call{}, Action{Tool: "launch", App: "Settings", Language: "ja"})
	if res.Status != StatusBlocked || res.Rule != policy.RuleReadOnly {
		t.Fatalf("read-only mode's launch in Japanese: %+v", res)
	}
	// Putting back is no change of the person's settings: it stays allowed.
	mustStatus(t, s.Device(ctx, DeviceRequest{Reset: []string{"all"}}), StatusSettled)
	if x.SwitchValue(driver.SwitchRotation) != "portrait" || x.AppLocale("com.android.settings") != "" {
		t.Fatal("something changed")
	}
	if got := deviceSteps(g, s); len(got) != 1 || got[0] != "Blocked by “Read-only”: set the screen's rotation to landscape" {
		t.Fatalf("steps %q", got)
	}
}

// A switch the person changed while the task ran stays as they set it.
func TestPhoneDeviceLeavesPersonsChange(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	x.SetVirtual(true)
	g := newExtraGateway(t, x, func(o *Options) {
		pol := testPolicy(t)
		if err := pol.SetPreset(policy.PresetPayments); err != nil {
			t.Fatal(err)
		}
		o.Policy = pol
	})
	s, _ := connect(t, g, "Test landscape")
	mustStatus(t, s.Device(ctx, DeviceRequest{Rotation: "landscape", Appearance: "dark"}), StatusSettled)
	if err := x.SetSwitch(ctx, driver.SwitchRotation, "upside_down"); err != nil {
		t.Fatal(err)
	}
	// A reset says so too.
	res := s.Device(ctx, DeviceRequest{Reset: []string{"rotation"}})
	mustStatus(t, res, StatusSettled)
	if sw, _ := switchOf(res, driver.SwitchRotation); sw.Value != "upside_down" || sw.Changed {
		t.Fatalf("rotation %+v", sw)
	}
	if err := x.SetSwitch(ctx, driver.SwitchAppearance, "light"); err != nil {
		t.Fatal(err)
	}
	if err := g.Stop("pixel"); err != nil {
		t.Fatal(err)
	}
	g.Flush()
	if x.SwitchValue(driver.SwitchRotation) != "upside_down" || x.SwitchValue(driver.SwitchAppearance) != "light" {
		t.Fatal("put back over the person's change")
	}
	want := []string{"Set the screen's rotation to landscape", "Set the appearance to dark",
		"Left the screen's rotation as the person set it", "Left the appearance as the person set it"}
	if got := deviceSteps(g, s); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("steps %q", got)
	}
}

// An agent that goes away without releasing gets its changes put back
// all the same; a phone that is offline at the end cannot be, and history
// says what stayed.
func TestPhoneDevicePutsBackWhenAgentLeaves(t *testing.T) {
	ctx := context.Background()
	newVirtual := func() (*extraDev, *Gateway) {
		x := newExtraDev("pixel", "Pixel 9")
		x.SetVirtual(true)
		g := newExtraGateway(t, x, func(o *Options) {
			pol := testPolicy(t)
			if err := pol.SetPreset(policy.PresetPayments); err != nil {
				t.Fatal(err)
			}
			o.Policy = pol
		})
		return x, g
	}

	x, g := newVirtual()
	s, _ := connect(t, g, "Test dark mode")
	mustStatus(t, s.Device(ctx, DeviceRequest{Appearance: "dark", Location: &Location{Latitude: 1.3521, Longitude: 103.8198}}), StatusSettled)
	s.CloseNow()
	g.Flush()
	if x.SwitchValue(driver.SwitchAppearance) != "light" || x.SwitchValue(driver.SwitchLocation) != "" {
		t.Fatal("not put back after the agent left")
	}
	want := []string{"Set the appearance to dark", "Set the location to 1.3521, 103.8198", "Stopped the simulated location", "Put the appearance back to light"}
	if got := deviceSteps(g, s); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("steps %q", got)
	}

	x, g = newVirtual()
	s, _ = connect(t, g, "Test dark mode")
	mustStatus(t, s.Device(ctx, DeviceRequest{Appearance: "dark"}), StatusSettled)
	x.SetOffline(true)
	s.CloseNow()
	g.Flush()
	var last StepView
	for _, st := range taskSteps(g, s) {
		last = st
	}
	if last.Text != "Could not put the appearance back to light" || last.Status != string(ErrDeviceOffline) {
		t.Fatalf("last step %+v", last)
	}
	x.SetOffline(false)
	if x.SwitchValue(driver.SwitchAppearance) != "dark" {
		t.Fatal("the appearance changed while the phone was offline")
	}
}

// reset puts back sooner, and the end of the task has nothing left to do.
func TestPhoneDeviceReset(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	x.SetVirtual(true)
	g := newExtraGateway(t, x)
	if err := g.Policy().SetPreset(policy.PresetPayments); err != nil {
		t.Fatal(err)
	}
	s, _ := connect(t, g, "Test a store finder")
	mustStatus(t, s.Device(ctx, DeviceRequest{Location: &Location{Latitude: 48.8584, Longitude: 2.2945}}), StatusSettled)
	mustStatus(t, s.Device(ctx, DeviceRequest{Location: &Location{Latitude: 51.5007, Longitude: -0.1246}}), StatusSettled)
	res := s.Device(ctx, DeviceRequest{Reset: []string{"location", "appearance"}})
	mustStatus(t, res, StatusSettled)
	if x.SwitchValue(driver.SwitchLocation) != "" || res.Message != "Stopped the simulated location." {
		t.Fatalf("reset: %q, %+v", x.SwitchValue(driver.SwitchLocation), res)
	}
	res = s.Device(ctx, DeviceRequest{Reset: []string{"all"}})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "nothing was put back") {
		t.Fatalf("second reset: %+v", res)
	}
	release(t, g, s)
	want := []string{"Set the location to 48.8584, 2.2945", "Set the location to 51.5007, -0.1246", "Stopped the simulated location"}
	if got := deviceSteps(g, s); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("steps %q", got)
	}
}

func TestPhoneDeviceUnsupported(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	x.SetVirtual(true)
	x.NoSwitch(driver.SwitchLocation, "android: an Android phone reports where it is; only emulators take a simulated location")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Test a store finder")
	res := s.Device(ctx, DeviceRequest{Appearance: "dark", Location: &Location{Latitude: 1, Longitude: 2}})
	if res.Error != ErrUnsupported || !strings.Contains(res.Message, "only emulators take a simulated location") {
		t.Fatalf("unsupported: %+v", res)
	}
	if x.SwitchValue(driver.SwitchAppearance) != "light" || len(g.Needs()) != 0 {
		t.Fatal("changed or asked before finding the phone lacks a switch")
	}
}

func TestPhoneDeviceInvalid(t *testing.T) {
	g, _ := newTestGateway(t)
	s, _ := connect(t, g, "Bad arguments")
	for _, req := range []DeviceRequest{
		{Rotation: "sideways"},
		{Appearance: "sepia"},
		{Location: &Location{Latitude: 91}},
		{Location: &Location{Longitude: -181}},
		{Reset: []string{"wifi"}},
		{Rotation: "landscape", Reset: []string{"rotation"}},
		{Appearance: "dark", Reset: []string{"all"}},
	} {
		if res := s.Device(context.Background(), req); res.Error != ErrInvalidArgument {
			t.Errorf("%+v: %+v", req, res)
		}
	}
	for _, a := range []Action{
		{Tool: "tap", Ref: 1, Language: "ja"},
		{Tool: "launch", App: "Settings", Language: "japanese"},
		{Tool: "launch", App: "Settings", Region: "Japan"},
		{Tool: "launch", App: "Settings", Language: "en-US", Region: "GB"},
	} {
		if res := s.Act(context.Background(), Call{}, a); res.Error != ErrInvalidArgument {
			t.Errorf("%+v: %+v", a, res)
		}
	}
}

// An app opened in another language asks on a real phone, and its
// language goes back when the task ends.
func TestLaunchInLanguage(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	if err := g.Policy().SetPreset(policy.PresetPayments); err != nil {
		t.Fatal(err)
	}
	s, _ := connect(t, g, "Check the Japanese strings")
	done := make(chan *Result, 1)
	go func() {
		done <- s.Act(ctx, Call{}, Action{Tool: "launch", App: "Settings", Language: "ja", Region: "jp"})
	}()
	n := waitNeedKind(t, g, NeedApproval)
	if n.Rule != settingsRule || n.Title != "Open Settings in ja-JP?" {
		t.Fatalf("approval %+v", n)
	}
	if err := g.Decide(n.ID, AllowTask, ""); err != nil {
		t.Fatal(err)
	}
	res := <-done
	mustStatus(t, res, StatusSettled)
	if got := x.AppLocale("com.android.settings"); got != "ja-JP" {
		t.Fatalf("Settings' language %q", got)
	}
	// Allowed for the task: phone_device does not ask either.
	mustStatus(t, s.Device(ctx, DeviceRequest{Appearance: "dark"}), StatusSettled)
	// The same app again, in another language: both are undone, newest first.
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "com.android.settings", Language: "zh-hans-cn"}), StatusSettled)
	if got := x.AppLocale("com.android.settings"); got != "zh-Hans-CN" {
		t.Fatalf("Settings' language %q", got)
	}
	// Without a language, a launch changes nothing.
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Messages"}), StatusSettled)
	release(t, g, s)
	if got := x.AppLocale("com.android.settings"); got != "" {
		t.Fatalf("Settings' language %q after the task", got)
	}
	var texts []string
	for _, st := range taskSteps(g, s) {
		texts = append(texts, st.Text)
	}
	all := strings.Join(texts, "|")
	for _, want := range []string{"Opened Settings in ja-JP", "Opened Settings in zh-Hans-CN", "Put the language of Settings back", "Put the appearance back to light"} {
		if !strings.Contains(all, want) {
			t.Fatalf("no %q in %q", want, texts)
		}
	}
	// The language changed first, so it goes back last.
	if strings.Index(all, "Put the language of Settings back") < strings.Index(all, "Put the appearance back") {
		t.Fatalf("not put back newest first: %q", texts)
	}
}

// The person set the app's language while the task ran: it stays.
func TestLaunchInLanguageLeavesPersonsChoice(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	x.SetVirtual(true)
	g := newExtraGateway(t, x)
	if err := g.Policy().SetPreset(policy.PresetPayments); err != nil {
		t.Fatal(err)
	}
	s, _ := connect(t, g, "Check the German strings")
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Settings", Region: "DE"}), StatusSettled)
	if got := x.AppLocale("com.android.settings"); got != "DE" {
		t.Fatalf("Settings' language %q", got)
	}
	x.SetAppLocale("com.android.settings", "fr-FR")
	release(t, g, s)
	if got := x.AppLocale("com.android.settings"); got != "fr-FR" {
		t.Fatalf("Settings' language %q", got)
	}
	var found bool
	for _, st := range taskSteps(g, s) {
		found = found || st.Text == "Left the language of Settings as the person set it"
	}
	if !found {
		t.Fatalf("steps %+v", taskSteps(g, s))
	}
}

func TestParseLocale(t *testing.T) {
	for _, c := range []struct {
		lang, region string
		want         driver.Locale
		bad          bool
	}{
		{"ja", "", driver.Locale{Language: "ja"}, false},
		{"JA", "jp", driver.Locale{Language: "ja", Region: "JP"}, false},
		{"pt-BR", "", driver.Locale{Language: "pt", Region: "BR"}, false},
		{"zh-hans-cn", "", driver.Locale{Language: "zh-Hans", Region: "CN"}, false},
		{"zh_Hant_TW", "tw", driver.Locale{Language: "zh-Hant", Region: "TW"}, false},
		{"es-419", "", driver.Locale{Language: "es", Region: "419"}, false},
		{"", "de", driver.Locale{Region: "DE"}, false},
		{"en-US", "GB", driver.Locale{}, true},
		{"english", "", driver.Locale{}, true},
		{"ja", "Japan", driver.Locale{}, true},
		{"en-x-private", "", driver.Locale{}, true},
	} {
		got, err := parseLocale(c.lang, c.region)
		if c.bad {
			if err == nil {
				t.Errorf("parseLocale(%q, %q) = %+v, want an error", c.lang, c.region, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("parseLocale(%q, %q) = %+v, %v; want %+v", c.lang, c.region, got, err, c.want)
		}
	}
}

// The person turns the screen from the ⋯ menu under the mirror: nobody is
// asked, a task on the phone's own screen pauses, a task on a background
// display goes on, an agent's handoff stays open, and a task that turned
// the screen earlier leaves it as the person set it.
func TestPersonSetsSwitch(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	x.SetVirtual(true) // the agent's own change asks nobody under the default preset
	g := newExtraGateway(t, x)
	if err := g.Policy().SetPreset(policy.PresetPayments); err != nil {
		t.Fatal(err)
	}
	if !g.Devices()[0].Features.Rotate {
		t.Fatalf("features %+v", g.Devices()[0].Features)
	}

	// No task: it turns, and reads back.
	if err := g.SetDeviceSwitch(ctx, "pixel", driver.SwitchRotation, driver.RotationLandscape); err != nil {
		t.Fatal(err)
	}
	if got, err := g.DeviceSwitch(ctx, "pixel", driver.SwitchRotation); err != nil || got != driver.RotationLandscape {
		t.Fatalf("rotation %q, %v", got, err)
	}
	if st := g.Devices()[0].Status; st == DeviceInControl {
		t.Fatal("turning an idle phone put the person in control")
	}
	for _, bad := range [][2]string{{driver.SwitchRotation, "sideways"}, {driver.SwitchAppearance, "auto"}, {"volume", "1"}} {
		if err := g.SetDeviceSwitch(ctx, "pixel", bad[0], bad[1]); !errors.Is(err, driver.ErrUnsupported) {
			t.Errorf("%s=%s: %v", bad[0], bad[1], err)
		}
	}
	if err := g.SetDeviceSwitch(ctx, "nope", driver.SwitchRotation, driver.RotationPortrait); err != ErrUnknownDevice {
		t.Fatalf("unknown phone: %v", err)
	}

	// A task on the phone's own screen pauses, and the agent's change is
	// left as the person set it when the task ends.
	s, _ := connect(t, g, "Test landscape")
	mustStatus(t, s.Device(ctx, DeviceRequest{Rotation: "reverse_landscape"}), StatusSettled)
	if err := g.SetDeviceSwitch(ctx, "pixel", driver.SwitchRotation, driver.RotationUpsideDown); err != nil {
		t.Fatal(err)
	}
	if len(g.Needs()) != 0 {
		t.Fatalf("the person's turn asked: %+v", g.Needs())
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "back"}), StatusTookOver)
	if err := g.HandBack("pixel", ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "back"}), StatusSettled)

	// While the agent hands the phone over, turning it keeps the handoff
	// open until the person hands back.
	done := make(chan *Result, 1)
	go func() { done <- s.Handoff(ctx, HandoffRequest{Reason: "Turn the screen to portrait, please"}) }()
	if n := waitNeed(t, g); n.Kind != NeedHandoff {
		t.Fatalf("need %+v", n)
	}
	if err := g.SetDeviceSwitch(ctx, "pixel", driver.SwitchRotation, driver.RotationPortrait); err != nil {
		t.Fatal(err)
	}
	if n := g.Needs(); len(n) != 1 || n[0].Kind != NeedHandoff {
		t.Fatalf("needs after the turn: %+v", n)
	}
	if err := g.HandBack("pixel", "turned"); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.Status != StatusSettled || res.Note != "turned" {
		t.Fatalf("handoff %+v", res)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "back"}), StatusSettled)
	release(t, g, s)
	if got := x.SwitchValue(driver.SwitchRotation); got != driver.RotationPortrait {
		t.Fatalf("rotation after the task %q, want the person's portrait", got)
	}
	if want := "Left the screen's rotation as the person set it"; !strings.Contains(strings.Join(deviceSteps(g, s), "\n"), want) {
		t.Fatalf("steps %q, want %q", deviceSteps(g, s), want)
	}

	// A task on a background display goes on.
	if err := g.SetBackground("pixel", true); err != nil {
		t.Fatal(err)
	}
	s, _ = connect(t, g, "Lunch")
	if len(x.backgrounds()) == 0 {
		t.Fatal("no background display")
	}
	if err := g.SetDeviceSwitch(ctx, "pixel", driver.SwitchRotation, driver.RotationLandscape); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
}
