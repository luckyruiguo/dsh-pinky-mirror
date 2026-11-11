package api

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// TestPipeHalfClose: a client can end its sending side alone, as the shim
// does when the agent ends its input, and still read the gateway's answer.
func TestPipeHalfClose(t *testing.T) {
	d := shortDirs(t)
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	ln, _, err := listenSocket(d)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b, _ := io.ReadAll(c) // ends at the client's CloseWrite
		c.Write([]byte("got " + string(b) + "\n"))
	}()
	c, err := dialSocket(d, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("hello"))
	hc, ok := c.(interface{ CloseWrite() error })
	if !ok {
		t.Fatalf("%T cannot close its sending side", c)
	}
	if err := hc.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || line != "got hello\n" {
		t.Fatalf("%q %v", line, err)
	}
}

// TestPipeIsExclusive: a second listener on the same pipe fails even
// without the lock, which Windows does not enforce.
func TestPipeIsExclusive(t *testing.T) {
	d := shortDirs(t)
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	ln, _, err := listenSocket(d)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, _, err := listenSocket(d); err == nil || !strings.Contains(err.Error(), "already holds the pipe") {
		t.Fatalf("second listener: %v", err)
	}
}

// TestDialOlderGateway: with no gateway on the pipe, a client reaches an
// older Pinky that listens on the Unix socket, and a new gateway does not
// start beside it.
func TestDialOlderGateway(t *testing.T) {
	d := shortDirs(t)
	if err := d.Ensure(); err != nil {
		t.Fatal(err)
	}
	old, err := net.Listen("unix", d.unixSocket())
	if err != nil {
		t.Skipf("no AF_UNIX here: %v", err)
	}
	defer old.Close()
	go func() {
		for {
			c, err := old.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("old\n"))
			c.Close()
		}
	}()
	c, err := Dial(d)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(c).ReadString('\n')
	c.Close()
	if line != "old\n" {
		t.Fatalf("read %q", line)
	}
	if _, err := Listen(d); !errors.As(err, new(*RunningError)) {
		t.Fatalf("listen beside an older gateway: %v", err)
	}
	old.Close()
	// Once it is gone, a gateway starts and clears its socket file.
	ln, err := Listen(d)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := os.Stat(d.unixSocket()); !os.IsNotExist(err) {
		t.Errorf("old socket file: %v", err)
	}
}
