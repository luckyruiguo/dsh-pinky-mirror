package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/tailnet"
	"github.com/DIYgod/pinky/ui"
)

// remoteAgents serves MCP over Streamable HTTP while the person has remote
// agents on (Settings › Remote agents, or `pinky daemon --http`). It is off
// by default and listens on this computer only unless the person picks
// another address.
type remoteAgents struct {
	mcp   *mcp.Server
	token string
	log   *slog.Logger

	mu   sync.Mutex
	addr string
	ln   net.Listener
	srv  *http.Server
	h    *mcp.HTTP
}

func newRemoteAgents(m *mcp.Server, token string, log *slog.Logger) *remoteAgents {
	return &remoteAgents{mcp: m, token: token, log: log}
}

// Set starts, moves or stops the HTTP endpoint.
func (r *remoteAgents) Set(on bool, addr string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	addr = strings.TrimSpace(addr)
	if on && r.srv != nil && addr == r.addr {
		return nil
	}
	r.stopLocked()
	if !on {
		return nil
	}
	if addr == "" {
		return errors.New("remote agents: no address")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("remote agents: listen on %s: %w", addr, err)
	}
	h := r.mcp.HTTP(r.token)
	h.OwnNames = tailnetNames
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	r.addr, r.ln, r.srv, r.h = addr, ln, srv, h
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			r.log.Warn("remote agents stopped", "addr", addr, "err", err)
		}
	}()
	r.log.Info("remote agents on", "url", r.urlLocked())
	if !loopback(ln.Addr()) {
		r.log.Warn("remote agents are reachable from other computers; anyone with the agent token can use the phones", "addr", ln.Addr().String())
	}
	return nil
}

// URL is where agents connect, or "" while off.
func (r *remoteAgents) URL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.urlLocked()
}

func (r *remoteAgents) urlLocked() string {
	if r.ln == nil {
		return ""
	}
	return "http://" + r.ln.Addr().String() + mcp.HTTPPath
}

// Close stops the endpoint and ends its sessions.
func (r *remoteAgents) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopLocked()
}

func (r *remoteAgents) stopLocked() {
	if r.srv == nil {
		return
	}
	// MCP sessions outlive requests: end them first, with their leases,
	// which also ends their open event streams, so Shutdown need not wait.
	r.h.CloseSessions()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r.srv.Shutdown(ctx)
	r.log.Info("remote agents off", "addr", r.addr)
	r.addr, r.ln, r.srv, r.h = "", nil, nil, nil
}

// tailnetNames returns this computer's tailnet name, which `tailscale
// serve` forwards requests to Pinky under (mcp.HTTP.OwnNames).
func tailnetNames(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, _ := tailnet.Read(ctx)
	if st.Self.DNSName == "" {
		return nil
	}
	return []string{st.Self.DNSName}
}

// remoteTailnet is what Settings › Remote agents shows about reaching
// Pinky through the tailnet: this computer's tailnet name while Tailscale
// is connected, and the URL at which `tailscale serve` forwards to the
// endpoint at addr. It only reads Tailscale's state.
func remoteTailnet(ctx context.Context, addr string) ui.RemoteTailnet {
	st, _ := tailnet.Read(ctx)
	if !st.Running() || st.Self.DNSName == "" {
		return ui.RemoteTailnet{}
	}
	out := ui.RemoteTailnet{Name: st.Self.DNSName}
	if s, err := tailnet.ReadServe(ctx); err == nil {
		if u := s.Forwarding(addr); u != "" {
			out.URL = u + mcp.HTTPPath
		}
	}
	return out
}

func loopback(a net.Addr) bool {
	tcp, ok := a.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}
