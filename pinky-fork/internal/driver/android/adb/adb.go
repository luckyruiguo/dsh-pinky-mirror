// Package adb is a small client for the adb server protocol. It covers the
// host services Pinky needs (version, device list, device tracking, port
// forwards) and the device services it opens through a transport: shell,
// exec, sync (push, pull, stat) and local sockets.
//
// The client reuses the adb server that is already running. It never kills
// or restarts it, except one that Pinky started itself when asked to
// (StopOwnServer, own.go); StartServer (tools.go) starts one for callers
// that find none.
//
// Protocol reference: SERVICES.TXT, SYNC.TXT and protocol.txt in AOSP's
// packages/modules/adb.
package adb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultAddr is where the adb server listens unless the environment says
// otherwise.
const DefaultAddr = "127.0.0.1:5037"

// Client talks to one adb server. The zero value uses ServerAddr().
type Client struct {
	// Addr is the server's host:port. Empty means ServerAddr().
	Addr string
	// Guard, when set, runs before Dial and Forward reach a device, and
	// an error from it stops them: the Android driver refuses a
	// connection over the network that is not the phone it set up.
	Guard func(ctx context.Context, serial string) error
}

// guard runs Guard for serial.
func (c *Client) guard(ctx context.Context, serial string) error {
	if c == nil || c.Guard == nil {
		return nil
	}
	return c.Guard(ctx, serial)
}

// ServerAddr returns the adb server address the adb tool itself would use:
// ADB_SERVER_SOCKET (tcp:host:port), else ANDROID_ADB_SERVER_ADDRESS and
// ANDROID_ADB_SERVER_PORT, else DefaultAddr.
func ServerAddr() string {
	if s := os.Getenv("ADB_SERVER_SOCKET"); strings.HasPrefix(s, "tcp:") {
		hp := strings.TrimPrefix(s, "tcp:")
		if _, _, err := net.SplitHostPort(hp); err == nil {
			return hp
		}
		if _, err := strconv.Atoi(hp); err == nil {
			return net.JoinHostPort("127.0.0.1", hp)
		}
	}
	host := os.Getenv("ANDROID_ADB_SERVER_ADDRESS")
	port := os.Getenv("ANDROID_ADB_SERVER_PORT")
	if host == "" && port == "" {
		return DefaultAddr
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "5037"
	}
	return net.JoinHostPort(host, port)
}

func (c *Client) addr() string {
	if c != nil && c.Addr != "" {
		return c.Addr
	}
	return ServerAddr()
}

// Sentinel errors, matched with errors.Is against errors from this package.
var (
	// ErrServerNotRunning means nothing listens at the server address.
	ErrServerNotRunning = errors.New("adb server not running")
	// ErrDeviceNotFound means the server does not know the serial.
	ErrDeviceNotFound = errors.New("device not found")
	// ErrDeviceOffline means the device is known but not usable now.
	ErrDeviceOffline = errors.New("device offline")
	// ErrUnauthorized means the phone has not trusted this computer.
	ErrUnauthorized = errors.New("device unauthorized")
)

// Error is a FAIL reply from the adb server or the device.
type Error struct {
	Msg string
}

func (e *Error) Error() string { return "adb: " + e.Msg }

// Is maps the server's messages onto the sentinel errors.
func (e *Error) Is(target error) bool {
	m := strings.ToLower(e.Msg)
	switch target {
	case ErrDeviceNotFound:
		// "device 'x' not found", "no devices/emulators found"
		return strings.Contains(m, "not found") || strings.Contains(m, "no devices")
	case ErrDeviceOffline:
		return strings.Contains(m, "offline")
	case ErrUnauthorized:
		return strings.Contains(m, "unauthorized")
	}
	return false
}

// dial connects to the server and cancels blocking I/O when ctx ends. The
// returned stop function detaches ctx; call it before handing the
// connection to code that outlives ctx.
func (c *Client) dial(ctx context.Context) (net.Conn, func() bool, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", c.addr())
	if err != nil {
		if isConnRefused(err) {
			return nil, nil, fmt.Errorf("adb: connect to %s: %w", c.addr(), ErrServerNotRunning)
		}
		return nil, nil, fmt.Errorf("adb: connect to %s: %w", c.addr(), err)
	}
	stop := context.AfterFunc(ctx, func() {
		// Unblock any read or write in progress.
		_ = conn.SetDeadline(time.Unix(1, 0))
	})
	return conn, stop, nil
}

func isConnRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	// Windows reports WSAECONNREFUSED, which is not syscall.ECONNREFUSED.
	return strings.Contains(strings.ToLower(err.Error()), "refused")
}

// ctxErr prefers the context's error when it ended, since a cancelled
// context shows up as an I/O deadline error.
func ctxErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ce := ctx.Err(); ce != nil {
		return ce
	}
	return err
}

// writeRequest sends one length-prefixed request.
func writeRequest(w io.Writer, req string) error {
	if len(req) > 0xffff {
		return fmt.Errorf("adb: request too long (%d bytes)", len(req))
	}
	_, err := io.WriteString(w, fmt.Sprintf("%04x", len(req))+req)
	return err
}

// readStatus reads OKAY, or FAIL and its message.
func readStatus(r io.Reader) error {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return fmt.Errorf("adb: read status: %w", err)
	}
	switch string(buf[:]) {
	case "OKAY":
		return nil
	case "FAIL":
		msg, err := readString(r)
		if err != nil {
			return fmt.Errorf("adb: read failure message: %w", err)
		}
		return &Error{Msg: msg}
	default:
		return fmt.Errorf("adb: unexpected status %q", buf[:])
	}
}

// readString reads a string prefixed with its length in four hex digits.
func readString(r io.Reader) (string, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", err
	}
	n, err := strconv.ParseUint(string(hdr[:]), 16, 32)
	if err != nil {
		return "", fmt.Errorf("adb: bad length %q", hdr[:])
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// hostConn runs a host service and returns the connection after OKAY.
func (c *Client) hostConn(ctx context.Context, service string) (net.Conn, func() bool, error) {
	conn, stop, err := c.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := writeRequest(conn, service); err != nil {
		stop()
		conn.Close()
		return nil, nil, ctxErr(ctx, err)
	}
	if err := readStatus(conn); err != nil {
		stop()
		conn.Close()
		return nil, nil, ctxErr(ctx, err)
	}
	return conn, stop, nil
}

// query runs a host service that answers with one length-prefixed string.
func (c *Client) query(ctx context.Context, service string) (string, error) {
	conn, stop, err := c.hostConn(ctx, service)
	if err != nil {
		return "", err
	}
	defer stop()
	defer conn.Close()
	s, err := readString(conn)
	if err != nil {
		return "", ctxErr(ctx, fmt.Errorf("adb: %s: %w", service, err))
	}
	return s, nil
}

// Version returns the server's protocol version (41 for current servers).
func (c *Client) Version(ctx context.Context) (int, error) {
	s, err := c.query(ctx, "host:version")
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(s, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("adb: bad version %q", s)
	}
	return int(v), nil
}

// Features returns the features the device and the server share, such as
// "shell_v2", "stat_v2" and "ls_v2".
func (c *Client) Features(ctx context.Context, serial string) (map[string]bool, error) {
	s, err := c.query(ctx, "host-serial:"+serial+":features")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, f := range strings.Split(strings.TrimSpace(s), ",") {
		if f = strings.TrimSpace(f); f != "" {
			out[f] = true
		}
	}
	return out, nil
}

// Dial opens a service on the device with the given serial, for example
// "shell:ls", "exec:screencap -p", "sync:", "localabstract:name" or
// "tcp:8080". The connection carries the service's raw stream; ctx bounds
// only the setup.
func (c *Client) Dial(ctx context.Context, serial, service string) (net.Conn, error) {
	if serial == "" {
		return nil, errors.New("adb: empty serial")
	}
	if err := c.guard(ctx, serial); err != nil {
		return nil, err
	}
	conn, stop, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (net.Conn, error) {
		stop()
		conn.Close()
		return nil, ctxErr(ctx, err)
	}
	if err := writeRequest(conn, "host:transport:"+serial); err != nil {
		return fail(err)
	}
	if err := readStatus(conn); err != nil {
		return fail(err)
	}
	if err := writeRequest(conn, service); err != nil {
		return fail(err)
	}
	if err := readStatus(conn); err != nil {
		return fail(fmt.Errorf("%s: %w", firstWord(service), err))
	}
	if !stop() {
		// ctx ended between the reply and now; the deadline is set.
		conn.Close()
		return nil, ctx.Err()
	}
	return conn, nil
}

func firstWord(s string) string {
	if i := strings.IndexAny(s, " "); i >= 0 {
		return s[:i]
	}
	return s
}
