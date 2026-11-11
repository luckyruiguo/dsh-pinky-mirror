//go:build !windows && !darwin && !linux

package adb

import "errors"

// sockets: Pinky cannot tell who uses the server here.
func sockets(server, self int) ([]sock, error) { return nil, errors.ErrUnsupported }
