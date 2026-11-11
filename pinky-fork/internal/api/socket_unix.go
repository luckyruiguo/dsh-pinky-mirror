//go:build !windows

package api

import (
	"net"
	"os"
	"time"
)

// listenSocket listens on the Unix socket, readable only by the user. The
// caller holds the lock, so a socket file left there is stale.
func listenSocket(d Dirs) (net.Listener, func(), error) {
	path := d.Socket()
	if err := checkSocketPath(path); err != nil {
		return nil, nil, err
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, nil, err
	}
	return ln, func() { os.Remove(path) }, nil
}

func dialSocket(d Dirs, timeout time.Duration) (net.Conn, error) {
	path := d.Socket()
	if err := checkSocketPath(path); err != nil {
		return nil, err
	}
	return net.DialTimeout("unix", path, timeout)
}
