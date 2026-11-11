package adb

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// sockets lists the TCP sockets in /proc/net/tcp and tcp6, with the
// server's and self's by their inodes in /proc/<pid>/fd.
func sockets(server, self int) ([]sock, error) {
	owner := map[uint64]int{}
	for _, pid := range []int{server, self} {
		fds, err := filepath.Glob(filepath.Join("/proc", strconv.Itoa(pid), "fd", "*"))
		if err != nil {
			return nil, err
		}
		for _, fd := range fds {
			l, err := os.Readlink(fd)
			if err != nil {
				continue
			}
			if s, ok := strings.CutPrefix(l, "socket:["); ok {
				if n, err := strconv.ParseUint(strings.TrimSuffix(s, "]"), 10, 64); err == nil {
					owner[n] = pid
				}
			}
		}
	}
	var socks []sock
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		socks = append(socks, parseProcNet(string(b), owner)...)
	}
	return socks, nil
}
