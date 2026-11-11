package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
)

// The control protocol is JSON-RPC 2.0, one message per line.

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"` // notifications
	Params  any             `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

// Error codes.
const (
	CodeParse       = -32700
	CodeMethod      = -32601
	CodeParams      = -32602
	CodeFailed      = -32000
	CodeNotTrusted  = -32001
	notTrustedError = "this method needs the person: use the Pinky app, which approves in its own window. The socket cannot prove a person is there"
)

// NeedInfo is a NeedView without its screenshot, for the socket.
type NeedInfo struct {
	ID            string
	Kind          gateway.NeedKind
	DeviceID      string
	DeviceName    string
	App           string
	Agent         string
	TaskID        string
	Title         string
	Detail        string
	Rule          string
	Created       time.Time
	Deadline      time.Time
	HasScreenshot bool
	Target        screen.Rect
}

// Control method parameters.
type (
	// DeviceParams name a device.
	DeviceParams struct {
		Device string `json:"device"`
		Note   string `json:"note,omitempty"`
	}
	// DecideParams answer a need.
	DecideParams struct {
		Need     string           `json:"need"`
		Decision gateway.Decision `json:"decision"`
		Note     string           `json:"note,omitempty"`
	}
	// HistoryParams filter Activity.
	HistoryParams struct {
		Search string    `json:"search,omitempty"`
		Before time.Time `json:"before,omitempty"`
		Limit  int       `json:"limit,omitempty"`
	}
	// VirtualParams name a virtual phone: its platform and the id
	// virtual_phones gave it (a Simulator's UDID, an emulator's AVD name).
	VirtualParams struct {
		Platform driver.Platform `json:"platform"`
		ID       string          `json:"id"`
	}
)

// How long the virtual phone methods may take, as long as Add phone gives
// them: listing runs simctl and the emulator's tools, and starting one
// waits until it runs.
const (
	virtualListTimeout = 30 * time.Second
	virtualTimeout     = 2 * time.Minute
)

func (s *Server) control(ctx context.Context, c net.Conn, br *bufio.Reader) {
	var wmu sync.Mutex
	send := func(v rpcResponse) error {
		wmu.Lock()
		defer wmu.Unlock()
		v.JSONRPC = "2.0"
		return writeJSON(c, v)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for {
		line, err := readLine(br, 1<<20)
		if err != nil {
			return
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			send(rpcResponse{Error: &RPCError{Code: CodeParse, Message: err.Error()}})
			continue
		}
		result, rerr := s.dispatch(ctx, req, send)
		if len(req.ID) == 0 {
			continue
		}
		resp := rpcResponse{ID: req.ID, Result: result}
		if rerr != nil {
			var re *RPCError
			if !errors.As(rerr, &re) {
				re = &RPCError{Code: CodeFailed, Message: rerr.Error()}
			}
			resp.Result, resp.Error = nil, re
		} else if result == nil {
			resp.Result = struct{}{}
		}
		if err := send(resp); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest, send func(rpcResponse) error) (any, error) {
	g := s.Gateway
	params := func(v any) error {
		if len(req.Params) == 0 {
			return nil
		}
		if err := json.Unmarshal(req.Params, v); err != nil {
			return &RPCError{Code: CodeParams, Message: err.Error()}
		}
		return nil
	}
	trusted := func() error {
		if !s.Trusted {
			return &RPCError{Code: CodeNotTrusted, Message: notTrustedError}
		}
		return nil
	}
	switch req.Method {
	case "version":
		v := map[string]any{"version": s.Version, "protocol": Protocol, "trusted": s.Trusted}
		if s.Kind != "" {
			v["kind"] = s.Kind
		}
		return v, nil
	case "devices":
		return g.Devices(), nil
	case "tasks":
		return g.Tasks(), nil
	case "needs":
		var out []NeedInfo
		for _, n := range g.Needs() {
			out = append(out, NeedInfo{ID: n.ID, Kind: n.Kind, DeviceID: n.DeviceID, DeviceName: n.DeviceName, App: n.App,
				Agent: n.Agent, TaskID: n.TaskID, Title: n.Title, Detail: n.Detail, Rule: n.Rule, Created: n.Created,
				Deadline: n.Deadline, HasScreenshot: n.Screenshot != nil, Target: n.Target})
		}
		return out, nil
	case "history":
		var p HistoryParams
		if err := params(&p); err != nil {
			return nil, err
		}
		return g.History(ctx, gateway.HistoryQuery{Search: p.Search, Before: p.Before, Limit: p.Limit})
	case "subscribe":
		ch := g.Subscribe(ctx)
		go func() {
			for ev := range ch {
				if send(rpcResponse{Method: "event", Params: ev}) != nil {
					return
				}
			}
		}()
		return nil, nil
	case "stop", "take_over":
		var p DeviceParams
		if err := params(&p); err != nil {
			return nil, err
		}
		if req.Method == "stop" {
			return nil, g.Stop(p.Device)
		}
		return nil, g.TakeOver(p.Device)
	case "stop_all":
		return nil, g.StopAll()
	case "decide":
		if err := trusted(); err != nil {
			return nil, err
		}
		var p DecideParams
		if err := params(&p); err != nil {
			return nil, err
		}
		return nil, g.Decide(p.Need, p.Decision, p.Note)
	case "hand_back", "unlock":
		if err := trusted(); err != nil {
			return nil, err
		}
		var p DeviceParams
		if err := params(&p); err != nil {
			return nil, err
		}
		if req.Method == "unlock" {
			return nil, g.Unlock(p.Device)
		}
		return nil, g.HandBack(p.Device, p.Note)
	case "quit":
		if s.Quit == nil {
			return nil, &RPCError{Code: CodeMethod, Message: "this gateway cannot be quit from the socket"}
		}
		// Answer first: quitting closes this connection.
		go func() {
			time.Sleep(100 * time.Millisecond)
			s.Quit()
		}()
		return nil, nil
	case "show":
		if s.Show == nil {
			return nil, &RPCError{Code: CodeMethod, Message: "this gateway has no window to show"}
		}
		s.Show()
		return nil, nil
	case "wipe_history":
		if err := trusted(); err != nil {
			return nil, err
		}
		return nil, g.WipeHistory(ctx)
	// Virtual phones, as Add phone › Virtual phone lists, starts and stops
	// them. Any process of the person's can start a Simulator or an
	// emulator on this computer anyway, so these need no person.
	case "virtual_phones":
		ctx, cancel := context.WithTimeout(ctx, virtualListTimeout)
		defer cancel()
		return g.VirtualPhones(ctx)
	case "start_virtual", "stop_virtual":
		var p VirtualParams
		if err := params(&p); err != nil {
			return nil, err
		}
		if p.Platform == "" || p.ID == "" {
			return nil, &RPCError{Code: CodeParams, Message: "platform and id are required"}
		}
		ctx, cancel := context.WithTimeout(ctx, virtualTimeout)
		defer cancel()
		if req.Method == "start_virtual" {
			return nil, g.StartVirtual(ctx, p.Platform, p.ID)
		}
		return nil, g.StopVirtual(ctx, p.Platform, p.ID)
	}
	return nil, &RPCError{Code: CodeMethod, Message: "unknown method " + strconv.Quote(req.Method)}
}

// Dial connects to the gateway's socket.
func Dial(d Dirs) (net.Conn, error) {
	return dialSocket(d, 2*time.Second)
}

// Greet sends the hello line and reads the reply. It returns a reader that
// holds whatever the server sent after the reply.
func Greet(c net.Conn, h Hello) (*bufio.Reader, HelloReply, error) {
	h.Pinky = Protocol
	if err := writeJSON(c, h); err != nil {
		return nil, HelloReply{}, err
	}
	br := bufio.NewReaderSize(c, 64<<10)
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := readLine(br, 4096)
	c.SetReadDeadline(time.Time{})
	if err != nil {
		return nil, HelloReply{}, fmt.Errorf("gateway hello: %w", err)
	}
	var r HelloReply
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, r, fmt.Errorf("gateway hello: %w", err)
	}
	if !r.OK {
		return nil, r, errors.New("gateway refused: " + r.Error)
	}
	return br, r, nil
}

// Control is a control client.
type Control struct {
	c     net.Conn
	br    *bufio.Reader
	mu    sync.Mutex
	next  int
	Reply HelloReply
}

// DialControl connects as a control client.
func DialControl(d Dirs) (*Control, error) {
	c, err := Dial(d)
	if err != nil {
		return nil, err
	}
	br, reply, err := Greet(c, Hello{Role: RoleControl})
	if err != nil {
		c.Close()
		return nil, err
	}
	return &Control{c: c, br: br, Reply: reply}, nil
}

// Close closes the connection.
func (c *Control) Close() error { return c.c.Close() }

// Call runs one method and decodes its result into out, which may be nil.
// Notifications that arrive meanwhile are skipped.
func (c *Control) Call(method string, params, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := strconv.Itoa(c.next)
	req := map[string]any{"jsonrpc": "2.0", "id": c.next, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := writeJSON(c.c, req); err != nil {
		return err
	}
	for {
		line, err := readLine(c.br, 64<<20)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("the gateway closed the connection")
			}
			return err
		}
		var resp struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
		}
		if err := json.Unmarshal(line, &resp); err != nil {
			return err
		}
		if string(resp.ID) != id {
			continue
		}
		if resp.Error != nil {
			return resp.Error
		}
		if out != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, out)
		}
		return nil
	}
}
