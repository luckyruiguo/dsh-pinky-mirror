package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
)

// scripted packages: a file whose contents are a key of extraDev.pkgs
// holds that package; any other file is read as the demo phone reads it.

func (x *extraDev) InspectPackage(ctx context.Context, paths []string) (driver.Package, error) {
	if p, ok := x.scripted(paths); ok {
		apps, err := x.Device.Apps(ctx)
		if err != nil {
			return driver.Package{}, err
		}
		for _, a := range apps {
			if a.ID == p.ID {
				p.Installed = "1.0"
			}
		}
		x.mu.Lock()
		if v, ok := x.installed[p.ID]; ok {
			p.Installed = v
		}
		x.mu.Unlock()
		return p, nil
	}
	return x.Device.InspectPackage(ctx, paths)
}

func (x *extraDev) InstallPackage(ctx context.Context, paths []string) error {
	if p, ok := x.scripted(paths); ok {
		x.mu.Lock()
		defer x.mu.Unlock()
		if x.installed == nil {
			x.installed = map[string]string{}
		}
		x.installed[p.ID] = p.Version
		return nil
	}
	return x.Device.InstallPackage(ctx, paths)
}

func (x *extraDev) scripted(paths []string) (driver.Package, bool) {
	if len(paths) != 1 {
		return driver.Package{}, false
	}
	b, err := os.ReadFile(paths[0])
	if err != nil {
		return driver.Package{}, false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	p, ok := x.pkgs[string(b)]
	return p, ok
}

// putApp copies a test APK into the phone's folder on the computer.
func putApp(t *testing.T, g *Gateway, rel, src string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, g, rel, string(b))
}

func putFile(t *testing.T, g *Gateway, rel, data string) {
	t.Helper()
	p := filepath.Join(g.FilesDir("pixel"), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

const testAPK = "../apppkg/testdata/pinky-test-1.apk"

func appSteps(g *Gateway, s *Session) []string {
	var out []string
	for _, st := range taskSteps(g, s) {
		if st.Tool == "phone_app" {
			out = append(out, st.Text+" ["+st.Status+"]")
		}
	}
	return out
}

func installed(t *testing.T, x *extraDev, id string) bool {
	t.Helper()
	apps, err := x.Apps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range apps {
		if a.ID == id {
			return true
		}
	}
	return false
}

func TestPhoneAppInstall(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Try the build")
	putApp(t, g, "build/app-debug.apk", testAPK)
	install := AppRequest{Action: "install", Paths: []string{"build/app-debug.apk"}}

	// Denied: nothing is installed.
	done := make(chan *Result, 1)
	go func() { done <- s.App(ctx, install) }()
	n := waitNeedKind(t, g, NeedApproval)
	if n.Rule != policy.RuleInstall || n.Title != "Install “Pinky Test” 1.0 (1) on Pixel 9?" || n.App != "Pinky Test" ||
		n.Detail != "Claude Code · app.pinky.testapp · signed by Pinky Test (292579aa) · from “build/app-debug.apk”" {
		t.Fatalf("approval %+v", n)
	}
	if !n.AlwaysAllow || !n.AllowTask {
		t.Fatalf("an install offers Allow for this task and Always allow: %+v", n)
	}
	if err := g.Decide(n.ID, Deny, ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, <-done, StatusDenied)
	if installed(t, x, "app.pinky.testapp") {
		t.Fatal("installed after Deny")
	}

	// Allowed for the task: this install and the next update go through.
	go func() { done <- s.App(ctx, install) }()
	n = waitNeedKind(t, g, NeedApproval)
	if err := g.Decide(n.ID, AllowTask, ""); err != nil {
		t.Fatal(err)
	}
	res := <-done
	mustStatus(t, res, StatusSettled)
	if !installed(t, x, "app.pinky.testapp") || len(res.Apps) != 1 || res.Apps[0].ID != "app.pinky.testapp" ||
		!strings.Contains(res.Message, "Installed “Pinky Test” 1.0 (1)") {
		t.Fatalf("install: %+v", res)
	}
	if x := g.policy.Exemptions(); len(x) != 0 {
		t.Fatalf("Allow for this task kept an Always allow: %+v", x)
	}
	res = s.App(ctx, install)
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "Updated “Pinky Test”") {
		t.Fatalf("update: %+v", res)
	}

	// The same id by another signer asks again.
	x.mu.Lock()
	x.pkgs = map[string]driver.Package{"other signer": {ID: "app.pinky.testapp", Name: "Pinky Test", Version: "2.0 (2)",
		Signer: "Someone Else (abcdef12)", SignerKey: "abcdef12"}}
	x.mu.Unlock()
	putFile(t, g, "other.apk", "other signer")
	go func() { done <- s.App(ctx, AppRequest{Action: "install", Paths: []string{"other.apk"}}) }()
	n = waitNeedKind(t, g, NeedApproval)
	if n.Title != "Update “Pinky Test” to 2.0 (2) on Pixel 9?" || !strings.Contains(n.Detail, "signed by Someone Else (abcdef12)") ||
		!strings.Contains(n.Detail, "replaces 1.0") {
		t.Fatalf("another signer: %+v", n)
	}
	if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, <-done, StatusSettled)

	want := []string{
		"Waiting for you: Install “Pinky Test” 1.0 (1) on Pixel 9 [approval_denied]",
		"Waiting for you: Install “Pinky Test” 1.0 (1) on Pixel 9 [allowed]",
		"Installed “Pinky Test” 1.0 (1) from “build/app-debug.apk” [settled]",
		"Updated “Pinky Test” to 1.0 (1) from “build/app-debug.apk” [settled]",
		"Waiting for you: Update “Pinky Test” to 2.0 (2) on Pixel 9 [allowed]",
		"Updated “Pinky Test” to 2.0 (2) from “other.apk” [settled]",
	}
	if got := appSteps(g, s); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The files are read again from a copy after the person's answer: a file
// changed meanwhile is not installed.
func TestPhoneAppInstallFileChanged(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	x.pkgs = map[string]driver.Package{
		"good": {ID: "com.example.tool", Name: "Tool", Version: "1.0 (1)", Signer: "Dev (11111111)", SignerKey: "11111111"},
		"bad":  {ID: "com.example.tool", Name: "Tool", Version: "6.6 (6)", Signer: "Dev (11111111)", SignerKey: "11111111"},
	}
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Try the build")
	putFile(t, g, "tool.apk", "good")
	done := make(chan *Result, 1)
	go func() { done <- s.App(ctx, AppRequest{Action: "install", Paths: []string{"tool.apk"}}) }()
	n := waitNeedKind(t, g, NeedApproval)
	putFile(t, g, "tool.apk", "bad")
	if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "changed after it was read") {
		t.Fatalf("changed file: %+v", res)
	}
	if len(x.installed) != 0 {
		t.Fatalf("installed %v", x.installed)
	}
}

func TestPhoneAppUninstallAndStop(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Clean up")

	// Stopping asks nothing and shows the screen after.
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	res := s.App(ctx, AppRequest{Action: "stop", App: "Dinely"})
	mustStatus(t, res, StatusSettled)
	if res.Screen == nil || res.Screen.App.ID == demo.Dinely {
		t.Fatalf("stop: %+v", res)
	}

	done := make(chan *Result, 1)
	go func() { done <- s.App(ctx, AppRequest{Action: "uninstall", App: "dinely"}) }()
	n := waitNeedKind(t, g, NeedApproval)
	if n.Rule != policy.RuleUninstall || n.Title != "Uninstall “Dinely” from Pixel 9?" ||
		n.Detail != "Claude Code · com.dinely.app · its data on the phone is deleted too" || n.AlwaysAllow || !n.AllowTask {
		t.Fatalf("approval %+v", n)
	}
	if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, <-done, StatusSettled)
	if installed(t, x, demo.Dinely) {
		t.Fatal("Dinely is still installed")
	}
	if res := s.App(ctx, AppRequest{Action: "uninstall", App: "Dinely"}); res.Error != ErrNotFound {
		t.Fatalf("uninstall of an app not installed: %+v", res)
	}

	// Under "Every action" a stop asks too.
	if err := g.policy.SetPreset(policy.PresetEvery); err != nil {
		t.Fatal(err)
	}
	go func() { done <- s.App(ctx, AppRequest{Action: "stop", App: "Chatter"}) }()
	n = waitNeedKind(t, g, NeedApproval)
	if n.Title != "Stop “Chatter” on Pixel 9?" || n.Detail != "Claude Code · com.chatter.app" {
		t.Fatalf("stop approval %+v", n)
	}
	if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, <-done, StatusSettled)

	want := []string{
		"Stopped “Dinely” [settled]",
		"Waiting for you: Uninstall “Dinely” from Pixel 9 [allowed]",
		"Uninstalled “Dinely” [settled]",
		"Failed: uninstall “Dinely” [not_found]",
		"Waiting for you: Stop “Chatter” on Pixel 9 [allowed]",
		"Stopped “Chatter” [settled]",
	}
	if got := appSteps(g, s); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPhoneAppRefuses(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	x.pkgs = map[string]driver.Package{
		"companion": {ID: "app.pinky.companion", Name: "Pinky Companion", Version: "9"},
		"settings":  {ID: "com.android.settings", Name: "Settings", Version: "99"},
		"chase":     {ID: "com.chase.sig.android", Name: "Chase", Version: "5.0"},
	}
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Mess around")
	for _, c := range []struct {
		req  AppRequest
		rule string
	}{
		{AppRequest{Action: "stop", App: "Settings"}, policy.RuleSystemApps},
		{AppRequest{Action: "uninstall", App: "Calculator"}, policy.RuleSystemApps},
		{AppRequest{Action: "uninstall", App: "Google Wallet"}, policy.RuleFinance},
		{AppRequest{Action: "stop", App: "Google Wallet"}, policy.RuleFinance},
		{AppRequest{Action: "install", Paths: []string{"companion.apk"}}, policy.RulePinkyApps},
		{AppRequest{Action: "install", Paths: []string{"settings.apk"}}, policy.RuleSystemApps},
		{AppRequest{Action: "install", Paths: []string{"chase.apk"}}, policy.RuleFinance},
	} {
		for k := range x.pkgs {
			putFile(t, g, k+".apk", k)
		}
		res := s.App(ctx, c.req)
		if res.Status != StatusBlocked || res.Rule != c.rule {
			t.Errorf("%+v: %+v, want blocked by %q", c.req, res, c.rule)
		}
	}
	if len(g.Needs()) != 0 || len(x.installed) != 0 {
		t.Fatalf("asked %v, installed %v", g.Needs(), x.installed)
	}
	steps := appSteps(g, s)
	if steps[0] != "Blocked by “System apps”: stop “Settings” [blocked_by_policy]" ||
		steps[4] != "Blocked by “Pinky's own apps”: install “Pinky Companion” 9 [blocked_by_policy]" {
		t.Fatalf("steps %q", steps)
	}

	// Read-only mode refuses all three; a watch-only agent may do none.
	if err := g.SetReadOnly("pixel", true); err != nil {
		t.Fatal(err)
	}
	if res := s.App(ctx, AppRequest{Action: "stop", App: "Dinely"}); res.Status != StatusBlocked || res.Rule != policy.RuleReadOnly {
		t.Fatalf("read-only stop: %+v", res)
	}
	if err := g.SetReadOnly("pixel", false); err != nil {
		t.Fatal(err)
	}
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeApps, Apps: []string{"Chatter"}})
	if res := s.App(ctx, AppRequest{Action: "stop", App: "Dinely"}); res.Error != ErrScopeDenied {
		t.Fatalf("stop outside the agent's scope: %+v", res)
	}
	mustStatus(t, s.App(ctx, AppRequest{Action: "stop", App: "Chatter"}), StatusSettled)
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeWatchOnly})
	if res := s.App(ctx, AppRequest{Action: "stop", App: "Chatter"}); res.Error != ErrScopeDenied {
		t.Fatalf("watch-only stop: %+v", res)
	}
}

func TestPhoneAppPaths(t *testing.T) {
	ctx := context.Background()
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	s, _ := connect(t, g, "Install")
	putFile(t, g, "notes.txt", "hi")
	putFile(t, g, "folder.apk/x", "hi")
	outside := filepath.Join(t.TempDir(), "evil.apk")
	b, _ := os.ReadFile(testAPK)
	os.WriteFile(outside, b, 0o600)
	links := true
	if err := os.Symlink(outside, filepath.Join(g.FilesDir("pixel"), "link.apk")); err != nil {
		links = false
	}
	os.Symlink(filepath.Dir(outside), filepath.Join(g.FilesDir("pixel"), "dir"))
	for _, c := range []struct {
		paths []string
		code  ErrorCode
		say   string
	}{
		{nil, ErrInvalidArgument, "install takes paths"},
		{[]string{"../evil.apk"}, ErrInvalidArgument, "not a path inside"},
		{[]string{"/etc/evil.apk"}, ErrInvalidArgument, "not a path inside"},
		{[]string{"notes.txt"}, ErrInvalidArgument, "not an app"},
		{[]string{"missing.apk"}, ErrNotFound, "is not in the phone's folder"},
		{[]string{"folder.apk"}, ErrNotFound, "is a folder"},
		{[]string{"link.apk"}, ErrNotFound, "symbolic link"},
		{[]string{"dir/evil.apk"}, ErrNotFound, "symbolic link"},
	} {
		if !links && strings.Contains(c.say, "symbolic") {
			continue
		}
		res := s.App(ctx, AppRequest{Action: "install", Paths: c.paths})
		if res.Error != c.code || !strings.Contains(res.Message, c.say) {
			t.Errorf("%v: %+v, want %s saying %q", c.paths, res, c.code, c.say)
		}
	}
	for _, c := range []struct {
		req AppRequest
		say string
	}{
		{AppRequest{Action: "uninstall"}, "takes app"},
		{AppRequest{Action: "stop", App: "Dinely", Paths: []string{"a.apk"}}, "not paths"},
		{AppRequest{Action: "reinstall", App: "Dinely"}, "install, uninstall or stop"},
		{AppRequest{Action: "stop", App: "https://example.com"}, "is a link"},
		{AppRequest{Action: "stop", App: "Nopeapp"}, "No installed app"},
	} {
		if res := s.App(ctx, c.req); !res.IsError() || !strings.Contains(res.Message, c.say) {
			t.Errorf("%+v: %+v, want an error saying %q", c.req, res, c.say)
		}
	}
	if len(g.Needs()) != 0 {
		t.Fatalf("asked %v", g.Needs())
	}
}

// A task's app list applies to the new app; a demo iPhone installs a .app
// folder but not a Simulator build.
func TestPhoneAppIPhone(t *testing.T) {
	ctx := context.Background()
	dev := demo.NewDevice("iphone", "iPhone 17", driver.IOS)
	g, err := New(Options{Providers: []driver.Provider{demo.NewProvider(driver.IOS, dev)}, Policy: testPolicy(t), TrustNewAgents: true,
		DataDir: t.TempDir(), NoScreenshots: true, DiscoverEvery: time.Hour, ApprovalTimeout: 3 * time.Second, MaxBlock: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	s := g.OpenSession(agent)
	t.Cleanup(s.Close)
	mustStatus(t, s.Connect(ctx, ConnectRequest{Task: "Try the build", Apps: []string{"Dinely", "Tool"}}), StatusSettled)
	dir := g.FilesDir("iphone")
	plist := func(id, name, platform string) string {
		return `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>` +
			`<key>CFBundleIdentifier</key><string>` + id + `</string><key>CFBundleName</key><string>` + name + `</string>` +
			`<key>CFBundleShortVersionString</key><string>2.1</string><key>CFBundleVersion</key><string>7</string>` +
			`<key>CFBundleSupportedPlatforms</key><array><string>` + platform + `</string></array></dict></plist>`
	}
	for rel, data := range map[string]string{
		"Tool.app/Info.plist":  plist("com.example.tool", "Tool", "iPhoneOS"),
		"Tool.app/Tool":        "binary",
		"Sim.app/Info.plist":   plist("com.example.tool", "Tool", "iPhoneSimulator"),
		"Other.app/Info.plist": plist("com.example.other", "Other", "iPhoneOS"),
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte(data), 0o600)
	}
	if res := s.App(ctx, AppRequest{Action: "install", Paths: []string{"Sim.app"}}); res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "iOS Simulator") {
		t.Fatalf("Simulator build: %+v", res)
	}
	if res := s.App(ctx, AppRequest{Action: "install", Paths: []string{"Other.app"}}); res.Error != ErrScopeDenied || res.Rule != policy.RuleTaskApps {
		t.Fatalf("an app outside the task's list: %+v", res)
	}
	done := make(chan *Result, 1)
	go func() { done <- s.App(ctx, AppRequest{Action: "install", Paths: []string{"Tool.app"}}) }()
	n := waitNeedKind(t, g, NeedApproval)
	if n.Title != "Install “Tool” 2.1 (7) on iPhone 17?" || !strings.Contains(n.Detail, "signed by Demo Team (DEMO123456)") {
		t.Fatalf("approval %+v", n)
	}
	if err := g.Decide(n.ID, AllowOnce, ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, <-done, StatusSettled)
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Tool"}), StatusSettled)

	// phone_files lists and takes uploads for an iPhone's folder on the
	// computer; it has no Pinky folder to copy to or from.
	res := s.Files(ctx, FilesRequest{})
	mustStatus(t, res, StatusSettled)
	if fileWhere(res)["Tool.app/Info.plist"] != FileComputer {
		t.Fatalf("list %+v", res.Files)
	}
	if res := s.Files(ctx, FilesRequest{Action: "push"}); res.Error != ErrUnsupported {
		t.Fatalf("push to an iPhone: %+v", res)
	}
}
