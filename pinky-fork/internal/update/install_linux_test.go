package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests update an install folder of the release's archive on Linux
// with the real helper operations: a shell script stands in for pinky.

// fakePinky is a pinky of version v: `version` says it, and a start writes
// launched and started into $PINKY_TEST_WORK, as the app does, unless it
// crashes.
func fakePinky(v string, crash bool) string {
	run := `echo $$ > "$PINKY_TEST_WORK/launched"; touch "$PINKY_TEST_WORK/started"; sleep 1`
	if crash {
		run = "exit 3"
	}
	return "#!/bin/sh\ncase \"$1\" in version) echo pinky " + v + ";; *) " + run + ";; esac\n"
}

type tarEntry struct {
	name, body string
	mode       int64
	kind       byte
}

func writeArchive(t *testing.T, path string, entries []tarEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.name, Mode: mode, Size: int64(len(e.body)), Typeflag: kind}
		if kind != tar.TypeReg {
			h.Size = 0
			h.Linkname = e.body
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	f.Close()
}

// release is the archive of version v, as the release pipeline makes it.
func release(t *testing.T, v string, crash bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pinky-"+v+"-linux-amd64.tar.gz")
	writeArchive(t, path, []tarEntry{
		{name: "pinky", body: fakePinky(v, crash), mode: 0o755},
		{name: "pinky-companion.apk", body: "apk " + v},
		{name: "lib/libavutil.so.61", body: "ffmpeg " + v, mode: 0o755},
		{name: fileList, body: "lib\npinky\npinky-companion.apk\n", mode: 0o600},
	})
	return path
}

// installed is an install folder of 2026.1107.1, unpacked from its
// archive, with a file and a folder of the person's next to pinky.
func installed(t *testing.T) (dir, exe string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "Pinky")
	if err := unpackArchive(release(t, "2026.1107.1", false), dir); err != nil {
		t.Fatal(err)
	}
	writeTree(t, dir, map[string]string{"notes.txt": "the person's", "photos/beach.jpg": "the person's"})
	time.Sleep(20 * time.Millisecond) // so that a later move shows in the ctimes
	return dir, filepath.Join(dir, "pinky")
}

// personal is when the person's file and folder in an install folder
// last changed, which a move changes too.
func personal(t *testing.T, dir string) map[string]syscall.Timespec {
	t.Helper()
	out := map[string]syscall.Timespec{}
	for _, name := range []string{"notes.txt", "photos"} {
		fi, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		out[name] = fi.Sys().(*syscall.Stat_t).Ctim
	}
	return out
}

// untouched checks that the person's file and folder never moved: Pinky
// moves only the entries that the archive's file list names (MyGo's
// contract), even for a moment.
func untouched(t *testing.T, dir string, before map[string]syscall.Timespec) {
	t.Helper()
	got := readTree(t, dir)
	if got["notes.txt"] != "the person's" || got["photos/beach.jpg"] != "the person's" {
		t.Errorf("the person's files: %v", got)
	}
	for name, ctime := range personal(t, dir) {
		if ctime != before[name] {
			t.Errorf("%s moved", name)
		}
	}
}

func stageRelease(t *testing.T, dir, exe, v string, crash bool) *stagedArchive {
	t.Helper()
	l := archiveLocation(exe, writableDir)
	if !l.CanReplace() {
		t.Fatalf("archiveLocation: %+v", l)
	}
	in, err := NewInstaller(l, filepath.Join(t.TempDir(), "update.log"), nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := in.Stage(context.Background(), release(t, v, crash), Release{Feed: Feed{Version: v}})
	if err != nil {
		t.Fatal(err)
	}
	return st.(*stagedArchive)
}

func TestUnpackArchive(t *testing.T) {
	for name, entries := range map[string][]tarEntry{
		"a path out of the folder": {{name: "../evil", body: "x"}},
		"an absolute path":         {{name: "/tmp/evil", body: "x"}},
		"a link":                   {{name: "pinky", body: "/bin/sh", kind: tar.TypeSymlink}},
	} {
		path := filepath.Join(t.TempDir(), "a.tar.gz")
		writeArchive(t, path, entries)
		if err := unpackArchive(path, filepath.Join(t.TempDir(), "out")); err == nil {
			t.Errorf("%s unpacks", name)
		}
	}
	dir := filepath.Join(t.TempDir(), "out")
	if err := unpackArchive(release(t, "2026.1107.2", false), dir); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "pinky")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("pinky: %v %v", fi, err)
	}
	if v, err := exeVersion(filepath.Join(dir, "pinky")); err != nil || v != "2026.1107.2" {
		t.Errorf("exeVersion = %q, %v", v, err)
	}
}

func TestArchiveStage(t *testing.T) {
	dir, exe := installed(t)
	st := stageRelease(t, dir, exe, "2026.1107.2", false)
	if filepath.Dir(st.work) != filepath.Dir(dir) || !strings.HasPrefix(filepath.Base(st.work), ".Pinky.update-") {
		t.Errorf("work folder %s", st.work)
	}
	if got := readTree(t, st.files)["pinky-companion.apk"]; got != "apk 2026.1107.2" {
		t.Errorf("staged apk %q", got)
	}
	if data, _ := os.ReadFile(st.helper); string(data) != fakePinky("2026.1107.1", false) {
		t.Error("the helper is not a copy of the running pinky")
	}
	st.Discard()
	if exists(st.work) {
		t.Error("Discard left the work folder")
	}

	// An archive without pinky is not staged.
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	writeArchive(t, bad, []tarEntry{{name: "adb", body: "x"}, {name: fileList, body: "adb\n"}})
	in, _ := NewInstaller(archiveLocation(exe, writableDir), "", nil)
	if _, err := in.Stage(context.Background(), bad, Release{Feed: Feed{Version: "2026.1107.2"}}); err == nil {
		t.Error("an archive without pinky is staged")
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), ".Pinky.update-*")); len(m) != 0 {
		t.Errorf("work folders left: %v", m)
	}
}

func TestArchiveSwapNow(t *testing.T) {
	dir, exe := installed(t)
	before := personal(t, dir)
	if err := stageRelease(t, dir, exe, "2026.1107.2", false).SwapNow(); err != nil {
		t.Fatal(err)
	}
	got := readTree(t, dir)
	if got["pinky-companion.apk"] != "apk 2026.1107.2" || got["lib/libavutil.so.61"] != "ffmpeg 2026.1107.2" {
		t.Errorf("after SwapNow: %v", got)
	}
	untouched(t, dir, before)
	if v, err := exeVersion(exe); err != nil || v != "2026.1107.2" {
		t.Errorf("pinky says %q, %v", v, err)
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), ".Pinky.update-*")); len(m) != 0 {
		t.Errorf("work folders left: %v", m)
	}
}

// helperRun swaps in version v with the real helper operations, as if
// Pinky (a process that has exited) had staged it and quit; before is
// personal before.
func helperRun(t *testing.T, v string, crash bool) (dir string, st *stagedArchive, before map[string]syscall.Timespec, err error) {
	t.Helper()
	oldGrace := helperGrace
	helperGrace = 300 * time.Millisecond
	t.Cleanup(func() { helperGrace = oldGrace })
	dir, exe := installed(t)
	before = personal(t, dir)
	st = stageRelease(t, dir, exe, v, crash)
	quit := exec.Command("true")
	if err := quit.Run(); err != nil {
		t.Fatal(err)
	}
	if err := writePending(st.work, "2026.1107.1", v); err != nil {
		t.Fatal(err)
	}
	job := filepath.Join(st.work, "job.json")
	if err := writeJob(job, helperJob{PID: quit.Process.Pid, Dir: dir, Exe: "pinky", Setup: st.files, Version: v,
		Env: []string{"PINKY_TEST_WORK=" + st.work}, Log: filepath.Join(t.TempDir(), "update.log"), Wait: 5 * time.Second, Listed: true}); err != nil {
		t.Fatal(err)
	}
	return dir, st, before, RunHelper(job)
}

func TestArchiveHelperUpdates(t *testing.T) {
	dir, st, before, err := helperRun(t, "2026.1107.2", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := readTree(t, dir); got["pinky-companion.apk"] != "apk 2026.1107.2" {
		t.Errorf("after the update: %v", got)
	}
	untouched(t, dir, before)
	if exists(filepath.Join(st.work, "old")) || !exists(filepath.Join(st.work, "done")) || exists(filepath.Join(st.work, "rolled-back")) {
		t.Errorf("work folder: %v", readTree(t, st.work))
	}
}

func TestArchiveHelperRollsBack(t *testing.T) {
	dir, st, before, err := helperRun(t, "2026.1107.2", true)
	if err == nil || !strings.Contains(err.Error(), "didn't start") {
		t.Fatalf("RunHelper = %v", err)
	}
	if got := readTree(t, dir); got["pinky-companion.apk"] != "apk 2026.1107.1" {
		t.Errorf("after the rollback: %v", got)
	}
	untouched(t, dir, before)
	if v, err := exeVersion(filepath.Join(dir, "pinky")); err != nil || v != "2026.1107.1" {
		t.Errorf("pinky says %q, %v", v, err)
	}
	// The helper started the old version again.
	for deadline := time.Now().Add(5 * time.Second); !exists(filepath.Join(st.work, "started")); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the old version was not started again")
		}
	}
	if why, _ := os.ReadFile(filepath.Join(st.work, "rolled-back")); !strings.Contains(string(why), "didn't start") {
		t.Errorf("rolled-back = %q", why)
	}
	sw := checkSwap(dir, "2026.1107.1", os.Getpid(), time.Now())
	if sw == nil || !sw.Failed || sw.To != "2026.1107.2" {
		t.Errorf("the old version's CheckSwap = %+v", sw)
	}
}

func TestProcessExited(t *testing.T) {
	// A child that exited and that nobody reaped yet: a zombie.
	zombie := exec.Command("true")
	if err := zombie.Start(); err != nil {
		t.Fatal(err)
	}
	if !processExited(zombie.Process.Pid, 5*time.Second) {
		t.Error("a zombie counts as running")
	}
	zombie.Wait()

	p, err := startPinky("/bin/sleep", []string{"30"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if processExited(os.Getpid(), 200*time.Millisecond) {
		t.Error("this process counts as exited")
	}
	p.Kill()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Error("a killed process is not done")
	}
}
