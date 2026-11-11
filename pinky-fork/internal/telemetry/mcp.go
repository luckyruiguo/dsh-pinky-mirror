package telemetry

import (
	"context"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
)

// MCPMiddleware counts what agents do over MCP: the tools they call, by
// name, the status or error code of each result, and the agents that
// connect, by product. Only fixed names are counted: tool names Pinky
// serves, the statuses and error codes of DESIGN.md, the agent products
// internal/mcp knows. Arguments and results are never kept.
func (c *Client) MCPMiddleware() sdk.Middleware {
	var seen sessions
	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			res, err := next(ctx, method, req)
			if c == nil || method != "tools/call" {
				return res, err
			}
			name := ""
			switch p := req.GetParams().(type) {
			case *sdk.CallToolParamsRaw:
				name = p.Name
			case *sdk.CallToolParams:
				name = p.Name
			}
			// An agent is counted at its first call: clients of the newer
			// protocol send no initialize, only their name with each call.
			if r, ok := req.(*sdk.CallToolRequest); ok && seen.first(req.GetSession()) {
				agent := "Agent"
				if ci := r.ClientInfo(); ci != nil {
					agent = mcp.DisplayName(ci.Name, ci.Title)
				}
				c.Count(Key("agents", "connected", agentName(agent)))
			}
			c.Count("tools." + toolName(name))
			c.Count("results." + resultName(res, err))
			return res, err
		}
	}
}

// sessions remembers the latest MCP sessions seen.
type sessions struct {
	mu    sync.Mutex
	seen  map[sdk.Session]bool
	order []sdk.Session
}

// first reports whether s is new, remembering the last 256 sessions.
func (ss *sessions) first(s sdk.Session) bool {
	if s == nil {
		return false
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.seen[s] {
		return false
	}
	if ss.seen == nil {
		ss.seen = map[sdk.Session]bool{}
	}
	ss.seen[s] = true
	ss.order = append(ss.order, s)
	if len(ss.order) > 256 {
		delete(ss.seen, ss.order[0])
		ss.order = ss.order[1:]
	}
	return true
}

// toolName keeps Pinky's tool names, phone_ and a few words.
func toolName(name string) string {
	if !strings.HasPrefix(name, "phone_") || len(name) > 32 {
		return "other"
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r == '_') {
			return "other"
		}
	}
	return name
}

// knownStatuses are the first words of Pinky's tool results.
var knownStatuses = map[string]bool{}

func init() {
	for _, s := range []gateway.Status{gateway.StatusSettled, gateway.StatusUncertain, gateway.StatusBlocked,
		gateway.StatusDenied, gateway.StatusPending, gateway.StatusStale, gateway.StatusTookOver, gateway.StatusPaused,
		gateway.StatusStopped, gateway.StatusLocked, gateway.StatusNeedsSetup} {
		knownStatuses[string(s)] = true
	}
	for _, e := range []gateway.ErrorCode{gateway.ErrDeviceOffline, gateway.ErrDevicePaused, gateway.ErrLeaseHeld, gateway.ErrScopeDenied,
		gateway.ErrNotFound, gateway.ErrUnsupported, gateway.ErrInvalidArgument, gateway.ErrInternal} {
		knownStatuses["error."+string(e)] = true
	}
}

// resultName is a tool result's status ("settled") or error code
// ("error.device_offline"), from the first line internal/mcp writes; "ok"
// for results without one, such as a list of phones.
func resultName(res sdk.Result, err error) string {
	if err != nil {
		return "protocol_error"
	}
	r, ok := res.(*sdk.CallToolResult)
	if !ok || r == nil {
		return "other"
	}
	first := ""
	for _, ct := range r.Content {
		if t, ok := ct.(*sdk.TextContent); ok {
			first, _, _ = strings.Cut(t.Text, "\n")
			break
		}
	}
	word, _, _ := strings.Cut(first, " · ")
	word = strings.TrimSpace(word)
	if code, ok := strings.CutPrefix(word, "error: "); ok {
		word = "error." + strings.TrimSpace(code)
	}
	if knownStatuses[word] {
		return word
	}
	if r.IsError {
		return "error.other"
	}
	return "ok"
}
