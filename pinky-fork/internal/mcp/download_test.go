package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolSaveSchema(t *testing.T) {
	h := newHarness(t)
	c := h.connect(t, "claude-code")
	tools, err := c.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"phone_observe": "save", "phone_release": "export"}
	for _, tl := range tools.Tools {
		p, ok := want[tl.Name]
		if !ok {
			continue
		}
		b, _ := json.Marshal(tl.InputSchema)
		if !strings.Contains(string(b), `"`+p+`"`) || !strings.Contains(string(b), "bug report or a pull request") {
			t.Errorf("%s lacks %s: %s", tl.Name, p, b)
		}
		delete(want, tl.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing tools %v", want)
	}
}

// An agent on this computer gets the saved files' paths.
func TestSaveLocal(t *testing.T) {
	h := newHarness(t, withData(t))
	c := h.connect(t, "claude-code")
	c.call(t, "phone_connect", map[string]any{"task": "Report a bug"})
	res, err := c.cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "phone_observe", Arguments: map[string]any{"save": "bug/home"}})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(h.g.FilesDir("pixel"), "bug", "home.png")
	text := res.Content[0].(*sdk.TextContent).Text
	if len(res.Content) != 1 || !strings.Contains(text, "Saved the screenshot as bug/home.png") || !strings.Contains(text, "\nsaved: "+p+" · ") {
		t.Fatalf("observe:\n%s", text)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	text, _ = c.call(t, "phone_release", map[string]any{"export": true})
	if strings.Count(text, "\nsaved: "+h.g.FilesDir("pixel")+string(filepath.Separator)+"pinky-task-") != 2 || strings.Contains(text, "curl") {
		t.Fatalf("release:\n%s", text)
	}
}

var downloadRe = regexp.MustCompile(`curl -fsS -o '([^']*)' '([^']*)'`)

func downloadLinks(t *testing.T, text string) map[string]string {
	t.Helper()
	links := map[string]string{}
	for _, m := range downloadRe.FindAllStringSubmatch(text, -1) {
		links[m[1]] = m[2]
	}
	if len(links) == 0 {
		t.Fatalf("no download link in:\n%s", text)
	}
	return links
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

// An agent on another computer gets one-time links it fetches without the
// agent token.
func TestDownloadLink(t *testing.T) {
	h := newHarness(t, withData(t))
	web := h.srv.HTTP("tok")
	ts := httptest.NewServer(web)
	defer ts.Close()
	cs := httpClient(t, ts.URL, "tok", "codex")
	defer cs.Close()
	callText(t, cs, "phone_connect", map[string]any{"task": "Report a bug"})
	dir := h.g.FilesDir("pixel")

	text := callText(t, cs, "phone_observe", map[string]any{"save": "bug/home"})
	links := downloadLinks(t, text)
	link := links["home.png"]
	if len(links) != 1 || !strings.HasPrefix(link, ts.URL+DownloadPath) || strings.Contains(text, "saved: ") || strings.Contains(text, dir) ||
		!strings.Contains(text, "Download it from your own shell (if this address") || !strings.Contains(text, "within 10 minutes; each link works once") {
		t.Fatalf("observe:\n%s", text)
	}

	// Not a GET: the link still works afterwards.
	resp, err := http.Post(link, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: %d", resp.StatusCode)
	}
	resp, body := get(t, link)
	want, _ := os.ReadFile(filepath.Join(dir, "bug", "home.png"))
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, want) || resp.Header.Get("Content-Type") != "image/png" ||
		resp.Header.Get("Content-Disposition") != `attachment; filename=home.png` {
		t.Fatalf("GET: %d %v, %d bytes", resp.StatusCode, resp.Header, len(body))
	}
	if _, err := png.Decode(bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	// Once only.
	if resp, body := get(t, link); resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "unknown, used or expired") {
		t.Fatalf("second GET: %d %s", resp.StatusCode, body)
	}

	// An expired link sends nothing.
	link = downloadLinks(t, callText(t, cs, "phone_observe", map[string]any{"save": "late.png"}))["late.png"]
	h.srv.mu.Lock()
	for _, d := range h.srv.downloads {
		d.expires = time.Now().Add(-time.Second)
	}
	h.srv.mu.Unlock()
	if resp, _ := get(t, link); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expired link: %d", resp.StatusCode)
	}

	// A file gone from the folder, or swapped for a link to another one,
	// is not sent.
	link = downloadLinks(t, callText(t, cs, "phone_observe", map[string]any{"save": "gone.png"}))["gone.png"]
	os.Remove(filepath.Join(dir, "gone.png"))
	if resp, body := get(t, link); resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "no longer on Pinky's computer") {
		t.Fatalf("gone: %d %s", resp.StatusCode, body)
	}
	link = downloadLinks(t, callText(t, cs, "phone_observe", map[string]any{"save": "swapped.png"}))["swapped.png"]
	secret := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(secret, []byte("secret"), 0o600)
	os.Remove(filepath.Join(dir, "swapped.png"))
	if err := os.Symlink(secret, filepath.Join(dir, "swapped.png")); err != nil {
		t.Fatal(err)
	}
	if resp, body := get(t, link); resp.StatusCode != http.StatusNotFound || strings.Contains(string(body), "secret") {
		t.Fatalf("swapped: %d %s", resp.StatusCode, body)
	}

	// A path that only starts like a link still needs the token.
	for _, p := range []string{"/download/../mcp", "/download/%2e%2e/mcp"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(`{}`))
		web.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK || rec.Code == http.StatusAccepted {
			t.Fatalf("%s: %d", p, rec.Code)
		}
	}

	// The task's report: a page that opens from the agent's disk, never
	// from Pinky's address.
	text = callText(t, cs, "phone_release", map[string]any{"export": true})
	links = downloadLinks(t, text)
	if len(links) != 2 || !strings.Contains(text, "Download them from your own shell") {
		t.Fatalf("release:\n%s", text)
	}
	for name, link := range links {
		resp, body := get(t, link)
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Fatalf("%s: %d", name, resp.StatusCode)
		}
		if strings.HasSuffix(name, ".html") && (resp.Header.Get("Content-Type") != "application/octet-stream" ||
			resp.Header.Get("Content-Security-Policy") != "sandbox" || !strings.Contains(string(body), "Report a bug")) {
			t.Fatalf("%s: %v", name, resp.Header)
		}
	}

	// Links outlive the session, so an agent that disconnects right after
	// phone_release still gets its files, but not turning remote agents
	// off.
	callText(t, cs, "phone_connect", map[string]any{"task": "Report another bug"})
	link = downloadLinks(t, callText(t, cs, "phone_observe", map[string]any{"save": "off.png"}))["off.png"]
	web.CloseSessions()
	if resp, _ := get(t, link); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after remote agents were turned off: %d", resp.StatusCode)
	}
}
