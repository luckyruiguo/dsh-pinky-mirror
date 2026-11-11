package android

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// Android Virtual Devices: the person starts the AVDs Android Studio made
// from Add phone (driver.VirtualLauncher), with the SDK's emulator, and a
// running one is a phone like any other, by its adb serial
// ("emulator-5554").

// avdStarting is how long an AVD Pinky started counts as starting before
// adb lists it.
const avdStarting = 3 * time.Minute

// avdLauncher finds the SDK's emulator and the AVDs, and starts and stops
// them. Tests replace its functions.
type avdLauncher struct {
	goos   string
	getenv func(string) string
	home   string
	sdks   func() []string
	// run runs the emulator (-list-avds) and returns its output.
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// launch starts an AVD with the emulator, detached from Pinky, on
	// the console port given (0: the first free one), and returns an
	// error when it quits at once. When it quits later, launch calls
	// quit with why, nil when it ended well.
	launch func(emulator, avd string, port int, quit func(error)) error
	// kill asks a running emulator to quit, by its adb serial.
	kill func(ctx context.Context, serial string) error
	// portFree reports whether nothing on this computer uses a console
	// port and the adb port after it.
	portFree func(port int) bool
	now      func() time.Time

	mu       sync.Mutex
	starting map[string]time.Time // AVDs Pinky started, by name
	// failed says why an AVD's emulator quit while it was starting, by
	// name, until it starts again or runs.
	failed map[string]string
	// ports is the console port each AVD's emulator last ran on, by
	// name. Started again, it runs there again, so its adb serial, and
	// with it its phone in Pinky, stays the same.
	ports map[string]int
	// started holds the adb serials of the emulators Pinky started on a
	// port it picked, until they quit, with when (Limit).
	started map[string]time.Time
}

// mine reports whether the emulator with this adb serial is one Pinky
// started and that has not quit.
func (l *avdLauncher) mine(serial string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.started[serial]
	return ok
}

// ranAt notes the adb serial an AVD's emulator runs as.
func (l *avdLauncher) ranAt(avd, serial string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ranAtLocked(avd, serial)
}

func (l *avdLauncher) ranAtLocked(avd, serial string) {
	port, err := strconv.Atoi(strings.TrimPrefix(serial, "emulator-"))
	if avd == "" || !emulatorSerial(serial) || err != nil {
		return
	}
	if l.ports == nil {
		l.ports = map[string]int{}
	}
	l.ports[avd] = port
}

func newAVDLauncher() *avdLauncher {
	l := &avdLauncher{goos: runtime.GOOS, getenv: os.Getenv, sdks: adb.SDKs, run: runOutput,
		launch: launchEmulator, kill: killEmulator, portFree: localPortFree, now: time.Now}
	l.home, _ = os.UserHomeDir()
	return l
}

// avdTools is where the emulator is.
type avdTools struct {
	SDK      string // the Android SDK's folder; "" when there is none
	Emulator string // the emulator; "" when the SDK has none
}

// tools finds the first SDK that has the emulator, else the first SDK.
func (l *avdLauncher) tools() avdTools {
	name := "emulator"
	if l.goos == "windows" {
		name = "emulator.exe"
	}
	var first string
	for _, sdk := range l.sdks() {
		if fi, err := os.Stat(sdk); err != nil || !fi.IsDir() {
			continue
		}
		if first == "" {
			first = sdk
		}
		if p := filepath.Join(sdk, "emulator", name); isRegular(p) {
			return avdTools{SDK: sdk, Emulator: p}
		}
	}
	return avdTools{SDK: first}
}

func isRegular(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// avdHome is the folder of the AVDs, as the emulator finds it:
// ANDROID_AVD_HOME, else the avd folder in ANDROID_USER_HOME, in
// ANDROID_EMULATOR_HOME, in ANDROID_SDK_HOME's .android, else in
// ~/.android.
func (l *avdLauncher) avdHome() string {
	if v := l.getenv("ANDROID_AVD_HOME"); v != "" {
		return v
	}
	for _, k := range []string{"ANDROID_USER_HOME", "ANDROID_EMULATOR_HOME"} {
		if v := l.getenv(k); v != "" {
			return filepath.Join(v, "avd")
		}
	}
	if v := l.getenv("ANDROID_SDK_HOME"); v != "" {
		return filepath.Join(v, ".android", "avd")
	}
	return filepath.Join(l.home, ".android", "avd")
}

// avdNameRE is an AVD's name, as avdmanager allows it.
var avdNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// parseAVDList reads `emulator -list-avds`: one name a line, among the
// emulator's own INFO and WARNING lines.
func parseAVDList(out string) []string {
	var names []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if avdNameRE.MatchString(l) && !slices.Contains(names, l) {
			names = append(names, l)
		}
	}
	return names
}

// avd is what an AVD's files say of it.
type avd struct {
	Name    string // its name, the emulator's -avd
	Display string // the name Android Studio shows
	Model   string // the device it is made as: "Pixel 6"
	OS      string // the Android version: "16"
	SysDir  string // its system image, in the SDK: "system-images/android-36/google_apis/arm64-v8a/"
}

// readAVD reads <home>/<name>.ini and the config.ini in the folder it
// points at. Missing files leave fields empty.
func readAVD(home, name string) avd {
	a := avd{Name: name}
	ini := readINI(filepath.Join(home, name+".ini"))
	dir := ini["path"]
	if dir == "" || !isDir(dir) {
		if rel := ini["path.rel"]; rel != "" {
			dir = filepath.Join(filepath.Dir(home), filepath.FromSlash(rel))
		}
	}
	if dir == "" {
		dir = filepath.Join(home, name+".avd")
	}
	cfg := readINI(filepath.Join(dir, "config.ini"))
	a.Display = cfg["avd.ini.displayname"]
	if a.Display == "" {
		a.Display = strings.ReplaceAll(name, "_", " ")
	}
	a.Model = deviceModel(cfg["hw.device.name"])
	a.SysDir = cfg["image.sysdir.1"]
	target := cfg["target"]
	if target == "" {
		target = ini["target"]
	}
	if target == "" {
		// system-images/android-36/google_apis/arm64-v8a/
		if parts := strings.Split(filepath.ToSlash(a.SysDir), "/"); len(parts) > 1 {
			target = parts[1]
		}
	}
	a.OS = androidVersion(target)
	return a
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// readINI reads an AVD's key=value file; nil when it cannot.
func readINI(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseINI(string(b))
}

func parseINI(s string) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if k = strings.TrimSpace(k); !ok || k == "" || strings.HasPrefix(k, "#") {
			continue
		}
		m[k] = strings.TrimSpace(v)
	}
	return m
}

// deviceModel puts an AVD's hw.device.name into words: "pixel_9_pro" is
// "Pixel 9 Pro", "medium_phone" "Medium Phone"; a name with capitals
// ("Nexus 5X") stays.
func deviceModel(hw string) string {
	if hw == "" || strings.ToLower(hw) != hw {
		return hw
	}
	words := strings.FieldsFunc(hw, func(r rune) bool { return r == '_' || r == ' ' || r == '-' })
	for i, w := range words {
		switch w {
		case "xl", "tv", "xr":
			words[i] = strings.ToUpper(w)
		default:
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// androidVersions maps API levels to the Android versions people know.
var androidVersions = map[int]string{
	21: "5.0", 22: "5.1", 23: "6", 24: "7.0", 25: "7.1", 26: "8.0", 27: "8.1", 28: "9",
	29: "10", 30: "11", 31: "12", 32: "12L", 33: "13", 34: "14", 35: "15", 36: "16", 37: "17",
}

// androidVersion is the Android version of an AVD's target, "android-36"
// or "android-37.0"; a target past this table, or a preview's code name,
// stays as it is.
func androidVersion(target string) string {
	t, ok := strings.CutPrefix(target, "android-")
	if !ok || t == "" {
		return ""
	}
	major, _, _ := strings.Cut(t, ".")
	if n, err := strconv.Atoi(major); err == nil {
		if v := androidVersions[n]; v != "" {
			return v
		}
		return "API " + t
	}
	return t
}

// emulatorSerial reports whether an adb serial is an emulator's.
func emulatorSerial(serial string) bool { return strings.HasPrefix(serial, "emulator-") }

// avdSerials maps the running AVDs to their emulators' adb serials, from
// the property each emulator reads its AVD's name into (deviceProps). An
// AVD started twice, read-only, has the lowest serial.
func (p *Provider) avdSerials(ctx context.Context) (map[string]string, error) {
	entries, err := p.devices(ctx)
	if err != nil {
		return nil, wrapErr(err)
	}
	slices.SortFunc(entries, func(a, b adb.DeviceEntry) int { return strings.Compare(a.Serial, b.Serial) })
	out := map[string]string{}
	for _, e := range entries {
		if !emulatorSerial(e.Serial) || e.State != adb.StateDevice {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		pr, err := p.deviceProps(pctx, e)
		cancel()
		if err == nil && pr.AVD != "" && out[pr.AVD] == "" {
			out[pr.AVD] = e.Serial
		}
	}
	return out, nil
}

// avdList is what VirtualPhones finds, for StartVirtual too.
func (p *Provider) avdList(ctx context.Context) (driver.VirtualPhones, avdTools, error) {
	l := p.avd
	vp := driver.VirtualPhones{Platform: driver.Android}
	t := l.tools()
	vp.Where = t.SDK
	switch {
	case t.SDK == "":
		vp.Missing = "android_sdk"
		vp.Detail = "Android emulators need the Android SDK, which Android Studio installs; Pinky found none (ANDROID_HOME, ANDROID_SDK_ROOT, or where Android Studio puts it)."
		return vp, t, nil
	case t.Emulator == "":
		vp.Missing = "emulator"
		vp.Detail = "The Android SDK in " + t.SDK + " has no emulator; install it in Android Studio's SDK Manager (Android Emulator)."
		return vp, t, nil
	}
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	out, err := l.run(lctx, t.Emulator, "-list-avds")
	cancel()
	if err != nil {
		return vp, t, fmt.Errorf("list the Android virtual devices: %w", err)
	}
	running, err := p.avdSerials(ctx)
	if err != nil {
		// No adb server: none runs that Pinky can use.
		p.cfg.logf("android: running emulators: %v", err)
	}
	home := l.avdHome()
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, name := range parseAVDList(string(out)) {
		a := readAVD(home, name)
		v := driver.VirtualPhone{ID: name, Name: a.Display, Model: a.Model, OSVersion: a.OS}
		if s := running[name]; s != "" {
			v.Running, v.DeviceID = true, s
			delete(l.starting, name)
			delete(l.failed, name)
			l.ranAtLocked(name, s)
		} else if at, ok := l.starting[name]; ok {
			if now.Sub(at) < avdStarting {
				v.Starting = true
			} else {
				delete(l.starting, name)
			}
		} else {
			v.Failed = l.failed[name]
		}
		if a.SysDir != "" && !isDir(filepath.Join(t.SDK, filepath.FromSlash(a.SysDir))) {
			v.Unavailable = "its system image is not installed (" + strings.TrimSuffix(filepath.ToSlash(a.SysDir), "/") + ")"
		}
		vp.Phones = append(vp.Phones, v)
	}
	slices.SortStableFunc(vp.Phones, func(a, b driver.VirtualPhone) int {
		if a.Running != b.Running {
			if a.Running {
				return -1
			}
			return 1
		}
		return 0
	})
	return vp, t, nil
}

// VirtualPhones implements driver.VirtualLauncher: the AVDs the SDK's
// emulator lists, those that run first.
func (p *Provider) VirtualPhones(ctx context.Context) (driver.VirtualPhones, error) {
	vp, _, err := p.avdList(ctx)
	return vp, err
}

// StartVirtual implements driver.VirtualLauncher: it starts the AVD with
// the emulator, in a window of its own that stays when Pinky quits. One
// that runs, or that Pinky is starting, is left as it is.
func (p *Provider) StartVirtual(ctx context.Context, id string) error {
	vp, t, err := p.avdList(ctx)
	if err != nil {
		return err
	}
	if vp.Missing != "" {
		return errors.New(vp.Detail)
	}
	i := slices.IndexFunc(vp.Phones, func(v driver.VirtualPhone) bool { return v.ID == id })
	if i < 0 {
		return fmt.Errorf("Android virtual device %q: %w", id, driver.ErrNotFound)
	}
	v := vp.Phones[i]
	if v.Running || v.Starting {
		return nil
	}
	if v.Unavailable != "" {
		return fmt.Errorf("%s cannot start: %s", v.Name, v.Unavailable)
	}
	l := p.avd
	l.mu.Lock()
	delete(l.failed, id)
	l.mu.Unlock()
	// The emulator may quit while it boots, as when it hangs and its own
	// watchdog ends it: then it no longer counts as starting, and Add
	// phone says why.
	var at time.Time
	var serial string // the emulator's adb serial, when Pinky picked its port
	ready := make(chan struct{})
	quit := func(err error) {
		<-ready
		l.mu.Lock()
		defer l.mu.Unlock()
		if got, ok := l.started[serial]; ok && got.Equal(at) {
			delete(l.started, serial)
		}
		if got, ok := l.starting[id]; !ok || !got.Equal(at) {
			return // it ran, or was started again
		}
		delete(l.starting, id)
		if err != nil {
			if l.failed == nil {
				l.failed = map[string]string{}
			}
			l.failed[id] = err.Error()
		}
		p.cfg.logf("android: the emulator for %s quit while it started: %v", id, err)
	}
	limited := p.limit() != nil
	port := p.freePort(ctx, id, 0)
	if limited && port == 0 {
		close(ready)
		return fmt.Errorf("start %s: no free port for its emulator", v.Name)
	}
	err = l.launch(t.Emulator, id, port, quit)
	if err != nil && port != 0 {
		// Its port was taken after all: any free one, or under Limit
		// the next one Pinky finds free.
		failed := port
		port = 0
		if limited {
			port = p.freePort(ctx, id, failed)
		}
		if !limited || port != 0 {
			err = l.launch(t.Emulator, id, port, quit)
		}
	}
	if err != nil {
		close(ready)
		return fmt.Errorf("start %s: %w", v.Name, err)
	}
	p.cfg.logf("android: started the emulator for %s", id)
	l.mu.Lock()
	if l.starting == nil {
		l.starting = map[string]time.Time{}
	}
	at = l.now()
	l.starting[id] = at
	if port != 0 {
		serial = "emulator-" + strconv.Itoa(port)
		if l.started == nil {
			l.started = map[string]time.Time{}
		}
		l.started[serial] = at
	}
	l.mu.Unlock()
	close(ready)
	return nil
}

// The console ports an emulator may run on: even ones, each with the next
// one for adb.
const (
	firstConsolePort = 5554
	lastConsolePort  = 5682
)

// freePort is the console port to start the AVD's emulator on, above
// after: the one it last ran on while no other emulator uses it, else 0
// (the emulator takes the first free one). Under Limit, it is one Pinky
// finds free, so that Pinky knows which emulator it started (mine); 0
// when there is none.
func (p *Provider) freePort(ctx context.Context, id string, after int) int {
	limited := p.limit() != nil
	p.avd.mu.Lock()
	port := p.avd.ports[id]
	p.avd.mu.Unlock()
	if port <= after {
		port = 0
	}
	if port == 0 && !limited {
		return 0
	}
	// Every emulator adb lists, those Pinky may not use too: listing
	// them asks them nothing.
	entries, err := p.cfg.Client.Devices(ctx)
	if err != nil && !limited {
		return 0
	}
	taken := func(port int) bool {
		for _, e := range entries {
			// An emulator takes its console port and the next for adb.
			if n, err := strconv.Atoi(strings.TrimPrefix(e.Serial, "emulator-")); err == nil && emulatorSerial(e.Serial) && n >= port-1 && n <= port+1 {
				return true
			}
		}
		return false
	}
	if port != 0 && !taken(port) {
		return port
	}
	if !limited {
		return 0
	}
	for port := firstConsolePort; port <= lastConsolePort; port += 2 {
		if port > after && !taken(port) && p.avd.portFree(port) {
			return port
		}
	}
	return 0
}

// localPortFree reports whether a console port and the adb port after it
// are free on this computer.
func localPortFree(port int) bool {
	for _, n := range []int{port, port + 1} {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(n)))
		if err != nil {
			return false
		}
		ln.Close()
	}
	return true
}

// StopVirtual implements driver.VirtualLauncher: it asks the AVD's
// emulator to quit, as closing its window does.
func (p *Provider) StopVirtual(ctx context.Context, id string) error {
	running, err := p.avdSerials(ctx)
	if err != nil {
		return err
	}
	serial := running[id]
	if serial == "" {
		return nil
	}
	p.avd.mu.Lock()
	delete(p.avd.starting, id)
	p.avd.mu.Unlock()
	return p.avd.kill(ctx, serial)
}

func runOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	hideConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
	}
	return out, err
}

// launchEmulator starts `emulator -avd <name> -no-audio`, with -port
// unless port is 0, in a session of its own, with its output in a log file
// in the temporary folder, and waits a few seconds for it to quit at once,
// as when the AVD runs already or its system image is missing. When it
// quits later, quit gets why.
//
// Without -no-audio the emulator opens the computer's microphone while it
// boots, though it passes the guest only silence: a Bluetooth headset then
// switches to its call mode, and one that takes 15 seconds to do so stalls
// the emulator until its own hang detector ends it.
func launchEmulator(emulator, name string, port int, quit func(error)) error {
	logPath := filepath.Join(os.TempDir(), "pinky-emulator-"+name+".log")
	f, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer f.Close()
	args := emulatorArgs(name, port)
	cmd := exec.Command(emulator, args...)
	cmd.Dir = filepath.Dir(emulator)
	cmd.Stdout, cmd.Stderr = f, f
	detachEmulator(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }() // also reaps it when it quits later
	select {
	case err := <-exited:
		if err == nil {
			err = errors.New("the emulator quit")
		}
		return emulatorQuit(err, logPath)
	case <-time.After(3 * time.Second):
		go func() {
			if err := <-exited; err != nil {
				quit(emulatorQuit(err, logPath))
			} else {
				quit(nil)
			}
		}()
		return nil
	}
}

// emulatorArgs are the emulator's arguments to start the AVD name on the
// console port given, or the first free one when port is 0.
func emulatorArgs(name string, port int) []string {
	args := []string{"-avd", name, "-no-audio"}
	if port != 0 {
		args = append(args, "-port", strconv.Itoa(port))
	}
	return args
}

// emulatorQuit is err with the emulator's last error line from its log.
func emulatorQuit(err error, logPath string) error {
	b, _ := os.ReadFile(logPath)
	if msg := lastLine(string(b)); msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// lastLine is the emulator's last error line, else its last line,
// without its level ("ERROR   | ").
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(out, "\r", "")), "\n")
	line := lines[len(lines)-1]
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "ERROR") || strings.Contains(lines[i], "FATAL") {
			line = lines[i]
			break
		}
	}
	if level, msg, ok := strings.Cut(line, "|"); ok {
		switch strings.TrimSpace(level) {
		case "FATAL", "ERROR", "WARNING", "INFO", "DEBUG", "VERBOSE":
			line = msg
		}
	}
	return strings.TrimSpace(line)
}

// killEmulator asks the emulator with the adb serial "emulator-<port>"
// to quit through its console on that port, as `adb emu kill` does.
func killEmulator(ctx context.Context, serial string) error {
	home, _ := os.UserHomeDir()
	return consoleKill(ctx, serial, filepath.Join(home, ".emulator_console_auth_token"))
}

// consoleKill speaks the emulator console's protocol: a banner ending in
// "OK", "auth <token>" when the banner asks for it (the token is in
// tokenFile), then "kill".
func consoleKill(ctx context.Context, serial, tokenFile string) error {
	port, ok := strings.CutPrefix(serial, "emulator-")
	if !ok {
		return fmt.Errorf("%s is not an emulator", serial)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return fmt.Errorf("the emulator's console: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(10 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	conn.SetDeadline(deadline)
	r := bufio.NewReader(conn)
	// reply reads up to the console's "OK" or "KO: ..." line.
	reply := func() (string, error) {
		var b strings.Builder
		for {
			line, err := r.ReadString('\n')
			b.WriteString(line)
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "OK") {
				return b.String(), nil
			}
			if strings.HasPrefix(line, "KO") {
				return b.String(), fmt.Errorf("the emulator's console: %s", line)
			}
			if err != nil {
				return b.String(), err
			}
		}
	}
	banner, err := reply()
	if err != nil {
		return fmt.Errorf("the emulator's console: %w", err)
	}
	if strings.Contains(banner, "Authentication required") {
		token, err := os.ReadFile(tokenFile)
		if err != nil {
			return fmt.Errorf("the emulator's console token: %w", err)
		}
		if _, err := fmt.Fprintf(conn, "auth %s\r\n", strings.TrimSpace(string(token))); err != nil {
			return err
		}
		if _, err := reply(); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(conn, "kill\r\n"); err != nil {
		return err
	}
	// It says "OK: killing emulator, bye bye" and hangs up; either will do.
	if _, err := reply(); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
		return err
	}
	return nil
}
