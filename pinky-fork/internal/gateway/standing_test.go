package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
)

// changeAppearance changes the phone's appearance and answers the
// approval it asks with d, when it asks; it reports whether it asked.
func changeAppearance(t *testing.T, g *Gateway, s *Session, to string, d Decision, check func(NeedView)) bool {
	t.Helper()
	done := make(chan *Result, 1)
	go func() { done <- s.Device(context.Background(), DeviceRequest{Appearance: to}) }()
	stop := make(chan struct{})
	defer close(stop)
	asked := false
	select {
	case res := <-done:
		mustStatus(t, res, StatusSettled)
		return false
	case n := <-needCh(g, stop):
		asked = true
		if check != nil {
			check(n)
		}
		if err := g.Decide(n.ID, d, ""); err != nil {
			t.Fatal(err)
		}
	}
	res := <-done
	if d == Deny {
		mustStatus(t, res, StatusDenied)
	} else {
		mustStatus(t, res, StatusSettled)
	}
	return asked
}

// needCh delivers the first approval card that shows, until stop closes.
func needCh(g *Gateway, stop <-chan struct{}) <-chan NeedView {
	ch := make(chan NeedView, 1)
	go func() {
		for range 1500 {
			for _, n := range g.Needs() {
				if n.Kind == NeedApproval {
					ch <- n
					return
				}
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	return ch
}

// Always allow on changing the phone's settings: the agent no longer asks
// on that phone, in later tasks and after a restart, while another agent
// still asks; the settings still go back when each task ends; asking
// every action, and taking it back in Settings, make it ask again.
func TestAlwaysAllowPhoneSettings(t *testing.T) {
	dir := t.TempDir()
	x := newExtraDev("pixel", "Pixel 9")
	open := func() *Gateway {
		pol := loadPolicy(t, dir)
		if err := pol.SetPreset(policy.PresetPayments); err != nil {
			t.Fatal(err)
		}
		return newExtraGateway(t, x, func(o *Options) { o.Policy = pol })
	}
	g := open()
	s, _ := connect(t, g, "Test dark mode")
	if !changeAppearance(t, g, s, "dark", AllowAlways, func(n NeedView) {
		if n.Rule != settingsRule || !n.AlwaysAllow || !n.AllowTask || n.Agent != agent.Name || n.DeviceName != "Pixel 9" {
			t.Errorf("approval %+v", n)
		}
	}) {
		t.Fatal("the first change did not ask")
	}
	got := g.Exemptions()
	if len(got) != 1 || got[0].Rule != settingsRule || got[0].Agent != agent.Name || got[0].DeviceID != "pixel" ||
		got[0].DeviceName != "Pixel 9" || got[0].AppID != "" || len(got[0].With) != 0 {
		t.Fatalf("exemptions %+v", got)
	}
	release(t, g, s)
	if v := x.SwitchValue(driver.SwitchAppearance); v != driver.AppearanceLight {
		t.Fatalf("appearance %q after the task", v)
	}

	// The next task, after a restart: no question, and still put back.
	g.Close()
	g = open()
	s, _ = connect(t, g, "Test dark mode again")
	if changeAppearance(t, g, s, "dark", AllowOnce, nil) {
		t.Fatal("asked again after Always allow")
	}
	if v := x.SwitchValue(driver.SwitchAppearance); v != "dark" {
		t.Fatalf("appearance %q", v)
	}
	release(t, g, s)
	if v := x.SwitchValue(driver.SwitchAppearance); v != driver.AppearanceLight {
		t.Fatalf("appearance %q after the second task", v)
	}

	// Another agent on the same phone asks.
	other := g.OpenSession(AgentInfo{Name: "Codex", Client: "codex 1.0"})
	t.Cleanup(other.Close)
	mustStatus(t, other.Connect(context.Background(), ConnectRequest{Task: "Dark mode"}), StatusSettled)
	if !changeAppearance(t, g, other, "dark", Deny, nil) {
		t.Fatal("another agent did not ask")
	}
	release(t, g, other)

	// While every action asks, it asks, and offers no Always allow.
	if err := g.Policy().SetPreset(policy.PresetEvery); err != nil {
		t.Fatal(err)
	}
	s, _ = connect(t, g, "Every action")
	if !changeAppearance(t, g, s, "dark", Deny, func(n NeedView) {
		if n.AlwaysAllow {
			t.Errorf("Always allow offered while every action asks: %+v", n)
		}
	}) {
		t.Fatal("did not ask while every action asks")
	}
	release(t, g, s)
	if err := g.Policy().SetPreset(policy.PresetPayments); err != nil {
		t.Fatal(err)
	}

	// Taken back in Settings: it asks again.
	if err := g.RemoveExemption(g.Exemptions()[0]); err != nil {
		t.Fatal(err)
	}
	if len(g.Exemptions()) != 0 {
		t.Fatalf("exemptions %+v", g.Exemptions())
	}
	s, _ = connect(t, g, "Taken back")
	if !changeAppearance(t, g, s, "dark", Deny, nil) {
		t.Fatal("did not ask after taking it back")
	}
}

// Always allow on reading an app's logs holds for that agent, phone and
// app: another app's logs still ask, and removing the phone drops it.
func TestAlwaysAllowAppLogs(t *testing.T) {
	ctx := context.Background()
	g, _ := newLogsGateway(t)
	s, _ := connect(t, g, "Test Crash Test")
	answer(t, g, AllowAlways, func(n NeedView) {
		if n.Rule != policy.RuleAppLogs || !n.AlwaysAllow || n.App != "Crash Test" {
			t.Errorf("approval %+v", n)
		}
	})
	mustStatus(t, s.Logs(ctx, LogsRequest{App: "Crash Test"}), StatusSettled)
	got := g.Exemptions()
	if len(got) != 1 || got[0].Rule != policy.RuleAppLogs || got[0].AppID != crashTest || got[0].AppName != "Crash Test" ||
		got[0].Agent != agent.Name || got[0].DeviceID != "pixel" {
		t.Fatalf("exemptions %+v", got)
	}
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)

	// The next task reads Crash Test's logs without asking.
	s, _ = connect(t, g, "Test again")
	mustStatus(t, s.Logs(ctx, LogsRequest{App: crashTest, Kind: "crashes"}), StatusSettled)
	if len(g.Needs()) != 0 {
		t.Fatalf("asked: %+v", g.Needs())
	}
	// Another app asks.
	answer(t, g, Deny, func(n NeedView) {
		if n.App != "Notes Two" {
			t.Errorf("approval %+v", n)
		}
	})
	mustStatus(t, s.Logs(ctx, LogsRequest{App: "Notes Two"}), StatusDenied)
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)

	// Removing the phone drops its answers.
	if err := g.Policy().ForgetDevice("pixel"); err != nil {
		t.Fatal(err)
	}
	if len(g.Exemptions()) != 0 {
		t.Fatalf("exemptions after forgetting the phone %+v", g.Exemptions())
	}
}

// tryInstall installs from rel in the phone's folder and answers the
// approval it asks with d, when it asks; it reports whether it asked.
func tryInstall(t *testing.T, g *Gateway, s *Session, rel string, d Decision, check func(NeedView)) (bool, *Result) {
	t.Helper()
	done := make(chan *Result, 1)
	go func() { done <- s.App(context.Background(), AppRequest{Action: "install", Paths: []string{rel}}) }()
	stop := make(chan struct{})
	defer close(stop)
	select {
	case res := <-done:
		return false, res
	case n := <-needCh(g, stop):
		if check != nil {
			check(n)
		}
		if err := g.Decide(n.ID, d, ""); err != nil {
			t.Fatal(err)
		}
	}
	return true, <-done
}

// Always allow on installing from the computer holds for that agent on
// that phone, for builds of that app id by that signer: the next task's
// build goes in without asking, while another signer, another app,
// another agent, an uninstall, every action asking and a rule of the
// person's own still ask, and taking it back makes it ask again.
func TestAlwaysAllowInstall(t *testing.T) {
	dir := t.TempDir()
	x := newExtraDev("pixel", "Pixel 9")
	x.pkgs = map[string]driver.Package{
		"v1":      {ID: "com.example.tool", Name: "Tool", Version: "1.0 (1)", Signer: "Example Dev (11111111)", SignerKey: "11111111aaaa"},
		"v2":      {ID: "com.example.tool", Name: "Tool", Version: "2.0 (2)", Signer: "Example Dev (11111111)", SignerKey: "11111111aaaa"},
		"resign":  {ID: "com.example.tool", Name: "Tool", Version: "3.0 (3)", Signer: "Someone Else (22222222)", SignerKey: "22222222bbbb"},
		"another": {ID: "com.example.other", Name: "Tool", Version: "1.0 (1)", Signer: "Example Dev (11111111)", SignerKey: "11111111aaaa"},
	}
	pol := loadPolicy(t, dir)
	g := newExtraGateway(t, x, func(o *Options) { o.Policy = pol })
	for _, f := range []string{"v1", "v2", "resign", "another"} {
		putFile(t, g, f+".apk", f)
	}
	mustAsk := func(s *Session, rel string, always bool) {
		t.Helper()
		asked, res := tryInstall(t, g, s, rel, Deny, func(n NeedView) {
			if n.Rule != policy.RuleInstall || n.AlwaysAllow != always {
				t.Errorf("%s: approval %+v", rel, n)
			}
		})
		if !asked {
			t.Fatalf("%s did not ask: %+v", rel, res)
		}
		mustStatus(t, res, StatusDenied)
	}

	s, _ := connect(t, g, "Build and test")
	asked, res := tryInstall(t, g, s, "v1.apk", AllowAlways, func(n NeedView) {
		if n.Rule != policy.RuleInstall || !n.AlwaysAllow || !n.AllowTask || n.Agent != agent.Name || n.DeviceName != "Pixel 9" || n.App != "Tool" {
			t.Errorf("approval %+v", n)
		}
	})
	if !asked {
		t.Fatal("the first install did not ask")
	}
	mustStatus(t, res, StatusSettled)
	got := g.Exemptions()
	if len(got) != 1 || got[0].Rule != policy.RuleInstall || got[0].AppID != "com.example.tool" || got[0].AppName != "Tool" ||
		got[0].Agent != agent.Name || got[0].DeviceID != "pixel" || got[0].DeviceName != "Pixel 9" ||
		got[0].Signer != "11111111aaaa" || got[0].SignerName != "Example Dev (11111111)" {
		t.Fatalf("exemptions %+v", got)
	}
	release(t, g, s)

	// The next task: the next build goes in without asking.
	s, _ = connect(t, g, "Build again")
	asked, res = tryInstall(t, g, s, "v2.apk", Deny, nil)
	if asked {
		t.Fatal("asked again after Always allow")
	}
	mustStatus(t, res, StatusSettled)
	if x.installed["com.example.tool"] != "2.0 (2)" {
		t.Fatalf("installed %v", x.installed)
	}
	// Another signer, and another app with the same name, ask, and offer
	// their own Always allow.
	mustAsk(s, "resign.apk", true)
	mustAsk(s, "another.apk", true)
	release(t, g, s)

	// Another agent on the same phone asks.
	other := g.OpenSession(AgentInfo{Name: "Codex", Client: "codex 1.0"})
	t.Cleanup(other.Close)
	mustStatus(t, other.Connect(context.Background(), ConnectRequest{Task: "Install"}), StatusSettled)
	mustAsk(other, "v2.apk", true)
	release(t, g, other)

	// A rule of the person's own that asks about the app still asks, and
	// Always allow is not offered: it would not stop that rule asking.
	if err := pol.SetFileRules(pol.FileRules(), []policy.Rule{{Name: "Tools", Decision: policy.Ask,
		Match: policy.Match{App: policy.StringList{"Tool"}}}}); err != nil {
		t.Fatal(err)
	}
	s, _ = connect(t, g, "With a rule")
	mustAsk(s, "v2.apk", false)
	release(t, g, s)
	if err := pol.SetFileRules(pol.FileRules(), nil); err != nil {
		t.Fatal(err)
	}

	// While every action asks, it asks, and offers no Always allow.
	if err := pol.SetPreset(policy.PresetEvery); err != nil {
		t.Fatal(err)
	}
	s, _ = connect(t, g, "Every action")
	mustAsk(s, "v2.apk", false)
	release(t, g, s)
	if err := pol.SetPreset(policy.PresetCommits); err != nil {
		t.Fatal(err)
	}

	// Read-only refuses it.
	if err := pol.SetReadOnly("pixel", true); err != nil {
		t.Fatal(err)
	}
	s, _ = connect(t, g, "Read-only")
	asked, res = tryInstall(t, g, s, "v2.apk", Deny, nil)
	if asked || res.Status != StatusBlocked || res.Rule != policy.RuleReadOnly {
		t.Fatalf("read-only: asked %v, %+v", asked, res)
	}
	release(t, g, s)
	if err := pol.SetReadOnly("pixel", false); err != nil {
		t.Fatal(err)
	}

	// Uninstalling still asks every time, with no Always allow.
	s, _ = connect(t, g, "Uninstall")
	done := make(chan *Result, 1)
	go func() { done <- s.App(context.Background(), AppRequest{Action: "uninstall", App: "com.dinely.app"}) }()
	n := waitNeedKind(t, g, NeedApproval)
	if n.Rule != policy.RuleUninstall || n.AlwaysAllow {
		t.Fatalf("uninstall approval %+v", n)
	}
	if err := g.Decide(n.ID, Deny, ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, <-done, StatusDenied)
	release(t, g, s)

	// Taken back in Settings: it asks again.
	if err := g.RemoveExemption(g.Exemptions()[0]); err != nil {
		t.Fatal(err)
	}
	if len(g.Exemptions()) != 0 {
		t.Fatalf("exemptions %+v", g.Exemptions())
	}
	s, _ = connect(t, g, "Taken back")
	mustAsk(s, "v2.apk", true)
}
