package adb

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetExtendedTcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

// listeningServer returns the process that listens on the server's port
// and the program it runs, from the system's TCP table: whoever started
// it, inCache then tells whether it runs Pinky's adb.
func listeningServer(port string) (int, string, bool) {
	socks, err := tcpTable()
	if err != nil {
		return 0, "", false
	}
	pid, ok := listener(socks, port)
	if !ok {
		return 0, "", false
	}
	exe, err := imagePath(uint32(pid))
	if err != nil {
		return 0, "", false
	}
	return pid, exe, true
}

// sockets lists every TCP socket of the computer, with its process.
func sockets(server, self int) ([]sock, error) { return tcpTable() }

func myPID() int { return os.Getpid() }

// tcpTable reads the IPv4 and IPv6 TCP tables with their owners
// (GetExtendedTcpTable, TCP_TABLE_OWNER_PID_ALL).
func tcpTable() ([]sock, error) {
	const tableOwnerPIDAll = 5
	var socks []sock
	for _, af := range []uint32{windows.AF_INET, windows.AF_INET6} {
		var b []byte
		size := uint32(16 << 10)
		for range 5 {
			b = make([]byte, size)
			r, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&b[0])), uintptr(unsafe.Pointer(&size)),
				0, uintptr(af), tableOwnerPIDAll, 0)
			if r == 0 {
				break
			}
			if syscall.Errno(r) != windows.ERROR_INSUFFICIENT_BUFFER {
				return nil, syscall.Errno(r)
			}
			b = nil
			size += 4 << 10 // the table may grow meanwhile
		}
		if b == nil {
			return nil, windows.ERROR_INSUFFICIENT_BUFFER
		}
		socks = append(socks, parseTCPTable(b, af == windows.AF_INET6)...)
	}
	return socks, nil
}

// imagePath is the full path of a process's program.
func imagePath(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}
