package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/secret"
	"github.com/DIYgod/pinky/ui"
)

func TestSettingsReachTheGateway(t *testing.T) {
	dir := t.TempDir()
	pol, err := policy.Load(filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	providers, _ := demo.World()
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	g, err := gateway.New(gateway.Options{Providers: providers, Policy: pol})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	rg := &runningGateway{Policy: pol, G: g}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	s := ui.DefaultSettings()
	gatewaySettings(&s, rg)
	if s.Preset != int(policy.PresetPayments) || !s.HideCodes || s.ReadOnly {
		t.Fatalf("defaults from the gateway: %+v", s)
	}
	for _, b := range s.BlockedApps {
		if b.Name == "Northbank" {
			t.Fatalf("the client's sample apps leaked into real settings: %+v", s.BlockedApps)
		}
	}

	next := s
	next.Preset, next.HideCodes, next.ReadOnly = int(policy.PresetCommits), false, true
	next.BlockedApps = append(append([]ui.BlockedApp(nil), s.BlockedApps...), ui.BlockedApp{Name: "Chatter"})
	applySettings(rg, s, next, log)
	if pol.Preset() != policy.PresetCommits || pol.HideCodes() || !pol.ReadOnly("") {
		t.Fatalf("after applying: preset %d, hide codes %v, read-only %v", pol.Preset(), pol.HideCodes(), pol.ReadOnly(""))
	}
	var again ui.Settings
	gatewaySettings(&again, rg)
	found := false
	for _, b := range again.BlockedApps {
		found = found || b.Name == "Chatter"
	}
	if !found {
		t.Fatalf("Chatter not blocked: %+v", again.BlockedApps)
	}

	// Unblocking it again.
	applySettings(rg, again, s, log)
	var third ui.Settings
	gatewaySettings(&third, rg)
	for _, b := range third.BlockedApps {
		if b.Name == "Chatter" {
			t.Fatalf("Chatter still blocked: %+v", third.BlockedApps)
		}
	}
}

// Unblocking a finance app in Settings › Safety unblocks it for agents. The
// bundled table names Google Wallet by package and by name; a row that kept
// only the name used to leave it blocked by its package (Wise on an iPhone).
func TestUnblockingABundledFinanceApp(t *testing.T) {
	dir := t.TempDir()
	pol, err := policy.Load(filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	providers, _ := demo.World()
	g, err := gateway.New(gateway.Options{Providers: providers, Policy: pol})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	rg := &runningGateway{Policy: pol, G: g}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	wallet := screen.App{ID: "com.google.android.apps.walletnfcrel", Name: "Google Wallet"}
	if _, blocked := pol.Blocked(wallet); !blocked {
		t.Fatal("Google Wallet is not blocked to begin with")
	}

	var s ui.Settings
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		gatewaySettings(&s, rg)
		if hasBlocked(s, "Google Wallet") || time.Now().After(deadline) {
			break
		}
	}
	if !hasBlocked(s, "Google Wallet") {
		t.Fatalf("Google Wallet is not listed: %+v", s.BlockedApps)
	}
	next := s
	next.BlockedApps = nil
	for _, b := range s.BlockedApps {
		if b.Name != "Google Wallet" {
			next.BlockedApps = append(next.BlockedApps, b)
		}
	}
	applySettings(rg, s, next, log)
	if rule, blocked := pol.Blocked(wallet); blocked {
		t.Fatalf("still blocked by %q after unblocking it in Settings", rule)
	}
	var again ui.Settings
	gatewaySettings(&again, rg)
	if hasBlocked(again, "Google Wallet") {
		t.Fatalf("listed again: %+v", again.BlockedApps)
	}
}

func hasBlocked(s ui.Settings, name string) bool {
	for _, b := range s.BlockedApps {
		if b.Name == name {
			return true
		}
	}
	return false
}

// TestSettingsFollowHandEdits reads what the gateway keeps of the
// settings again after hand edits of the rules files, only once the policy
// changed, and passes on a later change from Settings without undoing
// them.
func TestSettingsFollowHandEdits(t *testing.T) {
	dir := t.TempDir()
	rules, apps := filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml")
	pol, err := policy.Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	providers, _ := demo.World()
	g, err := gateway.New(gateway.Options{Providers: providers, Policy: pol})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	rg := &runningGateway{Policy: pol, G: g}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	blocked := func(s ui.Settings, name string) bool {
		for _, b := range s.BlockedApps {
			if b.Name == name {
				return b.Kind == policy.CategoryLabel(policy.CatBlocked)
			}
		}
		return false
	}

	s, v, ok := policySettings(rg, -1)
	if !ok || s.Preset != int(policy.PresetPayments) || !s.HideCodes || s.ReadOnly || blocked(s, "Chatter") {
		t.Fatalf("first read: %v %+v", ok, s)
	}
	if _, _, ok := policySettings(rg, v); ok {
		t.Fatal("read again with the policy as it was")
	}

	// By hand: every action asks, codes show, read-only for every phone,
	// Chatter blocked.
	data, err := os.ReadFile(rules)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.NewReplacer("\npreset: 2\n", "\npreset: 3\n", "\nmask_codes: true\n", "\nmask_codes: false\n",
		"\nread_only: false\n", "\nread_only: true\n").Replace(string(data))
	for path, text := range map[string]string{rules: edited, apps: "add:\n  blocked:\n    names: [Chatter]\n"} {
		if err := os.WriteFile(path+".edit", []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".edit", path); err != nil {
			t.Fatal(err)
		}
	}
	pol.Refresh() // as the gateway does every two seconds
	s2, v2, ok := policySettings(rg, v)
	if !ok || v2 == v || s2.Preset != 3 || s2.HideCodes || !s2.ReadOnly || !blocked(s2, "Chatter") {
		t.Fatalf("after the hand edits: %v, version %d to %d, %+v", ok, v, v2, s2)
	}

	// Hide codes turned back on in Settings changes that alone.
	next := s2
	next.BlockedApps = append([]ui.BlockedApp(nil), s2.BlockedApps...)
	next.HideCodes = true
	applySettings(rg, s2, next, log)
	if pol.Preset() != policy.PresetEvery || !pol.HideCodes() || !pol.ReadOnly("") {
		t.Fatalf("after Hide codes: preset %d, hide codes %v, read-only %v", pol.Preset(), pol.HideCodes(), pol.ReadOnly(""))
	}
	if _, ok := pol.Blocked(screen.App{Name: "Chatter"}); !ok {
		t.Fatal("Chatter is no longer blocked")
	}
	if data, _ := os.ReadFile(rules); !strings.Contains(string(data), "preset: 3") {
		t.Fatalf("rules.yaml:\n%s", data)
	}
}

func TestAppFromText(t *testing.T) {
	if a := appFromText(" com.dinely.app "); a.ID != "com.dinely.app" || a.Name != "" {
		t.Errorf("package: %+v", a)
	}
	if a := appFromText("Google Wallet"); a.Name != "Google Wallet" || a.ID != "" {
		t.Errorf("name: %+v", a)
	}
}

func TestRemoteAgentsAndRecordingSettings(t *testing.T) {
	dir := t.TempDir()
	providers, _ := demo.World()
	g, err := gateway.New(gateway.Options{Providers: providers, DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rg := &runningGateway{G: g, Policy: g.Policy(), Remote: newRemoteAgents(mcp.New(g, "test"), "tok", log)}
	defer rg.Remote.Close()

	s := ui.DefaultSettings()
	startSettings(rg, s, log)
	if g.Retention() != 30 || rg.Remote.URL() != "" {
		t.Fatalf("start: retention %d, remote %q", g.Retention(), rg.Remote.URL())
	}
	next := s
	next.KeepDays, next.RemoteAgents, next.RemoteAddr = 7, true, "127.0.0.1:0"
	applySettings(rg, s, next, log)
	if g.Retention() != 7 {
		t.Fatalf("retention %d", g.Retention())
	}
	url := rg.Remote.URL()
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, "/mcp") {
		t.Fatalf("remote url %q", url)
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	off := next
	off.RemoteAgents = false
	applySettings(rg, next, off, log)
	if rg.Remote.URL() != "" {
		t.Fatal("remote agents still on")
	}
	if _, err := http.Get(url); err == nil {
		t.Fatal("still listening")
	}
}

func TestPINNeedsTheSetting(t *testing.T) {
	ctx := context.Background()
	var reasons []string
	store := &secret.Memory{Confirm: func(reason string) error { reasons = append(reasons, reason); return nil }}
	p := pinSource{store: store}
	store.Save(secret.PINAccount("pixel"), "4711")
	if _, err := p.pin(ctx, "pixel"); err == nil || !strings.Contains(err.Error(), "off in Settings") {
		t.Fatalf("PIN with the setting off: %v", err)
	}
	p.on.Store(true)
	if pin, err := p.pin(ctx, "pixel"); err != nil || pin != "4711" {
		t.Fatalf("PIN %q, %v", pin, err)
	}
	if len(reasons) != 1 || reasons[0] != "unlock pixel" {
		t.Fatalf("confirmed for %q", reasons)
	}
	// Nothing saved: no prompt at all.
	if _, err := p.pin(ctx, "ipad"); !errors.Is(err, secret.ErrNotFound) || len(reasons) != 1 {
		t.Fatalf("no saved PIN: %v, %d prompts", err, len(reasons))
	}
	store.Confirm = func(string) error { return secret.ErrCancelled }
	if _, err := p.pin(ctx, "pixel"); !errors.Is(err, secret.ErrCancelled) {
		t.Fatalf("cancelled: %v", err)
	}
}
