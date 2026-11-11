// These tests stop daemons with SIGTERM, which Windows lacks.

//go:build !windows

package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/update"
)

// startFakeDaemon runs `pinky daemon --fake` until stopped or the test
// ends.
func startFakeDaemon(t *testing.T, bin, home string) (stop func()) {
	t.Helper()
	cmd := exec.Command(bin, "daemon", "--fake", "--no-update-check")
	cmd.Env = env(home)
	var logs bytes.Buffer
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			<-exited
		}
		if t.Failed() {
			t.Logf("daemon log:\n%s", logs.String())
		}
	}
	t.Cleanup(stop)
	waitSocket(t, dirsFor(t, home))
	return stop
}

// TestShimReconnectsAfterAnUpdate: when the gateway goes away because Pinky
// restarts for an update, `pinky mcp` waits for the new one and the agent's
// session goes on.
func TestShimReconnectsAfterAnUpdate(t *testing.T) {
	bin := buildPinky(t)
	home := pinkyHome(t)
	d := dirsFor(t, home)
	stop := startFakeDaemon(t, bin, home)

	agent := startMCP(t, bin, home, "claude-code")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := agent.cs.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	text, isErr := agent.call(t, "phone_connect", map[string]any{"device": "Pixel 9 (fake)", "task": "Before the update"})
	if isErr {
		t.Fatalf("connect: %s", text)
	}

	// The app marks its restart before it quits for an update.
	(&update.Updater{StatePath: filepath.Join(d.Config, "update.json")}).MarkRestart()
	stop()
	restarted := make(chan func())
	go func() {
		time.Sleep(2 * time.Second) // the new version takes a moment
		restarted <- startFakeDaemon(t, bin, home)
	}()
	tools, err := agent.cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("the session did not survive the restart: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools after the restart")
	}
	stop2 := <-restarted
	text, isErr = agent.call(t, "phone_connect", map[string]any{"device": "Pixel 9 (fake)", "task": "After the update"})
	if isErr || !strings.Contains(text, "Pixel 9 (fake)") {
		t.Fatalf("connect after the restart: %s", text)
	}
	if update.Restarting(d.Config) {
		t.Error("the new gateway left the restart marker")
	}
	stop2()
}

// TestShimWaitsForPinkyToOpenAgain: when the person quits Pinky, `pinky
// mcp` keeps the agent's session, answers its calls at once that Pinky is
// not running, and picks Pinky up again once it is opened (field test
// #20). It does not start Pinky itself.
func TestShimWaitsForPinkyToOpenAgain(t *testing.T) {
	bin := buildPinky(t)
	home := pinkyHome(t)
	d := dirsFor(t, home)
	stop := startFakeDaemon(t, bin, home)

	agent := startMCP(t, bin, home, "claude-code")
	text, isErr := agent.call(t, "phone_connect", map[string]any{"device": "Pixel 9 (fake)", "task": "Before quitting"})
	if isErr {
		t.Fatalf("connect: %s", text)
	}
	// A call waiting when Pinky quits gets an answer, and the session
	// stays.
	waiting := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, err := agent.cs.CallTool(ctx, &sdk.CallToolParams{Name: "phone_wait_for",
			Arguments: map[string]any{"text": "Never shown", "timeout_ms": 30000}})
		waiting <- err
	}()
	time.Sleep(500 * time.Millisecond)
	stop()
	select {
	case err := <-waiting:
		if err != nil {
			t.Fatalf("a call that waited when Pinky quit: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a call that waited when Pinky quit got no answer")
	}

	start := time.Now()
	text, isErr = agent.call(t, "phone_observe", map[string]any{})
	if !isErr || !strings.Contains(text, "Pinky is not running") || time.Since(start) > 5*time.Second {
		t.Fatalf("a call while Pinky is closed (%v): %v %s", time.Since(start), isErr, text)
	}
	if c, err := api.Dial(d); err == nil {
		c.Close()
		t.Fatal("the shim started Pinky again")
	}

	// The person opens Pinky: the session goes on.
	startFakeDaemon(t, bin, home)
	deadline := time.Now().Add(20 * time.Second)
	for {
		text, isErr = agent.call(t, "phone_connect", map[string]any{"device": "Pixel 9 (fake)", "task": "After opening Pinky again"})
		if !isErr {
			break
		}
		if !strings.Contains(text, "Pinky is not running") || time.Now().After(deadline) {
			t.Fatalf("connect after opening Pinky again: %s", text)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(text, "Pixel 9 (fake)") {
		t.Fatalf("connect after opening Pinky again: %s", text)
	}
	if tools, err := agent.cs.ListTools(context.Background(), nil); err != nil || len(tools.Tools) == 0 {
		t.Fatalf("tools after opening Pinky again: %v", err)
	}
}
