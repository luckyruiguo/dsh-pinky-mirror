package adb

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Forward is one port forward from the host to a device.
type Forward struct {
	Serial string
	Local  string // e.g. "tcp:27183"
	Remote string // e.g. "localabstract:scrcpy_1234abcd"
}

// Forward makes the server listen on local (such as "tcp:0" for any free
// port) and connect each accepted connection to remote on the device (such
// as "localabstract:name" or "tcp:8080"). It returns the local spec, with
// the port that was picked when local is "tcp:0". Without rebind, an
// existing forward on local is an error.
func (c *Client) Forward(ctx context.Context, serial, local, remote string, rebind bool) (string, error) {
	if err := c.guard(ctx, serial); err != nil {
		return "", err
	}
	svc := "host-serial:" + serial + ":forward:"
	if !rebind {
		svc += "norebind:"
	}
	svc += local + ";" + remote
	conn, stop, err := c.hostConn(ctx, svc)
	if err != nil {
		return "", err
	}
	defer stop()
	defer conn.Close()
	// On the host the first OKAY means the request was understood and the
	// second carries the result.
	if err := readStatus(conn); err != nil {
		return "", ctxErr(ctx, fmt.Errorf("forward %s: %w", local, err))
	}
	if local != "tcp:0" {
		return local, nil
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	port, err := readString(conn)
	if err != nil {
		return "", ctxErr(ctx, fmt.Errorf("adb: forward %s: read port: %w", local, err))
	}
	return "tcp:" + strings.TrimSpace(port), nil
}

// RemoveForward removes the forward listening on local.
func (c *Client) RemoveForward(ctx context.Context, serial, local string) error {
	conn, stop, err := c.hostConn(ctx, "host-serial:"+serial+":killforward:"+local)
	if err != nil {
		return err
	}
	defer stop()
	defer conn.Close()
	if err := readStatus(conn); err != nil {
		return ctxErr(ctx, fmt.Errorf("remove forward %s: %w", local, err))
	}
	return nil
}

// Forwards lists the server's forwards for all devices.
func (c *Client) Forwards(ctx context.Context) ([]Forward, error) {
	s, err := c.query(ctx, "host:list-forward")
	if err != nil {
		return nil, err
	}
	var out []Forward
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		out = append(out, Forward{Serial: f[0], Local: f[1], Remote: f[2]})
	}
	return out, nil
}
