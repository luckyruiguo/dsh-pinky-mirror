package api

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestEnsureTokenRace: the gateway and agents' `pinky mcp` create the
// token at the same time on a first run (a shim connects as soon as the
// socket listens, before the gateway has read or made the token). Each
// must get the same, complete token: a half-written file once gave the
// shim an empty one, which the gateway refused.
func TestEnsureTokenRace(t *testing.T) {
	for range 200 {
		path := filepath.Join(t.TempDir(), "config", "agent-token")
		const n = 8
		toks := make([]string, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				toks[i], errs[i] = EnsureToken(path)
			}()
		}
		close(start)
		wg.Wait()
		for i := range n {
			if errs[i] != nil {
				t.Fatal(errs[i])
			}
			if len(toks[i]) != 64 || toks[i] != toks[0] {
				t.Fatalf("racing callers got different tokens: %q", toks)
			}
		}
		b, err := os.ReadFile(path)
		if err != nil || string(b) != toks[0]+"\n" {
			t.Fatalf("token file %q (%v), want %q", b, err, toks[0])
		}
	}
}

// TestEnsureTokenEmptyFile: an empty token file, as a crash between
// creating and writing it left behind, is an error rather than an empty
// token every agent is refused with.
func TestEnsureTokenEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-token")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := EnsureToken(path); err == nil || tok != "" {
		t.Fatalf("empty token file: %q, %v", tok, err)
	}
}
