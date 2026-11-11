package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests run the Windows update helper on any system, with fakes for
// the installer and the processes, in temporary folders. The Windows job
// of the update workflow runs the real thing.

// fakeProc is a process of the fakes: done when closed.
type fakeProc struct {
	done chan struct{}
	once sync.Once
}

func newFakeProc() *fakeProc { return &fakeProc{done: make(chan struct{})} }

func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) Kill() error           { p.exit(); return nil }
func (p *fakeProc) exit()                 { p.once.Do(func() { close(p.done) }) }

// helperWorld is an install folder of version 2026.1107.1 with a file of
// the person's (FFmpeg next to Pinky.exe), a staged job for 2026.1107.2
// and fakes whose behavior the test picks.
type helperWorld struct {
	t         *testing.T
	dir, work string
	job       helperJob

	setupErr     error  // the installer fails
	setupVersion string // what the installer installs; "" is the job's
	newVersion   string // how the new version behaves: "up", "crash", "exit-after-launch", "slow", "hang"

	mu       sync.Mutex
	starts   []string // the versions started, with their arguments
	restored int
	procs    []*fakeProc
}

func newHelperWorld(t *testing.T) *helperWorld {
	t.Helper()
	oldGrace, oldTries, oldWait := helperGrace, renameTries, renameWait
	helperGrace, renameTries, renameWait = 300*time.Millisecond, 2, time.Millisecond
	t.Cleanup(func() { helperGrace, renameTries, renameWait = oldGrace, oldTries, oldWait })

	programs := t.TempDir()
	w := &helperWorld{t: t, dir: filepath.Join(programs, "Pinky"), newVersion: "up"}
	writeTree(t, w.dir, map[string]string{
		"Pinky.exe":                     "2026.1107.1",
		"Uninstall.exe":                 "uninstall 2026.1107.1",
		"resources/pinky-companion.apk": "apk 1",
		"avcodec-63.dll":                "the person's FFmpeg",
		"resources/notes.txt":           "the person's",
	})
	work, err := os.MkdirTemp(programs, workPrefix(w.dir))
	if err != nil {
		t.Fatal(err)
	}
	w.work = work
	setup := filepath.Join(work, "pinky-setup-2026.1107.2-amd64.exe")
	os.WriteFile(setup, []byte("MZ installer"), 0o700)
	if err := writePending(work, "2026.1107.1", "2026.1107.2"); err != nil {
		t.Fatal(err)
	}
	w.job = helperJob{PID: 1, Dir: w.dir, Exe: "Pinky.exe", Setup: setup, Version: "2026.1107.2",
		Args: []string{"--background"}, Env: []string{"PINKY_HOME=x"}, Log: filepath.Join(t.TempDir(), "update.log"),
		Wait: time.Second, work: work}
	return w
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// readTree returns the files under root and their contents.
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(root, p)
			data, _ := os.ReadFile(p)
			out[filepath.ToSlash(rel)] = string(data)
		}
		return nil
	})
	return out
}

func (w *helperWorld) ops() helperOps {
	return helperOps{
		exited: func(pid int, d time.Duration) bool { return true },
		install: func(ctx context.Context, setup, dir string) error {
			if setup != w.job.Setup || dir != w.dir {
				w.t.Errorf("install %s into %s", setup, dir)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				w.t.Errorf("the installer found %d entries in the folder, not none", len(entries))
			}
			v := w.setupVersion
			if v == "" {
				v = w.job.Version
			}
			writeTree(w.t, dir, map[string]string{
				"Pinky.exe":                     v,
				"Uninstall.exe":                 "uninstall " + v,
				"resources/pinky-companion.apk": "apk 2",
			})
			return w.setupErr
		},
		version: func(exe string) (string, error) {
			data, err := os.ReadFile(exe)
			return string(data), err
		},
		start: func(exe string, args, env []string) (helperProc, error) {
			data, err := os.ReadFile(exe)
			if err != nil {
				return nil, err
			}
			v := string(data)
			w.mu.Lock()
			w.starts = append(w.starts, strings.Join(append([]string{v}, args...), " "))
			p := newFakeProc()
			w.procs = append(w.procs, p)
			w.mu.Unlock()
			if !reflect.DeepEqual(env, w.job.Env) {
				w.t.Errorf("started with %q", env)
			}
			if v != w.job.Version {
				return p, nil // the old version again
			}
			launched := func() { os.WriteFile(filepath.Join(w.work, "launched"), []byte("4242"), 0o600) }
			started := func() { os.WriteFile(filepath.Join(w.work, "started"), nil, 0o600) }
			switch w.newVersion {
			case "up":
				launched()
				started()
			case "slow":
				launched()
				time.AfterFunc(500*time.Millisecond, started)
			case "crash":
				p.exit()
			case "exit-after-launch":
				launched()
				time.AfterFunc(100*time.Millisecond, p.exit)
			}
			return p, nil
		},
		watch: func(pid int) helperProc {
			if pid != 4242 {
				w.t.Errorf("watch %d", pid)
			}
			w.mu.Lock()
			defer w.mu.Unlock()
			return w.procs[len(w.procs)-1] // the new version launched
		},
		snapshot: func() func() error {
			return func() error { w.mu.Lock(); w.restored++; w.mu.Unlock(); return nil }
		},
	}
}

func (w *helperWorld) run() error { return runHelper(w.job, w.ops()) }

func (w *helperWorld) marks() []string {
	var out []string
	for _, m := range []string{"launched", "started", "rolled-back", "done", "kept", "old", "failed"} {
		if exists(filepath.Join(w.work, m)) {
			out = append(out, m)
		}
	}
	return out
}

var (
	oldInstall = map[string]string{
		"Pinky.exe": "2026.1107.1", "Uninstall.exe": "uninstall 2026.1107.1", "resources/pinky-companion.apk": "apk 1",
		"avcodec-63.dll": "the person's FFmpeg", "resources/notes.txt": "the person's",
	}
	newInstall = map[string]string{
		"Pinky.exe": "2026.1107.2", "Uninstall.exe": "uninstall 2026.1107.2", "resources/pinky-companion.apk": "apk 2",
		"avcodec-63.dll": "the person's FFmpeg", "resources/notes.txt": "the person's",
	}
)

func TestHelperInstalls(t *testing.T) {
	for _, behavior := range []string{"up", "slow"} {
		w := newHelperWorld(t)
		w.newVersion = behavior
		if err := w.run(); err != nil {
			t.Fatalf("%s: %v", behavior, err)
		}
		if got := readTree(t, w.dir); !reflect.DeepEqual(got, newInstall) {
			t.Errorf("%s: the folder holds %v", behavior, got)
		}
		if got := w.marks(); !reflect.DeepEqual(got, []string{"launched", "started", "done"}) {
			t.Errorf("%s: the work folder has %v", behavior, got)
		}
		if exists(w.job.Setup) {
			t.Errorf("%s: the installer is left", behavior)
		}
		if !reflect.DeepEqual(w.starts, []string{"2026.1107.2 --background"}) || w.restored != 0 {
			t.Errorf("%s: started %q, restored %d", behavior, w.starts, w.restored)
		}
		// The new version's next start removes the folder with the copy
		// of Pinky.exe the helper ran from.
		if sw := checkSwap(w.dir, "2026.1107.2", 1, time.Now()); sw != nil || exists(w.work) {
			t.Errorf("%s: after the update, checkSwap = %+v, the work folder is left: %v", behavior, sw, exists(w.work))
		}
	}
}

func TestHelperPutsTheOldVersionBack(t *testing.T) {
	for _, c := range []struct {
		name  string
		set   func(*helperWorld)
		why   string
		start bool // the new version was started
	}{
		{"the installer fails", func(w *helperWorld) { w.setupErr = errors.New("the installer exited with 2") }, "didn't install: the installer exited with 2", false},
		{"the installer installs another version", func(w *helperWorld) { w.setupVersion = "2026.1107.1" }, "didn't install: the installer put version 2026.1107.1 there", false},
		{"the new version crashes", func(w *helperWorld) { w.newVersion = "crash" }, "didn't start: it exited", true},
		{"the new version exits before it is up", func(w *helperWorld) { w.newVersion = "exit-after-launch" }, "didn't start: it exited before it was up", true},
		{"the new version never launches", func(w *helperWorld) { w.newVersion = "hang" }, "didn't start within 1s", true},
	} {
		w := newHelperWorld(t)
		c.set(w)
		err := w.run()
		if err == nil || err.Error() != c.why {
			t.Errorf("%s: %v", c.name, err)
		}
		if got := readTree(t, w.dir); !reflect.DeepEqual(got, oldInstall) {
			t.Errorf("%s: the folder holds %v", c.name, got)
		}
		data, _ := os.ReadFile(filepath.Join(w.work, "rolled-back"))
		if strings.TrimSpace(string(data)) != c.why || !exists(filepath.Join(w.work, "done")) {
			t.Errorf("%s: rolled-back says %q; marks %v", c.name, data, w.marks())
		}
		want := []string{"2026.1107.1 --background"}
		if c.start {
			want = append([]string{"2026.1107.2 --background"}, want...)
			select {
			case <-w.procs[0].Done():
			default:
				t.Errorf("%s: the new version still runs", c.name)
			}
		}
		if !reflect.DeepEqual(w.starts, want) || w.restored != 1 {
			t.Errorf("%s: started %q, restored the uninstall entry %d times", c.name, w.starts, w.restored)
		}
		// The old version learns why, and the folder goes.
		sw := checkSwap(w.dir, "2026.1107.1", 1, time.Now())
		if sw == nil || !sw.Failed || sw.To != "2026.1107.2" || sw.Why != c.why || exists(w.work) {
			t.Errorf("%s: the old version's checkSwap = %+v; the folder is left: %v", c.name, sw, exists(w.work))
		}
	}
}

func TestHelperWhenPinkyDoesNotQuit(t *testing.T) {
	w := newHelperWorld(t)
	ops := w.ops()
	ops.exited = func(int, time.Duration) bool { return false }
	ops.install = func(context.Context, string, string) error { t.Error("installed while Pinky runs"); return nil }
	if err := runHelper(w.job, ops); err == nil {
		t.Error("no error")
	}
	if got := readTree(t, w.dir); !reflect.DeepEqual(got, oldInstall) || len(w.starts) != 0 {
		t.Errorf("the folder holds %v; started %q", got, w.starts)
	}
	if got := w.marks(); !reflect.DeepEqual(got, []string{"done"}) {
		t.Errorf("marks %v", got)
	}
}

// TestHelperKeepsWhatItCannotMoveBack: a file of the person's that will not
// go back into the install folder stays in the work folder, which nothing
// removes then.
func TestHelperKeepsWhatItCannotMoveBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only folder takes new files on Windows")
	}
	w := newHelperWorld(t)
	ops := w.ops()
	install := ops.install
	ops.install = func(ctx context.Context, setup, dir string) error {
		err := install(ctx, setup, dir)
		os.Chmod(filepath.Join(dir, "resources"), 0o555) // takes nothing back
		return err
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(w.dir, "resources"), 0o755) })
	if err := runHelper(w.job, ops); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(w.dir, "avcodec-63.dll")) {
		t.Error("the person's file that could go back did not")
	}
	data, _ := os.ReadFile(filepath.Join(w.work, "old", "resources", "notes.txt"))
	if string(data) != "the person's" || !exists(filepath.Join(w.work, "kept")) {
		t.Errorf("the person's file that could not go back: %q, marks %v", data, w.marks())
	}
	checkSwap(w.dir, "2026.1107.2", 1, time.Now().Add(72*time.Hour))
	if !exists(filepath.Join(w.work, "old", "resources", "notes.txt")) {
		t.Error("checkSwap removed the person's file")
	}
}

func TestMoveAll(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "dst")
	writeTree(t, src, map[string]string{"a": "1", "b/c": "2"})
	if err := moveAll(src, dst); err != nil {
		t.Fatal(err)
	}
	if got := readTree(t, dst); !reflect.DeepEqual(got, map[string]string{"a": "1", "b/c": "2"}) || len(readTree(t, src)) != 0 {
		t.Errorf("moved %v, left %v", got, readTree(t, src))
	}
	// One that will not move: the others go back.
	oldTries := renameTries
	renameTries = 1
	defer func() { renameTries = oldTries }()
	writeTree(t, src, map[string]string{"a": "new a", "z": "z"})
	os.MkdirAll(filepath.Join(dst, "z", "x"), 0o755) // z cannot replace a folder that has something in it
	err := moveAll(src, dst)
	if err == nil || errors.Is(err, errMoveBack) {
		t.Errorf("moveAll = %v", err)
	}
	if got := readTree(t, src); !reflect.DeepEqual(got, map[string]string{"a": "new a", "z": "z"}) {
		t.Errorf("after a failed move, the source holds %v", got)
	}
}

func TestReadJob(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.json")
	j := helperJob{PID: 7, Dir: `C:\Users\x\AppData\Local\Programs\Pinky`, Exe: "Pinky.exe", Setup: "setup.exe", Version: "2026.1107.2", Args: []string{"--background"}, Log: "log"}
	if err := writeJob(path, j); err != nil {
		t.Fatal(err)
	}
	got, err := readJob(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.work != dir || got.Wait != 3*time.Minute || got.PID != 7 || got.Exe != "Pinky.exe" || got.Args[0] != "--background" {
		t.Errorf("readJob = %+v", got)
	}
	for _, bad := range []helperJob{{Dir: "d", Exe: "Pinky.exe", Setup: "s", Version: "0.0.0-dev"}, {Dir: "d", Exe: `..\x.exe`, Setup: "s", Version: "1"}, {Exe: "Pinky.exe", Setup: "s", Version: "1"}} {
		writeJob(path, bad)
		if _, err := readJob(path); err == nil {
			t.Errorf("readJob accepts %+v", bad)
		}
	}
}

func TestSetupLocation(t *testing.T) {
	ok := func(string) error { return nil }
	exe := filepath.Join("Users", "x", "AppData", "Local", "Programs", "Pinky", "Pinky.exe")
	dir := filepath.Dir(exe)

	l := setupLocation(exe, strings.ToUpper(dir), ok)
	if l.Method != MethodSelf || !l.CanReplace() || l.App != dir || !l.Setup {
		t.Errorf("the installed Pinky: %+v", l)
	}
	for _, registered := range []string{"", filepath.Join("elsewhere", "Pinky")} {
		if l := setupLocation(exe, registered, ok); l.Method != MethodInstaller || l.CanReplace() || !l.Setup {
			t.Errorf("a copy that Pinky's installer did not install (%q): %+v", registered, l)
		}
	}
	readOnly := func(d string) error {
		if d == filepath.Dir(dir) {
			return errors.New("denied")
		}
		return nil
	}
	if l := setupLocation(exe, dir, readOnly); l.CanReplace() || !strings.Contains(l.Problem, filepath.Dir(dir)) {
		t.Errorf("where the work folder cannot go: %+v", l)
	}
}

// TestArchiveLocation: on Linux, a pinky that the archive's file list names
// replaces the files of its folder; any other copy downloads the release.
func TestArchiveLocation(t *testing.T) {
	ok := func(string) error { return nil }
	dir := filepath.Join(t.TempDir(), "Pinky")
	exe := filepath.Join(dir, "pinky")
	writeTree(t, dir, map[string]string{"pinky": "", "adb": ""})

	if l := archiveLocation(exe, ok); l.Method != MethodDownload || l.CanReplace() || l.App != exe {
		t.Errorf("a pinky without the archive's file list: %+v", l)
	}
	writeTree(t, dir, map[string]string{fileList: "adb\npinkyctl\n"})
	if l := archiveLocation(exe, ok); l.Method != MethodDownload {
		t.Errorf("a pinky that the file list does not name: %+v", l)
	}
	writeTree(t, dir, map[string]string{fileList: "adb\npinky\npinky.png\n"})
	if l := archiveLocation(exe, ok); l.Method != MethodSelf || !l.CanReplace() || l.App != dir {
		t.Errorf("the archive's pinky: %+v", l)
	}
	readOnly := func(d string) error {
		if d == filepath.Dir(dir) {
			return errors.New("denied")
		}
		return nil
	}
	if l := archiveLocation(exe, readOnly); l.CanReplace() || !strings.Contains(l.Problem, filepath.Dir(dir)) {
		t.Errorf("where the work folder cannot go: %+v", l)
	}
}

func TestMethodForAnInstalledWindowsPinky(t *testing.T) {
	u := &Updater{Location: Location{Method: MethodSelf, Setup: true, App: `C:\Programs\Pinky`}}
	for url, want := range map[string]Method{
		"https://example.com/pinky-setup-2026.1107.2-amd64.exe":   MethodSelf,
		"https://example.com/pinky-2026.1107.2-windows-amd64.zip": MethodDownload,
	} {
		if got := u.MethodFor(Release{Asset: Asset{URL: url}}); got != want {
			t.Errorf("%s: %v, want %v", url, got, want)
		}
	}
}

func TestEnvironment(t *testing.T) {
	env := []string{"PATH=/bin", "MYGO_AUTOMATION=/tmp/ui.sock", "Mygo_Automation=x", "PINKY_HOME=/old"}
	if got := withoutVar(env, "MYGO_AUTOMATION"); !reflect.DeepEqual(got, []string{"PATH=/bin", "PINKY_HOME=/old"}) {
		t.Errorf("withoutVar = %q", got)
	}
	got := withVars([]string{"PATH=/bin", "PINKY_HOME=/old"}, []string{"PINKY_HOME=/new", "MYGO_AUTOMATION=/tmp/ui.sock"})
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"MYGO_AUTOMATION=/tmp/ui.sock", "PATH=/bin", "PINKY_HOME=/new"}) {
		t.Errorf("withVars = %q", got)
	}
}

func TestCheckSetup(t *testing.T) {
	dir := t.TempDir()
	exe, zip := filepath.Join(dir, "setup.exe"), filepath.Join(dir, "setup.zip")
	os.WriteFile(exe, []byte("MZ\x90\x00"), 0o644)
	os.WriteFile(zip, []byte("PK\x03\x04"), 0o644)
	if err := checkSetup(exe); err != nil {
		t.Error(err)
	}
	if err := checkSetup(zip); err == nil {
		t.Error("a zip passes as the installer")
	}
}

// TestCheckSwapOfAnInstallFolder: the work folders of the helper on Windows,
// next to the install folder.
func TestCheckSwapOfAnInstallFolder(t *testing.T) {
	programs := t.TempDir()
	dir := filepath.Join(programs, "Pinky")
	os.MkdirAll(dir, 0o755)
	mk := func(name string, files ...string) string {
		work := filepath.Join(programs, ".Pinky.update-"+name)
		os.MkdirAll(work, 0o700)
		writePending(work, "2026.1107.1", "2026.1107.2")
		for _, f := range files {
			if f == "old" {
				os.MkdirAll(filepath.Join(work, f), 0o755)
			} else {
				os.WriteFile(filepath.Join(work, f), nil, 0o600)
			}
		}
		return work
	}
	live := mk("live", "old", "pinky-update.exe")
	done := mk("done", "launched", "started", "done", "pinky-update.exe")
	kept := mk("kept", "old", "started", "done", "kept")
	sw := checkSwap(dir, "2026.1107.2", 99, time.Now())
	if sw == nil || sw.Failed || sw.dir != live {
		t.Fatalf("checkSwap = %+v", sw)
	}
	if data, _ := os.ReadFile(filepath.Join(live, "launched")); string(data) != "99" {
		t.Errorf("launched = %q", data)
	}
	if exists(done) || !exists(kept) {
		t.Errorf("the folder of a helper that is through is left: %v; the person's files are gone: %v", exists(done), !exists(kept))
	}
	if err := sw.Confirm(); err != nil || !exists(filepath.Join(live, "started")) {
		t.Errorf("Confirm: %v", err)
	}
}
