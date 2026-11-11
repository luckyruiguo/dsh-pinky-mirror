package api

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
)

// Protocol is the version in the hello line.
const Protocol = 1

// Roles a connection can take.
const (
	RoleMCP     = "mcp"
	RoleControl = "control"
)

// Hello is the first line a client sends.
type Hello struct {
	Pinky int    `json:"pinky"`
	Role  string `json:"role"`
	Token string `json:"token,omitempty"`
}

// HelloReply is the server's answer to Hello.
type HelloReply struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Version string `json:"version,omitempty"`
	Trusted bool   `json:"trusted,omitempty"`
}

// What can host a gateway (Server.Kind).
const (
	KindApp    = "app"
	KindDaemon = "daemon"
)

// RunningError means another gateway already runs for this user.
type RunningError struct{ PID string }

func (e *RunningError) Error() string {
	if e.PID != "" {
		return "a Pinky gateway is already running for this user (pid " + e.PID + ")"
	}
	return "a Pinky gateway is already running for this user"
}

// Listener is the gateway's socket, held together with its lock.
type Listener struct {
	net.Listener
	lock    *os.File
	cleanup func()
	once    sync.Once
}

// Listen takes the per-user lock and listens on the socket.
func Listen(d Dirs) (*Listener, error) {
	if err := d.Ensure(); err != nil {
		return nil, err
	}
	lk, err := lock(d.LockFile())
	if err != nil {
		return nil, err
	}
	// The lock is in the data folder, so holding it does not make a socket
	// here stale: a gateway on other data, or an older Pinky whose lock was
	// next to its socket, may answer on it. Leave that one alone.
	if c, err := dialSocket(d, time.Second); err == nil {
		c.Close()
		lk.Close()
		return nil, &RunningError{}
	}
	ln, cleanup, err := listenSocket(d)
	if err != nil {
		lk.Close()
		return nil, err
	}
	return &Listener{Listener: ln, lock: lk, cleanup: cleanup}, nil
}

// Close stops listening, removes the socket and releases the lock.
func (l *Listener) Close() error {
	var err error
	l.once.Do(func() {
		err = l.Listener.Close()
		l.cleanup()
		l.lock.Close()
	})
	return err
}

// Server answers connections on the socket.
type Server struct {
	Gateway *gateway.Gateway
	MCP     *mcp.Server
	// Token is the agent token MCP connections must present.
	Token string
	// Trusted lets control clients approve, hand back and unlock. Set it only
	// when every device is fake: a process on the socket cannot prove that a
	// person is there.
	Trusted bool
	Version string
	Log     *slog.Logger
	// Quit, when set, ends the process that hosts the gateway: `pinky quit`
	// calls it over the socket. Windows has no signal to end a daemon, and
	// scripts use it to end the app.
	Quit func()
	// Kind says what hosts the gateway, KindApp or KindDaemon; the version
	// method reports it, so that the app can take over from a daemon.
	Kind string
	// Show, when set, brings the app's window to the front: the show method
	// calls it when the person opens Pinky while it already runs.
	Show func()

	wg sync.WaitGroup
}

// Serve accepts connections until ctx ends, then waits for them to finish.
// It closes ln, and ln is closed by the time Serve returns: for a Listener
// the socket is gone and the lock is free, so a gateway can start again at
// once.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if s.Log == nil {
		s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		<-ctx.Done()
		ln.Close()
	}()
	var err error
	backoff := 5 * time.Millisecond
	for {
		c, aerr := ln.Accept()
		if aerr != nil {
			if ctx.Err() != nil || errors.Is(aerr, net.ErrClosed) {
				break
			}
			// Out of file descriptors and the like: wait and keep serving.
			s.Log.Warn("accept failed", "err", aerr)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
			}
			if backoff < time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = 5 * time.Millisecond
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(ctx, c)
		}()
	}
	cancel()
	// Accept fails as soon as the listener starts closing, before Close has
	// removed the socket and released the lock.
	<-closed
	s.wg.Wait()
	return err
}

func (s *Server) handle(ctx context.Context, c net.Conn) {
	defer c.Close()
	// When the gateway stops, close the connection so blocked reads end.
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if uc, ok := c.(*net.UnixConn); ok {
		uid, err := peerUID(uc)
		if err != nil || uid != os.Getuid() {
			s.Log.Warn("refused a connection from another user", "uid", uid, "err", err)
			return
		}
	}
	br := bufio.NewReaderSize(c, 64<<10)
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := readLine(br, 4096)
	if err != nil {
		return
	}
	c.SetReadDeadline(time.Time{})
	var h Hello
	if err := json.Unmarshal(line, &h); err != nil || h.Pinky != Protocol {
		writeJSON(c, HelloReply{Error: fmt.Sprintf("expected a pinky %d hello", Protocol)})
		return
	}
	switch h.Role {
	case RoleMCP:
		if s.Token == "" || subtle.ConstantTimeCompare([]byte(h.Token), []byte(s.Token)) != 1 {
			writeJSON(c, HelloReply{Error: "bad agent token"})
			return
		}
		if err := writeJSON(c, HelloReply{OK: true, Version: s.Version}); err != nil {
			return
		}
		t := &sdk.IOTransport{Reader: readCloser{br, c}, Writer: c}
		if err := s.MCP.Serve(ctx, t); err != nil && !errors.Is(err, io.EOF) {
			s.Log.Debug("mcp session ended", "err", err)
		}
	case RoleControl:
		if err := writeJSON(c, HelloReply{OK: true, Version: s.Version, Trusted: s.Trusted}); err != nil {
			return
		}
		s.control(ctx, c, br)
	default:
		writeJSON(c, HelloReply{Error: "unknown role " + h.Role})
	}
}

type readCloser struct {
	io.Reader
	c io.Closer
}

func (r readCloser) Close() error { return r.c.Close() }

// readLine reads one line of at most max bytes.
func readLine(br *bufio.Reader, max int) ([]byte, error) {
	var out []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		if len(out) > max {
			return nil, errors.New("line too long")
		}
		if !isPrefix {
			return out, nil
		}
	}
}

func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
