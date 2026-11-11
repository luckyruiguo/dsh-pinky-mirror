package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/update"
)

// shimSession forwards an agent's MCP session (newline-delimited JSON-RPC
// on stdio) to the gateway's socket. When the gateway goes away, it keeps
// the agent's side open, connects again once a gateway is back and replays
// the agent's initialize and initialized, so the agent's session goes on:
//
//   - when the app restarts for an update (update.Restarting), it waits for
//     the new version, and the agent's requests wait with it. Requests the
//     old gateway did not answer get an error to retry.
//   - when the person quits Pinky, it waits until they open it again, as
//     long as the agent keeps the session. Meanwhile every request gets an
//     answer at once that says Pinky is not running.
type shimSession struct {
	dirs  api.Dirs
	token string
	out   io.Writer
	// wait is how long to wait for the new version, and poll how often to
	// look for Pinky after it quit; tests shorten them.
	wait, poll time.Duration

	mu           sync.Mutex
	cond         *sync.Cond
	conn         net.Conn                  // nil while reconnecting
	closed       bool                      // gave up: writes fail
	away         bool                      // Pinky quit: requests are answered here until it is back
	init, inited []byte                    // the agent's initialize request and initialized notification
	pending      map[string]pendingRequest // by id
	replayID     string
	agentDone    bool
	seq          int
	tools        shimTools // the tool definitions on both sides (shimtools.go)
	outMu        sync.Mutex
}

func newShimSession(dirs api.Dirs, token string, out io.Writer) *shimSession {
	s := &shimSession{dirs: dirs, token: token, out: out, wait: 3 * time.Minute, poll: time.Second,
		pending: map[string]pendingRequest{}}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// greetMCP says hello on a new connection as an MCP client.
func greetMCP(conn net.Conn, token string) (*bufio.Reader, error) {
	br, _, err := api.Greet(conn, api.Hello{Role: api.RoleMCP, Token: token})
	return br, err
}

// run forwards until the agent closes stdin and the gateway ends the
// session.
func (s *shimSession) run(conn net.Conn, br *bufio.Reader, stdin io.Reader) error {
	s.setConn(conn)
	go s.fromAgent(stdin)
	for {
		err := s.toAgent(br)
		s.mu.Lock()
		done := s.agentDone
		s.conn = nil
		s.mu.Unlock()
		conn.Close()
		if done {
			s.giveUp()
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				return err
			}
			return nil
		}
		err = errQuit
		if update.Restarting(s.dirs.Config) {
			conn, br, err = s.reconnect()
		}
		if err != nil && !errors.Is(err, errAgentGone) {
			// Pinky quit, or did not come back from its update: wait for
			// the person to open it.
			conn, br, err = s.awaitReopen()
		}
		if err != nil {
			s.giveUp()
			return nil // the agent ended the session
		}
		// The agent may have closed its side while the shim reconnected,
		// when there was no connection to tell: tell this one.
		s.mu.Lock()
		gone := s.agentDone
		s.mu.Unlock()
		if gone {
			halfClose(conn)
		}
	}
}

// halfClose tells the gateway the agent is done sending.
func halfClose(c net.Conn) {
	// Unix sockets and Windows message-mode pipes can close one side.
	if hc, ok := c.(interface{ CloseWrite() error }); ok {
		hc.CloseWrite()
	} else if c != nil {
		c.Close()
	}
}

// errAgentGone ends a reconnect: the agent closed its side meanwhile.
var errAgentGone = errors.New("the agent ended the session")

// errQuit means the gateway went away without restarting for an update.
var errQuit = errors.New("Pinky quit")

func (s *shimSession) setConn(c net.Conn) {
	s.mu.Lock()
	s.conn, s.away = c, false
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *shimSession) giveUp() {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
}

// pendingRequest is a request sent to the gateway and not answered yet.
type pendingRequest struct {
	method string
	line   []byte
	tool   string   // tools/call: the tool
	args   []string // tools/call: the names of the arguments passed
	cursor bool     // tools/list: a later page
}

// jsonrpcHead is what the shim reads of a message.
type jsonrpcHead struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

func head(line []byte) (jsonrpcHead, bool) {
	var h jsonrpcHead
	if json.Unmarshal(line, &h) != nil {
		return h, false
	}
	return h, true
}

// fromAgent forwards the agent's messages, noting its initialize and the
// requests not yet answered, and waits while the gateway reconnects.
func (s *shimSession) fromAgent(stdin io.Reader) {
	r := bufio.NewReaderSize(stdin, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			request, method := "", ""
			if h, ok := head(line); ok {
				s.mu.Lock()
				switch {
				case h.Method == "initialize":
					s.init = append([]byte(nil), line...)
				case h.Method == "notifications/initialized":
					s.inited = append([]byte(nil), line...)
				}
				s.mu.Unlock()
				if len(h.ID) > 0 && h.Method != "" {
					request, method = string(h.ID), h.Method
				}
			}
			if !s.send(line, request, method) {
				return
			}
		}
		if err != nil {
			break
		}
	}
	// The agent closed stdin: let the gateway end the session.
	s.mu.Lock()
	s.agentDone = true
	c := s.conn
	s.mu.Unlock()
	if c != nil {
		halfClose(c)
	}
}

// send writes one line to the gateway, waiting out a reconnect for an
// update, and notes a request (its id) as unanswered on that connection;
// false once the shim gave up. Nothing is sent while there is no
// connection, so what is pending when the gateway goes away went to the old
// one. While Pinky is away after it quit, the shim answers requests itself
// and drops notifications.
func (s *shimSession) send(line []byte, request, method string) bool {
	s.mu.Lock()
	for s.conn == nil && !s.closed && !s.away {
		s.cond.Wait()
	}
	c, closed, away := s.conn, s.closed, s.away
	if !closed && !away && request != "" {
		p := pendingRequest{method: method, line: append([]byte(nil), line...)}
		switch method {
		case "tools/call":
			p.tool, p.args = callTarget(line)
		case "tools/list":
			p.cursor = hasCursor(line)
		}
		s.pending[request] = p
	}
	s.mu.Unlock()
	if closed {
		return false
	}
	if away {
		if request != "" {
			s.write(awayAnswer(request, method, notRunning))
		}
		return true
	}
	// A write that fails means the gateway went away; its request stays
	// pending and is answered with an error once the shim reconnects.
	c.Write(line)
	return true
}

// toAgent forwards the gateway's messages to stdout until its connection
// ends. A message cut off by the end is dropped: its request is still
// pending, and is sent again or answered once the shim reconnects.
func (s *shimSession) toAgent(br *bufio.Reader) error {
	for {
		line, err := br.ReadBytes('\n')
		if err != nil && len(line) > 0 && line[len(line)-1] != '\n' {
			line = nil
		}
		if len(line) > 0 {
			if h, ok := head(line); ok && len(h.ID) > 0 && h.Method == "" {
				s.mu.Lock()
				p, ok := s.pending[string(h.ID)]
				delete(s.pending, string(h.ID))
				s.mu.Unlock()
				if ok {
					line = s.answered(p, line)
				}
			}
			s.write(line)
		}
		if err != nil {
			return err
		}
	}
}

func (s *shimSession) write(line []byte) {
	s.outMu.Lock()
	s.out.Write(line)
	s.outMu.Unlock()
}

// reconnect waits for the new version's gateway, says hello and replays
// the agent's initialize, swallowing its answer.
func (s *shimSession) reconnect() (net.Conn, *bufio.Reader, error) {
	deadline := time.Now().Add(s.wait)
	for {
		s.mu.Lock()
		gone := s.agentDone
		s.mu.Unlock()
		if gone {
			return nil, nil, errAgentGone
		}
		if conn, br, ok := s.connectOnce(); ok {
			return conn, br, nil
		}
		if time.Now().After(deadline) {
			return nil, nil, fmt.Errorf("Pinky did not come back within %s of restarting for an update", s.wait)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// awaitReopen waits, as long as the agent keeps its session, for the
// person to open Pinky again after it quit. Meanwhile the shim answers the
// agent's requests itself (send), starting with those the gateway left
// unanswered. It does not start Pinky: quitting it is how the person stops
// every agent.
func (s *shimSession) awaitReopen() (net.Conn, *bufio.Reader, error) {
	s.mu.Lock()
	s.away = true
	left := s.pending
	s.pending = map[string]pendingRequest{}
	s.cond.Broadcast()
	s.mu.Unlock()
	for id, p := range left {
		msg := notRunning
		if p.method == "tools/call" {
			msg = quitDuring // it may have acted on the phone
		}
		s.write(awayAnswer(id, p.method, msg))
	}
	for {
		s.mu.Lock()
		gone := s.agentDone
		s.mu.Unlock()
		if gone {
			return nil, nil, errAgentGone
		}
		// The person may have made a new agent token meanwhile.
		if tok, err := api.EnsureToken(s.dirs.TokenFile()); err == nil {
			s.mu.Lock()
			s.token = tok
			s.mu.Unlock()
		}
		if conn, br, ok := s.connectOnce(); ok {
			return conn, br, nil
		}
		time.Sleep(s.poll)
	}
}

// connectOnce dials the gateway, says hello, replays the agent's
// initialize and deals with what the old gateway left unanswered.
func (s *shimSession) connectOnce() (net.Conn, *bufio.Reader, bool) {
	conn, err := api.Dial(s.dirs)
	if err != nil {
		return nil, nil, false
	}
	s.mu.Lock()
	tok := s.token
	s.mu.Unlock()
	br, err := greetMCP(conn, tok)
	if err == nil {
		var started bool
		if started, err = s.replay(conn, br); err == nil {
			// The new version's tools may differ from those the agent
			// listed (shimtools.go).
			changed := started && s.refreshTools(conn, br)
			s.settlePending(conn)
			s.setConn(conn)
			if changed {
				s.write(toolsListChanged)
			}
			return conn, br, true
		}
	}
	conn.Close()
	return nil, nil, false
}

// What the shim answers while Pinky is away.
const (
	notRunning = "Pinky is not running on this computer, so nothing was done. Ask the person to open Pinky; " +
		"this connection picks it up by itself. Then call phone_connect again."
	quitDuring = "Pinky quit during this call, which may or may not have acted on the phone. Ask the person to open " +
		"Pinky again; this connection picks it up by itself. Then call phone_connect and observe before anything else."
)

// awayAnswer is the shim's own answer to a request while Pinky is away: an
// empty result to a ping, a tool error the model reads to a tool call, and
// an error to anything else.
func awayAnswer(id, method, msg string) []byte {
	text, _ := json.Marshal(msg)
	switch method {
	case "ping":
		return []byte(`{"jsonrpc":"2.0","id":` + id + `,"result":{}}` + "\n")
	case "tools/call":
		return []byte(`{"jsonrpc":"2.0","id":` + id + `,"result":{"content":[{"type":"text","text":` + string(text) +
			`}],"isError":true}}` + "\n")
	}
	return []byte(`{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32603,"message":` + string(text) + `}}` + "\n")
}

// replay sends the agent's initialize and initialized on a new connection,
// if it had started its session (started), and notes the new gateway's
// version.
func (s *shimSession) replay(conn net.Conn, br *bufio.Reader) (started bool, err error) {
	s.mu.Lock()
	init, inited := s.init, s.inited
	s.seq++
	id := fmt.Sprintf("pinky-reconnect-%d", s.seq)
	s.mu.Unlock()
	if init == nil {
		return false, nil // the agent had not started its session
	}
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(init, &msg); err != nil {
		return false, err
	}
	msg["id"], _ = json.Marshal(id)
	data, err := json.Marshal(msg)
	if err != nil {
		return false, err
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer conn.SetDeadline(time.Time{})
	if _, err := conn.Write(append(data, '\n')); err != nil {
		return false, err
	}
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return false, err
		}
		var r response
		if json.Unmarshal(line, &r) == nil && string(r.ID) == `"`+id+`"` {
			s.mu.Lock()
			s.tools.version = serverVersion(r.Result)
			s.mu.Unlock()
			break
		}
	}
	if inited != nil {
		if _, err := conn.Write(inited); err != nil {
			return false, err
		}
	}
	return true, nil
}

// settlePending deals with what the old gateway left unanswered, most
// often a request the agent sent as it went away: requests that only read
// are sent again to the new one; a tool call may have acted on a phone, so
// the agent gets an error and decides whether to call it again.
func (s *shimSession) settlePending(conn net.Conn) {
	s.mu.Lock()
	var resend [][]byte
	var failed []string
	for id, p := range s.pending {
		if p.method == "tools/call" || p.method == "initialize" {
			failed = append(failed, id)
			delete(s.pending, id)
		} else {
			resend = append(resend, p.line)
		}
	}
	s.mu.Unlock()
	for _, id := range failed {
		s.write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32603,"message":"Pinky restarted to update itself; call the tool again"}}` + "\n"))
	}
	for _, line := range resend {
		conn.Write(line)
	}
}
