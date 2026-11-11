// Command mcpagent drives Pinky as an agent would, one call at a time: it
// keeps a single MCP session to `pinky mcp` (one lease, one task, refs that
// carry over) and serves tool calls on a local HTTP port, so a person or a
// model can decide each step from the last result. scripts/mcpcall runs a
// fixed list of calls instead. Used for the field test in
// docs/notes/field-test-iphone.md.
//
//	go run ./scripts/mcpagent serve -port 7701 -name claude-field-test -dir /tmp/pinky-field
//	go run ./scripts/mcpagent call -port 7701 phone_connect '{"task":"Check the weather"}'
//	go run ./scripts/mcpagent call -port 7701 -timeout 5s phone_tap '{"ref":3}'   # cancels after 5 s
//	go run ./scripts/mcpagent quit -port 7701
//
// serve writes every call, result, progress notification and screenshot to
// -dir. Results print as the agent reads them, with progress lines first.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: mcpagent serve|call|quit ...")
	}
	switch os.Args[1] {
	case "serve":
		serve(os.Args[2:])
	case "call":
		call(os.Args[2:])
	case "quit":
		fs := flag.NewFlagSet("quit", flag.ExitOnError)
		port := fs.Int("port", 7701, "")
		fs.Parse(os.Args[2:])
		resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/quit", *port), "text/plain", nil)
		if err != nil {
			fail("%v", err)
		}
		io.Copy(os.Stdout, resp.Body)
	default:
		fail("unknown command %q", os.Args[1])
	}
}

type callReq struct {
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args"`
	Timeout string         `json:"timeout"`
}

func serve(argv []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 7701, "local HTTP port")
	pinky := fs.String("pinky", "pinky", "the Pinky binary (/Applications/Pinky.app/Contents/MacOS/Pinky where pinky on the PATH is another program)")
	name := fs.String("name", "claude-field-test", "MCP client name")
	title := fs.String("title", "", "MCP client title (shown by Pinky when the name is unknown)")
	dir := fs.String("dir", ".", "where to write the call log and screenshots")
	fs.Parse(argv)

	if err := os.MkdirAll(*dir, 0o700); err != nil {
		fail("%v", err)
	}
	logf, err := os.OpenFile(filepath.Join(*dir, fmt.Sprintf("calls-%s.jsonl", *name)), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fail("%v", err)
	}
	var logMu sync.Mutex
	writeLog := func(v map[string]any) {
		v["t"] = time.Now().Format(time.RFC3339Nano)
		b, _ := json.Marshal(v)
		logMu.Lock()
		logf.Write(append(b, '\n'))
		logMu.Unlock()
	}

	var progressLines sync.Map // token -> *[]string
	opts := &sdk.ClientOptions{
		ProgressNotificationHandler: func(ctx context.Context, req *sdk.ProgressNotificationClientRequest) {
			p := req.Params
			line := fmt.Sprintf("%s progress %v/%v %s", time.Now().Format("15:04:05"), p.Progress, p.Total, p.Message)
			if v, ok := progressLines.Load(fmt.Sprint(p.ProgressToken)); ok {
				l := v.(*[]string)
				*l = append(*l, line)
			}
			writeLog(map[string]any{"progress": p})
		},
		LoggingMessageHandler: func(ctx context.Context, req *sdk.LoggingMessageRequest) {
			writeLog(map[string]any{"log": req.Params})
		},
	}
	cl := sdk.NewClient(&sdk.Implementation{Name: *name, Title: *title, Version: "1.0"}, opts)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	cmd := exec.Command(*pinky, "mcp")
	cmd.Stderr = os.Stderr
	cs, err := cl.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	cancel()
	if err != nil {
		fail("connect: %v", err)
	}
	init := cs.InitializeResult()
	writeLog(map[string]any{"initialized": init})
	if init != nil {
		fmt.Printf("connected to %s %s; instructions:\n%s\n", init.ServerInfo.Name, init.ServerInfo.Version, init.Instructions)
	}

	var seq atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/call", func(w http.ResponseWriter, r *http.Request) {
		var req callReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		n := seq.Add(1)
		tok := fmt.Sprintf("p%d", n)
		lines := &[]string{}
		progressLines.Store(tok, lines)
		defer progressLines.Delete(tok)
		cctx := context.Background()
		timeout := 120 * time.Second
		if req.Timeout != "" {
			if d, err := time.ParseDuration(req.Timeout); err == nil {
				timeout = d
			}
		}
		cctx, ccancel := context.WithTimeout(cctx, timeout)
		defer ccancel()
		params := &sdk.CallToolParams{Name: req.Tool, Arguments: req.Args}
		params.SetProgressToken(tok)
		writeLog(map[string]any{"n": n, "call": req.Tool, "args": req.Args})
		start := time.Now()
		res, err := cs.CallTool(cctx, params)
		took := time.Since(start)
		var b strings.Builder
		if err != nil {
			fmt.Fprintf(&b, "!! call error after %v: %v\n", took.Round(time.Millisecond), err)
			writeLog(map[string]any{"n": n, "error": err.Error(), "ms": took.Milliseconds()})
			w.Write([]byte(b.String()))
			return
		}
		var text strings.Builder
		var images []string
		for i, c := range res.Content {
			switch c := c.(type) {
			case *sdk.TextContent:
				text.WriteString(c.Text)
			case *sdk.ImageContent:
				ext := ".jpg"
				if c.MIMEType == "image/png" {
					ext = ".png"
				}
				p := filepath.Join(*dir, "shots", fmt.Sprintf("%s-%03d-%d%s", time.Now().Format("150405"), n, i, ext))
				os.MkdirAll(filepath.Dir(p), 0o700)
				data := c.Data
				if dec, err := base64.StdEncoding.DecodeString(string(c.Data)); err == nil && len(dec) > 0 && !bytes.HasPrefix(c.Data, []byte{0xff, 0xd8}) {
					data = dec
				}
				os.WriteFile(p, data, 0o600)
				images = append(images, p)
			}
		}
		fmt.Fprintf(&b, "=== #%d %s (%v, isError=%v)\n", n, req.Tool, took.Round(time.Millisecond), res.IsError)
		for _, l := range *lines {
			b.WriteString(l + "\n")
		}
		b.WriteString(strings.TrimRight(text.String(), "\n") + "\n")
		for _, p := range images {
			fmt.Fprintf(&b, "[image saved: %s]\n", p)
		}
		writeLog(map[string]any{"n": n, "ms": took.Milliseconds(), "isError": res.IsError, "text": text.String(), "images": images})
		w.Write([]byte(b.String()))
	})
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", *port), Handler: mux}
	mux.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("closing the MCP session\n"))
		go func() {
			cs.Close()
			writeLog(map[string]any{"closed": true})
			time.Sleep(200 * time.Millisecond)
			srv.Close()
		}()
	})
	fmt.Printf("serving on %s\n", srv.Addr)
	go func() {
		cs.Wait()
		writeLog(map[string]any{"session_ended": true})
		fmt.Println("MCP session ended")
		srv.Close()
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fail("%v", err)
	}
}

func call(argv []string) {
	fs := flag.NewFlagSet("call", flag.ExitOnError)
	port := fs.Int("port", 7701, "")
	timeout := fs.String("timeout", "", "cancel the call after this long (MCP cancellation)")
	fs.Parse(argv)
	rest := fs.Args()
	if len(rest) < 1 {
		fail("usage: mcpagent call [-port N] [-timeout D] tool [json-args]")
	}
	req := callReq{Tool: rest[0], Args: map[string]any{}, Timeout: *timeout}
	if len(rest) > 1 && strings.TrimSpace(rest[1]) != "" {
		if err := json.Unmarshal([]byte(rest[1]), &req.Args); err != nil {
			fail("args: %v", err)
		}
	}
	b, _ := json.Marshal(req)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/call", *port), "application/json", bytes.NewReader(b))
	if err != nil {
		fail("%v", err)
	}
	defer resp.Body.Close()
	io.Copy(os.Stdout, resp.Body)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "mcpagent: "+format+"\n", args...)
	os.Exit(1)
}
