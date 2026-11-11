package adb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Starting an adb server. Pinky uses whichever adb server runs and never
// kills or restarts one that another program started (one it started
// itself it may stop: own.go). When none runs, StartServer starts one with the
// first adb of Tools() that runs and is recent enough (MinTools): the one
// PINKY_ADB names, else the Android SDK's (whose server Android Studio
// shares), Homebrew's, those on PATH, and last the adb that comes with
// Pinky (scripts/adb.sh builds it from AOSP's source). An adb the person
// installed comes first so that their own adb commands find a server of
// their version: an adb client that finds a server of another version
// kills it and starts its own.
//
// For the same reason StartServer runs the server process itself (`adb
// fork-server server`, what `adb start-server` runs after its checks)
// rather than `adb start-server`, which would kill a server of another
// version that came up meanwhile. A server that cannot listen because
// another one does just exits.
//
// Pinky's own adb runs from a copy in the user's cache folder, as the
// server outlives Pinky: running from the app's folder, it would keep the
// Windows installer from replacing adb.exe, and a disk image from being
// ejected.

// EnvADB names the only adb StartServer uses: a path, or "pinky" for the
// one that comes with Pinky.
const EnvADB = "PINKY_ADB"

// Where an adb comes from, in Tool.Source.
const (
	FromEnv      = "PINKY_ADB"
	FromSDK      = "Android SDK"
	FromHomebrew = "Homebrew"
	FromPath     = "PATH"
	FromPinky    = "Pinky"
)

// MinTools is the oldest platform-tools release StartServer prefers:
// 30.0.0 (2020) brought Wireless debugging (host:pair). An older one, such
// as the adb package of Ubuntu 22.04, is passed over for a newer one, and
// used only when no newer one works.
const MinTools = 30

// ErrNoADB means there is no adb to start a server with.
var ErrNoADB = errors.New("no adb found")

// Tool is an adb executable.
type Tool struct {
	Path   string // the executable
	Source string // FromEnv, FromSDK, FromHomebrew, FromPath or FromPinky
	// Version is the platform-tools version `adb version` reports, such as
	// "37.0.0-14910828", once StartServer has run it.
	Version string
}

// Tools lists the adb executables StartServer may use, in order: those
// that exist, each once.
func Tools() []Tool { return sysEnv().tools() }

// FindADB returns the path of the first adb of Tools() that runs and is
// recent enough, for commands that run adb as a client (the companion's
// debug tool). Pinky's own is a copy in the user's cache folder.
func FindADB() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t, err := usable(ctx, Tools())
	return t.Path, err
}

// StartServer starts an adb server unless one answers already, and
// returns the adb it started it with (a zero Tool when one was running).
// It never kills a server.
func StartServer(ctx context.Context) (Tool, error) { return startServer(ctx, Tools, ServerOptions{}) }

// StartServerWith is StartServer, for a server started as opt says.
func StartServerWith(ctx context.Context, opt ServerOptions) (Tool, error) {
	return startServer(ctx, Tools, opt)
}

// ServerOptions tunes a server that StartServerWith starts. A server that
// runs already is used as it is.
type ServerOptions struct {
	// NoAutoConnect starts the server with ADB_MDNS_AUTO_CONNECT=0: it then
	// connects by itself to none of the phones its mDNS discovery finds
	// (by default it connects to every phone it paired with that
	// advertises Wireless debugging), and whoever started it decides what
	// to connect to. Its discovery still lists the phones
	// (host:mdns:services), and it still pairs, connects when asked (to an
	// address, or to a phone's service name from that list), and
	// reconnects a connection to an address that dropped.
	NoAutoConnect bool
}

// env is the server's environment, from the computer's.
func (o ServerOptions) env(base []string) []string {
	if !o.NoAutoConnect {
		return nil // the computer's, as is
	}
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if k, _, _ := strings.Cut(kv, "="); !strings.EqualFold(k, envAutoConnect) {
			out = append(out, kv)
		}
	}
	return append(out, envAutoConnect+"=0")
}

// envAutoConnect lists the mDNS services the adb server connects to by
// itself; "0" turns that off (adb_mdns.cpp).
const envAutoConnect = "ADB_MDNS_AUTO_CONNECT"

func startServer(ctx context.Context, list func() []Tool, opt ServerOptions) (Tool, error) {
	c := &Client{}
	if _, err := c.Version(ctx); !errors.Is(err, ErrServerNotRunning) {
		return Tool{}, err
	}
	host, _, err := net.SplitHostPort(c.addr())
	if err == nil && !isLoopback(host) {
		return Tool{}, fmt.Errorf("adb: the server address %s is not this computer's, so Pinky cannot start one there", c.addr())
	}
	var errs []string
	var old []Tool // used only when no recent adb works
	// start starts a server with t, and reports whether one answers then:
	// whoever started it, that is what is needed (a zero Tool when it was
	// not t's).
	start := func(t Tool) (Tool, bool, error) {
		serr := spawnServer(ctx, t.Path, opt)
		if _, err := c.Version(ctx); err == nil {
			if serr != nil {
				return Tool{}, true, nil
			}
			return t, true, nil
		}
		if ctx.Err() != nil {
			return Tool{}, false, ctx.Err()
		}
		if serr == nil {
			serr = errors.New("it did not answer")
		}
		errs = append(errs, fmt.Sprintf("%s (%s): starting a server: %v", t.Path, t.Source, serr))
		return Tool{}, false, nil
	}
	for _, t := range list() {
		t, err := check(ctx, t)
		if errors.Is(err, errOld) {
			old = append(old, t)
		}
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if got, ok, err := start(t); ok || err != nil {
			return got, err
		}
	}
	for _, t := range old {
		if got, ok, err := start(t); ok || err != nil {
			return got, err
		}
	}
	if len(errs) == 0 {
		return Tool{}, fmt.Errorf("adb: %w (Pinky looks in the Android SDK, Homebrew, PATH and its own folder; %s names one)", ErrNoADB, EnvADB)
	}
	return Tool{}, fmt.Errorf("adb: could not start a server: %s", strings.Join(errs, "; "))
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// usable returns the first tool that runs and is recent enough, else the
// first that runs.
func usable(ctx context.Context, tools []Tool) (Tool, error) {
	var errs []string
	var old []Tool
	for _, t := range tools {
		t, err := check(ctx, t)
		if err == nil {
			return t, nil
		}
		if errors.Is(err, errOld) {
			old = append(old, t)
		}
		errs = append(errs, err.Error())
	}
	if len(old) > 0 {
		return old[0], nil
	}
	if len(errs) == 0 {
		return Tool{}, fmt.Errorf("adb: %w (Pinky looks in the Android SDK, Homebrew, PATH and its own folder; %s names one)", ErrNoADB, EnvADB)
	}
	return Tool{}, fmt.Errorf("adb: no adb that works: %s", strings.Join(errs, "; "))
}

// errOld is check's error for an adb that runs but is older than
// MinTools, which StartServer uses only when no other one works.
var errOld = errors.New("older than platform-tools " + strconv.Itoa(MinTools))

// check readies a tool to run: Pinky's own is copied to the cache first.
// It runs `adb version`, and reports an adb older than MinTools with
// errOld, unless PINKY_ADB names it.
func check(ctx context.Context, t Tool) (Tool, error) {
	if t.Source == FromPinky {
		p, err := stage(t.Path)
		if err != nil {
			return t, fmt.Errorf("%s (%s): %v", t.Path, t.Source, err)
		}
		t.Path = p
	}
	v, err := runVersion(ctx, t.Path)
	if err != nil {
		return t, fmt.Errorf("%s (%s): %v", t.Path, t.Source, err)
	}
	t.Version = v.Tools
	if t.Source != FromEnv && !v.recent() {
		return t, fmt.Errorf("%s (%s) is platform-tools %s, %w", t.Path, t.Source, nonEmpty(v.Tools, "1.0."+strconv.Itoa(v.Server)), errOld)
	}
	return t, nil
}

func nonEmpty(s, or string) string {
	if s != "" {
		return s
	}
	return or
}

// toolVersion is what `adb version` prints, without touching a server:
//
//	Android Debug Bridge version 1.0.41
//	Version 37.0.0-14910828
type toolVersion struct {
	Server int    // the server's protocol version: 41 in "1.0.41"
	Tools  string // the platform-tools version: "37.0.0-14910828"
}

func parseToolVersion(out string) (toolVersion, error) {
	var v toolVersion
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if s, ok := strings.CutPrefix(line, "Android Debug Bridge version "); ok {
			s = strings.TrimSpace(s)
			n, err := strconv.Atoi(s[strings.LastIndex(s, ".")+1:])
			if err != nil {
				return v, fmt.Errorf("bad version %q", s)
			}
			v.Server = n
		} else if s, ok := strings.CutPrefix(line, "Version "); ok {
			v.Tools = strings.TrimSpace(s)
		}
	}
	if v.Server == 0 {
		first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
		return v, fmt.Errorf("not adb's version: %q", first)
	}
	return v, nil
}

// recent reports whether the tool speaks protocol 41 or later and is
// platform-tools MinTools or later.
func (v toolVersion) recent() bool {
	if v.Server < 41 {
		return false
	}
	i := 0
	for i < len(v.Tools) && v.Tools[i] >= '0' && v.Tools[i] <= '9' {
		i++
	}
	major, err := strconv.Atoi(v.Tools[:i])
	return err == nil && major >= MinTools
}

func runVersion(ctx context.Context, path string) (toolVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return toolVersion{}, fmt.Errorf("adb version: %w", err)
	}
	return parseToolVersion(string(out))
}

// env is what Tools reads; tests make their own.
type env struct {
	goos   string
	getenv func(string) string
	home   string // the user's home folder
	exe    string // this program, symlinks resolved
	isFile func(string) bool
	real   func(string) string // a path with symlinks resolved
}

func sysEnv() env {
	e := env{goos: runtime.GOOS, getenv: os.Getenv, isFile: isFile, real: realPath}
	e.home, _ = os.UserHomeDir()
	if exe, err := os.Executable(); err == nil {
		e.exe = realPath(exe)
	}
	return e
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func (e env) tools() []Tool {
	name := "adb"
	if e.goos == "windows" {
		name = "adb.exe"
	}
	if v := e.getenv(EnvADB); v != "" {
		if strings.EqualFold(v, "pinky") {
			if p := e.pinkyADB(); p != "" && e.isFile(p) {
				return []Tool{{Path: p, Source: FromPinky}}
			}
			return nil
		}
		return []Tool{{Path: v, Source: FromEnv}}
	}
	var out []Tool
	seen := map[string]bool{}
	key := func(p string) string {
		k := e.real(p)
		if e.goos == "windows" || e.goos == "darwin" {
			k = strings.ToLower(k) // file systems that ignore case
		}
		return k
	}
	// Pinky's own comes last, and from the cache, even when its folder is
	// on PATH.
	pinky := e.pinkyADB()
	if pinky != "" {
		pinky = key(pinky)
	}
	add := func(p, source string) {
		if p == "" || !e.isFile(p) {
			return
		}
		k := key(p)
		if seen[k] || (k == pinky && source != FromPinky) {
			return
		}
		seen[k] = true
		out = append(out, Tool{Path: p, Source: source})
	}
	for _, sdk := range e.sdks() {
		add(filepath.Join(sdk, "platform-tools", name), FromSDK)
	}
	for _, dir := range e.homebrew() {
		add(filepath.Join(dir, name), FromHomebrew)
	}
	for _, dir := range filepath.SplitList(e.getenv("PATH")) {
		if dir != "" && filepath.IsAbs(dir) {
			add(filepath.Join(dir, name), FromPath)
		}
	}
	add(e.pinkyADB(), FromPinky)
	return out
}

// SDKs lists the folders an Android SDK may be in, as Android Studio
// looks for one: ANDROID_HOME, ANDROID_SDK_ROOT, then where Android Studio
// installs it on this OS. They may not exist.
func SDKs() []string { return sysEnv().sdks() }

// sdks lists the Android SDKs to look in: ANDROID_HOME, ANDROID_SDK_ROOT,
// then where Android Studio installs it.
func (e env) sdks() []string {
	var out []string
	for _, k := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if v := e.getenv(k); v != "" {
			out = append(out, v)
		}
	}
	switch e.goos {
	case "darwin":
		if e.home != "" {
			out = append(out, filepath.Join(e.home, "Library", "Android", "sdk"))
		}
	case "windows":
		if v := e.getenv("LOCALAPPDATA"); v != "" {
			out = append(out, filepath.Join(v, "Android", "Sdk"))
		}
	default:
		if e.home != "" {
			out = append(out, filepath.Join(e.home, "Android", "Sdk"))
		}
	}
	return out
}

// homebrew lists Homebrew's bin folders. An app opened from the Finder or
// the Dock gets the system's PATH, which has none of them.
func (e env) homebrew() []string {
	var out []string
	if v := e.getenv("HOMEBREW_PREFIX"); v != "" {
		out = append(out, filepath.Join(v, "bin"))
	}
	switch e.goos {
	case "darwin":
		out = append(out, "/opt/homebrew/bin", "/usr/local/bin")
	case "linux":
		out = append(out, "/home/linuxbrew/.linuxbrew/bin")
		if e.home != "" {
			out = append(out, filepath.Join(e.home, ".linuxbrew", "bin"))
		}
	}
	return out
}

// pinkyADB is where the adb that comes with Pinky is: Contents/Helpers in
// Pinky.app, else next to the executable.
func (e env) pinkyADB() string {
	if e.exe == "" {
		return ""
	}
	dir := filepath.Dir(e.exe)
	switch e.goos {
	case "darwin":
		// Pinky.app/Contents/MacOS/Pinky -> Pinky.app/Contents/Helpers/adb
		return filepath.Join(filepath.Dir(dir), "Helpers", "adb")
	case "windows":
		return filepath.Join(dir, "adb.exe")
	}
	return filepath.Join(dir, "adb")
}

// pinkyFiles are the files Pinky's own adb needs next to it.
func pinkyFiles(goos string) []string {
	if goos == "windows" {
		return []string{"adb.exe", "AdbWinApi.dll", "AdbWinUsbApi.dll"}
	}
	return []string{"adb"}
}

// cacheDir is where copies of Pinky's own adb go: the user's cache folder
// (Pinky, or pinky on Linux), or $PINKY_HOME/cache.
func cacheDir() (string, error) {
	if h := os.Getenv("PINKY_HOME"); h != "" {
		abs, err := filepath.Abs(h)
		if err != nil {
			return "", err
		}
		return filepath.Join(abs, "cache", "adb"), nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	name := "Pinky"
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		name = "pinky"
	}
	return filepath.Join(base, name, "adb"), nil
}

// stage copies Pinky's own adb (and on Windows the DLLs it loads) into a
// folder of the cache named after their contents, unless the copy is
// there, and returns the copy of the executable. Copies of other versions
// go, unless a server still runs from them (Windows keeps those).
func stage(src string) (string, error) {
	base, err := cacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(src)
	files := pinkyFiles(runtime.GOOS)
	files[0] = filepath.Base(src)
	sums := make([]string, len(files))
	h := sha256.New()
	for i, f := range files {
		sum, err := fileSum(filepath.Join(dir, f))
		if err != nil {
			return "", err
		}
		sums[i] = sum
		io.WriteString(h, sum)
	}
	into := filepath.Join(base, hex.EncodeToString(h.Sum(nil))[:16])
	if err := os.MkdirAll(into, 0o755); err != nil {
		return "", err
	}
	for i, f := range files {
		dst := filepath.Join(into, f)
		if sum, err := fileSum(dst); err == nil && sum == sums[i] {
			continue
		}
		if err := copyFile(filepath.Join(dir, f), dst); err != nil {
			return "", fmt.Errorf("copying Pinky's adb to %s: %w", into, err)
		}
	}
	if entries, err := os.ReadDir(base); err == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != filepath.Base(into) {
				_ = os.RemoveAll(filepath.Join(base, e.Name()))
			}
		}
	}
	return filepath.Join(into, files[0]), nil
}

func fileSum(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFile writes a copy next to dst, then renames it into place.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
