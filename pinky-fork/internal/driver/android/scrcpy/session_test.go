package scrcpy

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// fakePhone plays an adb server with one device that runs a scripted scrcpy
// server: the shell carries its log, the first abstract-socket connection
// is the video socket and the second the control socket.
type fakePhone struct {
	t  *testing.T
	ln net.Listener

	mu        sync.Mutex
	shell     net.Conn
	command   string
	sockets   int
	failFirst int // abstract-socket connections to refuse before accepting
	pushed    int
	control   []byte
	clipboard string
}

func newFakePhone(t *testing.T) (*fakePhone, *adb.Client) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakePhone{t: t, ln: ln, clipboard: "copied"}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f, &adb.Client{Addr: ln.Addr().String()}
}

func readReq(r io.Reader) (string, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return "", err
	}
	var size int
	fmt.Sscanf(string(n[:]), "%04x", &size)
	b := make([]byte, size)
	_, err := io.ReadFull(r, b)
	return string(b), err
}

func (f *fakePhone) handle(c net.Conn) {
	r := bufio.NewReader(c)
	req, err := readReq(r)
	if err != nil || req != "host:transport:phone" {
		io.WriteString(c, "FAIL0010device not found")
		c.Close()
		return
	}
	io.WriteString(c, "OKAY")
	svc, err := readReq(r)
	if err != nil {
		c.Close()
		return
	}
	switch {
	case svc == "sync:":
		io.WriteString(c, "OKAY")
		f.sync(c, r)
		c.Close()
	case strings.HasPrefix(svc, "shell:rm "):
		io.WriteString(c, "OKAY")
		c.Close()
	case strings.HasPrefix(svc, "shell:"):
		// Keep the shell before answering: the client goes on as soon as it
		// reads OKAY. Answering under the lock keeps others from writing the
		// server's log to the shell before the OKAY.
		f.mu.Lock()
		f.shell, f.command = c, strings.TrimPrefix(svc, "shell:")
		io.WriteString(c, "OKAY")
		f.mu.Unlock()
		io.WriteString(c, "[server] INFO: Device: [Fake] Phone (Android 17)\n")
		if strings.Contains(svc, "list_apps=true") {
			io.WriteString(c, "[server] INFO: List of apps:\n * Calculator                     com.android.calculator2\n")
			c.Close()
		}
	case strings.HasPrefix(svc, "localabstract:scrcpy_"):
		f.mu.Lock()
		if f.failFirst > 0 {
			f.failFirst--
			f.mu.Unlock()
			io.WriteString(c, "FAIL0006closed")
			c.Close()
			return
		}
		f.sockets++
		n := f.sockets
		f.mu.Unlock()
		io.WriteString(c, "OKAY")
		if n == 1 {
			f.video(c)
		} else {
			f.controlLoop(c, r)
		}
	default:
		io.WriteString(c, "FAIL0006closed")
		c.Close()
	}
}

func (f *fakePhone) sync(c net.Conn, r *bufio.Reader) {
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		id, n := string(hdr[:4]), binary.LittleEndian.Uint32(hdr[4:])
		switch id {
		case "SEND":
			io.CopyN(io.Discard, r, int64(n))
		case "DATA":
			io.CopyN(io.Discard, r, int64(n))
			f.mu.Lock()
			f.pushed += int(n)
			f.mu.Unlock()
		case "DONE":
			c.Write([]byte("OKAY\x00\x00\x00\x00"))
		case "QUIT":
			return
		}
	}
}

func (f *fakePhone) video(c net.Conn) {
	c.Write([]byte{0}) // dummy byte
	name := make([]byte, 64)
	copy(name, "Fake Phone")
	c.Write(name)
	c.Write([]byte{0x68, 0x32, 0x36, 0x34})
	sess := make([]byte, 12)
	binary.BigEndian.PutUint32(sess[0:], 0x80000000)
	binary.BigEndian.PutUint32(sess[4:], 1080)
	binary.BigEndian.PutUint32(sess[8:], 2340)
	c.Write(sess)
	c.Write(header(flagConfig, 2))
	c.Write([]byte{1, 2})
	c.Write(header(flagKeyFrame|1000, 3))
	c.Write([]byte{3, 4, 5})
}

func (f *fakePhone) controlLoop(c net.Conn, r *bufio.Reader) {
	for {
		t, err := r.ReadByte()
		if err != nil {
			return
		}
		switch t {
		case TypeInjectKeycode:
			buf := make([]byte, 13)
			io.ReadFull(r, buf)
			f.record(append([]byte{t}, buf...))
		case TypeGetClipboard:
			r.ReadByte()
			f.mu.Lock()
			text := f.clipboard
			f.mu.Unlock()
			msg := []byte{DeviceClipboard, 0, 0, 0, 0}
			binary.BigEndian.PutUint32(msg[1:], uint32(len(text)))
			c.Write(append(msg, text...))
		case TypeSetClipboard:
			var hdr [13]byte
			io.ReadFull(r, hdr[:])
			seq := binary.BigEndian.Uint64(hdr[:8])
			text := make([]byte, binary.BigEndian.Uint32(hdr[9:]))
			io.ReadFull(r, text)
			f.mu.Lock()
			f.clipboard = string(text)
			f.mu.Unlock()
			ack := make([]byte, 9)
			ack[0] = DeviceAckClipboard
			binary.BigEndian.PutUint64(ack[1:], seq)
			c.Write(ack)
		case TypeStartApp:
			n, _ := r.ReadByte()
			name := make([]byte, n)
			io.ReadFull(r, name)
			f.mu.Lock()
			sh := f.shell
			f.mu.Unlock()
			if string(name) == "com.android.calculator2" {
				io.WriteString(sh, "[server] INFO: Starting app \"Calculator\" [com.android.calculator2] on display 0...\n")
			} else {
				io.WriteString(sh, "[server] WARN: No app found for package \""+string(name)+"\"\n")
			}
		case TypeResetVideo:
		default:
			f.t.Errorf("unexpected control message %d", t)
			return
		}
	}
}

func (f *fakePhone) record(msg []byte) {
	f.mu.Lock()
	f.control = append(f.control, msg...)
	f.mu.Unlock()
}

func TestSessionWithFakePhone(t *testing.T) {
	f, c := newFakePhone(t)
	f.failFirst = 2 // the server is not listening yet
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Start(ctx, c, "phone", Options{MaxSize: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.DeviceName != "Fake Phone" || s.Codec != CodecH264 {
		t.Errorf("header: name %q codec %#x", s.DeviceName, s.Codec)
	}
	f.mu.Lock()
	cmd, pushed := f.command, f.pushed
	f.mu.Unlock()
	if !strings.Contains(cmd, "app_process / com.genymobile.scrcpy.Server 5.0.1 scid=") || !strings.Contains(cmd, "max_size=1024") {
		t.Errorf("command = %q", cmd)
	}
	if pushed != len(ServerJar()) {
		t.Errorf("pushed %d bytes, want %d", pushed, len(ServerJar()))
	}

	p, err := s.ReadPacket()
	if err != nil || !p.Session || p.Width != 1080 {
		t.Fatalf("session = %+v, %v", p, err)
	}
	if p, _ = s.ReadPacket(); !p.Config {
		t.Fatalf("config = %+v", p)
	}
	if p, _ = s.ReadPacket(); !p.KeyFrame || p.PTS != time.Millisecond {
		t.Fatalf("key frame = %+v", p)
	}

	if err := s.Key(66, 0); err != nil {
		t.Fatal(err)
	}
	text, err := s.Clipboard(ctx)
	if err != nil || text != "copied" {
		t.Fatalf("clipboard = %q, %v", text, err)
	}
	if err := s.SetClipboard(ctx, "\u4f60\u597d", true); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if f.clipboard != "\u4f60\u597d" {
		t.Errorf("phone clipboard = %q", f.clipboard)
	}
	keys := append([]byte(nil), f.control...)
	f.mu.Unlock()
	if len(keys) != 28 || keys[1] != ActionDown || keys[15] != ActionUp {
		t.Errorf("key messages = %x", keys)
	}

	app, err := s.StartApp(ctx, "com.android.calculator2")
	if err != nil || app.Package != "com.android.calculator2" || app.Name != "Calculator" || app.Display != 0 {
		t.Fatalf("start app = %+v, %v", app, err)
	}
	if _, err := s.StartApp(ctx, "com.example.none"); !errors.Is(err, ErrAppNotFound) {
		t.Fatalf("missing app = %v", err)
	}

	s.Close()
	if err := s.Send(ResetVideo()); err == nil {
		t.Error("send after close succeeded")
	}
	if !errors.Is(s.Err(), ErrClosed) {
		t.Errorf("err after close = %v", s.Err())
	}
}

func TestListAppsWithFakePhone(t *testing.T) {
	_, c := newFakePhone(t)
	apps, err := ListApps(context.Background(), c, "phone")
	if err != nil || len(apps) != 1 || apps[0].Package != "com.android.calculator2" || !apps[0].System {
		t.Fatalf("apps = %+v, %v", apps, err)
	}
}

func TestStartFailsWhenServerExits(t *testing.T) {
	f, c := newFakePhone(t)
	f.failFirst = 1 << 30 // the server never listens
	go func() {
		// The server prints an error and exits.
		for {
			time.Sleep(20 * time.Millisecond)
			f.mu.Lock()
			sh := f.shell
			f.mu.Unlock()
			if sh != nil {
				io.WriteString(sh, "[server] ERROR: Could not create display\n")
				sh.Close()
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Start(ctx, c, "phone", Options{})
	if err == nil || !strings.Contains(err.Error(), "Could not create display") {
		t.Fatalf("err = %v", err)
	}
}
