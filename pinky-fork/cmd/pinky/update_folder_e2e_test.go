//go:build e2e && (windows || linux)

package main

// What the end-to-end tests of updates of an install folder share, on
// Windows (the installer's) and Linux (the archive's): a local feed, an
// agent on `pinky mcp` across the update, the helper's work folders.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/update"
)

// testFeed serves latest.json for the version it offers, and its file for
// this platform: the installer on Windows, the archive on Linux.
type testFeed struct {
	srv  *httptest.Server
	mu   sync.Mutex
	body string
	file []byte
	name string
}

func newTestFeed(t *testing.T) *testFeed {
	f := &testFeed{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		body, file, name := f.body, f.file, f.name
		f.mu.Unlock()
		switch r.URL.Path {
		case "/latest.json":
			w.Write([]byte(body))
		case "/download/" + name:
			w.Write(file)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *testFeed) offer(t *testing.T, version, file string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	name := filepath.Base(file)
	body := fmt.Sprintf(`{"version": %q, "tag": "v%s", "date": %q, "notes": "- Updates itself", "assets": {%q: {"url": %q, "sha256": %q, "size": %d}}}`,
		version, version, time.Now().UTC().Format(time.RFC3339), update.Platform(), f.srv.URL+"/download/"+name, hex.EncodeToString(sum[:]), len(data))
	f.mu.Lock()
	f.body, f.file, f.name = body, data, name
	f.mu.Unlock()
}

// exeVersion is what `pinky version` says.
func exeVersion(t *testing.T, exe string, env []string) string {
	t.Helper()
	cmd := exec.Command(exe, "version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s version: %v", exe, err)
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "pinky"))
}

func workFolders(programs string) []string {
	m, _ := filepath.Glob(filepath.Join(programs, ".Pinky.update-*"))
	return m
}

// testAgent is an MCP client on `pinky mcp`, connected before the update.
type testAgent struct {
	cs     *sdk.ClientSession
	stderr *syncBuf
}

func connectAgent(t *testing.T, exe string, env []string) *testAgent {
	t.Helper()
	cmd := exec.Command(exe, "mcp")
	cmd.Env = env
	a := &testAgent{stderr: &syncBuf{}}
	cmd.Stderr = a.stderr
	cl := sdk.NewClient(&sdk.Implementation{Name: "claude-code", Version: "e2e"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cs, err := cl.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect to pinky mcp: %v\n%s", err, a.stderr)
	}
	t.Cleanup(func() { cs.Close() })
	a.cs = cs
	if _, err := cs.ListTools(ctx, nil); err != nil {
		t.Fatalf("tools before the update: %v", err)
	}
	return a
}

// check checks that the agent's session survived the update: the helper
// moved the old executable that this `pinky mcp` runs out of the install
// folder first (on Windows the installer stops what runs from there).
func (a *testAgent) check(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	tools, err := a.cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) == 0 {
		t.Errorf("the agent's session through `pinky mcp` did not survive the update: %v\n%s", err, a.stderr)
		return
	}
	t.Logf("the agent's session through `pinky mcp` survived the update: %d tools", len(tools.Tools))
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
