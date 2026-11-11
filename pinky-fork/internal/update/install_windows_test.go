package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The Windows parts of the helper that the fakes of helper_test.go stand in
// for, against the system: the installer's command line, how Pinky is
// started, versions and processes. The test binary plays the installer and
// Pinky (childEnv).

const childEnv = "PINKY_UPDATE_TEST_CHILD"

func TestMain(m *testing.M) {
	switch os.Getenv(childEnv) {
	case "":
		os.Exit(m.Run())
	case "record":
		// Writes its command line and how it was asked to show its
		// window, then exits with PINKY_UPDATE_TEST_EXIT.
		var si windows.StartupInfo
		windows.GetStartupInfo(&si)
		line := windows.UTF16PtrToString(windows.GetCommandLine())
		os.WriteFile(os.Getenv("PINKY_UPDATE_TEST_OUT"), []byte(fmt.Sprintf("%d %d\n%s", si.Flags, si.ShowWindow, line)), 0o600)
		code, _ := strconv.Atoi(os.Getenv("PINKY_UPDATE_TEST_EXIT"))
		os.Exit(code)
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func recorded(t *testing.T, out string) (flags, show uint64, line string) {
	t.Helper()
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	head, line, _ := strings.Cut(string(data), "\n")
	f, s, _ := strings.Cut(head, " ")
	flags, _ = strconv.ParseUint(f, 10, 32)
	show, _ = strconv.ParseUint(s, 10, 32)
	return flags, show, line
}

// TestRunSetup: the installer gets /S and /D= last and unquoted, even with
// spaces in the folder, and its exit code says how it went.
func TestRunSetup(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	dir := filepath.Join(t.TempDir(), "Program Files Here", "Pinky")
	t.Setenv(childEnv, "record")
	t.Setenv("PINKY_UPDATE_TEST_OUT", out)
	t.Setenv("PINKY_UPDATE_TEST_EXIT", "0")
	if err := runSetup(context.Background(), os.Args[0], dir); err != nil {
		t.Fatal(err)
	}
	if _, _, line := recorded(t, out); !strings.HasSuffix(line, " /S /D="+dir) {
		t.Errorf("the installer's command line: %s", line)
	}
	t.Setenv("PINKY_UPDATE_TEST_EXIT", "2")
	if err := runSetup(context.Background(), os.Args[0], dir); err == nil || err.Error() != "the installer exited with 2" {
		t.Errorf("an installer that exits with 2: %v", err)
	}
}

// TestStartPinky: the new version is started so that its window shows,
// and the helper hidden.
func TestStartPinky(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	p, err := startPinky(os.Args[0], []string{"--background"}, []string{childEnv + "=record", "PINKY_UPDATE_TEST_OUT=" + out, "PINKY_UPDATE_TEST_EXIT=0"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the started process did not exit")
	}
	flags, show, line := recorded(t, out)
	if flags&windows.STARTF_USESHOWWINDOW != 0 && show == windows.SW_HIDE {
		t.Error("Pinky was started hidden: its window would not show")
	}
	if !strings.HasSuffix(line, " --background") {
		t.Errorf("Pinky's command line: %s", line)
	}
}

func TestProcesses(t *testing.T) {
	p, err := startPinky(os.Args[0], nil, []string{childEnv + "=sleep"})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.(*winProc).pid
	if processExited(pid, 100*time.Millisecond) {
		t.Fatal("a running process counts as exited")
	}
	w := watchProcess(pid)
	if err := w.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the watched process did not end")
	}
	if !processExited(pid, 10*time.Second) {
		t.Error("an ended process does not count as exited")
	}
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Error("the started process is not done")
	}
	if !processExited(1<<30, 0) {
		t.Error("no such process counts as running")
	}
	select {
	case <-watchProcess(1 << 30).Done():
	default:
		t.Error("watching no such process is not done")
	}
}

func TestFileVersion(t *testing.T) {
	v, err := fileVersion(filepath.Join(os.Getenv("SystemRoot"), "System32", "kernel32.dll"))
	if err != nil || !IsRelease(v) {
		t.Errorf("kernel32.dll's version: %q, %v", v, err)
	}
	if _, err := fileVersion(os.Args[0]); err == nil {
		t.Error("a program without a version resource has one")
	}
}
