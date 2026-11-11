package ios

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

const simDevicesJSON = `{
  "devices" : {
    "com.apple.CoreSimulator.SimRuntime.iOS-26-5" : [
      {"udid" : "AAA", "name" : "iPhone 17e", "state" : "Booted", "isAvailable" : true,
       "deviceTypeIdentifier" : "com.apple.CoreSimulator.SimDeviceType.iPhone-17e", "lastUsedAt" : "2026-11-09T09:59:50Z"},
      {"udid" : "BBB", "name" : "Work iPad", "state" : "Shutdown", "isAvailable" : true,
       "deviceTypeIdentifier" : "com.apple.CoreSimulator.SimDeviceType.iPad-Pro-11-inch-M5-8GB", "lastUsedAt" : "2026-11-08T09:59:50Z"}
    ],
    "com.apple.CoreSimulator.SimRuntime.iOS-18-5" : [
      {"udid" : "CCC", "name" : "Old iPhone", "state" : "Shutdown", "isAvailable" : false,
       "availabilityError" : "runtime profile not found",
       "deviceTypeIdentifier" : "com.apple.CoreSimulator.SimDeviceType.iPhone-16", "lastUsedAt" : "2026-11-09T10:00:00Z"}
    ],
    "com.apple.CoreSimulator.SimRuntime.watchOS-26-0" : [
      {"udid" : "DDD", "name" : "Apple Watch", "state" : "Booted", "isAvailable" : true,
       "deviceTypeIdentifier" : "com.apple.CoreSimulator.SimDeviceType.Apple-Watch-Series-11-46mm"}
    ]
  }
}`

const simTypesJSON = `{"devicetypes" : [
  {"identifier" : "com.apple.CoreSimulator.SimDeviceType.iPhone-17e", "name" : "iPhone 17e"},
  {"identifier" : "com.apple.CoreSimulator.SimDeviceType.iPad-Pro-11-inch-M5-8GB", "name" : "iPad Pro 11-inch (M5)"}
]}`

func TestParseSimDevices(t *testing.T) {
	devs, err := parseSimDevices([]byte(simDevicesJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 3 {
		t.Fatalf("got %d devices, want the 3 iOS ones: %+v", len(devs), devs)
	}
	if d := devs[0]; d.UDID != "AAA" || d.OSVersion != "26.5" || !d.booted() || !d.IsAvailable {
		t.Errorf("first = %+v", d)
	}
	if d := devs[2]; d.UDID != "CCC" || d.OSVersion != "18.5" || d.IsAvailable || d.AvailabilityError == "" {
		t.Errorf("third = %+v", d)
	}
	if _, err := parseSimDevices([]byte("not json")); err == nil {
		t.Error("no error for bad output")
	}
}

func TestIOSRuntimeVersion(t *testing.T) {
	for id, want := range map[string]string{
		"com.apple.CoreSimulator.SimRuntime.iOS-26-5": "26.5",
		"com.apple.CoreSimulator.SimRuntime.iOS-27-0": "27.0",
		"com.apple.CoreSimulator.SimRuntime.iOS-18":   "18",
	} {
		if got, ok := iosRuntimeVersion(id); !ok || got != want {
			t.Errorf("%s: %q, %v", id, got, ok)
		}
	}
	for _, id := range []string{"com.apple.CoreSimulator.SimRuntime.watchOS-26-0", "com.apple.CoreSimulator.SimRuntime.xrOS-26-0", ""} {
		if _, ok := iosRuntimeVersion(id); ok {
			t.Errorf("%s taken as iOS", id)
		}
	}
}

func TestTypeName(t *testing.T) {
	types, err := parseDeviceTypes([]byte(simTypesJSON))
	if err != nil {
		t.Fatal(err)
	}
	if got := typeName(types, "com.apple.CoreSimulator.SimDeviceType.iPad-Pro-11-inch-M5-8GB"); got != "iPad Pro 11-inch (M5)" {
		t.Errorf("listed type: %q", got)
	}
	if got := typeName(types, "com.apple.CoreSimulator.SimDeviceType.iPhone-16-Pro"); got != "iPhone 16 Pro" {
		t.Errorf("unlisted type: %q", got)
	}
}

func TestSimctlMessage(t *testing.T) {
	out := `An error was encountered processing the command (domain=com.apple.CoreSimulator.SimError, code=401):
The iOS 27.0 simulator runtime is not available.
runtime path not found
Underlying error (domain=com.apple.CoreSimulator.SimError, code=401):
	runtime path not found
`
	got := simctlMessage(out)
	if !strings.HasPrefix(got, "The iOS 27.0 simulator runtime is not available. runtime path not found") || strings.Contains(got, "domain=") {
		t.Errorf("simctlMessage = %q", got)
	}
}

func TestParseSimApps(t *testing.T) {
	// simctl listapps writes an old-style (OpenStep) property list.
	out := `{
    "com.apple.Preferences" =     {
        ApplicationType = System;
        CFBundleDisplayName = Settings;
        CFBundleName = Settings;
        IsHidden = 0;
    };
    "com.apple.CarPlayApp" =     {
        ApplicationType = System;
        CFBundleDisplayName = CarPlay;
        IsHidden = 1;
    };
    "com.example.app" =     {
        ApplicationType = User;
        CFBundleName = Example;
    };
    "com.facebook.WebDriverAgentRunner.xctrunner" =     {
        ApplicationType = User;
        CFBundleName = "WebDriverAgentRunner-Runner";
    };
}`
	apps, err := parseSimApps([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []driver.App{{ID: "com.apple.Preferences", Name: "Settings", System: true}, {ID: "com.example.app", Name: "Example"}}
	if len(apps) != len(want) {
		t.Fatalf("apps = %+v", apps)
	}
	for i := range want {
		if a := apps[i]; a.ID != want[i].ID || a.Name != want[i].Name || a.System != want[i].System {
			t.Errorf("app %d = %+v, want %+v", i, apps[i], want[i])
		}
	}
}

func TestXcodeAt(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Xcode-beta.app")
	dev := filepath.Join(app, "Contents", "Developer")
	if err := os.MkdirAll(filepath.Join(dev, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := xcodeAt(dev); ok {
		t.Fatal("an Xcode without its tools was taken")
	}
	for _, tool := range []string{"simctl", "xcodebuild"} {
		os.WriteFile(filepath.Join(dev, "usr", "bin", tool), nil, 0o755)
	}
	if _, ok := xcodeAt(dev); ok {
		t.Fatal("an Xcode without version.plist was taken")
	}
	os.WriteFile(filepath.Join(app, "Contents", "version.plist"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleShortVersionString</key><string>27.2</string>
<key>ProductBuildVersion</key><string>27B52</string>
</dict></plist>`), 0o644)
	for _, dir := range []string{dev, app, app + "/"} {
		x, ok := xcodeAt(dir)
		if !ok || x.Developer != dev || x.App != app || x.Version != "27.2" || x.Build != "27B52" {
			t.Errorf("xcodeAt(%s) = %+v, %v", dir, x, ok)
		}
	}
	// The command line tools have neither simctl nor xcodebuild.
	clt := filepath.Join(t.TempDir(), "CommandLineTools")
	os.MkdirAll(filepath.Join(clt, "usr", "bin"), 0o755)
	if _, ok := xcodeAt(clt); ok {
		t.Error("the command line tools were taken for Xcode")
	}
}

// tarball makes a gzipped tar of entries.
func tarball(t *testing.T, entries ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, h := range entries {
		body := []byte(h.Linkname)
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(body))
			h.Linkname = ""
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write(body)
		}
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

// tarFile is a regular file for tarball, its content in Linkname.
func tarFile(name, content string, mode int64) *tar.Header {
	return &tar.Header{Name: name, Typeflag: tar.TypeReg, Linkname: content, Mode: mode}
}

func TestUntar(t *testing.T) {
	good := tarball(t,
		&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader},
		&tar.Header{Name: "WDA-abc/", Typeflag: tar.TypeDir, Mode: 0o755},
		tarFile("WDA-abc/README.md", "hello", 0o644),
		&tar.Header{Name: "WDA-abc/Scripts/", Typeflag: tar.TypeDir, Mode: 0o755},
		tarFile("WDA-abc/Scripts/build.sh", "#!/bin/sh", 0o755),
	)
	dir := t.TempDir()
	sum, err := untar(bytes.NewReader(good), dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "README.md")); string(b) != "hello" {
		t.Errorf("README.md = %q", b)
	}
	// Windows keeps no executable bit (and builds no WebDriverAgent).
	if st, err := os.Stat(filepath.Join(dir, "Scripts", "build.sh")); err != nil || (runtime.GOOS != "windows" && st.Mode()&0o111 == 0) {
		t.Errorf("build.sh: %v, %v", st, err)
	}
	// The same files hash the same, whatever the archive.
	want := treeHash([]treeFile{
		{"Scripts/build.sh", true, sha256Hex("#!/bin/sh")},
		{"README.md", false, sha256Hex("hello")},
	})
	if sum != want {
		t.Errorf("hash %s, want %s", sum, want)
	}
	// A changed executable bit changes the hash.
	other := tarball(t, tarFile("WDA-abc/README.md", "hello", 0o644), tarFile("WDA-abc/Scripts/build.sh", "#!/bin/sh", 0o644))
	if s, err := untar(bytes.NewReader(other), t.TempDir(), 1<<20); err != nil || s == sum {
		t.Errorf("mode change: %s, %v", s, err)
	}

	bad := map[string][]byte{
		"parent":    tarball(t, tarFile("WDA-abc/../../evil", "x", 0o644)),
		"absolute":  tarball(t, tarFile("/etc/evil", "x", 0o644)),
		"two tops":  tarball(t, tarFile("WDA-abc/a", "x", 0o644), tarFile("other/b", "x", 0o644)),
		"too large": tarball(t, tarFile("WDA-abc/big", strings.Repeat("x", 2048), 0o644)),
		"empty":     tarball(t, &tar.Header{Name: "WDA-abc/", Typeflag: tar.TypeDir}),
		"not gzip":  []byte("plain"),
	}
	for name, b := range bad {
		dir := t.TempDir()
		if _, err := untar(bytes.NewReader(b), dir, 1024); !errors.Is(err, errSourceMismatch) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// Links are skipped, never followed or made.
	linked := tarball(t, tarFile("WDA-abc/a", "x", 0o644),
		&tar.Header{Name: "WDA-abc/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	dir = t.TempDir()
	if _, err := untar(bytes.NewReader(linked), dir, 1024); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "link")); !os.IsNotExist(err) {
		t.Errorf("a link was made: %v", err)
	}
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestXcodeErrors(t *testing.T) {
	out := "CompileC foo.m\n/tmp/a.m:3:1: error: use of undeclared identifier 'x'\nnote: something\n/tmp/a.m:3:1: error: use of undeclared identifier 'x'\n** TEST BUILD FAILED **\n"
	if got := xcodeErrors(out); got != "/tmp/a.m:3:1: error: use of undeclared identifier 'x'" {
		t.Errorf("xcodeErrors = %q", got)
	}
	if got := xcodeErrors("line one\nYou have not agreed to the Xcode license agreements.\n"); !strings.Contains(got, "license") {
		t.Errorf("last lines: %q", got)
	}
}

func TestLastLines(t *testing.T) {
	l := &lastLines{max: 16}
	l.Write([]byte("first line\nsecond line\nthird\n"))
	if got := l.String(); got != "third\n" {
		t.Errorf("kept %q", got)
	}
}

// fakeSims is a SimProvider over a fake simctl and build.
func fakeSims(t *testing.T) (*SimProvider, *fakeSimctl) {
	t.Helper()
	f := &fakeSimctl{devices: simDevicesJSON}
	p := NewSimProvider(t.TempDir())
	p.findXcodeFn = func() (xcode, error) {
		return xcode{Developer: "/X/Contents/Developer", App: "/X", Version: "27.2", Build: "27B52"}, nil
	}
	p.simctlFn = f.run
	p.setDir = t.TempDir() // no device.plist: the list is read again after listFresh
	return p, f
}

type fakeSimctl struct {
	mu      sync.Mutex
	devices string
	calls   []string
	bootErr error
}

func (f *fakeSimctl) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	switch strings.Join(args, " ") {
	case "list -j devices":
		return []byte(f.devices), nil
	case "list -j devicetypes":
		return []byte(simTypesJSON), nil
	}
	if args[0] == "boot" {
		if f.bootErr != nil {
			return nil, f.bootErr
		}
		f.devices = strings.Replace(f.devices, `"udid" : "BBB", "name" : "Work iPad", "state" : "Shutdown"`, `"udid" : "BBB", "name" : "Work iPad", "state" : "Booting"`, 1)
		return nil, nil
	}
	if args[0] == "shutdown" {
		return nil, nil
	}
	return nil, errors.New("unexpected simctl " + strings.Join(args, " "))
}

func (f *fakeSimctl) count(cmd string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == cmd {
			n++
		}
	}
	return n
}

func TestSimListBuildsHelper(t *testing.T) {
	p, _ := fakeSims(t)
	release := make(chan struct{})
	var builds int
	var mu sync.Mutex
	fail := true
	p.buildFn = func(ctx context.Context, x xcode) (string, error) {
		<-release
		mu.Lock()
		defer mu.Unlock()
		builds++
		if fail {
			return "", errors.New("You have not agreed to the Xcode license agreements.")
		}
		return "/built", nil
	}
	ctx := context.Background()
	infos, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 {
		t.Fatalf("listed %+v, want the booted iPhone", infos)
	}
	i := infos[0]
	if i.ID != "AAA" || !i.Virtual || i.VirtualID != "AAA" || i.Platform != driver.IOS || i.Connection != "usb" || i.Model != "iPhone 17e" ||
		i.OSVersion != "26.5" || i.State != driver.StateNeedsSetup || i.Setup != driver.SetupPreparing {
		t.Errorf("while building: %+v", i)
	}
	// The failure is shown and not built again at once.
	release <- struct{}{}
	simEventually(t, func() bool {
		infos, _ := p.List(ctx)
		return len(infos) == 1 && infos[0].Setup == driver.SetupSimulatorHelper
	})
	infos, _ = p.List(ctx)
	if !strings.Contains(infos[0].Detail, "license") {
		t.Errorf("detail %q", infos[0].Detail)
	}
	// Starting the Simulator again tries again.
	mu.Lock()
	fail = false
	mu.Unlock()
	if err := p.StartVirtual(ctx, "AAA"); err != nil {
		t.Fatal(err)
	}
	p.List(ctx)
	release <- struct{}{}
	simEventually(t, func() bool {
		infos, _ := p.List(ctx)
		return len(infos) == 1 && infos[0].State == driver.StateOnline
	})
	mu.Lock()
	defer mu.Unlock()
	if builds != 2 {
		t.Errorf("built %d times", builds)
	}
}

func TestSimListCachesSimctl(t *testing.T) {
	p, f := fakeSims(t)
	p.buildFn = func(context.Context, xcode) (string, error) { return "/built", nil }
	now := time.Unix(1e9, 0)
	p.now = func() time.Time { return now }
	// A device set with a device.plist: the list is read again when it
	// changes.
	plistPath := filepath.Join(p.setDir, "AAA", "device.plist")
	os.MkdirAll(filepath.Dir(plistPath), 0o755)
	os.WriteFile(plistPath, []byte("state 3"), 0o644)
	ctx := context.Background()
	p.List(ctx)
	p.List(ctx)
	now = now.Add(30 * time.Second)
	p.List(ctx)
	if n := f.count("list -j devices"); n != 1 {
		t.Errorf("simctl listed %d times with no change", n)
	}
	// Rewritten at the same size, a second later: a clock as coarse as
	// Windows' would otherwise give it the same time.
	os.WriteFile(plistPath, []byte("state 1"), 0o644)
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(plistPath, later, later); err != nil {
		t.Fatal(err)
	}
	p.List(ctx)
	if n := f.count("list -j devices"); n != 2 {
		t.Errorf("simctl listed %d times after a change", n)
	}
	now = now.Add(2 * time.Minute)
	p.List(ctx)
	if n := f.count("list -j devices"); n != 3 {
		t.Errorf("simctl listed %d times after a minute", n)
	}
}

func TestSimVirtualPhones(t *testing.T) {
	p, f := fakeSims(t)
	ctx := context.Background()
	vp, err := p.VirtualPhones(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if vp.Platform != driver.IOS || vp.Missing != "" || vp.Where != "/X" {
		t.Errorf("phones = %+v", vp)
	}
	var ids []string
	for _, v := range vp.Phones {
		ids = append(ids, v.ID)
	}
	// Running first, then the last used.
	if strings.Join(ids, ",") != "AAA,CCC,BBB" {
		t.Errorf("order %v", ids)
	}
	if v := vp.Phones[0]; !v.Running || v.DeviceID != "AAA" || v.Model != "iPhone 17e" || v.Unavailable != "" {
		t.Errorf("running = %+v", v)
	}
	if v := vp.Phones[1]; v.Unavailable != "runtime profile not found" {
		t.Errorf("unavailable = %+v", v)
	}
	if v := vp.Phones[2]; v.Running || v.DeviceID != "" || v.Model != "iPad Pro 11-inch (M5)" {
		t.Errorf("shut down = %+v", v)
	}

	if err := p.StartVirtual(ctx, "BBB"); err != nil {
		t.Fatal(err)
	}
	if f.count("boot BBB") != 1 {
		t.Errorf("calls %v", f.calls)
	}
	vp, _ = p.VirtualPhones(ctx)
	for _, v := range vp.Phones {
		if v.ID == "BBB" && !v.Starting {
			t.Errorf("not starting: %+v", v)
		}
	}
	// Starting one that runs boots nothing.
	if err := p.StartVirtual(ctx, "AAA"); err != nil || f.count("boot AAA") != 0 {
		t.Errorf("start running: %v, %v", err, f.calls)
	}
	if err := p.StartVirtual(ctx, "ZZZ"); !errors.Is(err, driver.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	f.bootErr = errors.New("simctl boot: The iOS 18.5 simulator runtime is not available.")
	if err := p.StartVirtual(ctx, "CCC"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("boot error: %v", err)
	}
	if err := p.StopVirtual(ctx, "AAA"); err != nil || f.count("shutdown AAA") != 1 {
		t.Errorf("stop: %v, %v", err, f.calls)
	}
}

func TestSimNoXcode(t *testing.T) {
	p, _ := fakeSims(t)
	p.findXcodeFn = func() (xcode, error) { return xcode{}, errNoXcode }
	ctx := context.Background()
	if infos, err := p.List(ctx); err != nil || len(infos) != 0 {
		t.Errorf("List = %v, %v", infos, err)
	}
	vp, err := p.VirtualPhones(ctx)
	if err != nil || vp.Missing != "xcode" || vp.Detail == "" || len(vp.Phones) != 0 {
		t.Errorf("VirtualPhones = %+v, %v", vp, err)
	}
	if err := p.StartVirtual(ctx, "AAA"); err == nil {
		t.Error("started without Xcode")
	}
}

func TestBuiltProducts(t *testing.T) {
	dir := t.TempDir()
	x := xcode{Build: "27B52"}
	key := buildKey(x, "arm64")
	if key != wdaVersion+"-xcode27B52-arm64" {
		t.Errorf("key %q", key)
	}
	if builtProducts(dir, key) != "" {
		t.Error("found a build in an empty folder")
	}
	p := filepath.Join(dir, "builds", key)
	os.MkdirAll(p, 0o755)
	os.WriteFile(filepath.Join(p, "WebDriverAgentRunner_iphonesimulator27.2-arm64.xctestrun"), nil, 0o644)
	if builtProducts(dir, key) != p {
		t.Error("the build was not found")
	}
	if builtProducts(dir, buildKey(xcode{Build: "28A1"}, "arm64")) != "" {
		t.Error("another Xcode's build was taken")
	}
}

func simEventually(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
