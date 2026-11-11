package adb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path"
	"strconv"
	"strings"
	"time"
)

// The servers Pinky started. Pinky never stops a server another program
// started, but it may stop one it started itself, in this run or an
// earlier one, when that server cannot do its work (StopOwnServer): on
// macOS a server has the Local Network access of the Pinky that started
// it, and loses it once that Pinky is gone, as when an update replaced it.
//
// Pinky tells its own servers by their command line. It runs the server
// process itself (spawnServer) as `<the adb's full path> -L tcp:<port>
// fork-server server --reply-fd 3`: the pipe is the first file it passes.
// adb's own `start-server`, which Android Studio and the person's own adb
// commands use, runs the same with "adb" for the program's name and the
// pipe at another number (`adb -L tcp:5037 fork-server server --reply-fd
// 4`). Every Pinky since its servers started that way (2026-11-08) is
// told so, also the one an update replaced.

// ErrNotOwnServer is StopOwnServer's error for a server that Pinky did not
// start, or that it cannot tell it started; it leaves that one running.
var ErrNotOwnServer = errors.New("the adb server that runs is not one Pinky started")

// OwnServer returns the process id of the server at the client's address
// when Pinky started it, in this run or an earlier one (above). It reports
// false for any other server, for one on another computer, when it cannot
// tell, and on Windows, where Pinky stops no server.
func (c *Client) OwnServer() (int, bool) {
	host, port, err := net.SplitHostPort(c.addr())
	if err != nil || !isLoopback(host) {
		return 0, false // a server on another computer is never Pinky's to stop
	}
	procs, err := serverProcesses(port)
	if err != nil || len(procs) != 1 {
		return 0, false // none, or more than one: one of them is about to quit
	}
	if !ownCommandLine(procs[0].command, port) {
		return 0, false
	}
	return procs[0].pid, true
}

// Local reports whether the client's server is on this computer (a
// loopback address), rather than on another one that
// ANDROID_ADB_SERVER_ADDRESS or ADB_SERVER_SOCKET names.
func (c *Client) Local() bool {
	host, _, err := net.SplitHostPort(c.addr())
	return err == nil && isLoopback(host)
}

// process is a running process and its command line.
type process struct {
	pid     int
	command string
}

// serverLine is what an adb server's command line has after its program
// for port.
func serverLine(port string) string { return " -L tcp:" + port + " fork-server server " }

// ownCommandLine reports whether a command line, as ps shows it on macOS
// and Linux, is that of a server Pinky started for port: an adb by its
// full path, the pipe at 3. Its paths are a Unix system's whatever this
// program was built for, hence package path.
func ownCommandLine(line, port string) bool {
	prog, rest, ok := strings.Cut(line, serverLine(port))
	if !ok || rest != "--reply-fd 3" {
		return false
	}
	return path.IsAbs(prog) && path.Base(prog) == "adb"
}

// StopOwnServer stops the server at the client's address when Pinky
// started it (OwnServer), and waits until it no longer answers, so that
// the next StartServer starts one. Any other server it leaves running, and
// returns ErrNotOwnServer.
func (c *Client) StopOwnServer(ctx context.Context) error {
	pid, ok := c.OwnServer()
	if !ok {
		return ErrNotOwnServer
	}
	if err := stopProcess(pid); err != nil {
		return fmt.Errorf("adb: stopping the server Pinky started (process %d): %w", pid, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		if _, err := c.Version(ctx); errors.Is(err, ErrServerNotRunning) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("adb: the server Pinky started (process %d) did not stop: %w", pid, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// parseProcesses picks the adb servers for port from ps's lines, "<pid>
// <command line>".
func parseProcesses(out, port string) []process {
	var procs []process
	for _, line := range strings.Split(out, "\n") {
		pid, command, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.Contains(command, serverLine(port)) {
			continue
		}
		n, err := strconv.Atoi(pid)
		if err != nil || n <= 0 {
			continue
		}
		procs = append(procs, process{pid: n, command: strings.TrimSpace(command)})
	}
	return procs
}
