package awake

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// On Linux, Hold runs systemd-inhibit, which asks logind for a lock against
// idle handling and sleep and, once logind gives it, runs its command while
// it holds the lock. The command is cat between two pipes of this process:
// Hold sends it a byte and returns once the byte comes back, so the lock is
// held by then. When logind refuses the lock, which may take a moment while
// polkit starts, systemd-inhibit exits without running cat and Hold returns
// its error. The lock ends when release closes cat's input, or when this
// process exits, however it exits. systemd-inhibit --list shows it, by
// Pinky, with the reason. logind may refuse the lock to a process outside a
// login session.

// inhibitWait is how long Hold waits for logind to answer; tests shorten it.
var inhibitWait = 10 * time.Second

func hold(reason string) (func(), error) {
	path, err := exec.LookPath("systemd-inhibit")
	if err != nil {
		return nil, fmt.Errorf("awake: no systemd-inhibit: %w", ErrUnsupported)
	}
	out, outW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("awake: %w", err)
	}
	defer out.Close()
	cmd := exec.Command(path, "--what=idle:sleep", "--who=Pinky", "--why="+reason, "--mode=block", "cat")
	cmd.Stdout = outW
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		outW.Close()
		return nil, fmt.Errorf("awake: %w", err)
	}
	err = cmd.Start()
	outW.Close()
	if err != nil {
		return nil, fmt.Errorf("awake: systemd-inhibit: %w", err)
	}
	// If systemd-inhibit has failed already, this fails too; the select
	// below says why.
	in.Write([]byte{'\n'})
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	echoed := make(chan bool, 1)
	go func() {
		var b [1]byte
		n, _ := out.Read(b[:])
		echoed <- n == 1
	}()
	timeout := time.NewTimer(inhibitWait)
	defer timeout.Stop()
	var exit error
	select {
	case held := <-echoed:
		if held {
			return func() {
				in.Close()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					cmd.Process.Kill()
					<-done
				}
			}, nil
		}
		// No byte back: systemd-inhibit exited without running cat.
		exit = <-done
	case exit = <-done:
	case <-timeout.C:
		in.Close()
		cmd.Process.Kill()
		<-done
		return nil, fmt.Errorf("awake: systemd-inhibit: no answer from logind in %v", inhibitWait)
	}
	msg := strings.TrimSpace(stderr.String())
	switch {
	case msg != "":
	case exit != nil:
		msg = exit.Error()
	default:
		msg = "ended without holding the lock"
	}
	return nil, fmt.Errorf("awake: systemd-inhibit: %s", msg)
}

// lockedBuffer keeps the first KiB a process writes, safe for the
// goroutine that copies it and a reader.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := 1024 - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
