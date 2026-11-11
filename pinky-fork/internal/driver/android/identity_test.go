package android

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// The phone in these tests, and its connection over Wireless debugging.
const (
	markSerial = "R5CN20ABC"
	markAddr   = "192.168.1.42:41000"
)

// markServer is enough of an adb server for the identity checks
// (identity.go): the device list and its tracking, the phone's properties,
// rm -f, chmod 600, and sync's STAT, RECV and SEND on files each phone
// keeps (SEND gives the owner's permissions to everyone, as adbd does). A
// connection leads to a phone, which a test may swap for another device in
// place, under the same serial and transport id, as adb does when it
// reconnects a connection made by address.
type markServer struct {
	t  *testing.T
	ln net.Listener

	mu       sync.Mutex
	conns    []*markConn
	trackers map[chan string]bool
	open     map[net.Conn]bool
}

type markConn struct {
	serial, transport string
	usb               bool
	off               bool
	phone             *markPhone
}

// markPhone is a device behind a connection: the phone, or one posing as
// it under its serial.
type markPhone struct {
	model, serial string
	files         map[string]phoneFile
	noRead        map[string]bool // files the shell may see but not read
	got           []string        // the services it was asked for
}

type phoneFile struct {
	data []byte
	mode uint32
}

func newMarkPhone(serial string) *markPhone {
	return &markPhone{model: "Pixel 9", serial: serial, files: map[string]phoneFile{}, noRead: map[string]bool{}}
}

func startMarkServer(t *testing.T) *markServer {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &markServer{t: t, ln: ln, trackers: map[chan string]bool{}, open: map[net.Conn]bool{}}
	t.Cleanup(func() {
		ln.Close()
		s.mu.Lock()
		for c := range s.open {
			c.Close()
		}
		s.mu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	return s
}

// provider returns a driver that checks phones over the network and keeps
// their marks in file.
func (s *markServer) provider(file string) *Provider {
	p := NewProvider(Config{Client: &adb.Client{Addr: s.ln.Addr().String()}, VerifyWiFi: true, IdentityFile: file, Logf: s.t.Logf})
	p.usbNodes = nil
	return p
}

// set changes the server's connections and tells its trackers.
func (s *markServer) set(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
	l := s.listLocked()
	for ch := range s.trackers {
		ch <- l
	}
}

func (s *markServer) add(c *markConn) { s.set(func() { s.conns = append(s.conns, c) }) }

func (s *markServer) remove(serial string) {
	s.set(func() {
		for i, c := range s.conns {
			if c.serial == serial {
				s.conns = append(s.conns[:i:i], s.conns[i+1:]...)
				return
			}
		}
	})
}

// swap has the connection at serial lead to another device, as adb's
// reconnect in place: offline first, then back under the same transport.
func (s *markServer) swap(serial string, to *markPhone) {
	conn := s.conn(serial)
	s.set(func() { conn.off = true })
	s.set(func() { conn.phone, conn.off = to, false })
}

func (s *markServer) conn(serial string) *markConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		if c.serial == serial {
			return c
		}
	}
	s.t.Fatalf("no connection %s", serial)
	return nil
}

func (s *markServer) listLocked() string {
	var b strings.Builder
	for _, c := range s.conns {
		if c.off {
			fmt.Fprintf(&b, "%s\toffline transport_id:%s\n", c.serial, c.transport)
			continue
		}
		usb := ""
		if c.usb {
			usb = " usb:1-1"
		}
		fmt.Fprintf(&b, "%s\tdevice%s product:x model:%s device:x transport_id:%s\n",
			c.serial, usb, strings.ReplaceAll(c.phone.model, " ", "_"), c.transport)
	}
	return b.String()
}

// mark returns the mark a phone keeps for Pinky, if it keeps one.
func (s *markServer) mark(ph *markPhone) (phoneFile, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for path, f := range ph.files {
		if strings.HasPrefix(path, markDir+markName) {
			return f, path, true
		}
	}
	return phoneFile{}, "", false
}

func (s *markServer) asked(ph *markPhone, service string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range ph.got {
		if g == service {
			return true
		}
	}
	return false
}

func (s *markServer) handle(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	req, ok := readRequest(r)
	if !ok {
		return
	}
	str := func(v string) { fmt.Fprintf(c, "%04x%s", len(v), v) }
	fail := func(msg string) { io.WriteString(c, "FAIL"); str(msg) }
	switch {
	case req == "host:version":
		io.WriteString(c, "OKAY")
		str("0029")
	case req == "host:devices-l":
		s.mu.Lock()
		l := s.listLocked()
		s.mu.Unlock()
		io.WriteString(c, "OKAY")
		str(l)
	case req == "host:track-devices-l":
		ch := make(chan string, 64)
		s.mu.Lock()
		s.trackers[ch] = true
		s.open[c] = true
		l := s.listLocked()
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.trackers, ch)
			delete(s.open, c)
			s.mu.Unlock()
		}()
		io.WriteString(c, "OKAY")
		str(l)
		for l := range ch {
			if _, err := fmt.Fprintf(c, "%04x%s", len(l), l); err != nil {
				return
			}
		}
	case strings.HasPrefix(req, "host:transport:"):
		serial := strings.TrimPrefix(req, "host:transport:")
		var ph *markPhone
		s.mu.Lock()
		for _, mc := range s.conns {
			if mc.serial == serial && !mc.off {
				ph = mc.phone
			}
		}
		s.mu.Unlock()
		if ph == nil {
			fail("device '" + serial + "' not found")
			return
		}
		io.WriteString(c, "OKAY")
		svc, ok := readRequest(r)
		if !ok {
			return
		}
		s.mu.Lock()
		ph.got = append(ph.got, svc)
		s.mu.Unlock()
		s.device(c, r, ph, svc)
	default:
		fail("unsupported")
	}
}

func (s *markServer) device(c net.Conn, r *bufio.Reader, ph *markPhone, svc string) {
	cmd, shell := strings.CutPrefix(svc, "shell:")
	switch {
	case shell && strings.HasPrefix(cmd, "getprop ro.product.model"):
		io.WriteString(c, "OKAY")
		fmt.Fprintf(c, "%s\n16\n36\n%s\n%s\npinky-boot=%s\n", ph.model, ph.model, ph.serial, ph.serial)
	case shell && strings.HasPrefix(cmd, "rm -f "):
		path := strings.Trim(strings.TrimPrefix(cmd, "rm -f "), "'")
		s.mu.Lock()
		delete(ph.files, path)
		delete(ph.noRead, path)
		s.mu.Unlock()
		io.WriteString(c, "OKAY")
	case shell && strings.HasPrefix(cmd, "chmod 600 "):
		path := strings.Trim(strings.TrimPrefix(cmd, "chmod 600 "), "'")
		s.mu.Lock()
		f, there := ph.files[path]
		if there {
			f.mode = f.mode&^0o777 | 0o600
			ph.files[path] = f
		}
		s.mu.Unlock()
		io.WriteString(c, "OKAY")
		if !there {
			io.WriteString(c, "chmod: "+path+": No such file or directory\n")
		}
	case shell:
		io.WriteString(c, "OKAY")
		io.WriteString(c, "ran "+cmd)
	case svc == "sync:":
		io.WriteString(c, "OKAY")
		s.sync(c, r, ph)
	default:
		io.WriteString(c, "FAIL0011unsupported")
	}
}

func (s *markServer) sync(c net.Conn, r *bufio.Reader, ph *markPhone) {
	header := func() (string, uint32, bool) {
		var h [8]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return "", 0, false
		}
		return string(h[:4]), binary.LittleEndian.Uint32(h[4:]), true
	}
	send := func(id string, data []byte) {
		h := make([]byte, 8, 8+len(data))
		copy(h, id)
		binary.LittleEndian.PutUint32(h[4:], uint32(len(data)))
		c.Write(append(h, data...))
	}
	for {
		id, n, ok := header()
		if !ok || id == "QUIT" {
			return
		}
		arg := make([]byte, n)
		if _, err := io.ReadFull(r, arg); err != nil {
			return
		}
		switch id {
		case "STAT":
			s.mu.Lock()
			f, there := ph.files[string(arg)]
			s.mu.Unlock()
			var out [16]byte
			copy(out[:], "STAT")
			if there {
				binary.LittleEndian.PutUint32(out[4:], f.mode)
				binary.LittleEndian.PutUint32(out[8:], uint32(len(f.data)))
				binary.LittleEndian.PutUint32(out[12:], 1700000000)
			}
			c.Write(out[:])
		case "RECV":
			s.mu.Lock()
			f, there := ph.files[string(arg)]
			locked := ph.noRead[string(arg)]
			s.mu.Unlock()
			switch {
			case !there:
				send("FAIL", []byte("remote object '"+string(arg)+"' does not exist"))
			case locked:
				send("FAIL", []byte("remote open failed: Permission denied"))
			default:
				if len(f.data) > 0 {
					send("DATA", f.data)
				}
				send("DONE", nil)
			}
		case "SEND":
			path, modeStr, _ := strings.Cut(string(arg), ",")
			mode, _ := strconv.ParseUint(modeStr, 10, 32)
			var buf bytes.Buffer
			for {
				id, n, ok := header()
				if !ok {
					return
				}
				if id == "DONE" {
					break
				}
				if _, err := io.CopyN(&buf, r, int64(n)); err != nil {
					return
				}
			}
			s.mu.Lock()
			m := uint32(mode)
			m |= m >> 3 & 0o070
			m |= m >> 3 & 0o007
			ph.files[path] = phoneFile{data: buf.Bytes(), mode: m}
			delete(ph.noRead, path)
			s.mu.Unlock()
			send("OKAY", nil)
		default:
			send("FAIL", []byte("unsupported"))
			return
		}
	}
}

// wifiPhone starts a server with the phone connected over Wireless
// debugging, and a file for the marks.
func wifiPhone(t *testing.T) (*markServer, *markPhone, string) {
	s := startMarkServer(t)
	ph := newMarkPhone(markSerial)
	s.add(&markConn{serial: markAddr, transport: "8", phone: ph})
	return s, ph, filepath.Join(t.TempDir(), "phone-identities.json")
}

// listedAs returns the phone with id as the provider lists it.
func listedAs(t *testing.T, p *Provider, id string) driver.Info {
	t.Helper()
	infos, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range infos {
		if in.ID == id {
			return in
		}
	}
	t.Fatalf("%s not listed: %+v", id, infos)
	return driver.Info{}
}

// markedPhone has a provider mark the phone, as a phone Pinky knew before
// it checked phones: the first check over the network marks it.
func markedPhone(t *testing.T, s *markServer, ph *markPhone, file string) {
	t.Helper()
	p := s.provider(file)
	p.UseOverWiFi(markSerial)
	if in := listedAs(t, p, markSerial); in.State != driver.StateOnline {
		t.Fatalf("known phone at its first check: %s (%s)", in.State, in.Detail)
	}
	if _, _, ok := s.mark(ph); !ok {
		t.Fatal("no mark left on the phone")
	}
}

func readIDFile(t *testing.T, file string) idFile {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var f idFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestIdentityNewPhoneWaitsForThePerson(t *testing.T) {
	s, ph, file := wifiPhone(t)
	p := s.provider(file)
	in := listedAs(t, p, markSerial)
	if in.State != driver.StateNeedsSetup || in.Setup != driver.SetupConfirmWiFi {
		t.Fatalf("a phone Pinky never used: %s/%s, want needs setup/%s", in.State, in.Setup, driver.SetupConfirmWiFi)
	}
	if _, err := p.OpenDevice(context.Background(), markSerial); !errors.Is(err, driver.ErrOffline) {
		t.Fatalf("OpenDevice = %v, want ErrOffline", err)
	}
	if _, _, ok := s.mark(ph); ok {
		t.Fatal("a phone nobody vouched for was marked")
	}
}

func TestIdentityFirstCheckMarksAKnownPhone(t *testing.T) {
	s, ph, file := wifiPhone(t)
	markedPhone(t, s, ph, file)

	f, path, _ := s.mark(ph)
	if len(f.data) != markLen || f.mode != 0o100600 {
		t.Fatalf("mark: %d bytes, mode %o; want %d bytes, mode 100600", len(f.data), f.mode, markLen)
	}
	rec := readIDFile(t, file)
	if path != markDir+markName+rec.Host || !validHost(rec.Host) {
		t.Fatalf("mark at %s, host %q", path, rec.Host)
	}
	if got := rec.Phones[markSerial].Mark; got != sum(f.data) {
		t.Fatalf("recorded %s, want the mark's hash %s", got, sum(f.data))
	}
	if fi, err := os.Stat(file); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("identity file: %v, %v", fi.Mode(), err)
	}
}

func TestIdentitySamePhonePasses(t *testing.T) {
	s, ph, file := wifiPhone(t)
	markedPhone(t, s, ph, file)

	// Pinky again, with the record: the phone has its mark.
	p := s.provider(file)
	if in := listedAs(t, p, markSerial); in.State != driver.StateOnline {
		t.Fatalf("the marked phone: %s (%s)", in.State, in.Detail)
	}
	d, err := p.OpenDevice(context.Background(), markSerial)
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.client.Shell(context.Background(), markAddr, "echo hi")
	if err != nil || string(out) != "ran echo hi" {
		t.Fatalf("shell through the guard = %q, %v", out, err)
	}
}

func TestIdentityRefusesAnotherDevice(t *testing.T) {
	s, ph, file := wifiPhone(t)
	markedPhone(t, s, ph, file)
	before := readIDFile(t, file).Phones[markSerial]

	// Something else answers at the phone's address under its serial.
	other := newMarkPhone(markSerial)
	s.swap(markAddr, other)
	p := s.provider(file)
	p.UseOverWiFi(markSerial) // the gateway knows the phone: no help to the other
	in := listedAs(t, p, markSerial)
	if in.State != driver.StateNeedsSetup || in.Setup != driver.SetupPhoneChanged {
		t.Fatalf("another device: %s/%s, want needs setup/%s", in.State, in.Setup, driver.SetupPhoneChanged)
	}
	if _, err := p.OpenDevice(context.Background(), markSerial); !errors.Is(err, driver.ErrOffline) {
		t.Fatalf("OpenDevice = %v, want ErrOffline", err)
	}
	if _, _, ok := s.mark(other); ok {
		t.Fatal("the other device got a mark")
	}
	if after := readIDFile(t, file).Phones[markSerial]; after != before {
		t.Fatalf("record changed: %+v, was %+v", after, before)
	}
}

func TestIdentityGuardChecksAgainAfterAReconnect(t *testing.T) {
	s, ph, file := wifiPhone(t)
	markedPhone(t, s, ph, file)
	p := s.provider(file)
	ctx := context.Background()
	if in := listedAs(t, p, markSerial); in.State != driver.StateOnline {
		t.Fatalf("the marked phone: %s (%s)", in.State, in.Detail)
	}
	waitFor(t, "the watch follows the server", func() bool {
		_, ok := p.ids.watch.entry(markAddr)
		return ok
	})
	d, err := p.OpenDevice(ctx, markSerial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.client.Shell(ctx, markAddr, "echo one"); err != nil {
		t.Fatal(err)
	}

	// adb makes the connection again, in place, to another device.
	st, _ := p.ids.watch.stamp(markAddr)
	other := newMarkPhone(markSerial)
	s.swap(markAddr, other)
	waitFor(t, "the watch sees the connection come back", func() bool {
		e, ok := p.ids.watch.entry(markAddr)
		now, _ := p.ids.watch.stamp(markAddr)
		return ok && e.State == adb.StateDevice && now != st
	})
	if _, err := d.client.Shell(ctx, markAddr, "echo private"); !errors.Is(err, driver.ErrOffline) {
		t.Fatalf("shell to another device = %v, want ErrOffline", err)
	}
	if s.asked(other, "shell:echo private") {
		t.Fatal("the request reached the other device")
	}

	// And back to the phone.
	s.swap(markAddr, ph)
	waitFor(t, "the guard lets requests through again", func() bool {
		_, err := d.client.Shell(ctx, markAddr, "echo two")
		return err == nil
	})
}

func TestIdentityTrustPhoneMarksItAnew(t *testing.T) {
	s, ph, file := wifiPhone(t)
	markedPhone(t, s, ph, file)
	before := readIDFile(t, file).Phones[markSerial]

	// The person reset the phone: its files are gone.
	reset := newMarkPhone(markSerial)
	s.swap(markAddr, reset)
	p := s.provider(file)
	if in := listedAs(t, p, markSerial); in.Setup != driver.SetupPhoneChanged {
		t.Fatalf("the reset phone: %s/%s", in.State, in.Setup)
	}
	ctx := context.Background()
	if err := p.TrustPhone(ctx, "R5CN99XYZ"); !errors.Is(err, driver.ErrOffline) {
		t.Fatalf("TrustPhone of a phone not connected = %v, want ErrOffline", err)
	}
	if err := p.TrustPhone(ctx, markSerial); err != nil {
		t.Fatal(err)
	}
	f, _, ok := s.mark(reset)
	if !ok {
		t.Fatal("the trusted phone has no mark")
	}
	after := readIDFile(t, file).Phones[markSerial]
	if after.Mark == before.Mark || after.Mark != sum(f.data) {
		t.Fatalf("record %s, was %s; the new mark's hash is %s", after.Mark, before.Mark, sum(f.data))
	}
	if in := listedAs(t, p, markSerial); in.State != driver.StateOnline {
		t.Fatalf("after trusting: %s (%s)", in.State, in.Detail)
	}
}

func TestIdentityCableMarksAgain(t *testing.T) {
	s, ph, file := wifiPhone(t)
	markedPhone(t, s, ph, file)

	reset := newMarkPhone(markSerial)
	s.swap(markAddr, reset)
	// The person plugs the phone in.
	s.add(&markConn{serial: markSerial, transport: "3", usb: true, phone: reset})
	p := s.provider(file)
	p.UseOverWiFi(markSerial) // the gateway knows the phone
	if in := listedAs(t, p, markSerial); in.State != driver.StateOnline || in.Connection != "usb" {
		t.Fatalf("on the cable: %s over %s", in.State, in.Connection)
	}
	if _, _, ok := s.mark(reset); !ok {
		t.Fatal("not marked over the cable")
	}
	s.remove(markSerial)
	if in := listedAs(t, p, markSerial); in.State != driver.StateOnline || in.Connection != "wifi" {
		t.Fatalf("over Wi-Fi after the cable: %s over %s (%s)", in.State, in.Connection, in.Detail)
	}
}

func TestIdentityPairingMarksOnlyThePairedAddress(t *testing.T) {
	s, ph, file := wifiPhone(t)
	p := s.provider(file)
	ctx := context.Background()
	e := adb.DeviceEntry{Serial: markAddr, State: adb.StateDevice, TransportID: "8"}

	// The phone paired from another address than the one connected.
	p.trustPaired(ctx, e, "adb-R5CN20ABC-x1y2z3", "192.168.1.99")
	if _, _, ok := s.mark(ph); ok {
		t.Fatal("marked a connection to another address than the paired one")
	}
	if in := listedAs(t, p, markSerial); in.Setup != driver.SetupConfirmWiFi {
		t.Fatalf("after pairing elsewhere: %s/%s, want the person to say", in.State, in.Setup)
	}

	p.trustPaired(ctx, e, "adb-R5CN20ABC-x1y2z3", "192.168.1.42")
	if _, _, ok := s.mark(ph); !ok {
		t.Fatal("the paired phone was not marked")
	}
	if in := listedAs(t, p, markSerial); in.State != driver.StateOnline {
		t.Fatalf("the paired phone: %s (%s)", in.State, in.Detail)
	}
}

func TestIdentityReplacesAFileNotPinkys(t *testing.T) {
	for _, tc := range []struct {
		name string
		file phoneFile
		lock bool
	}{
		{"short", phoneFile{data: []byte("hello"), mode: 0o100644}, false},
		{"unreadable", phoneFile{data: bytes.Repeat([]byte{7}, markLen), mode: 0o100600}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ph, file := wifiPhone(t)
			p := s.provider(file)
			host, err := p.ids.store.host()
			if err != nil {
				t.Fatal(err)
			}
			path := markDir + markName + host
			s.set(func() {
				ph.files[path] = tc.file
				ph.noRead[path] = tc.lock
			})
			p.UseOverWiFi(markSerial)
			if in := listedAs(t, p, markSerial); in.State != driver.StateOnline {
				t.Fatalf("%s (%s)", in.State, in.Detail)
			}
			f, _, _ := s.mark(ph)
			if len(f.data) != markLen || bytes.Equal(f.data, tc.file.data) || f.mode != 0o100600 {
				t.Fatalf("mark %x mode %o", f.data, f.mode)
			}
		})
	}
}

func TestIdentityCheckFailureIsOffline(t *testing.T) {
	s, ph, file := wifiPhone(t)
	markedPhone(t, s, ph, file)
	p := s.provider(file)
	p.ids.readMark = func(context.Context, string, string) ([]byte, bool, error) {
		return nil, false, errors.New("connection reset")
	}
	if in := listedAs(t, p, markSerial); in.State != driver.StateOffline || !strings.Contains(in.Detail, "connection reset") {
		t.Fatalf("%s (%s), want offline with the error", in.State, in.Detail)
	}
}

func TestReadMarkADB(t *testing.T) {
	s, ph, file := wifiPhone(t)
	p := s.provider(file)
	ctx := context.Background()
	s.set(func() {
		ph.files["/data/local/tmp/a"] = phoneFile{data: []byte("abc"), mode: 0o100600}
		ph.files["/data/local/tmp/b"] = phoneFile{data: []byte("xyz"), mode: 0o100600}
		ph.noRead["/data/local/tmp/b"] = true
		ph.files["/data/local/tmp/big"] = phoneFile{data: bytes.Repeat([]byte{1}, 3*markMax), mode: 0o100600}
	})
	if b, there, err := p.ids.readMarkADB(ctx, markAddr, "/data/local/tmp/a"); err != nil || !there || string(b) != "abc" {
		t.Fatalf("a = %q, %v, %v", b, there, err)
	}
	if b, there, err := p.ids.readMarkADB(ctx, markAddr, "/data/local/tmp/none"); err != nil || there || b != nil {
		t.Fatalf("none = %q, %v, %v", b, there, err)
	}
	if _, there, err := p.ids.readMarkADB(ctx, markAddr, "/data/local/tmp/b"); err == nil || !there {
		t.Fatalf("unreadable = %v, %v; want there, with an error", there, err)
	}
	if b, _, err := p.ids.readMarkADB(ctx, markAddr, "/data/local/tmp/big"); err != nil || len(b) != markMax {
		t.Fatalf("big = %d bytes, %v; want %d", len(b), err, markMax)
	}
	if _, _, err := p.ids.readMarkADB(ctx, "10.0.0.9:5555", "/data/local/tmp/a"); !errors.Is(err, adb.ErrDeviceNotFound) {
		t.Fatalf("no such device = %v", err)
	}
	if err := p.ids.writeMarkADB(ctx, markAddr, "/data/local/tmp/b", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _, err := p.ids.readMarkADB(ctx, markAddr, "/data/local/tmp/b"); err != nil || string(b) != "new" {
		t.Fatalf("b after write = %q, %v", b, err)
	}
}

func TestIdentityStore(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ids.json")
	if err := os.WriteFile(file, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	s := &idStore{path: file, logf: t.Logf, now: func() time.Time { return now }}
	if _, ok := s.get(markSerial); ok {
		t.Fatal("a record from a broken file")
	}
	mark := bytes.Repeat([]byte{5}, markLen)
	if changed, err := s.set(markSerial, mark); err != nil || !changed {
		t.Fatalf("set = %v, %v", changed, err)
	}
	if changed, err := s.set(markSerial, mark); err != nil || changed {
		t.Fatalf("set again = %v, %v", changed, err)
	}
	host, err := s.host()
	if err != nil || !validHost(host) {
		t.Fatalf("host = %q, %v", host, err)
	}

	// Pinky again: the records come back, without one that is no hash.
	f := readIDFile(t, file)
	f.Phones["bad"] = idRecord{Mark: "abc"}
	b, _ := json.Marshal(f)
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	s = &idStore{path: file, logf: t.Logf, now: time.Now}
	r, ok := s.get(markSerial)
	if !ok || !r.matches(mark) || !r.Since.Equal(now) {
		t.Fatalf("record = %+v, %v", r, ok)
	}
	if _, ok := s.get("bad"); ok {
		t.Fatal("kept a record that is no hash")
	}
	if h, _ := s.host(); h != host {
		t.Fatalf("host = %q, was %q", h, host)
	}
	if r.matches(mark[:16]) || r.matches(bytes.Repeat([]byte{6}, markLen)) {
		t.Fatal("matches another mark")
	}
}

func TestTrustedConnections(t *testing.T) {
	for serial, want := range map[string]bool{
		"R5CN20ABC":            true,
		"emulator-5554":        true,
		"127.0.0.1:5555":       true,
		"localhost:5555":       true,
		"[::1]:5555":           true,
		"192.168.1.42:41000":   false,
		"100.101.102.103:5555": false,
		"adb-R5CN20ABC-x1y2z3._adb-tls-connect._tcp": false,
	} {
		if got := trusted(adb.DeviceEntry{Serial: serial}); got != want {
			t.Errorf("trusted(%s) = %v, want %v", serial, got, want)
		}
	}
}
