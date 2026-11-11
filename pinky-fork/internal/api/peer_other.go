//go:build !darwin && !linux

package api

import (
	"net"
	"os"
)

// peerUID cannot read peer credentials here; the socket's directory
// permissions are the only guard.
func peerUID(*net.UnixConn) (int, error) { return os.Getuid(), nil }
