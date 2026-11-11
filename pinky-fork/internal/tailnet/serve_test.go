package tailnet

import (
	"context"
	"errors"
	"testing"
)

// serveJSON is the shape of `tailscale serve status --json` (1.104) after
// `tailscale serve --bg 7465`, with a folder on another port, and
// `tailscale serve --http 8080 3000` running in the foreground.
const serveJSON = `{
  "TCP": {"443": {"HTTPS": true}, "8443": {"HTTPS": true}},
  "Web": {
    "macbook.tail1234.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:7465"}}},
    "macbook.tail1234.ts.net:8443": {"Handlers": {"/files/": {"Path": "/Users/someone/Public"}, "/pinky": {"Proxy": "localhost:7466"}}}
  },
  "Foreground": {
    "abc": {
      "TCP": {"8080": {"HTTP": true}},
      "Web": {"macbook.tail1234.ts.net:8080": {"Handlers": {"/": {"Proxy": "3000"}}}}
    }
  }
}`

func TestParseServe(t *testing.T) {
	s, err := ParseServe([]byte(serveJSON))
	if err != nil {
		t.Fatal(err)
	}
	want := []ServeWeb{
		{URL: "http://macbook.tail1234.ts.net:8080", Proxy: "3000"},
		{URL: "https://macbook.tail1234.ts.net", Proxy: "http://127.0.0.1:7465"},
		{URL: "https://macbook.tail1234.ts.net:8443/files", Proxy: ""},
		{URL: "https://macbook.tail1234.ts.net:8443/pinky", Proxy: "localhost:7466"},
	}
	if len(s.Web) != len(want) {
		t.Fatalf("web = %+v", s.Web)
	}
	for i := range want {
		if s.Web[i] != want[i] {
			t.Fatalf("web[%d] = %+v, want %+v", i, s.Web[i], want[i])
		}
	}
	for addr, url := range map[string]string{
		"127.0.0.1:7465":   "https://macbook.tail1234.ts.net",
		"localhost:7465":   "https://macbook.tail1234.ts.net",
		"0.0.0.0:7465":     "https://macbook.tail1234.ts.net",
		":7465":            "https://macbook.tail1234.ts.net",
		"127.0.0.1:7466":   "https://macbook.tail1234.ts.net:8443/pinky",
		"127.0.0.1:3000":   "http://macbook.tail1234.ts.net:8080",
		"127.0.0.1:9999":   "",
		"192.168.1.5:7465": "", // it forwards to 127.0.0.1, where nothing listens
		"nonsense":         "",
	} {
		if got := s.Forwarding(addr); got != url {
			t.Errorf("Forwarding(%q) = %q, want %q", addr, got, url)
		}
	}

	// Nothing shared.
	if s, err := ParseServe([]byte(`{}`)); err != nil || len(s.Web) != 0 || s.Forwarding("127.0.0.1:7465") != "" {
		t.Fatalf("empty: %+v, %v", s, err)
	}
	if _, err := ParseServe([]byte(`not json`)); err == nil {
		t.Fatal("no error for nonsense")
	}
}

func TestReaderServe(t *testing.T) {
	var args []string
	r := &Reader{
		Find: func() (string, error) { return "/usr/local/bin/tailscale", nil },
		Run: func(_ context.Context, _ string, a ...string) ([]byte, error) {
			args = a
			return []byte(serveJSON), nil
		},
	}
	s, err := r.Serve(context.Background())
	if err != nil || s.Forwarding("127.0.0.1:7465") != "https://macbook.tail1234.ts.net" {
		t.Fatalf("serve = %+v, %v", s, err)
	}
	if len(args) != 3 || args[0] != "serve" || args[1] != "status" || args[2] != "--json" {
		t.Fatalf("ran %q", args)
	}
	r.Find = func() (string, error) { return "", ErrNotInstalled }
	if _, err := r.Serve(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("not installed: %v", err)
	}
}
