package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/gateway"
)

// bearer adds the agent token to every request.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

func httpClient(t *testing.T, url, token, name string) *sdk.ClientSession {
	t.Helper()
	tr := &sdk.StreamableClientTransport{Endpoint: url + HTTPPath, MaxRetries: -1,
		HTTPClient: &http.Client{Transport: bearer{token, http.DefaultTransport}}}
	cl := sdk.NewClient(&sdk.Implementation{Name: name, Version: "1.0"}, nil)
	cs, err := cl.Connect(context.Background(), tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func callText(t *testing.T, cs *sdk.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*sdk.TextContent).Text
}

func waitIdle(t *testing.T, g *gateway.Gateway) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if g.Devices()[0].Status == gateway.DeviceIdle {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the phone is still %s", g.Devices()[0].Status)
}

func TestHTTP(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.ResumeGrace = 50 * time.Millisecond })
	web := h.srv.HTTP("secret-token")
	ts := httptest.NewServer(web)
	defer ts.Close()

	// Without the token, or with another, nothing is served.
	for _, auth := range []string{"", "Bearer nope", "Basic secret-token"} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+HTTPPath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Authorization %q: status %d", auth, resp.StatusCode)
		}
	}

	// With it, a session works like one over stdio, and ends with the
	// client: the phone is free again once no newer session of the agent
	// continued its task (ResumeGrace).
	cs := httpClient(t, ts.URL, "secret-token", "codex")
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 19 {
		t.Fatalf("%d tools, %v", len(tools.Tools), err)
	}
	text := callText(t, cs, "phone_connect", map[string]any{"task": "Lunch"})
	if !strings.HasPrefix(text, "settled") {
		t.Fatalf("connect:\n%s", text)
	}
	if d := h.g.Devices()[0]; d.Status != gateway.DeviceWorking || !strings.HasPrefix(d.StatusLine, "Codex (remote) · ") {
		t.Fatalf("device %+v", d)
	}
	cs.Close()
	waitIdle(t, h.g)

	// Turning remote agents off ends the sessions it carries.
	cs2 := httpClient(t, ts.URL, "secret-token", "codex")
	defer cs2.Close()
	if text := callText(t, cs2, "phone_connect", map[string]any{"task": "Lunch"}); !strings.HasPrefix(text, "settled") {
		t.Fatalf("connect:\n%s", text)
	}
	web.CloseSessions()
	waitIdle(t, h.g)
}

// The first connection of an agent asks the person over HTTP too.
func TestHTTPNewAgentAsks(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.TrustNewAgents = false; o.MaxBlock = 300 * time.Millisecond })
	ts := httptest.NewServer(h.srv.HTTP("tok"))
	defer ts.Close()
	cs := httpClient(t, ts.URL, "tok", "codex")
	defer cs.Close()
	text := callText(t, cs, "phone_connect", map[string]any{"task": "Lunch"})
	if !strings.HasPrefix(text, "approval_pending") || !strings.Contains(text, "rule: New agent") {
		t.Fatalf("first connection over HTTP:\n%s", text)
	}
}

// `tailscale serve` forwards requests to Pinky on 127.0.0.1 with the Host
// they had, this computer's tailnet name: served for that name only, with
// links at it.
func TestHTTPThroughTailscaleServe(t *testing.T) {
	h := newHarness(t, withData(t))
	web := h.srv.HTTP("tok")
	var names atomic.Value
	names.Store([]string(nil))
	web.OwnNames = func(context.Context) []string { return names.Load().([]string) }
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// What tailscale serve sends on, at a loopback address.
		host := r.Header.Get("X-Test-Host")
		r.Header.Del("X-Test-Host")
		r.Host = host
		r.Header.Set("X-Forwarded-Host", host)
		r.Header.Set("X-Forwarded-Proto", "https")
		web.ServeHTTP(w, r)
	}))
	defer ts.Close()
	initialize := func(host string) int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+HTTPPath, strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"grok","version":"1"}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("X-Test-Host", host)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// While Pinky does not know its tailnet name, it is a name like any
	// other: DNS rebinding.
	if code := initialize("mac.tail1234.ts.net"); code != http.StatusForbidden {
		t.Fatalf("an unknown name: %d, want 403", code)
	}
	names.Store([]string{"Mac.tail1234.ts.net."})
	for _, host := range []string{"mac.tail1234.ts.net", "mac.tail1234.ts.net:443", "MAC.TAIL1234.TS.NET.", "127.0.0.1:7465", "localhost"} {
		if code := initialize(host); code != http.StatusOK {
			t.Fatalf("Host %q: %d, want 200", host, code)
		}
	}
	for _, host := range []string{"evil.example", "mac.tail1234.ts.net.evil.example", "mac", "other.tail1234.ts.net", ""} {
		if code := initialize(host); code != http.StatusForbidden {
			t.Fatalf("Host %q: %d, want 403", host, code)
		}
	}

	// A whole task, as Grok Bot drives it, with links at the tailnet name.
	cl := &http.Client{Transport: hostHeader{"mac.tail1234.ts.net", http.DefaultTransport}}
	tr := &sdk.StreamableClientTransport{Endpoint: ts.URL + HTTPPath, MaxRetries: -1, HTTPClient: &http.Client{Transport: bearer{"tok", cl.Transport}}}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "grok", Version: "1.0"}, nil).Connect(context.Background(), tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if text := callText(t, cs, "phone_connect", map[string]any{"task": "Lunch"}); !strings.HasPrefix(text, "settled") {
		t.Fatalf("connect:\n%s", text)
	}
	link := downloadLinks(t, callText(t, cs, "phone_observe", map[string]any{"save": "home.png"}))["home.png"]
	if !strings.HasPrefix(link, "https://mac.tail1234.ts.net"+DownloadPath) {
		t.Fatalf("link %q, want one at the tailnet name", link)
	}
}

// hostHeader asks the test tunnel to forward requests under host.
type hostHeader struct {
	host string
	next http.RoundTripper
}

func (h hostHeader) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Test-Host", h.host)
	return h.next.RoundTrip(r)
}
