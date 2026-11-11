//go:build linux

package standalone

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// peerAllowed lets only this app's own uid use the control socket: its
// screens, in its other process. Other apps, and adb's shell, are refused.
func peerAllowed(conn net.Conn) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if int(cred.Uid) != os.Getuid() {
		return fmt.Errorf("uid %d is not this app's", cred.Uid)
	}
	return nil
}
