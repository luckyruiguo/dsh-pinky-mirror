package adb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
)

// spawnServer starts an adb server with the adb at path, as `adb
// start-server` does once it has found no server: it runs `adb -L
// tcp:<port> fork-server server --reply-fd <fd>` detached from Pinky, for
// the port of ServerAddr, and waits for the server's "OK\n" on that pipe,
// which it writes once it has looked for devices and listens. A server
// that cannot listen (another one does) exits, and so does this wait.
func spawnServer(ctx context.Context, path string, opt ServerOptions) error {
	host, port, err := net.SplitHostPort(ServerAddr())
	if err != nil {
		return err
	}
	if !isLoopback(host) {
		return fmt.Errorf("the server address %s is not this computer's", ServerAddr())
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	// tcp:<port>, as adb's own default, listens on the loopback addresses.
	cmd, err := serverCommand(path, "tcp:"+port, w)
	if err != nil {
		w.Close()
		return err
	}
	cmd.Env = opt.env(os.Environ())
	// The server's first words, until it writes to its own log (adb.log
	// in the temporary folder) instead.
	stderr := &tailBuffer{max: 2048}
	if captureStderr {
		cmd.Stderr = stderr
	}
	err = cmd.Start()
	w.Close()
	if err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }() // also reaps it when it quits later
	ack := make(chan error, 1)
	go func() {
		var b [3]byte
		_, err := io.ReadFull(r, b[:])
		if err == nil && string(b[:]) != "OK\n" {
			err = fmt.Errorf("the server answered %q", b[:])
		}
		ack <- err
	}()
	select {
	case err := <-ack:
		if err == nil {
			return nil
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			err = errors.New("the server quit before it was ready (another server may be listening; adb's log, adb.log in the temporary folder, says why)")
			select {
			case werr := <-exited:
				if werr != nil {
					err = fmt.Errorf("the server quit before it was ready: %v", werr)
				}
			case <-ctx.Done():
			}
		}
		if s := strings.TrimSpace(stderr.String()); s != "" {
			err = fmt.Errorf("%w: %s", err, s)
		}
		return err
	case <-ctx.Done():
		// It may still come up; it is not Pinky's to stop.
		return ctx.Err()
	}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	mu  sync.Mutex
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
