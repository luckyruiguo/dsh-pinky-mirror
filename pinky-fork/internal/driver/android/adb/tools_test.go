package adb

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary is also a fake adb, for StartServer: with
// PINKY_FAKE_ADB=1 it answers `adb version` with PINKY_FAKE_ADB_VERSION,
// and `adb -L tcp:<port> fork-server server --reply-fd N` listens on that
// port, replies OK on N and answers host:version and host:kill.
func TestMain(m *testing.M) {
	if os.Getenv("PINKY_FAKE_ADB") == "1" {
		fakeADBMain(os.Args[1:])
		return
	}
	os.Exit(m.Run())
}

func fakeADBMain(args []string) {
	switch {
	case len(args) == 2 && args[0] == "pinky-hold":
		// Another program connected to the server, as Android Studio
		// stays (TestStopPinkyADB): it holds a connection until the
		// server or the test ends it.
		c, err := net.Dial("tcp", args[1])
		if err != nil {
			os.Exit(1)
		}
		fmt.Println("connected")
		go func() { time.Sleep(time.Minute); os.Exit(0) }()
		io.Copy(io.Discard, c)
		os.Exit(0)
	case len(args) == 1 && args[0] == "version":
		fmt.Println("Android Debug Bridge version 1.0.41")
		fmt.Println("Version " + os.Getenv("PINKY_FAKE_ADB_VERSION"))
		fmt.Println("Installed as " + os.Args[0])
		os.Exit(0)
	case len(args) == 6 && args[0] == "-L" && strings.HasPrefix(args[1], "tcp:") &&
		args[2] == "fork-server" && args[3] == "server" && args[4] == "--reply-fd":
		fd, err := strconv.ParseUint(args[5], 10, 64)
		if err != nil {
			os.Exit(2)
		}
		reply := os.NewFile(uintptr(fd), "reply")
		ln, err := net.Listen("tcp", "127.0.0.1:"+strings.TrimPrefix(args[1], "tcp:"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "could not install *smartsocket* listener:", err)
			os.Exit(1)
		}
		io.WriteString(reply, "OK\n")
		reply.Close()
		go func() {
			// Never outlive the test by much.
			time.Sleep(time.Minute)
			os.Exit(0)
		}()
		for {
			c, err := ln.Accept()
			if err != nil {
				os.Exit(0)
			}
			go func() {
				defer c.Close()
				req, err := readReq(bufio.NewReader(c))
				if err != nil {
					return
				}
				switch req {
				case "host:version":
					io.WriteString(c, "OKAY")
					writeStr(c, "0029")
				case "host:kill":
					io.WriteString(c, "OKAY")
					os.Exit(0)
				case "host:pinky-autoconnect":
					// The server's ADB_MDNS_AUTO_CONNECT, for
					// TestStartServerNoAutoConnect.
					v, ok := os.LookupEnv("ADB_MDNS_AUTO_CONNECT")
					if !ok {
						v = "unset"
					}
					io.WriteString(c, "OKAY")
					writeStr(c, v)
				default:
					fail(c, "unknown host service")
				}
			}()
		}
	}
	fmt.Fprintln(os.Stderr, "fake adb: unexpected arguments", args)
	os.Exit(2)
}

// fakeEnv is an environment with the given files, environment variables,
// home folder and executable; links maps a path to the one it points to.
func fakeEnv(goos string, files []string, vars map[string]string, home, exe string, links map[string]string) env {
	have := map[string]bool{}
	for _, f := range files {
		have[f] = true
	}
	return env{
		goos:   goos,
		getenv: func(k string) string { return vars[k] },
		home:   home,
		exe:    exe,
		isFile: func(p string) bool { return have[p] },
		real: func(p string) string {
			if r, ok := links[p]; ok {
				return r
			}
			return p
		},
	}
}

func toolList(tools []Tool) string {
	var b strings.Builder
	for _, t := range tools {
		fmt.Fprintf(&b, "%s=%s\n", t.Source, t.Path)
	}
	return b.String()
}

// TestToolsOrder: the order of the search on each system. The macOS and
// Linux cases use this computer's paths, which on Windows are not
// absolute without a drive, so they run elsewhere only.
func TestToolsOrder(t *testing.T) {
	j := filepath.Join
	if runtime.GOOS == "windows" {
		t.Run("windows", testToolsWindows)
		return
	}
	testToolsWindows(t)
	home := j("/", "Users", "pat")
	sdk := j("/", "sdk")
	defaultSDK := j(home, "Library", "Android", "sdk", "platform-tools", "adb")
	pinkyExe := j("/", "Applications", "Pinky.app", "Contents", "MacOS", "Pinky")
	pinky := j("/", "Applications", "Pinky.app", "Contents", "Helpers", "adb")
	e := fakeEnv("darwin",
		[]string{j(sdk, "platform-tools", "adb"), defaultSDK, "/opt/homebrew/bin/adb", "/usr/local/bin/adb", j("/", "bin2", "adb"), pinky},
		map[string]string{"ANDROID_HOME": sdk, "PATH": "/usr/bin:/opt/homebrew/bin:relative:" + j("/", "bin2")},
		home, pinkyExe,
		// The adb on PATH is the SDK's, linked.
		map[string]string{j("/", "bin2", "adb"): j(sdk, "platform-tools", "adb")})
	want := strings.Join([]string{
		FromSDK + "=" + j(sdk, "platform-tools", "adb"),
		FromSDK + "=" + defaultSDK,
		FromHomebrew + "=/opt/homebrew/bin/adb",
		FromHomebrew + "=/usr/local/bin/adb",
		FromPinky + "=" + pinky,
	}, "\n") + "\n"
	if got := toolList(e.tools()); got != want {
		t.Errorf("macOS:\n%s\nwant\n%s", got, want)
	}

	// Without any adb but Pinky's.
	e = fakeEnv("darwin", []string{pinky}, map[string]string{"PATH": "/usr/bin:/bin"}, home, pinkyExe, nil)
	if got := toolList(e.tools()); got != FromPinky+"="+pinky+"\n" {
		t.Errorf("macOS, Pinky's only: %s", got)
	}

	// PINKY_ADB names the only one.
	e = fakeEnv("darwin", []string{pinky, "/opt/homebrew/bin/adb"}, map[string]string{EnvADB: "/elsewhere/adb"}, home, pinkyExe, nil)
	if got := toolList(e.tools()); got != FromEnv+"=/elsewhere/adb\n" {
		t.Errorf("PINKY_ADB: %s", got)
	}
	e = fakeEnv("darwin", []string{pinky, "/opt/homebrew/bin/adb"}, map[string]string{EnvADB: "pinky"}, home, pinkyExe, nil)
	if got := toolList(e.tools()); got != FromPinky+"="+pinky+"\n" {
		t.Errorf("PINKY_ADB=pinky: %s", got)
	}
	e = fakeEnv("darwin", []string{"/opt/homebrew/bin/adb"}, map[string]string{EnvADB: "pinky"}, home, j("/", "build", "pinky"), nil)
	if got := e.tools(); len(got) != 0 {
		t.Errorf("PINKY_ADB=pinky in a build without it: %v", got)
	}

	// Linux: the SDK, Linuxbrew, the package's /usr/bin/adb, Pinky's.
	home = j("/", "home", "pat")
	e = fakeEnv("linux",
		[]string{j(home, "Android", "Sdk", "platform-tools", "adb"), "/home/linuxbrew/.linuxbrew/bin/adb", "/usr/bin/adb", "/opt/pinky/adb"},
		map[string]string{"PATH": "/usr/local/bin:/usr/bin"},
		home, "/opt/pinky/pinky", nil)
	want = strings.Join([]string{
		FromSDK + "=" + j(home, "Android", "Sdk", "platform-tools", "adb"),
		FromHomebrew + "=/home/linuxbrew/.linuxbrew/bin/adb",
		FromPath + "=/usr/bin/adb",
		FromPinky + "=/opt/pinky/adb",
	}, "\n") + "\n"
	if got := toolList(e.tools()); got != want {
		t.Errorf("Linux:\n%s\nwant\n%s", got, want)
	}
}

func TestParseToolVersion(t *testing.T) {
	for _, c := range []struct {
		out    string
		server int
		tools  string
		recent bool
		err    bool
	}{
		{"Android Debug Bridge version 1.0.41\nVersion 37.0.0-14910828\nInstalled as /x/adb\nRunning on Darwin 27.2.0 (arm64)\n", 41, "37.0.0-14910828", true, false},
		{"Android Debug Bridge version 1.0.41\r\nVersion 37.0.0p1-pinky\r\n", 41, "37.0.0p1-pinky", true, false},
		{"Android Debug Bridge version 1.0.41\nVersion 30.0.0-6374843\n", 41, "30.0.0-6374843", true, false},
		{"Android Debug Bridge version 1.0.41\nVersion 29.0.6-debian\n", 41, "29.0.6-debian", false, false},
		{"Android Debug Bridge version 1.0.39\nRevision debian\n", 39, "", false, false},
		{"Android Debug Bridge version 1.0.42\nVersion 38.0.0-1\n", 42, "38.0.0-1", true, false},
		{"bash: adb: command not found\n", 0, "", false, true},
	} {
		v, err := parseToolVersion(c.out)
		if (err != nil) != c.err {
			t.Errorf("%q: err %v", c.out, err)
			continue
		}
		if v.Server != c.server || v.Tools != c.tools || v.recent() != c.recent {
			t.Errorf("%q: %+v recent %v", c.out, v, v.recent())
		}
	}
}

// writeFiles writes Pinky's own adb files into dir, with content that
// differs by tag.
func writeFiles(t *testing.T, dir, tag string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range pinkyFiles(runtime.GOOS) {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(f+" "+tag), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, pinkyFiles(runtime.GOOS)[0])
}

func TestStage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PINKY_HOME", home)
	src := writeFiles(t, filepath.Join(t.TempDir(), "app"), "one")
	got, err := stage(src)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(home, "cache", "adb")
	if filepath.Dir(filepath.Dir(got)) != base || filepath.Base(got) != filepath.Base(src) {
		t.Fatalf("staged at %s, want a folder of %s", got, base)
	}
	for _, f := range pinkyFiles(runtime.GOOS) {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(got), f))
		if err != nil || string(b) != f+" one" {
			t.Errorf("%s: %q %v", f, b, err)
		}
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(got); err != nil || fi.Mode()&0o111 == 0 {
			t.Errorf("the copy is not executable: %v %v", fi.Mode(), err)
		}
	}
	// Again: the same copy, left as it was.
	fi1, _ := os.Stat(got)
	again, err := stage(src)
	if err != nil || again != got {
		t.Fatalf("again: %s %v", again, err)
	}
	if fi2, _ := os.Stat(got); !fi2.ModTime().Equal(fi1.ModTime()) {
		t.Errorf("the copy was written again")
	}
	// Another version gets its own folder, and the old one goes.
	src2 := writeFiles(t, filepath.Join(t.TempDir(), "app"), "two")
	got2, err := stage(src2)
	if err != nil || got2 == got {
		t.Fatalf("a new version: %s %v", got2, err)
	}
	if _, err := os.Stat(filepath.Dir(got)); !os.IsNotExist(err) {
		t.Errorf("the old copy is still there: %v", err)
	}
	// A spoiled copy is written again.
	os.WriteFile(got2, []byte("spoiled"), 0o755)
	if _, err := stage(src2); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got2); string(b) != pinkyFiles(runtime.GOOS)[0]+" two" {
		t.Errorf("not repaired: %q", b)
	}
	// Windows: the DLLs must be there.
	if runtime.GOOS == "windows" {
		os.Remove(filepath.Join(filepath.Dir(src2), "AdbWinApi.dll"))
		if _, err := stage(src2); err == nil {
			t.Errorf("staged without AdbWinApi.dll")
		}
	}
}

// fakeADB copies the test binary to dir as adb, the fake adb of TestMain.
func fakeADB(t *testing.T, dir string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := pinkyFiles(runtime.GOOS)[0]
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range pinkyFiles(runtime.GOOS)[1:] {
		os.WriteFile(filepath.Join(dir, f), []byte(f), 0o644)
	}
	return p
}

func TestCheck(t *testing.T) {
	t.Setenv("PINKY_HOME", t.TempDir())
	t.Setenv("PINKY_FAKE_ADB", "1")
	ctx := context.Background()
	adb := fakeADB(t, filepath.Join(t.TempDir(), "Helpers"))

	t.Setenv("PINKY_FAKE_ADB_VERSION", "37.0.0p1-pinky")
	got, err := check(ctx, Tool{Path: adb, Source: FromPinky})
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "37.0.0p1-pinky" || !strings.HasPrefix(got.Path, filepath.Join(os.Getenv("PINKY_HOME"), "cache", "adb")) {
		t.Errorf("Pinky's: %+v", got)
	}

	t.Setenv("PINKY_FAKE_ADB_VERSION", "29.0.6-debian")
	if _, err := check(ctx, Tool{Path: adb, Source: FromPath}); !errors.Is(err, errOld) {
		t.Errorf("an old adb: %v", err)
	}
	// An old one, when it is the only one that runs.
	missing := Tool{Path: filepath.Join(t.TempDir(), "missing"), Source: FromSDK}
	if got, err := usable(ctx, []Tool{missing, {Path: adb, Source: FromPath}}); err != nil || got.Path != adb {
		t.Errorf("the old adb as the last resort: %+v %v", got, err)
	}
	if got, err := check(ctx, Tool{Path: adb, Source: FromEnv}); err != nil || got.Version != "29.0.6-debian" {
		t.Errorf("an old adb that PINKY_ADB names: %+v %v", got, err)
	}
	if _, err := check(ctx, Tool{Path: filepath.Join(t.TempDir(), "missing"), Source: FromPath}); err == nil {
		t.Errorf("a missing adb passed")
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	return port
}

func TestStartServer(t *testing.T) {
	t.Setenv("ADB_SERVER_SOCKET", "")
	t.Setenv("ANDROID_ADB_SERVER_ADDRESS", "")
	t.Setenv("ANDROID_ADB_SERVER_PORT", freePort(t))
	t.Setenv("PINKY_HOME", t.TempDir())
	t.Setenv("PINKY_FAKE_ADB", "1")
	t.Setenv("PINKY_FAKE_ADB_VERSION", "37.0.0-fake")
	adb := fakeADB(t, t.TempDir())
	t.Setenv(EnvADB, adb)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := &Client{}
	t.Cleanup(func() {
		// Stop the fake server.
		if conn, err := net.Dial("tcp", ServerAddr()); err == nil {
			writeRequest(conn, "host:kill")
			readStatus(conn)
			conn.Close()
		}
	})

	tool, err := StartServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tool.Path != adb || tool.Source != FromEnv || tool.Version != "37.0.0-fake" {
		t.Errorf("started with %+v", tool)
	}
	if v, err := c.Version(ctx); err != nil || v != 41 {
		t.Fatalf("the server: %d %v", v, err)
	}
	// One runs: nothing to start.
	if tool, err := StartServer(ctx); err != nil || tool != (Tool{}) {
		t.Errorf("with a server running: %+v %v", tool, err)
	}
	// Starting another one where one listens: it quits, and the running
	// one is left alone.
	if err := spawnServer(ctx, adb, ServerOptions{}); err == nil {
		t.Errorf("a second server started")
	} else {
		t.Logf("a second server: %v", err)
	}
	if v, err := c.Version(ctx); err != nil || v != 41 {
		t.Errorf("the first server after the second: %d %v", v, err)
	}
}

// TestStartServerNoAutoConnect: a server started with NoAutoConnect gets
// ADB_MDNS_AUTO_CONNECT=0 in place of the computer's, and one started
// without it the computer's.
func TestStartServerNoAutoConnect(t *testing.T) {
	t.Setenv("ADB_SERVER_SOCKET", "")
	t.Setenv("ANDROID_ADB_SERVER_ADDRESS", "")
	t.Setenv("PINKY_FAKE_ADB", "1")
	t.Setenv("PINKY_FAKE_ADB_VERSION", "37.0.0-fake")
	t.Setenv("ADB_MDNS_AUTO_CONNECT", "adb-tls-connect")
	adb := fakeADB(t, t.TempDir())
	t.Setenv(EnvADB, adb)
	for _, tc := range []struct {
		opt  ServerOptions
		want string
	}{
		{ServerOptions{NoAutoConnect: true}, "0"},
		{ServerOptions{}, "adb-tls-connect"},
	} {
		t.Setenv("ANDROID_ADB_SERVER_PORT", freePort(t))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if _, err := StartServerWith(ctx, tc.opt); err != nil {
			cancel()
			t.Fatal(err)
		}
		got, err := (&Client{}).query(ctx, "host:pinky-autoconnect")
		cancel()
		if conn, err := net.Dial("tcp", ServerAddr()); err == nil {
			writeRequest(conn, "host:kill")
			readStatus(conn)
			conn.Close()
		}
		if err != nil || got != tc.want {
			t.Errorf("%+v: the server's ADB_MDNS_AUTO_CONNECT is %q (%v), want %q", tc.opt, got, err, tc.want)
		}
	}
	env := ServerOptions{NoAutoConnect: true}.env([]string{"HOME=/h", "ADB_MDNS_AUTO_CONNECT=1", "adb_mdns_auto_connect=all", "PATH=/bin"})
	if strings.Join(env, " ") != "HOME=/h PATH=/bin ADB_MDNS_AUTO_CONNECT=0" {
		t.Errorf("env: %q", env)
	}
	if env := (ServerOptions{}).env([]string{"HOME=/h"}); env != nil {
		t.Errorf("env without options: %q (want the computer's)", env)
	}
}

// TestStartServerOld: an adb older than MinTools starts the server only
// when no other one works.
func TestStartServerOld(t *testing.T) {
	t.Setenv("ADB_SERVER_SOCKET", "")
	t.Setenv("ANDROID_ADB_SERVER_ADDRESS", "")
	t.Setenv("ANDROID_ADB_SERVER_PORT", freePort(t))
	t.Setenv("PINKY_FAKE_ADB", "1")
	t.Setenv("PINKY_FAKE_ADB_VERSION", "29.0.6-debian")
	old := fakeADB(t, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		if conn, err := net.Dial("tcp", ServerAddr()); err == nil {
			writeRequest(conn, "host:kill")
			readStatus(conn)
			conn.Close()
		}
	})
	missing := Tool{Path: filepath.Join(t.TempDir(), "adb"), Source: FromSDK}
	tool, err := startServer(ctx, func() []Tool { return []Tool{{Path: old, Source: FromPath}, missing} }, ServerOptions{})
	if err != nil || tool.Path != old || tool.Version != "29.0.6-debian" {
		t.Fatalf("started with %+v: %v", tool, err)
	}
}

func TestStartServerNone(t *testing.T) {
	t.Setenv("ADB_SERVER_SOCKET", "")
	t.Setenv("ANDROID_ADB_SERVER_ADDRESS", "")
	t.Setenv("ANDROID_ADB_SERVER_PORT", freePort(t))
	t.Setenv(EnvADB, filepath.Join(t.TempDir(), "no-adb"))
	if _, err := StartServer(context.Background()); err == nil {
		t.Errorf("started a server with an adb that is not there")
	}
	for host, want := range map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true, "192.168.1.2": false, "pc.local": false} {
		if isLoopback(host) != want {
			t.Errorf("isLoopback(%q) = %v", host, !want)
		}
	}
}

// testToolsWindows: the SDK in LOCALAPPDATA, PATH, Pinky's next to
// Pinky.exe, in this computer's paths.
func testToolsWindows(t *testing.T) {
	j := filepath.Join
	root := "/"
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	local := j(root, "AppData", "Local")
	winExe := j(root, "Programs", "Pinky", "Pinky.exe")
	e := fakeEnv("windows",
		[]string{j(local, "Android", "Sdk", "platform-tools", "adb.exe"), j(root, "scoop", "shims", "adb.exe"), j(root, "Programs", "Pinky", "adb.exe")},
		map[string]string{"LOCALAPPDATA": local, "PATH": j(root, "scoop", "shims") + string(filepath.ListSeparator) + j(root, "Programs", "Pinky")},
		j(root, "Users", "pat"), winExe, nil)
	want := strings.Join([]string{
		FromSDK + "=" + j(local, "Android", "Sdk", "platform-tools", "adb.exe"),
		FromPath + "=" + j(root, "scoop", "shims", "adb.exe"),
		// Pinky's own folder is on PATH: still Pinky's, and last.
		FromPinky + "=" + j(root, "Programs", "Pinky", "adb.exe"),
	}, "\n") + "\n"
	if got := toolList(e.tools()); got != want {
		t.Errorf("Windows:\n%s\nwant\n%s", got, want)
	}
}
