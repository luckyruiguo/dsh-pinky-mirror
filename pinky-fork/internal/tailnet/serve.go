package tailnet

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Serve is what `tailscale serve` shares from this computer with the
// tailnet over HTTP or HTTPS.
type Serve struct {
	Web []ServeWeb // by URL
}

// ServeWeb is one name, port and path `tailscale serve` answers at.
type ServeWeb struct {
	// URL is where devices on the tailnet reach it, without a final slash:
	// "https://macbook.tail1234.ts.net", or with the path it is served at,
	// "https://macbook.tail1234.ts.net/pinky".
	URL string
	// Proxy is where it forwards the requests, as Tailscale keeps it:
	// "http://127.0.0.1:7465", "localhost:7465" or "7465"; "" when it
	// serves something else, such as a folder.
	Proxy string
}

// Forwarding returns the URL at which `tailscale serve` forwards requests
// to a plain HTTP server on this computer listening at addr (such as
// "127.0.0.1:7465"), "" when it does not. An HTTPS URL comes first.
func (s Serve) Forwarding(addr string) string {
	lhost, lport, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	found := ""
	for _, w := range s.Web {
		host, port, ok := proxyTarget(w.Proxy)
		if !ok || port != lport || !sameHost(host, lhost) {
			continue
		}
		if strings.HasPrefix(w.URL, "https://") {
			return w.URL
		}
		if found == "" {
			found = w.URL
		}
	}
	return found
}

// proxyTarget returns the host and port a serve handler's Proxy forwards
// to over plain HTTP: "7465" (Tailscale's shorthand for 127.0.0.1),
// "localhost:7465" or "http://127.0.0.1:7465", at the root path.
func proxyTarget(p string) (host, port string, ok bool) {
	if p == "" {
		return "", "", false
	}
	if n, err := strconv.Atoi(p); err == nil && n > 0 && n < 65536 {
		return "127.0.0.1", p, true
	}
	if !strings.Contains(p, "://") {
		p = "http://" + p
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "http" || u.Port() == "" || (u.Path != "" && u.Path != "/") {
		return "", "", false
	}
	return u.Hostname(), u.Port(), true
}

// sameHost reports whether a proxy forwarding to host reaches a server
// listening on lhost: the same host, or a loopback one for a server on
// loopback or on every address.
func sameHost(host, lhost string) bool {
	if strings.EqualFold(host, lhost) {
		return true
	}
	if !loopback(host) {
		return false
	}
	if lhost == "" || loopback(lhost) {
		return true
	}
	ip, err := netip.ParseAddr(lhost)
	return err == nil && ip.IsUnspecified()
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// ReadServe returns what `tailscale serve` shares from this computer, with
// Default's command line. It only reads; Pinky never changes it.
func ReadServe(ctx context.Context) (Serve, error) { return Default.Serve(ctx) }

// Serve returns what `tailscale serve` shares from this computer: nothing
// when it shares nothing, or Tailscale does not run.
func (r *Reader) Serve(ctx context.Context) (Serve, error) {
	find := r.Find
	if find == nil {
		find = findCommand
	}
	path, err := find()
	if err != nil {
		return Serve{}, err
	}
	run := r.Run
	if run == nil {
		run = runCommand
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := run(ctx, path, "serve", "status", "--json")
	if err != nil {
		return Serve{}, fmt.Errorf("tailscale serve status: %s", commandError(err, out))
	}
	return ParseServe(out)
}

// rawServe is the part of `tailscale serve status --json` (ipn.ServeConfig)
// Pinky reads. Foreground has what `tailscale serve` without --bg serves
// while it runs.
type rawServe struct {
	TCP map[string]*struct {
		HTTPS bool
		HTTP  bool
	}
	Web map[string]*struct {
		Handlers map[string]*struct{ Proxy string }
	}
	Foreground map[string]*rawServe
}

// ParseServe reads the output of `tailscale serve status --json`.
func ParseServe(b []byte) (Serve, error) {
	var raw rawServe
	if err := json.Unmarshal(b, &raw); err != nil {
		return Serve{}, err
	}
	var s Serve
	raw.add(&s)
	for _, fg := range raw.Foreground {
		if fg != nil {
			fg.add(&s)
		}
	}
	slices.SortFunc(s.Web, func(a, b ServeWeb) int { return strings.Compare(a.URL, b.URL) })
	return s, nil
}

func (raw *rawServe) add(s *Serve) {
	for hostport, web := range raw.Web {
		host, port, err := net.SplitHostPort(hostport)
		if err != nil || web == nil {
			continue
		}
		tcp := raw.TCP[port]
		if tcp == nil {
			continue
		}
		base := ""
		switch {
		case tcp.HTTPS && port == "443":
			base = "https://" + host
		case tcp.HTTPS:
			base = "https://" + net.JoinHostPort(host, port)
		case tcp.HTTP && port == "80":
			base = "http://" + host
		case tcp.HTTP:
			base = "http://" + net.JoinHostPort(host, port)
		default:
			continue
		}
		for mount, h := range web.Handlers {
			if h == nil || !strings.HasPrefix(mount, "/") {
				continue
			}
			s.Web = append(s.Web, ServeWeb{URL: base + strings.TrimSuffix(mount, "/"), Proxy: h.Proxy})
		}
	}
}
