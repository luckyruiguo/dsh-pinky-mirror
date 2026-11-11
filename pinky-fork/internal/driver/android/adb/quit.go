package adb

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Stopping Pinky's adb when Pinky quits. An adb server outlives whoever
// started it, so that every adb tool shares it. A server that runs the adb
// that comes with Pinky (its copy in the user's cache folder, tools.go) is
// one Pinky started for itself, since the person has no adb of their own,
// and seen in Task Manager it looks like Pinky never quit. StopPinkyADB
// stops such a server, and only such a server, unless another program is
// connected to it (Android Studio, scrcpy, an `adb logcat`, their
// forwarded ports). A server of an adb the person installed keeps
// running: their own tools expect it to.
//
// Pinky tells its adb by the server's program: on macOS and Linux its
// command line (`<the copy's full path> -L tcp:<port> fork-server server
// --reply-fd 3`, as spawnServer runs it, see own.go), on Windows the image
// of the process that listens on the server's port. It tells who is
// connected from the system's TCP table: connections to a port the server
// listens on from a process other than the server and Pinky.

// ErrServerInUse is StopPinkyADB's error for a server that other programs
// are connected to; it leaves that one running.
var ErrServerInUse = errors.New("another program is using the adb server")

// StopPinkyADB stops the server at the client's address when it runs the
// adb that comes with Pinky and no other program is connected to it, and
// waits briefly until it no longer answers. It returns ErrNotOwnServer
// for any other server and ErrServerInUse for one in use, and leaves those
// running. When it cannot tell who is connected, it stops the server.
func (c *Client) StopPinkyADB(ctx context.Context) error {
	host, port, err := net.SplitHostPort(c.addr())
	if err != nil || !isLoopback(host) {
		return ErrNotOwnServer
	}
	dir, err := cacheDir()
	if err != nil {
		return ErrNotOwnServer
	}
	pid, exe, ok := listeningServer(port)
	if !ok || !inCache(exe, dir, runtime.GOOS) {
		return ErrNotOwnServer
	}
	socks, err := sockets(pid, myPID())
	if err == nil {
		if n := otherClients(socks, pid, myPID()); n > 0 {
			return fmt.Errorf("%w (%d connection(s) from other processes)", ErrServerInUse, n)
		}
	}
	if err := c.kill(ctx); err != nil {
		return fmt.Errorf("adb: stopping Pinky's adb server (process %d): %w", pid, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		if _, err := c.Version(ctx); errors.Is(err, ErrServerNotRunning) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("adb: Pinky's adb server (process %d) did not stop: %w", pid, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// kill asks the server to quit, as `adb kill-server` does.
func (c *Client) kill(ctx context.Context) error {
	conn, stop, err := c.hostConn(ctx, "host:kill")
	if err != nil {
		if errors.Is(err, ErrServerNotRunning) {
			return nil
		}
		return err
	}
	stop()
	return conn.Close()
}

// inCache reports whether exe is an adb in Pinky's cache folder dir.
func inCache(exe, dir, goos string) bool {
	if exe == "" || dir == "" {
		return false
	}
	name := "adb"
	if goos == "windows" {
		name = "adb.exe"
	}
	clean := func(p string) string {
		p = filepath.Clean(p)
		if goos == "windows" || goos == "darwin" {
			p = strings.ToLower(p) // file systems that ignore case
		}
		return p
	}
	e, d := clean(exe), clean(dir)
	if !strings.EqualFold(filepath.Base(e), name) {
		return false
	}
	rel, err := filepath.Rel(d, e)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// sock is a TCP socket of a process.
type sock struct {
	pid           int
	listen        bool // listening, rather than connected
	local, remote netip.AddrPort
}

// otherClients counts the connections to the server's listening ports
// from processes other than the server and self. A connection shows as
// two sockets on this computer: the server's, from a port it listens on,
// and the client's, whose own port is the server's socket's remote one.
// Only the server's and self's sockets need an owner; any others' may
// have pid 0.
func otherClients(socks []sock, server, self int) int {
	ports := map[uint16]bool{}
	for _, s := range socks {
		if s.pid == server && s.listen {
			ports[s.local.Port()] = true
		}
	}
	mine := map[netip.AddrPort]bool{}
	for _, s := range socks {
		if s.pid == self && !s.listen {
			mine[unmap(s.local)] = true
		}
	}
	n := 0
	for _, s := range socks {
		if s.pid != server || s.listen || !ports[s.local.Port()] {
			continue
		}
		if !mine[unmap(s.remote)] {
			n++
		}
	}
	return n
}

func unmap(a netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
}

// parseLsof reads `lsof -FpnT` for TCP sockets: a "p<pid>" line for each
// process, then for each socket an "n<local>->[<remote>]" line and
// "T<field>" lines, of which "TST=" is the state. Sockets other than
// listening and established ones are left out.
func parseLsof(out string) []sock {
	var socks []sock
	pid := 0
	var cur *sock
	var state string
	flush := func() {
		if cur != nil && (state == "LISTEN" || state == "ESTABLISHED") {
			cur.listen = state == "LISTEN"
			socks = append(socks, *cur)
		}
		cur, state = nil, ""
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			flush()
			pid, _ = strconv.Atoi(line[1:])
		case 'f':
			flush()
		case 'n':
			flush()
			local, remote, _ := strings.Cut(line[1:], "->")
			l, err := netip.ParseAddrPort(local)
			if err != nil {
				continue // "*:5037" and the like: not loopback
			}
			s := sock{pid: pid, local: l}
			if remote != "" {
				if r, err := netip.ParseAddrPort(remote); err == nil {
					s.remote = r
				}
			}
			cur = &s
		case 'T':
			if v, ok := strings.CutPrefix(line, "TST="); ok {
				state = v
			}
		}
	}
	flush()
	return socks
}

// parseProcNet reads Linux's /proc/net/tcp or tcp6: the sockets whose
// inode owner maps to a process; others get pid 0.
func parseProcNet(out string, owner map[uint64]int) []sock {
	var socks []sock
	for i, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 10 {
			continue
		}
		var listen bool
		switch f[3] {
		case "0A":
			listen = true
		case "01":
		default:
			continue
		}
		l, ok1 := procAddr(f[1])
		r, ok2 := procAddr(f[2])
		inode, err := strconv.ParseUint(f[9], 10, 64)
		if !ok1 || !ok2 || err != nil {
			continue
		}
		socks = append(socks, sock{pid: owner[inode], listen: listen, local: l, remote: r})
	}
	return socks
}

// procAddr reads "0100007F:13AD": the address as the kernel keeps it, in
// 32-bit words of the host's order (little-endian on the systems Pinky
// runs on), and the port in hex.
func procAddr(s string) (netip.AddrPort, bool) {
	a, p, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, false
	}
	b, err := hex.DecodeString(a)
	port, perr := strconv.ParseUint(p, 16, 16)
	if err != nil || perr != nil || (len(b) != 4 && len(b) != 16) {
		return netip.AddrPort{}, false
	}
	for i := 0; i < len(b); i += 4 {
		w := binary.LittleEndian.Uint32(b[i:])
		binary.BigEndian.PutUint32(b[i:], w)
	}
	addr, _ := netip.AddrFromSlice(b)
	return netip.AddrPortFrom(addr, uint16(port)), true
}

// The rows of Windows' GetExtendedTcpTable with TCP_TABLE_OWNER_PID_ALL:
// MIB_TCPTABLE_OWNER_PID and MIB_TCP6TABLE_OWNER_PID, a count then rows.
const (
	tcpRowSize     = 24 // MIB_TCPROW_OWNER_PID
	tcp6RowSize    = 56 // MIB_TCP6ROW_OWNER_PID
	mibListen      = 2  // MIB_TCP_STATE_LISTEN
	mibEstablished = 5  // MIB_TCP_STATE_ESTAB
)

// parseTCPTable reads an IPv4 (v6 false) or IPv6 table as
// GetExtendedTcpTable fills it, in the machine's order (little-endian);
// ports are in network order in a DWORD's first two bytes.
func parseTCPTable(b []byte, v6 bool) []sock {
	if len(b) < 4 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(b))
	size := tcpRowSize
	if v6 {
		size = tcp6RowSize
	}
	b = b[4:]
	var socks []sock
	for i := 0; i < n && len(b) >= (i+1)*size; i++ {
		r := b[i*size : (i+1)*size]
		var state, pid uint32
		var local, remote netip.AddrPort
		port := func(p []byte) uint16 { return binary.BigEndian.Uint16(p) }
		if v6 {
			// ucLocalAddr[16], dwLocalScopeId, dwLocalPort, ucRemoteAddr[16],
			// dwRemoteScopeId, dwRemotePort, dwState, dwOwningPid
			local = netip.AddrPortFrom(netip.AddrFrom16([16]byte(r[0:16])), port(r[20:]))
			remote = netip.AddrPortFrom(netip.AddrFrom16([16]byte(r[24:40])), port(r[44:]))
			state, pid = binary.LittleEndian.Uint32(r[48:]), binary.LittleEndian.Uint32(r[52:])
		} else {
			// dwState, dwLocalAddr, dwLocalPort, dwRemoteAddr, dwRemotePort,
			// dwOwningPid; addresses in network order
			state = binary.LittleEndian.Uint32(r[0:])
			local = netip.AddrPortFrom(netip.AddrFrom4([4]byte(r[4:8])), port(r[8:]))
			remote = netip.AddrPortFrom(netip.AddrFrom4([4]byte(r[12:16])), port(r[16:]))
			pid = binary.LittleEndian.Uint32(r[20:])
		}
		if state != mibListen && state != mibEstablished {
			continue
		}
		socks = append(socks, sock{pid: int(pid), listen: state == mibListen, local: local, remote: remote})
	}
	return socks
}

// listener returns the process that listens on port at a loopback (or
// any) address, when exactly one does.
func listener(socks []sock, port string) (int, bool) {
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0, false
	}
	pid := 0
	for _, s := range socks {
		a := s.local.Addr().Unmap()
		if !s.listen || s.local.Port() != uint16(p) || !(a.IsLoopback() || a.IsUnspecified()) {
			continue
		}
		if pid != 0 && pid != s.pid {
			return 0, false
		}
		pid = s.pid
	}
	return pid, pid > 0
}
