package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

// An agent's MCP client reads Pinky's tools once, at the start of its
// session, and the shim keeps that session open across Pinky's updates
// (shimconn.go). The new version's tools may differ: phone_connect gained
// its connection argument in 2026.1110.21, and a session started before
// kept calling the old phone_connect, whose definition lacks it, so the
// connection the agent meant to pass never reached Pinky (field run of
// 2026-11-10). So, when it reconnects, the shim reads the new gateway's
// tools, and when they differ from those the agent listed:
//
//   - it tells the agent with notifications/tools/list_changed, after which
//     a client that follows MCP lists the tools again; and
//   - until the agent has listed them again, the result of a call of a tool
//     that changed, or that passes arguments the agent's definitions lack,
//     says that the tools come from an older Pinky and that the MCP server
//     must be restarted to use the current ones. Clients that drop
//     arguments their definitions lack never send them, so the shim cannot
//     tell what was meant: it says so once per tool rather than silently.

// shimTools is what the shim knows of the tool definitions on both sides.
// The shimSession's mu guards it.
type shimTools struct {
	version     string              // Pinky's version on the current connection
	seen        map[string]shimTool // the definitions the agent last listed, by name; nil before it listed
	seenVersion string              // the version that gave them
	now         map[string]shimTool // the current gateway's, read when the shim reconnected; nil before
	told        map[string]bool     // tools whose results said they are out of date
}

// shimTool is one tool's definition: its JSON, compacted, and its
// arguments.
type shimTool struct {
	raw   string
	props map[string]bool
}

// parseTools reads the tools of a tools/list result.
func parseTools(result json.RawMessage) (map[string]shimTool, bool) {
	var r struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(result, &r) != nil || r.Tools == nil {
		return nil, false
	}
	out := map[string]shimTool{}
	for _, raw := range r.Tools {
		var t struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"inputSchema"`
		}
		if json.Unmarshal(raw, &t) != nil || t.Name == "" {
			continue
		}
		var buf bytes.Buffer
		if json.Compact(&buf, raw) != nil {
			continue
		}
		props := map[string]bool{}
		for p := range t.InputSchema.Properties {
			props[p] = true
		}
		out[t.Name] = shimTool{raw: buf.String(), props: props}
	}
	return out, true
}

// sameTools reports whether two sets of definitions are the same.
func sameTools(a, b map[string]shimTool) bool {
	if len(a) != len(b) {
		return false
	}
	for name, t := range a {
		if u, ok := b[name]; !ok || u.raw != t.raw {
			return false
		}
	}
	return true
}

// response is the part of a JSON-RPC response the shim reads.
type response struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
}

// serverVersion reads serverInfo.version from an initialize result.
func serverVersion(result json.RawMessage) string {
	var r struct {
		ServerInfo struct {
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	json.Unmarshal(result, &r)
	return r.ServerInfo.Version
}

// callTarget reads the tool a tools/call request calls and the names of
// the arguments it passes.
func callTarget(line []byte) (tool string, args []string) {
	var r struct {
		Params struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &r) != nil {
		return "", nil
	}
	for a := range r.Params.Arguments {
		args = append(args, a)
	}
	sort.Strings(args)
	return r.Params.Name, args
}

// answered notes what a response to the agent's request says about the
// tools, and returns the line to send the agent: a tool call's result with
// a note when the agent's definitions of the tool are out of date.
func (s *shimSession) answered(p pendingRequest, line []byte) []byte {
	var r response
	if json.Unmarshal(line, &r) != nil || r.Result == nil {
		return line
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch p.method {
	case "initialize":
		s.tools.version = serverVersion(r.Result)
	case "tools/list":
		if tools, ok := parseTools(r.Result); ok {
			if p.cursor && s.tools.seen != nil {
				for name, t := range tools {
					s.tools.seen[name] = t
				}
			} else {
				s.tools.seen, s.tools.seenVersion, s.tools.told = tools, s.tools.version, nil
			}
		}
	case "tools/call":
		if note := s.staleNoteLocked(p.tool, p.args); note != "" {
			return withNote(line, note)
		}
	}
	return line
}

// staleNoteLocked returns what to tell the agent about a call of tool with
// args when its definitions come from an older Pinky: once per tool, and
// whenever the call passes arguments its definitions lack.
func (s *shimSession) staleNoteLocked(tool string, args []string) string {
	t := &s.tools
	if tool == "" || t.seen == nil || t.now == nil || sameTools(t.seen, t.now) {
		return ""
	}
	seen, cur := t.seen[tool], t.now[tool]
	var extra, missing []string
	for _, a := range args {
		if !seen.props[a] {
			extra = append(extra, a)
		}
	}
	for p := range cur.props {
		if !seen.props[p] {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)
	changed := seen.raw != cur.raw
	if len(extra) == 0 && (!changed || t.told[tool]) {
		return ""
	}
	if t.told == nil {
		t.told = map[string]bool{}
	}
	t.told[tool] = true
	return staleNote(tool, t.seenVersion, t.version, missing, extra, cur.props)
}

// staleNote words the note on a call whose tool definitions are out of
// date.
func staleNote(tool, old, cur string, missing, extra []string, known map[string]bool) string {
	var b strings.Builder
	from := "an older Pinky"
	if old != "" {
		from = "Pinky " + old
	}
	to := "a newer version"
	if cur != "" {
		to = cur
	}
	fmt.Fprintf(&b, "Note from Pinky: Pinky was updated to %s while this MCP connection stayed open, but your tool definitions come from %s and are out of date.", to, from)
	if len(missing) > 0 {
		fmt.Fprintf(&b, " %s now takes %s, which your definitions lack: your client may have dropped such arguments before they reached Pinky.", tool, quoteList(missing))
	}
	var used, unknown []string
	for _, a := range extra {
		if known[a] {
			used = append(used, a)
		} else {
			unknown = append(unknown, a)
		}
	}
	if len(used) > 0 {
		fmt.Fprintf(&b, " This call passed %s, which Pinky received although your definitions lack it.", quoteList(used))
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&b, " This call passed %s, which %s does not know.", quoteList(unknown), tool)
	}
	b.WriteString(" Restart Pinky's MCP server (reconnect it in your agent, or start a new session) to use the current tools; until then, ask the person to do it if a call needs them.")
	return b.String()
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "`" + n + "`"
	}
	return strings.Join(q, ", ")
}

// withNote adds a text block to a tool call's result.
func withNote(line []byte, note string) []byte {
	var msg map[string]json.RawMessage
	if json.Unmarshal(line, &msg) != nil {
		return line
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(msg["result"], &result) != nil {
		return line
	}
	var content []json.RawMessage
	if c, ok := result["content"]; ok && json.Unmarshal(c, &content) != nil {
		return line
	}
	block, _ := json.Marshal(map[string]string{"type": "text", "text": note})
	content = append(content, block)
	var err error
	if result["content"], err = json.Marshal(content); err != nil {
		return line
	}
	if msg["result"], err = json.Marshal(result); err != nil {
		return line
	}
	out, err := json.Marshal(msg)
	if err != nil {
		return line
	}
	return append(out, '\n')
}

// toolsListChanged is the notification that tells the agent to list the
// tools again.
var toolsListChanged = []byte(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}` + "\n")

// refreshTools reads the tools of the gateway the shim just reconnected to
// and reports whether the agent should list them again: it listed tools
// that differ from these.
func (s *shimSession) refreshTools(conn net.Conn, br *bufio.Reader) (changed bool) {
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("pinky-tools-%d", s.seq)
	s.mu.Unlock()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":"` + id + `","method":"tools/list"}` + "\n")); err != nil {
		return false
	}
	var tools map[string]shimTool
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			return false
		}
		var r response
		if json.Unmarshal(line, &r) != nil || string(r.ID) != `"`+id+`"` {
			continue
		}
		var ok bool
		if tools, ok = parseTools(r.Result); !ok {
			return false
		}
		break
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools.now = tools
	return s.tools.seen != nil && !sameTools(s.tools.seen, tools)
}

// hasCursor reports whether a tools/list request asks for a later page.
func hasCursor(line []byte) bool {
	var r struct {
		Params struct {
			Cursor string `json:"cursor"`
		} `json:"params"`
	}
	return json.Unmarshal(line, &r) == nil && r.Params.Cursor != ""
}
