package adb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
)

// Shell runs cmd with the legacy shell service and returns stdout and
// stderr interleaved. It has no exit status; use Run when you need one.
func (c *Client) Shell(ctx context.Context, serial, cmd string) ([]byte, error) {
	return c.readAll(ctx, serial, "shell:"+cmd)
}

// Exec runs cmd with the exec service: stdout only, byte for byte, which
// suits binary output such as `screencap -p`.
func (c *Client) Exec(ctx context.Context, serial, cmd string) ([]byte, error) {
	return c.readAll(ctx, serial, "exec:"+cmd)
}

func (c *Client) readAll(ctx context.Context, serial, service string) ([]byte, error) {
	conn, err := c.Dial(ctx, serial, service)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, conn); err != nil && !isClosedErr(err) {
		return buf.Bytes(), ctxErr(ctx, fmt.Errorf("adb: %s: %w", firstWord(service), err))
	}
	if err := ctx.Err(); err != nil {
		return buf.Bytes(), err
	}
	return buf.Bytes(), nil
}

func isClosedErr(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF)
}

// Result is the outcome of Run.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Shell protocol v2 packet ids.
const (
	shellStdin      = 0
	shellStdout     = 1
	shellStderr     = 2
	shellExit       = 3
	shellCloseStdin = 4
)

// Run runs cmd with shell protocol v2 (Android 7 and later) and returns
// stdout, stderr and the exit code. A non-zero exit code is not an error.
func (c *Client) Run(ctx context.Context, serial, cmd string) (Result, error) {
	conn, err := c.Dial(ctx, serial, "shell,v2,raw:"+cmd)
	if err != nil {
		return Result{}, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()
	var res Result
	var out, errb bytes.Buffer
	r := bufio.NewReader(conn)
	var hdr [5]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return res, ctxErr(ctx, fmt.Errorf("adb: shell v2: %w (no exit status)", err))
		}
		n := binary.LittleEndian.Uint32(hdr[1:])
		if n > 1<<26 {
			return res, fmt.Errorf("adb: shell v2: packet too large (%d)", n)
		}
		switch hdr[0] {
		case shellStdout:
			if _, err := io.CopyN(&out, r, int64(n)); err != nil {
				return res, ctxErr(ctx, err)
			}
		case shellStderr:
			if _, err := io.CopyN(&errb, r, int64(n)); err != nil {
				return res, ctxErr(ctx, err)
			}
		case shellExit:
			var code [1]byte
			if n >= 1 {
				if _, err := io.ReadFull(r, code[:]); err != nil {
					return res, ctxErr(ctx, err)
				}
				if _, err := io.CopyN(io.Discard, r, int64(n-1)); err != nil {
					return res, ctxErr(ctx, err)
				}
			}
			res.Stdout, res.Stderr, res.ExitCode = out.Bytes(), errb.Bytes(), int(code[0])
			return res, nil
		default:
			if _, err := io.CopyN(io.Discard, r, int64(n)); err != nil {
				return res, ctxErr(ctx, err)
			}
		}
	}
}

// Quote quotes s for the device's sh, unless it needs no quoting.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			strings.ContainsRune("@%+=:,./-_", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Command joins args into one shell command line, quoting each.
func Command(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = Quote(a)
	}
	return strings.Join(q, " ")
}

// Props reads the device's system properties (getprop).
func (c *Client) Props(ctx context.Context, serial string) (map[string]string, error) {
	out, err := c.Shell(ctx, serial, "getprop")
	if err != nil {
		return nil, err
	}
	return ParseProps(string(out)), nil
}

// ParseProps parses getprop output: "[key]: [value]" per line. Values may
// span lines.
func ParseProps(text string) map[string]string {
	props := map[string]string{}
	var key string
	var val strings.Builder
	open := false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if open {
			val.WriteByte('\n')
			if strings.HasSuffix(line, "]") {
				val.WriteString(strings.TrimSuffix(line, "]"))
				props[key] = val.String()
				open = false
			} else {
				val.WriteString(line)
			}
			continue
		}
		if !strings.HasPrefix(line, "[") {
			continue
		}
		k, rest, ok := strings.Cut(line[1:], "]: [")
		if !ok {
			continue
		}
		key = k
		if strings.HasSuffix(rest, "]") {
			props[key] = strings.TrimSuffix(rest, "]")
			continue
		}
		val.Reset()
		val.WriteString(rest)
		open = true
	}
	return props
}
