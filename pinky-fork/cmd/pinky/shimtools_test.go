package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/update"
)

// fakeToolsGateway is a gateway on the socket that speaks just enough MCP:
// its version and tools, and tool calls answered with their arguments.
type fakeToolsGateway struct {
	ln    *api.Listener
	conns chan net.Conn
}

func startFakeToolsGateway(t *testing.T, d api.Dirs, version string, connectProps ...string) *fakeToolsGateway {
	t.Helper()
	ln, err := api.Listen(d)
	if err != nil {
		t.Fatal(err)
	}
	props := map[string]any{}
	for _, p := range connectProps {
		props[p] = map[string]any{"type": "string"}
	}
	tools, _ := json.Marshal(map[string]any{"tools": []any{
		map[string]any{"name": "phone_connect", "inputSchema": map[string]any{"type": "object", "properties": props}},
		map[string]any{"name": "phone_observe", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
	}})
	g := &fakeToolsGateway{ln: ln, conns: make(chan net.Conn, 4)}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			g.conns <- c
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				if _, err := br.ReadBytes('\n'); err != nil {
					return
				}
				io.WriteString(c, `{"ok":true}`+"\n")
				for {
					line, err := br.ReadBytes('\n')
					if err != nil {
						return
					}
					var req struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
						Params json.RawMessage `json:"params"`
					}
					json.Unmarshal(line, &req)
					if len(req.ID) == 0 {
						continue
					}
					var result []byte
					switch req.Method {
					case "initialize":
						result, _ = json.Marshal(map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{"listChanged": true}},
							"serverInfo": map[string]any{"name": "pinky", "version": version}})
					case "tools/list":
						result = tools
					case "tools/call":
						result, _ = json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": "args " + string(req.Params)}}})
					default:
						result = []byte(`{}`)
					}
					c.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":` + string(result) + "}\n"))
				}
			}()
		}
	}()
	return g
}

// stop ends the gateway and its connections.
func (g *fakeToolsGateway) stop() {
	g.ln.Close()
	for {
		select {
		case c := <-g.conns:
			c.Close()
		default:
			return
		}
	}
}

// TestShimToolsAfterAnUpdate: when the shim reconnects to a newer Pinky
// whose tools differ, it tells the agent to list them again, and until the
// agent does, the results of calls of a changed tool say the definitions
// are out of date (field run of 2026-11-10: phone_connect's connection).
func TestShimToolsAfterAnUpdate(t *testing.T) {
	base := "/tmp"
	if runtime.GOOS == "windows" {
		base = ""
	}
	home, err := os.MkdirTemp(base, "pinky-shim-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv(api.HomeEnv, home)
	d, err := api.DefaultDirs()
	if err != nil {
		t.Fatal(err)
	}
	old := startFakeToolsGateway(t, d, "2026.1110.20", "task")
	conn, err := api.Dial(d)
	if err != nil {
		t.Fatal(err)
	}
	br, err := greetMCP(conn, "tok")
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := newShimSession(d, "tok", outW)
	done := make(chan error, 1)
	go func() { done <- s.run(conn, br, inR) }()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})

	lines := make(chan string, 16)
	go func() {
		r := bufio.NewReader(outR)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				close(lines)
				return
			}
			lines <- line
		}
	}()
	next := func() string {
		t.Helper()
		select {
		case l := <-lines:
			return l
		case <-time.After(10 * time.Second):
			t.Fatal("no message from the shim")
			return ""
		}
	}
	send := func(msg string) { io.WriteString(inW, msg+"\n") }
	call := func(id string, args string) string {
		t.Helper()
		send(`{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"phone_connect","arguments":` + args + `}}`)
		return next()
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	next()
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	next()
	if r := call("3", `{"task":"a"}`); strings.Contains(r, "Note from Pinky") {
		t.Fatalf("a note before any update: %s", r)
	}

	// Pinky restarts for an update; the new version's phone_connect takes
	// a connection.
	(&update.Updater{StatePath: filepath.Join(d.Config, "update.json")}).MarkRestart()
	old.stop()
	cur := startFakeToolsGateway(t, d, "2026.1110.27", "task", "connection")
	t.Cleanup(cur.stop)
	if l := next(); !strings.Contains(l, "notifications/tools/list_changed") {
		t.Fatalf("want list_changed, got %s", l)
	}

	// A client that ignores it, and drops the connection it does not know.
	r := call("4", `{"task":"b"}`)
	for _, want := range []string{"Note from Pinky", "2026.1110.27", "Pinky 2026.1110.20", "`connection`", "Restart Pinky's MCP server"} {
		if !strings.Contains(r, want) {
			t.Fatalf("note lacks %q: %s", want, r)
		}
	}
	var res struct {
		ID     int `json:"id"`
		Result struct {
			Content []struct{ Type, Text string } `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(r), &res); err != nil || res.ID != 4 || len(res.Result.Content) != 2 || !strings.HasPrefix(res.Result.Content[0].Text, "args") {
		t.Fatalf("result: %v %s", err, r)
	}
	// Once per tool, but always when a call passes what its definitions lack.
	if r := call("5", `{"task":"c"}`); strings.Contains(r, "Note from Pinky") {
		t.Fatalf("the note again: %s", r)
	}
	if r := call("6", `{"task":"c","connection":"helper"}`); !strings.Contains(r, "Pinky received") {
		t.Fatalf("no note on an argument the definitions lack: %s", r)
	}

	// The agent lists the tools again: no more notes.
	send(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`)
	if l := next(); !strings.Contains(l, "connection") {
		t.Fatalf("tools: %s", l)
	}
	if r := call("8", `{"task":"d","connection":"helper"}`); strings.Contains(r, "Note from Pinky") {
		t.Fatalf("a note after listing again: %s", r)
	}
}

// A reconnect to a gateway with the same tools says nothing.
func TestStaleNoteSameTools(t *testing.T) {
	tools, _ := parseTools(json.RawMessage(`{"tools":[{"name":"phone_connect","inputSchema":{"properties":{"task":{}}}}]}`))
	s := &shimSession{tools: shimTools{seen: tools, now: tools, version: "2"}}
	if n := s.staleNoteLocked("phone_connect", []string{"task", "connection"}); n != "" {
		t.Fatalf("note: %s", n)
	}
}
