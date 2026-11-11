package ui

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/ui/fakeapi"
)

// remoteTailnetApp is Settings › Agents with remote agents on and a fake
// Tailscale that says what tn holds.
func remoteTailnetApp(t *testing.T) (*testApp, *mg.Tester, func(RemoteTailnet)) {
	t.Helper()
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.opt.AgentToken = "tok-5ecret"
	var mu sync.Mutex
	var tn RemoteTailnet
	var addrs []string
	ta.opt.RemoteTailnet = func(_ context.Context, addr string) RemoteTailnet {
		mu.Lock()
		defer mu.Unlock()
		addrs = append(addrs, addr)
		return tn
	}
	ta.settings.RemoteAgents = true
	ta.sync()
	ta.page, ta.set.sec = pageSettings, secAgents
	tt := mg.NewTester(ta.view, 1280, 1000)
	set := func(v RemoteTailnet) {
		mu.Lock()
		tn = v
		mu.Unlock()
		ta.remote.tnAt = ta.remote.tnAt.AddDate(-1, 0, 0) // read again at the next frame
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, a := range addrs {
			if a != DefaultRemoteAddr {
				t.Errorf("Tailscale read for %q, want %q", a, DefaultRemoteAddr)
			}
		}
	})
	return ta, tt, set
}

// Remote agents shows nothing about the tailnet without Tailscale; the
// command that serves Pinky on the tailnet while Tailscale is connected
// but does not serve it; and the tailnet URL once it does.
func TestRemoteAgentsTailnet(t *testing.T) {
	ta, tt, set := remoteTailnetApp(t)
	tt.Frame()
	ta.waitFor(t, "Tailscale read", func() bool { tt.Frame(); return !ta.remote.tnAt.IsZero() })
	if hasText(tt, "Agents on your tailnet") || hasText(tt, "Tailnet URL") {
		t.Fatalf("the tailnet shows without Tailscale: %q", tt.Texts())
	}

	set(RemoteTailnet{Name: "macbook.tail1234.ts.net"})
	ta.waitFor(t, "the serve command", func() bool { tt.Frame(); return hasText(tt, "Agents on your tailnet") })
	if !hasText(tt, "Command to serve Pinky on your tailnet") || !hasText(tt, "https://macbook.tail1234.ts.net/mcp") || hasText(tt, "Tailnet URL") {
		t.Errorf("the serve command: %q", tt.Texts())
	}

	set(RemoteTailnet{Name: "macbook.tail1234.ts.net", URL: "https://macbook.tail1234.ts.net/mcp"})
	ta.waitFor(t, "the tailnet URL", func() bool { tt.Frame(); return hasText(tt, "Tailnet URL") })
	if hasText(tt, "Agents on your tailnet") || hasText(tt, "Command to serve Pinky on your tailnet") {
		t.Errorf("the serve command stays once Tailscale serves Pinky: %q", tt.Texts())
	}
	if err := tt.Click("Copy the tailnet url"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); got != "https://macbook.tail1234.ts.net/mcp" {
		t.Errorf("copied %q", got)
	}
}

func TestServeCommand(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1:7465":   "tailscale serve --bg 7465",
		"localhost:7466":   "tailscale serve --bg 7466",
		"[::1]:7465":       "tailscale serve --bg 7465",
		"0.0.0.0:7465":     "tailscale serve --bg 7465",
		":7465":            "tailscale serve --bg 7465",
		"192.168.1.5:7465": "tailscale serve --bg http://192.168.1.5:7465",
		"100.101.2.3:7465": "tailscale serve --bg http://100.101.2.3:7465",
		"not an address":   "",
	} {
		if got := serveCommand(addr); got != want {
			t.Errorf("%s: %q, want %q", addr, got, want)
		}
	}
}

// TestRemoteAgentsTailnetScreenshots draws Remote agents with the serve
// command and with the tailnet URL, in light and dark, in English and
// Chinese, into $PINKY_SCREENSHOTS, for the notes.
func TestRemoteAgentsTailnetScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS to a folder to write the screenshots")
	}
	ta, tt, set := remoteTailnetApp(t)
	shoot := func(name string) {
		t.Helper()
		for _, dark := range []bool{false, true} {
			st := mg.NewTester(ta.view, 1280, 1000)
			st.SetScale(2)
			st.SetDark(dark)
			st.Frame()
			st.Frame()
			file := name + "-light.png"
			if dark {
				file = name + "-dark.png"
			}
			f, err := os.Create(filepath.Join(dir, file))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, st.Image()); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
	for _, lang := range []string{"en", "zh-Hans"} {
		inLanguage(t, lang, tt)
		set(RemoteTailnet{Name: "macbook.tail1234.ts.net"})
		ta.waitFor(t, "the serve command", func() bool { tt.Frame(); return ta.remote.tn.Name != "" && ta.remote.tn.URL == "" })
		shoot("remote-tailnet-serve-" + lang)
		set(RemoteTailnet{Name: "macbook.tail1234.ts.net", URL: "https://macbook.tail1234.ts.net/mcp"})
		ta.waitFor(t, "the tailnet URL", func() bool { tt.Frame(); return ta.remote.tn.URL != "" })
		shoot("remote-tailnet-url-" + lang)
	}
}
