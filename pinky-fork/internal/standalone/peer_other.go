//go:build !linux

package standalone

import "net"

// peerAllowed accepts every peer where the kernel cannot name it; the
// prototype runs on Android (Linux) only, and tests run elsewhere.
func peerAllowed(net.Conn) error { return nil }
