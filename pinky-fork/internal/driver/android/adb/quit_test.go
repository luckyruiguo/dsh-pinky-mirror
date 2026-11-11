package adb

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestStopPinkyADB: a server of Pinky's own adb (its copy in the cache)
// stops, but not while another program is connected to it; Pinky's own
// connections do not count. A server of another adb keeps running.
func TestStopPinkyADB(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("Pinky cannot tell who uses the server here")
	}
	t.Setenv("ADB_SERVER_SOCKET", "")
	t.Setenv("ANDROID_ADB_SERVER_ADDRESS", "")
	t.Setenv("ANDROID_ADB_SERVER_PORT", freePort(t))
	t.Setenv("PINKY_HOME", t.TempDir())
	t.Setenv("PINKY_FAKE_ADB", "1")
	t.Setenv("PINKY_FAKE_ADB_VERSION", "37.0.0p1-pinky")
	dir, err := cacheDir()
	if err != nil {
		t.Fatal(err)
	}
	own := fakeADB(t, filepath.Join(dir, "64df412b2df9aab8"))
	t.Setenv(EnvADB, own)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := &Client{}
	t.Cleanup(func() { c.kill(context.Background()) })

	if _, err := StartServer(ctx); err != nil {
		t.Fatal(err)
	}
	// Another program, as Android Studio, holds a connection.
	hold := exec.Command(own, "pinky-hold", ServerAddr())
	out, err := hold.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := hold.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hold.Process.Kill(); hold.Wait() })
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line == "" {
		t.Fatalf("the other program did not connect: %q %v", line, err)
	}
	// And so does Pinky.
	mine, err := net.Dial("tcp", ServerAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer mine.Close()

	if err := c.StopPinkyADB(ctx); !errors.Is(err, ErrServerInUse) {
		t.Fatalf("StopPinkyADB with another program connected: %v, want ErrServerInUse", err)
	}
	if _, err := c.Version(ctx); err != nil {
		t.Fatalf("the server in use stopped: %v", err)
	}
	hold.Process.Kill()
	hold.Wait()
	// The server's side of the connection closes a moment later.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = c.StopPinkyADB(ctx)
		if !errors.Is(err, ErrServerInUse) || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("StopPinkyADB with only Pinky connected: %v", err)
	}
	if _, err := c.Version(ctx); !errors.Is(err, ErrServerNotRunning) {
		t.Fatalf("after StopPinkyADB, the server answers: %v", err)
	}

	// An adb the person installed.
	t.Setenv(EnvADB, fakeADB(t, t.TempDir()))
	if _, err := StartServer(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.StopPinkyADB(ctx); !errors.Is(err, ErrNotOwnServer) {
		t.Errorf("StopPinkyADB on the person's adb: %v, want ErrNotOwnServer", err)
	}
	if _, err := c.Version(ctx); err != nil {
		t.Errorf("the person's server stopped: %v", err)
	}
	// Nothing runs: nothing to stop.
	c.kill(ctx)
	for c.alive(ctx) {
		time.Sleep(50 * time.Millisecond)
	}
	if err := c.StopPinkyADB(ctx); !errors.Is(err, ErrNotOwnServer) {
		t.Errorf("StopPinkyADB without a server: %v", err)
	}
}

func (c *Client) alive(ctx context.Context) bool {
	_, err := c.Version(ctx)
	return !errors.Is(err, ErrServerNotRunning)
}

func TestInCache(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "Pinky", "adb")
	name := pinkyFiles(runtime.GOOS)[0]
	for _, tc := range []struct {
		exe  string
		goos string
		want bool
	}{
		{filepath.Join(cache, "64df412b2df9aab8", name), runtime.GOOS, true},
		{filepath.Join(cache, "old", "..", "64df412b2df9aab8", name), runtime.GOOS, true},
		{filepath.Join(cache, name), runtime.GOOS, true},
		{filepath.Join(cache, "64df412b2df9aab8", "scrcpy"), runtime.GOOS, false},
		{filepath.Join(cache, "..", "x", name), runtime.GOOS, false},
		{filepath.Join(filepath.Dir(cache), "adb-other", name), runtime.GOOS, false},
		{filepath.Join(t.TempDir(), "platform-tools", name), runtime.GOOS, false},
		{"", runtime.GOOS, false},
	} {
		if got := inCache(tc.exe, cache, tc.goos); got != tc.want {
			t.Errorf("inCache(%q, %q) = %v, want %v", tc.exe, cache, got, tc.want)
		}
	}
	if runtime.GOOS == "windows" {
		// As QueryFullProcessImageName reports it: the case may differ.
		dir := `C:\Users\me\AppData\Local\Pinky\adb`
		for exe, want := range map[string]bool{
			`C:\Users\me\AppData\Local\Pinky\adb\64df412b2df9aab8\adb.exe`: true,
			`c:\users\ME\appdata\local\pinky\ADB\64df412b2df9aab8\ADB.EXE`: true,
			`C:\Users\me\AppData\Local\Android\Sdk\platform-tools\adb.exe`: false,
			`C:\Program Files\Pinky\adb.exe`:                               false,
			`D:\Users\me\AppData\Local\Pinky\adb\x\adb.exe`:                false,
		} {
			if got := inCache(exe, dir, "windows"); got != want {
				t.Errorf("inCache(%q) = %v, want %v", exe, got, want)
			}
		}
	}
}

func ap(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

// TestOtherClients: connections to the server's ports count unless Pinky
// holds them; the server's own connections to phones and Pinky's other
// connections do not.
func TestOtherClients(t *testing.T) {
	const server, self, studio = 100, 200, 300
	base := []sock{
		{pid: server, listen: true, local: ap("127.0.0.1:5037")},
		{pid: server, listen: true, local: ap("127.0.0.1:27183")}, // a forward
		// Pinky's connection, both ends.
		{pid: self, local: ap("127.0.0.1:50001"), remote: ap("127.0.0.1:5037")},
		{pid: server, local: ap("127.0.0.1:5037"), remote: ap("127.0.0.1:50001")},
		// The server's connection to an emulator, and Pinky's to the web.
		{pid: server, local: ap("127.0.0.1:50002"), remote: ap("127.0.0.1:5555")},
		{pid: self, local: ap("192.168.1.2:50003"), remote: ap("1.1.1.1:443")},
	}
	if n := otherClients(base, server, self); n != 0 {
		t.Errorf("only Pinky: %d other clients", n)
	}
	studioConn := []sock{
		{pid: studio, local: ap("127.0.0.1:50010"), remote: ap("127.0.0.1:5037")},
		{pid: server, local: ap("127.0.0.1:5037"), remote: ap("127.0.0.1:50010")},
	}
	if n := otherClients(append(base, studioConn...), server, self); n != 1 {
		t.Errorf("with Android Studio: %d other clients, want 1", n)
	}
	// Linux knows the owners of the server's and Pinky's sockets only;
	// scrcpy's video through a forwarded port, over IPv6 mapped addresses.
	scrcpy := []sock{
		{pid: 0, local: ap("[::ffff:127.0.0.1]:50020"), remote: ap("[::ffff:127.0.0.1]:27183")},
		{pid: server, local: ap("[::ffff:127.0.0.1]:27183"), remote: ap("[::ffff:127.0.0.1]:50020")},
	}
	if n := otherClients(append(base, scrcpy...), server, self); n != 1 {
		t.Errorf("with scrcpy: %d other clients, want 1", n)
	}
}

func TestParseLsof(t *testing.T) {
	out := `p29570
f14
n127.0.0.1:51154->127.0.0.1:5037
TST=ESTABLISHED
TQR=0
TQS=0
f20
n*:49905
TST=LISTEN
f21
n127.0.0.1:51160->127.0.0.1:5037
TST=CLOSE_WAIT
p85509
f15
n127.0.0.1:5037->127.0.0.1:51154
TST=ESTABLISHED
f16
n127.0.0.1:5037
TST=LISTEN
f17
n[::1]:5037
TST=LISTEN
`
	got := parseLsof(out)
	want := []sock{
		{pid: 29570, local: ap("127.0.0.1:51154"), remote: ap("127.0.0.1:5037")},
		{pid: 85509, local: ap("127.0.0.1:5037"), remote: ap("127.0.0.1:51154")},
		{pid: 85509, listen: true, local: ap("127.0.0.1:5037")},
		{pid: 85509, listen: true, local: ap("[::1]:5037")},
	}
	if len(got) != len(want) {
		t.Fatalf("parseLsof = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseLsof[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if n := otherClients(got, 85509, 29570); n != 0 {
		t.Errorf("otherClients = %d", n)
	}
}

func TestParseProcNet(t *testing.T) {
	tcp := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:13AD 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0000000000000000 100 0 0 10 0
   1: 0100007F:C351 0100007F:13AD 01 00000000:00000000 00:00000000 00000000  1000        0 222 1 0000000000000000 20 4 30 10 -1
   2: 0100007F:13AD 0100007F:C351 01 00000000:00000000 00:00000000 00000000  1000        0 333 1 0000000000000000 20 4 30 10 -1
   3: 0100007F:13AD 0100007F:C352 06 00000000:00000000 03:00000000 00000000     0        0 0 3 0000000000000000
`
	tcp6 := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:13AD 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 444 1 0000000000000000 100 0 0 10 0
   1: 0000000000000000FFFF00000100007F:C360 0000000000000000FFFF00000100007F:13AD 01 00000000:00000000 00:00000000 00000000  1000        0 555 1 0000000000000000 20 4 30 10 -1
`
	owner := map[uint64]int{111: 1, 222: 2, 333: 1, 444: 1}
	got := append(parseProcNet(tcp, owner), parseProcNet(tcp6, owner)...)
	want := []sock{
		{pid: 1, listen: true, local: ap("127.0.0.1:5037"), remote: ap("0.0.0.0:0")},
		{pid: 2, local: ap("127.0.0.1:50001"), remote: ap("127.0.0.1:5037")},
		{pid: 1, local: ap("127.0.0.1:5037"), remote: ap("127.0.0.1:50001")},
		{pid: 1, listen: true, local: ap("[::1]:5037"), remote: ap("[::]:0")},
		{pid: 0, local: ap("[::ffff:127.0.0.1]:50016"), remote: ap("[::ffff:127.0.0.1]:5037")},
	}
	if len(got) != len(want) {
		t.Fatalf("parseProcNet = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parseProcNet[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if n := otherClients(got, 1, 2); n != 0 {
		t.Errorf("otherClients = %d", n)
	}
}

// TestParseTCPTable reads tables laid out as GetExtendedTcpTable fills
// them on Windows.
func TestParseTCPTable(t *testing.T) {
	le := binary.LittleEndian
	row4 := func(state uint32, local, remote netip.AddrPort, pid uint32) []byte {
		r := make([]byte, tcpRowSize)
		le.PutUint32(r[0:], state)
		copy(r[4:8], local.Addr().AsSlice())
		binary.BigEndian.PutUint16(r[8:], local.Port())
		copy(r[12:16], remote.Addr().AsSlice())
		binary.BigEndian.PutUint16(r[16:], remote.Port())
		le.PutUint32(r[20:], pid)
		return r
	}
	b := le.AppendUint32(nil, 4)
	b = append(b, row4(mibListen, ap("127.0.0.1:5037"), ap("0.0.0.0:0"), 4242)...)
	b = append(b, row4(mibEstablished, ap("127.0.0.1:50001"), ap("127.0.0.1:5037"), 77)...)
	b = append(b, row4(mibEstablished, ap("127.0.0.1:5037"), ap("127.0.0.1:50001"), 4242)...)
	b = append(b, row4(8 /* CLOSE_WAIT */, ap("127.0.0.1:5037"), ap("127.0.0.1:50002"), 4242)...)
	got := parseTCPTable(b, false)
	if len(got) != 3 || !got[0].listen || got[0].pid != 4242 || got[0].local != ap("127.0.0.1:5037") ||
		got[1].pid != 77 || got[1].local != ap("127.0.0.1:50001") || got[1].remote != ap("127.0.0.1:5037") {
		t.Fatalf("parseTCPTable v4 = %+v", got)
	}
	if pid, ok := listener(got, "5037"); !ok || pid != 4242 {
		t.Errorf("listener = %d %v", pid, ok)
	}
	if n := otherClients(got, 4242, 1); n != 1 {
		t.Errorf("otherClients = %d, want 1", n)
	}
	if n := otherClients(got, 4242, 77); n != 0 {
		t.Errorf("otherClients from Pinky = %d, want 0", n)
	}

	r6 := make([]byte, tcp6RowSize)
	copy(r6[0:16], netip.MustParseAddr("::1").AsSlice())
	binary.BigEndian.PutUint16(r6[20:], 5037)
	le.PutUint32(r6[48:], mibListen)
	le.PutUint32(r6[52:], 4242)
	got = parseTCPTable(append(le.AppendUint32(nil, 1), r6...), true)
	if len(got) != 1 || got[0] != (sock{pid: 4242, listen: true, local: ap("[::1]:5037"), remote: ap("[::]:0")}) {
		t.Fatalf("parseTCPTable v6 = %+v", got)
	}
	// A short table, as when it grew between the calls, reads no further.
	if got := parseTCPTable(le.AppendUint32(nil, 3), false); len(got) != 0 {
		t.Errorf("short table: %+v", got)
	}
}

func TestListener(t *testing.T) {
	socks := []sock{
		{pid: 1, listen: true, local: ap("127.0.0.1:5037")},
		{pid: 1, listen: true, local: ap("[::1]:5037")},
		{pid: 2, listen: true, local: ap("192.168.1.2:5037")}, // not loopback
		{pid: 3, local: ap("127.0.0.1:5037"), remote: ap("127.0.0.1:1")},
	}
	if pid, ok := listener(socks, "5037"); !ok || pid != 1 {
		t.Errorf("listener = %d %v", pid, ok)
	}
	if _, ok := listener(append(socks, sock{pid: 9, listen: true, local: ap("0.0.0.0:5037")}), "5037"); ok {
		t.Error("two listeners: one of them is about to quit")
	}
	if _, ok := listener(socks, "5038"); ok {
		t.Error("nothing on 5038")
	}
}
