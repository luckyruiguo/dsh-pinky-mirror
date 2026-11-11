package android

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

func TestParseAVDList(t *testing.T) {
	out := "INFO    | Storing crashdata in: /tmp/android-me/emu-crash-37.2.13.db\n" +
		"Pixel_9_API_36\npinky-api29\n\nWARNING | something\nPixel_9_API_36\n"
	if got := parseAVDList(out); !slices.Equal(got, []string{"Pixel_9_API_36", "pinky-api29"}) {
		t.Errorf("parseAVDList = %q", got)
	}
}

func TestAndroidVersion(t *testing.T) {
	for in, want := range map[string]string{
		"android-29": "10", "android-32": "12L", "android-36": "16", "android-36.1": "16",
		"android-37.0": "17", "android-40": "API 40", "android-Baklava": "Baklava", "": "", "Google Inc.:Google APIs:23": "",
	} {
		if got := androidVersion(in); got != want {
			t.Errorf("androidVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeviceModel(t *testing.T) {
	for in, want := range map[string]string{
		"pixel_6": "Pixel 6", "pixel_9_pro_xl": "Pixel 9 Pro XL", "medium_phone": "Medium Phone",
		"pixel": "Pixel", "Nexus 5X": "Nexus 5X", "": "",
	} {
		if got := deviceModel(in); got != want {
			t.Errorf("deviceModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// avdFixture is an Android SDK with the emulator and one system image, and
// an AVD home with three AVDs: pinky-api36, made as a Pixel 6; Medium
// Phone, named in Android Studio, whose image is missing; and pinky-api29,
// whose folder moved (path.rel finds it).
type avdFixture struct {
	sdk, home string
	mu        sync.Mutex
	launched  []string
	ports     []int // the port of each launch
	killed    []string
	launchErr error
	portErr   error         // a launch on a given port fails so
	quits     []func(error) // each launch's quit, to end its emulator
}

func newAVDFixture(t *testing.T, p *Provider) *avdFixture {
	t.Helper()
	root := t.TempDir()
	fx := &avdFixture{sdk: filepath.Join(root, "sdk"), home: filepath.Join(root, "user", ".android", "avd")}
	write := func(path, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(fx.sdk, "emulator", "emulator"), "")
	if err := os.MkdirAll(filepath.Join(fx.sdk, "system-images", "android-36", "google_apis", "arm64-v8a"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(fx.home, "pinky-api36.ini"), "avd.ini.encoding=UTF-8\npath="+filepath.Join(fx.home, "pinky-api36.avd")+"\ntarget=android-36\n")
	write(filepath.Join(fx.home, "pinky-api36.avd", "config.ini"), "hw.device.name=pixel_6\nimage.sysdir.1=system-images/android-36/google_apis/arm64-v8a/\n")
	write(filepath.Join(fx.home, "Medium_Phone_API_37.ini"), "path="+filepath.Join(fx.home, "Medium_Phone_API_37.avd")+"\n")
	write(filepath.Join(fx.home, "Medium_Phone_API_37.avd", "config.ini"),
		"avd.ini.displayname = Medium Phone API 37\nhw.device.name = medium_phone\nimage.sysdir.1 = system-images/android-37.0/google_apis_playstore/arm64-v8a/\ntarget = android-37.0\n")
	write(filepath.Join(fx.home, "pinky-api29.ini"), "path=/gone/pinky-api29.avd\npath.rel=avd/pinky-api29.avd\ntarget=android-29\n")
	write(filepath.Join(fx.home, "pinky-api29.avd", "config.ini"), "hw.device.name=pixel\n")

	l := p.avd
	l.goos = "darwin"
	l.getenv = func(string) string { return "" }
	l.home = filepath.Join(root, "user")
	l.sdks = func() []string { return []string{filepath.Join(root, "none"), fx.sdk} }
	l.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != filepath.Join(fx.sdk, "emulator", "emulator") || !slices.Equal(args, []string{"-list-avds"}) {
			t.Errorf("ran %s %q", name, args)
		}
		return []byte("INFO    | Storing crashdata in: /tmp\nMedium_Phone_API_37\npinky-api29\npinky-api36\n"), nil
	}
	l.launch = func(emulator, avd string, port int, quit func(error)) error {
		fx.mu.Lock()
		defer fx.mu.Unlock()
		fx.launched = append(fx.launched, avd)
		fx.ports = append(fx.ports, port)
		fx.quits = append(fx.quits, quit)
		if port != 0 && fx.portErr != nil {
			return fx.portErr
		}
		return fx.launchErr
	}
	l.portFree = func(int) bool { return true }
	l.kill = func(_ context.Context, serial string) error {
		fx.mu.Lock()
		defer fx.mu.Unlock()
		fx.killed = append(fx.killed, serial)
		return nil
	}
	return fx
}

func (fx *avdFixture) calls() (launched, killed []string) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return slices.Clone(fx.launched), slices.Clone(fx.killed)
}

// TestAVDs: Add phone lists the AVDs, the running one first with its
// emulator's serial; the emulator is a virtual phone named as its AVD;
// starting one launches the emulator once, and stopping one asks its
// emulator to quit.
func TestAVDs(t *testing.T) {
	f, p := newFakeADB(t)
	fx := newAVDFixture(t, p)
	f.set(func(f *fakeADB) {
		f.devices = []string{"emulator-5554\tdevice product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a transport_id:3"}
		f.props = map[string]string{"emulator-5554": "sdk_gphone64_arm64\n16\n36\nsdk_gphone64_arm64\nEMULATOR36X1X11X0\npinky-boot=\npinky-avd=pinky-api36\n"}
	})
	ctx := context.Background()

	infos, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != "emulator-5554" || !infos[0].Virtual || infos[0].VirtualID != "pinky-api36" || infos[0].Name != "pinky-api36" ||
		infos[0].Model != "Pixel 6" || infos[0].OSVersion != "16" || infos[0].Connection != "usb" {
		t.Fatalf("listed %+v", infos)
	}

	vp, err := p.VirtualPhones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if vp.Platform != driver.Android || vp.Missing != "" || vp.Where != fx.sdk || len(vp.Phones) != 3 {
		t.Fatalf("VirtualPhones = %+v", vp)
	}
	want := []driver.VirtualPhone{
		{ID: "pinky-api36", Name: "pinky-api36", Model: "Pixel 6", OSVersion: "16", Running: true, DeviceID: "emulator-5554"},
		{ID: "Medium_Phone_API_37", Name: "Medium Phone API 37", Model: "Medium Phone", OSVersion: "17",
			Unavailable: "its system image is not installed (system-images/android-37.0/google_apis_playstore/arm64-v8a)"},
		{ID: "pinky-api29", Name: "pinky-api29", Model: "Pixel", OSVersion: "10"},
	}
	if !slices.Equal(vp.Phones, want) {
		t.Errorf("phones\n%+v\nwant\n%+v", vp.Phones, want)
	}

	if err := p.StartVirtual(ctx, "nope"); !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("start an unknown AVD: %v", err)
	}
	if err := p.StartVirtual(ctx, "pinky-api36"); err != nil {
		t.Errorf("start a running AVD: %v", err)
	}
	if err := p.StartVirtual(ctx, "Medium_Phone_API_37"); err == nil || !strings.Contains(err.Error(), "system image") {
		t.Errorf("start an AVD without its image: %v", err)
	}
	if err := p.StartVirtual(ctx, "pinky-api29"); err != nil {
		t.Fatal(err)
	}
	if err := p.StartVirtual(ctx, "pinky-api29"); err != nil {
		t.Fatal(err)
	}
	vp, _ = p.VirtualPhones(ctx)
	if v := vp.Phones[2]; v.ID != "pinky-api29" || !v.Starting || v.Running {
		t.Errorf("starting: %+v", v)
	}
	// Starting for three minutes at most.
	p.avd.now = func() time.Time { return time.Now().Add(avdStarting + time.Second) }
	vp, _ = p.VirtualPhones(ctx)
	if v := vp.Phones[2]; v.Starting {
		t.Errorf("still starting: %+v", v)
	}
	fx.launchErr = errors.New("the emulator quit: ERROR | Running multiple emulators with the same AVD")
	if err := p.StartVirtual(ctx, "pinky-api29"); err == nil || !strings.Contains(err.Error(), "multiple emulators") {
		t.Errorf("a launch that fails: %v", err)
	}

	if err := p.StopVirtual(ctx, "pinky-api36"); err != nil {
		t.Fatal(err)
	}
	if err := p.StopVirtual(ctx, "pinky-api29"); err != nil {
		t.Errorf("stop an AVD that does not run: %v", err)
	}
	launched, killed := fx.calls()
	if !slices.Equal(launched, []string{"pinky-api29", "pinky-api29"}) || !slices.Equal(killed, []string{"emulator-5554"}) {
		t.Errorf("launched %q, killed %q", launched, killed)
	}
}

// TestAVDQuitWhileStarting: an emulator that quits while it boots no
// longer counts as starting, and Add phone says why until it starts again;
// one that quits well says nothing, and a launch started again before
// keeps counting only its own emulator.
func TestAVDQuitWhileStarting(t *testing.T) {
	_, p := newFakeADB(t)
	fx := newAVDFixture(t, p)
	ctx := context.Background()
	phone := func() driver.VirtualPhone {
		t.Helper()
		vp, err := p.VirtualPhones(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vp.Phones {
			if v.ID == "pinky-api29" {
				return v
			}
		}
		t.Fatal("no pinky-api29")
		return driver.VirtualPhone{}
	}
	quit := func(i int, err error) {
		fx.mu.Lock()
		q := fx.quits[i]
		fx.mu.Unlock()
		q(err)
	}

	if err := p.StartVirtual(ctx, "pinky-api29"); err != nil {
		t.Fatal(err)
	}
	quit(0, errors.New("signal: abort trap: detected a hanging thread 'QEMU2 main loop'. No response for 15020 ms"))
	if v := phone(); v.Starting || v.Running || !strings.Contains(v.Failed, "hanging thread") {
		t.Errorf("after a crash: %+v", v)
	}

	// Started again: starting, and what failed before is gone.
	if err := p.StartVirtual(ctx, "pinky-api29"); err != nil {
		t.Fatal(err)
	}
	if v := phone(); !v.Starting || v.Failed != "" {
		t.Errorf("started again: %+v", v)
	}
	quit(1, nil) // the person closed its window
	if v := phone(); v.Starting || v.Failed != "" {
		t.Errorf("after it quit well: %+v", v)
	}

	// An emulator that outlived its starting time and quits says nothing
	// about the one started after it.
	if err := p.StartVirtual(ctx, "pinky-api29"); err != nil {
		t.Fatal(err)
	}
	p.avd.now = func() time.Time { return time.Now().Add(avdStarting + time.Second) }
	if err := p.StartVirtual(ctx, "pinky-api29"); err != nil {
		t.Fatal(err)
	}
	quit(2, errors.New("signal: killed"))
	if v := phone(); !v.Starting || v.Failed != "" {
		t.Errorf("the earlier emulator's end changed the later one: %+v", v)
	}
}

// TestLastLine: what an emulator that quit says, without its level.
func TestLastLine(t *testing.T) {
	for _, c := range []struct{ log, want string }{
		{"INFO         | Android emulator version 37.2.13.0\nERROR        | detected a hanging thread 'QEMU2 main loop'. No response for 18284 ms\nINFO         | Advertising in: x.ini\n",
			"detected a hanging thread 'QEMU2 main loop'. No response for 18284 ms"},
		{"INFO    | Storing crashdata\nFATAL   | Running multiple emulators with the same AVD\n", "Running multiple emulators with the same AVD"},
		{"PANIC: Missing emulator engine program for 'x86' CPU.\n", "PANIC: Missing emulator engine program for 'x86' CPU."},
		{"INFO | a | b\n", "a | b"},
	} {
		if got := lastLine(c.log); got != c.want {
			t.Errorf("lastLine(%q) = %q, want %q", c.log, got, c.want)
		}
	}
}

// TestAVDSamePort: an emulator started again runs on the port it ran on,
// so its phone keeps its id, unless another emulator took that port; a
// launch on the port that fails starts it on any.
func TestAVDSamePort(t *testing.T) {
	f, p := newFakeADB(t)
	fx := newAVDFixture(t, p)
	// running sets the emulators adb lists: serial, AVD, serial, AVD…
	// Each is a new connection, as an emulator started again is.
	transport := 0
	running := func(pairs ...string) {
		f.set(func(f *fakeADB) {
			f.devices, f.props = nil, map[string]string{}
			for i := 0; i < len(pairs); i += 2 {
				s, avd := pairs[i], pairs[i+1]
				transport++
				f.devices = append(f.devices, s+"\tdevice product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a transport_id:"+strconv.Itoa(transport))
				f.props[s] = "sdk_gphone64_arm64\n16\n36\nsdk_gphone64_arm64\nEMULATOR36X1X11X0\npinky-boot=\npinky-avd=" + avd + "\n"
			}
		})
	}
	ctx := context.Background()
	// Seen running on 5560 by the phone list alone.
	running("emulator-5560", "pinky-api29")
	if _, err := p.List(ctx); err != nil {
		t.Fatal(err)
	}
	running()
	if err := p.StartVirtual(ctx, "pinky-api29"); err != nil {
		t.Fatal(err)
	}
	// Taken by another emulator meanwhile: any port.
	p.avd.mu.Lock()
	p.avd.starting = nil
	p.avd.mu.Unlock()
	running("emulator-5560", "pinky-api36")
	if err := p.StartVirtual(ctx, "pinky-api29"); err != nil {
		t.Fatal(err)
	}
	// A launch on the port that fails tries again on any.
	p.avd.mu.Lock()
	p.avd.starting = nil
	p.avd.mu.Unlock()
	running()
	fx.portErr = errors.New("the emulator quit: ERROR | port 5560 is used")
	if err := p.StartVirtual(ctx, "pinky-api36"); err != nil {
		t.Fatal(err)
	}
	launched, _ := fx.calls()
	fx.mu.Lock()
	ports := slices.Clone(fx.ports)
	fx.mu.Unlock()
	if !slices.Equal(launched, []string{"pinky-api29", "pinky-api29", "pinky-api36", "pinky-api36"}) || !slices.Equal(ports, []int{5560, 0, 5560, 0}) {
		t.Errorf("launched %q on %v", launched, ports)
	}
}

// TestAVDsMissing: without an SDK, or with one without the emulator, Add
// phone says what is missing.
func TestAVDsMissing(t *testing.T) {
	_, p := newFakeADB(t)
	fx := newAVDFixture(t, p)
	p.avd.sdks = func() []string { return []string{filepath.Join(fx.sdk, "none")} }
	vp, err := p.VirtualPhones(context.Background())
	if err != nil || vp.Missing != "android_sdk" || vp.Detail == "" || len(vp.Phones) != 0 {
		t.Errorf("no SDK: %+v, %v", vp, err)
	}
	if err := p.StartVirtual(context.Background(), "pinky-api29"); err == nil {
		t.Error("started an AVD without an SDK")
	}
	if err := os.Remove(filepath.Join(fx.sdk, "emulator", "emulator")); err != nil {
		t.Fatal(err)
	}
	p.avd.sdks = func() []string { return []string{fx.sdk} }
	vp, err = p.VirtualPhones(context.Background())
	if err != nil || vp.Missing != "emulator" || vp.Where != fx.sdk {
		t.Errorf("no emulator: %+v, %v", vp, err)
	}
}

func TestAVDHome(t *testing.T) {
	l := &avdLauncher{home: "/Users/me"}
	for env, want := range map[string]string{
		"":                         "/Users/me/.android/avd",
		"ANDROID_AVD_HOME=/avds":   "/avds",
		"ANDROID_USER_HOME=/u":     "/u/avd",
		"ANDROID_EMULATOR_HOME=/e": "/e/avd",
		"ANDROID_SDK_HOME=/s":      "/s/.android/avd",
		"ANDROID_USER_HOME=/u ANDROID_SDK_HOME=/s": "/u/avd",
	} {
		vars := map[string]string{}
		for _, kv := range strings.Fields(env) {
			k, v, _ := strings.Cut(kv, "=")
			vars[k] = v
		}
		l.getenv = func(k string) string { return vars[k] }
		if got := filepath.ToSlash(l.avdHome()); got != want {
			t.Errorf("%q: avdHome = %q, want %q", env, got, want)
		}
	}
}

// TestConsoleKill: the emulator's console asks for the token from the
// file, then quits on kill, as `adb emu kill` has it.
func TestConsoleKill(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	token := filepath.Join(t.TempDir(), ".emulator_console_auth_token")
	if err := os.WriteFile(token, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := make(chan []string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte("Android Console: Authentication required\r\nAndroid Console: type 'auth <auth_token>' to authenticate\r\n" +
			"Android Console: you can find your <auth_token> in \r\n'" + token + "'\r\nOK\r\n"))
		r := bufio.NewReader(c)
		var lines []string
		for range 2 {
			l, err := r.ReadString('\n')
			if err != nil {
				break
			}
			lines = append(lines, strings.TrimSpace(l))
			if strings.HasPrefix(l, "auth ") {
				c.Write([]byte("Android Console: type 'help' for a list of commands\r\nOK\r\n"))
			} else {
				c.Write([]byte("OK: killing emulator, bye bye\r\n"))
			}
		}
		got <- lines
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	if err := consoleKill(context.Background(), "emulator-"+port, token); err != nil {
		t.Fatal(err)
	}
	if lines := <-got; !slices.Equal(lines, []string{"auth s3cret", "kill"}) {
		t.Errorf("the console heard %q", lines)
	}
	if err := consoleKill(context.Background(), "3e8be48e", token); err == nil {
		t.Error("killed a phone")
	}
}

func TestEmulatorArgs(t *testing.T) {
	for _, c := range []struct {
		port int
		want string
	}{
		{0, "-avd Pixel_9 -no-audio"},
		{5560, "-avd Pixel_9 -no-audio -port 5560"},
	} {
		if got := strings.Join(emulatorArgs("Pixel_9", c.port), " "); got != c.want {
			t.Errorf("port %d: %q, want %q", c.port, got, c.want)
		}
	}
}

// TestAVDBooting: an emulator adb lists before it has finished booting is
// offline and still starting, not a phone named after its settings
// command's error; once booted it is online with its AVD's name.
func TestAVDBooting(t *testing.T) {
	f, p := newFakeADB(t)
	newAVDFixture(t, p)
	ctx := context.Background()
	if err := p.StartVirtual(ctx, "pinky-api29"); err != nil {
		t.Fatal(err)
	}
	props := func(booted string) {
		f.set(func(f *fakeADB) {
			f.devices = []string{"emulator-5554\tdevice product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a transport_id:3"}
			f.props = map[string]string{"emulator-5554": "sdk_gphone64_arm64\n10\n29\ncmd: Can't find service: settings\nEMULATOR29X1X11X0\npinky-boot=\npinky-avd=pinky-api29\npinky-booted=" + booted + "\n"}
		})
	}

	props("")
	infos, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].State != driver.StateOffline || !strings.Contains(infos[0].Detail, "still starting") || strings.Contains(infos[0].Name, "service") {
		t.Fatalf("booting: %+v", infos)
	}
	if v := avdNamed(t, p, "pinky-api29"); !v.Starting || v.Running {
		t.Errorf("booting: %+v", v)
	}
	if _, err := p.Open(ctx, "emulator-5554"); !errors.Is(err, driver.ErrOffline) {
		t.Errorf("open while booting: %v", err)
	}

	props("1")
	f.set(func(f *fakeADB) {
		f.props["emulator-5554"] = strings.Replace(f.props["emulator-5554"], "cmd: Can't find service: settings", "null", 1)
	})
	infos, err = p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].State != driver.StateOnline || infos[0].Name != "pinky-api29" || infos[0].VirtualID != "pinky-api29" {
		t.Fatalf("booted: %+v", infos)
	}
	if v := avdNamed(t, p, "pinky-api29"); !v.Running || v.Starting || v.DeviceID != "emulator-5554" {
		t.Errorf("booted: %+v", v)
	}
}

// avdNamed is the AVD id as Add phone lists it.
func avdNamed(t *testing.T, p *Provider, id string) driver.VirtualPhone {
	t.Helper()
	vp, err := p.VirtualPhones(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vp.Phones {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("no AVD %s in %+v", id, vp.Phones)
	return driver.VirtualPhone{}
}
