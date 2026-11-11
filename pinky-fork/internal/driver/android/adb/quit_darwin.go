package adb

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

// sockets lists the TCP sockets of the server and self with lsof. -b and
// -w keep lsof from waiting on a network volume that does not answer
// (a Time Machine share took it two minutes); -p keeps it to the two
// processes.
func sockets(server, self int) ([]sock, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-b", "-w", "-nP", "-a",
		"-p", strconv.Itoa(server)+","+strconv.Itoa(self), "-iTCP", "-FpnT").Output()
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return parseLsof(string(out)), nil
}
