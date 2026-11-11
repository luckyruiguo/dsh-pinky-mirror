package api

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// listenSocket listens on the gateway's named pipe. Only the user can open
// it, and the pipe refuses clients on other computers. The first instance
// of a pipe name is exclusive, so a second gateway cannot listen beside a
// running one even where the lock file does not hold (Windows has no flock).
func listenSocket(d Dirs) (net.Listener, func(), error) {
	sid, err := userSID()
	if err != nil {
		return nil, nil, err
	}
	// Owner and the only access entry: the user. An elevated Pinky would
	// otherwise own the pipe as Administrators, and clients check the owner.
	sd := "O:" + sid.String() + "D:P(A;;GA;;;" + sid.String() + ")"
	path := d.Socket()
	ln, err := winio.ListenPipe(path, &winio.PipeConfig{
		SecurityDescriptor: sd,
		// Message mode lets the shim close its sending side alone (CloseWrite)
		// when the agent ends its input, as on a Unix socket.
		MessageMode:      true,
		InputBufferSize:  64 << 10,
		OutputBufferSize: 64 << 10,
	})
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PIPE_BUSY) {
		return nil, nil, fmt.Errorf("another process already holds the pipe: %w", err)
	}
	if err != nil {
		return nil, nil, err
	}
	// A socket file an older Pinky left behind would only slow clients down.
	_ = os.Remove(d.unixSocket())
	return ln, func() {}, nil
}

// dialSocket connects to the named pipe, and, when no gateway listens
// there, to the Unix socket an older Pinky listens on, so that clients of
// this version still reach (and can quit) a gateway that was started before
// an update.
func dialSocket(d Dirs, timeout time.Duration) (net.Conn, error) {
	c, err := winio.DialPipe(d.Socket(), &timeout)
	if err == nil {
		if err := checkPipeOwner(c); err != nil {
			c.Close()
			return nil, err
		}
		return c, nil
	}
	if _, serr := os.Stat(d.unixSocket()); serr == nil {
		if lc, lerr := net.DialTimeout("unix", d.unixSocket(), timeout); lerr == nil {
			return lc, nil
		}
	}
	return nil, err
}

// checkPipeOwner makes sure the user's own gateway created the pipe, not
// another user who took its name first: the client sends the agent token.
func checkPipeOwner(c net.Conn) error {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return errors.New("named pipe: no handle")
	}
	info, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("named pipe owner: %w", err)
	}
	owner, _, err := info.Owner()
	if err != nil {
		return fmt.Errorf("named pipe owner: %w", err)
	}
	me, err := userSID()
	if err != nil {
		return err
	}
	if !owner.Equals(me) {
		return fmt.Errorf("the Pinky pipe %s belongs to another user (%s)", c.RemoteAddr(), owner)
	}
	return nil
}

// userSID is the user this process runs as.
func userSID() (*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("current user: %w", err)
	}
	return tu.User.Sid, nil
}
