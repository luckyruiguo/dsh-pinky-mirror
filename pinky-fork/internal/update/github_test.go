package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// gitHub fakes github.com's release links and the REST API for one release
// with latest.json and one package.
type gitHub struct {
	t       *testing.T
	links   *httptest.Server // github.com/<repo>/releases/download/...
	api     *httptest.Server // api.github.com
	files   *httptest.Server // release-assets.githubusercontent.com
	version string
	pkg     []byte

	mu       sync.Mutex
	linkCode int      // what the links answer instead of the file; 0 serves it
	apiCalls []string // the API's paths, in order
}

func newGitHub(t *testing.T, version string, pkg []byte) *gitHub {
	g := &gitHub{t: t, version: version, pkg: pkg}
	g.files = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			w.Write(g.feed())
		case "/Pinky.zip":
			g.mu.Lock()
			pkg := g.pkg
			g.mu.Unlock()
			w.Write(pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	g.links = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		code := g.linkCode
		g.mu.Unlock()
		if code != 0 {
			http.Error(w, http.StatusText(code), code)
			return
		}
		http.Redirect(w, r, g.files.URL+"/"+filepath.Base(r.URL.Path), http.StatusFound)
	}))
	g.api = httptest.NewServer(http.HandlerFunc(g.serveAPI))
	t.Cleanup(func() { g.links.Close(); g.api.Close(); g.files.Close() })
	return g
}

func (g *gitHub) feed() []byte {
	return fmt.Appendf(nil, `{"version": %q, "tag": "v%s", "notes": "- New", "assets": {%q: {"url": %q, "sha256": %q, "size": %d}}}`,
		g.version, g.version, Platform(), g.links.URL+"/DIYgod/pinky/releases/download/v"+g.version+"/Pinky.zip", sum(g.pkg), len(g.pkg))
}

func (g *gitHub) serveAPI(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.apiCalls = append(g.apiCalls, r.URL.Path)
	g.mu.Unlock()
	if !strings.HasPrefix(r.UserAgent(), "Pinky/") {
		g.t.Errorf("API request without Pinky's User-Agent: %q", r.UserAgent())
	}
	release := fmt.Sprintf(`{"tag_name": "v%s", "assets": [
		{"name": "latest.json", "url": "%s/repos/DIYgod/pinky/releases/assets/1"},
		{"name": "Pinky.zip", "url": "%s/repos/DIYgod/pinky/releases/assets/2"}]}`, g.version, g.api.URL, g.api.URL)
	switch r.URL.Path {
	case "/repos/DIYgod/pinky/releases/latest", "/repos/DIYgod/pinky/releases/tags/v" + g.version:
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			g.t.Errorf("release asked with Accept %q", r.Header.Get("Accept"))
		}
		io.WriteString(w, release)
	case "/repos/DIYgod/pinky/releases/assets/1", "/repos/DIYgod/pinky/releases/assets/2":
		if r.Header.Get("Accept") != "application/octet-stream" {
			// The API describes the asset in JSON instead of serving it.
			io.WriteString(w, `{"name": "not the file"}`)
			return
		}
		name := map[string]string{"1": "latest.json", "2": "Pinky.zip"}[filepath.Base(r.URL.Path)]
		http.Redirect(w, r, g.files.URL+"/"+name, http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

func (g *gitHub) failLinks(code int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.linkCode = code
}

// calls returns the API's paths asked since the last call.
func (g *gitHub) calls() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.apiCalls
	g.apiCalls = nil
	return c
}

func (g *gitHub) updater(t *testing.T) *Updater {
	dir := t.TempDir()
	return &Updater{
		Version:   "2026.1107.1",
		Feed:      g.links.URL + "/DIYgod/pinky/releases/latest/download/latest.json",
		API:       g.api.URL,
		Location:  Location{Method: MethodDownload},
		Cache:     filepath.Join(dir, "cache"),
		Downloads: filepath.Join(dir, "Downloads"),
	}
}

func (g *gitHub) prepare(t *testing.T, u *Updater, r *Release) {
	t.Helper()
	p, err := u.Prepare(context.Background(), *r, nil)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got, _ := os.ReadFile(p.File); string(got) != string(g.pkg) {
		t.Errorf("downloaded %q", got)
	}
}

func TestLinksWorkWithoutAPI(t *testing.T) {
	g := newGitHub(t, "2026.1107.2", []byte("a zip of Pinky.app"))
	u := g.updater(t)
	r, err := u.Check(context.Background())
	if err != nil || r == nil {
		t.Fatalf("Check = %v, %v", r, err)
	}
	g.prepare(t, u, r)
	if c := g.calls(); len(c) != 0 {
		t.Errorf("the API was asked while the links worked: %v", c)
	}
}

func TestFeedAndPackageFallBackToAPI(t *testing.T) {
	g := newGitHub(t, "2026.1107.2", []byte("a zip of Pinky.app"))
	g.failLinks(http.StatusGatewayTimeout)
	u := g.updater(t)
	r, err := u.Check(context.Background())
	if err != nil || r == nil {
		t.Fatalf("Check = %v, %v", r, err)
	}
	if r.Version != "2026.1107.2" {
		t.Errorf("release %+v", r)
	}
	want := []string{"/repos/DIYgod/pinky/releases/latest", "/repos/DIYgod/pinky/releases/assets/1"}
	if c := g.calls(); fmt.Sprint(c) != fmt.Sprint(want) {
		t.Errorf("the feed asked the API for %v, want %v", c, want)
	}
	// The feed came through the API, so the package needs one request.
	g.prepare(t, u, r)
	if c := g.calls(); fmt.Sprint(c) != "[/repos/DIYgod/pinky/releases/assets/2]" {
		t.Errorf("the package asked the API for %v", c)
	}
}

func TestPackageFallsBackToAPI(t *testing.T) {
	g := newGitHub(t, "2026.1107.2", []byte("a zip of Pinky.app"))
	u := g.updater(t)
	r, err := u.Check(context.Background())
	if err != nil || r == nil {
		t.Fatalf("Check = %v, %v", r, err)
	}
	g.failLinks(http.StatusBadGateway)
	g.prepare(t, u, r)
	want := []string{"/repos/DIYgod/pinky/releases/tags/v2026.1107.2", "/repos/DIYgod/pinky/releases/assets/2"}
	if c := g.calls(); fmt.Sprint(c) != fmt.Sprint(want) {
		t.Errorf("the package asked the API for %v, want %v", c, want)
	}
}

func TestFeedFallsBackWhenNoConnection(t *testing.T) {
	g := newGitHub(t, "2026.1107.2", []byte("a zip of Pinky.app"))
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	u := g.updater(t)
	u.Feed = dead.URL + "/latest.json"
	if r, err := u.Check(context.Background()); err != nil || r == nil {
		t.Fatalf("Check = %v, %v", r, err)
	}
	if c := g.calls(); len(c) != 2 {
		t.Errorf("the API was asked for %v", c)
	}
}

func TestNotFoundDoesNotFallBack(t *testing.T) {
	g := newGitHub(t, "2026.1107.2", []byte("a zip of Pinky.app"))
	u := g.updater(t)
	r, err := u.Check(context.Background())
	if err != nil || r == nil {
		t.Fatalf("Check = %v, %v", r, err)
	}
	g.failLinks(http.StatusNotFound)
	if _, err := u.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("Check of a missing feed = %v", err)
	}
	if _, err := u.Prepare(context.Background(), *r, nil); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("Prepare of a missing package = %v", err)
	}
	if c := g.calls(); len(c) != 0 {
		t.Errorf("a 404 asked the API for %v", c)
	}
}

func TestFallbackFailsToo(t *testing.T) {
	g := newGitHub(t, "2026.1107.2", []byte("a zip of Pinky.app"))
	g.failLinks(http.StatusGatewayTimeout)
	u := g.updater(t)
	u.API = g.links.URL // answers 504 as well
	_, err := u.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "504") || !strings.Contains(err.Error(), "through GitHub's API") {
		t.Errorf("Check = %v", err)
	}
}

func TestPackageFromAPIIsChecked(t *testing.T) {
	g := newGitHub(t, "2026.1107.2", []byte("a zip of Pinky.app"))
	u := g.updater(t)
	r, err := u.Check(context.Background())
	if err != nil || r == nil {
		t.Fatalf("Check = %v, %v", r, err)
	}
	g.failLinks(http.StatusServiceUnavailable)
	g.mu.Lock()
	g.pkg = []byte("a zip of Pinky.apq") // the same size, another checksum
	g.mu.Unlock()
	if _, err := u.Prepare(context.Background(), *r, nil); !errors.Is(err, ErrChecksum) {
		t.Errorf("a package that does not match latest.json: %v", err)
	}
	entries, _ := os.ReadDir(u.Downloads)
	if len(entries) != 0 {
		t.Errorf("the failed download left %d files", len(entries))
	}
}

func TestNoFallbackForCustomFeed(t *testing.T) {
	t.Setenv(FeedEnv, "")
	if a := (&Updater{}).api(); a != GitHubAPI {
		t.Errorf("default feed: API %q", a)
	}
	if a := (&Updater{Feed: "https://example.com/latest.json"}).api(); a != "" {
		t.Errorf("custom feed: API %q", a)
	}
	t.Setenv(FeedEnv, "http://127.0.0.1:9/latest.json")
	if a := (&Updater{}).api(); a != "" {
		t.Errorf("%s: API %q", FeedEnv, a)
	}

	g := newGitHub(t, "2026.1107.2", []byte("a zip of Pinky.app"))
	g.failLinks(http.StatusGatewayTimeout)
	u := g.updater(t)
	u.API = ""
	if _, err := u.Check(context.Background()); err == nil {
		t.Error("a failing custom feed passed")
	}
	if c := g.calls(); len(c) != 0 {
		t.Errorf("a custom feed asked the API for %v", c)
	}
}

func TestRetryable(t *testing.T) {
	ctx := context.Background()
	done, cancel := context.WithCancel(ctx)
	cancel()
	for _, c := range []struct {
		ctx  context.Context
		err  error
		want bool
	}{
		{ctx, &statusError{Code: 504}, true},
		{ctx, &statusError{Code: 500}, true},
		{ctx, &statusError{Code: 404}, false},
		{ctx, &statusError{Code: 403}, false},
		{ctx, fmt.Errorf("download: %w", ErrStalled), true},
		{ctx, context.DeadlineExceeded, true},
		{ctx, io.ErrUnexpectedEOF, true},
		{ctx, ErrChecksum, false},
		{ctx, errors.New(`refusing "http://example.com": updates need HTTPS`), false},
		{done, &statusError{Code: 504}, false},
		{ctx, nil, false},
	} {
		if got := retryable(c.ctx, c.err); got != c.want {
			t.Errorf("retryable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestPackageFallsBackAfterStall(t *testing.T) {
	defer func(d time.Duration) { StallTimeout = d }(StallTimeout)
	StallTimeout = 300 * time.Millisecond
	g := newGitHub(t, "2026.1107.2", []byte("a zip of Pinky.app"))
	u := g.updater(t)
	r, err := u.Check(context.Background())
	if err != nil || r == nil {
		t.Fatalf("Check = %v, %v", r, err)
	}
	hang := make(chan struct{})
	defer close(hang)
	stuck := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // no answer, as the edge that took 11 s to say 504
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	defer stuck.Close()
	r.Asset.URL = stuck.URL + "/DIYgod/pinky/releases/download/v2026.1107.2/Pinky.zip"
	g.prepare(t, u, r)
	if c := g.calls(); len(c) != 2 {
		t.Errorf("the API was asked for %v", c)
	}
}
