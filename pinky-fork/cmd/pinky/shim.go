package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/DIYgod/pinky/internal/api"
)

// runMCP is the stdio shim agents launch. It forwards messages between
// stdio and the gateway's socket, where the MCP server runs, starts the
// gateway first when none is running, and keeps the agent's session when
// the gateway goes away: it waits for the app to restart for an update, or
// for the person to open it again after quitting it.
func runMCP(args []string) error {
	fs := flag.NewFlagSet("pinky mcp", flag.ContinueOnError)
	fake := fs.Bool("fake", false, "when no gateway runs, start one with fake phones")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dirs, err := api.DefaultDirs()
	if err != nil {
		return err
	}
	conn, err := api.Dial(dirs)
	if err != nil {
		if conn, err = launchGateway(dirs, *fake); err != nil {
			return err
		}
	}
	defer conn.Close()
	tok, err := api.EnsureToken(dirs.TokenFile())
	if err != nil {
		return err
	}
	br, err := greetMCP(conn, tok)
	if err != nil {
		return err
	}
	// The session survives the app restarting for an update, and being quit
	// and opened again (shimconn.go).
	return newShimSession(dirs, tok, os.Stdout).run(conn, br, os.Stdin)
}

// launchGateway starts the gateway and connects to it. Where the person
// has a desktop it starts the desktop app in the menu bar or tray, as
// Pinky's icon would: only the app can answer approvals for real phones,
// and the person opening Pinky later finds it already open. Without a
// desktop, with fake phones, or when the app cannot start, it starts
// `pinky daemon`, which the app takes over when the person opens it.
func launchGateway(dirs api.Dirs, fake bool) (net.Conn, error) {
	mode := daemonMode
	switch {
	case fake:
		mode = fakeMode
	case hasDesktop():
		mode = appMode
	}
	exited, err := startDaemon(dirs, mode)
	if err != nil {
		return nil, fmt.Errorf("start the gateway: %w", err)
	}
	conn, err := waitForGateway(dirs, 20*time.Second, exited)
	if errors.Is(err, errExited) && mode == appMode {
		// No display after all, or the app's libraries are missing.
		if exited, err = startDaemon(dirs, daemonMode); err != nil {
			return nil, fmt.Errorf("start the gateway: %w", err)
		}
		conn, err = waitForGateway(dirs, 20*time.Second, exited)
	}
	if err != nil {
		return nil, fmt.Errorf("the gateway did not start (see %s): %w", dirs.DaemonLog(), err)
	}
	return conn, nil
}

// hasDesktop reports whether this session can show the desktop app.
func hasDesktop() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	}
	return os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != ""
}

// startDaemon starts the gateway in the background, detached from this
// process, logging to the data folder. The returned channel is closed
// when the process exits.
func startDaemon(dirs api.Dirs, mode launchMode) (<-chan struct{}, error) {
	if err := dirs.Ensure(); err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(dirs.DaemonLog(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	args := []string{"daemon"}
	switch mode {
	case appMode:
		args = []string{"app", "--background"}
	case fakeMode:
		args = append(args, "--fake")
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Reap it should it exit while this shim runs, as when the app quits to
	// update itself: a zombie would look alive to the update's helper.
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	return exited, nil
}

// What the shim starts for the gateway.
type launchMode int

const (
	daemonMode launchMode = iota // `pinky daemon`
	fakeMode                     // `pinky daemon --fake`
	appMode                      // the desktop app in the menu bar or tray
)

// errExited means the process started for the gateway exited before the
// gateway came up.
var errExited = errors.New("it exited")

// waitForGateway connects to the gateway once it is up, until timeout or
// until the process started for it exits (errExited; nil waits the whole
// timeout).
func waitForGateway(dirs api.Dirs, timeout time.Duration, exited <-chan struct{}) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := api.Dial(dirs)
		if err == nil {
			return conn, nil
		}
		select {
		case <-exited:
			// It may have handed over to another gateway as it exited.
			if conn, err := api.Dial(dirs); err == nil {
				return conn, nil
			}
			return nil, errExited
		default:
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
