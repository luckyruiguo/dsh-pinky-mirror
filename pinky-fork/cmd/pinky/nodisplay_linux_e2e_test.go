//go:build e2e && linux

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/api"
)

// TestAppWithoutDisplay starts `pinky mcp` where DISPLAY names a display
// that is not there, as in an SSH or tmux session that kept the variable of
// a desktop session that ended. The shim starts the app, whose gateway
// answers it before the app finds out that it cannot show itself. The
// agent's session must go on all the same, on `pinky daemon`.
//
// It needs no desktop session and touches nothing outside its temporary
// folder:
//
//	go test -tags e2e -run TestAppWithoutDisplay -v ./cmd/pinky
func TestAppWithoutDisplay(t *testing.T) {
	tmp, err := os.MkdirTemp("/tmp", "pk") // Unix socket paths must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	exe := filepath.Join(tmp, "pinky")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	t.Setenv("PINKY_HOME", filepath.Join(tmp, "h"))
	t.Setenv("DISPLAY", ":987")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("GDK_BACKEND", "x11") // GTK finds a Wayland desktop without the variable
	dirs, err := api.DefaultDirs()
	if err != nil {
		t.Fatal(err)
	}
	kind := func() string {
		ctl, err := api.DialControl(dirs)
		if err != nil {
			return ""
		}
		defer ctl.Close()
		var v struct {
			Kind string `json:"kind"`
		}
		ctl.Call("version", nil, &v)
		return v.Kind
	}
	t.Cleanup(func() {
		ctl, err := api.DialControl(dirs)
		if err != nil {
			return
		}
		ctl.Call("quit", nil, nil)
		ctl.Close()
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
			c, err := api.Dial(dirs)
			if err != nil {
				return // gone, before its folder goes
			}
			c.Close()
		}
	})

	shim := exec.Command(exe, "mcp")
	stdin, _ := shim.StdinPipe()
	stdout, _ := shim.StdoutPipe()
	if err := shim.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); shim.Wait() })
	lines := make(chan map[string]json.RawMessage)
	go func() {
		defer close(lines)
		br := bufio.NewReader(stdout)
		for {
			line, err := br.ReadBytes('\n')
			var m map[string]json.RawMessage
			if json.Unmarshal(line, &m) == nil {
				lines <- m
			}
			if err != nil {
				return
			}
		}
	}()
	call := func(id int, method, params string) map[string]json.RawMessage {
		t.Helper()
		fmt.Fprintf(stdin, `{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`+"\n", id, method, params)
		for {
			select {
			case m, ok := <-lines:
				if !ok {
					t.Fatalf("%s: pinky mcp ended", method)
				}
				if string(m["id"]) == fmt.Sprint(id) {
					return m
				}
			case <-time.After(time.Minute):
				t.Fatalf("%s: no answer", method)
			}
		}
	}
	if m := call(1, "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}`); m["error"] != nil {
		t.Fatalf("initialize: %s", m["error"])
	}
	io.WriteString(stdin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")

	deadline := time.Now().Add(30 * time.Second)
	for kind() != api.KindDaemon {
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(dirs.DaemonLog())
			t.Fatalf("no daemon took the app's place (the gateway now: %q)\n%s", kind(), log)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if m := call(2, "tools/list", `{}`); m["error"] != nil {
		t.Fatalf("the agent's session did not go on: %s", m["error"])
	}
}
