package ios

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeXcodebuild makes a Developer folder whose xcodebuild starts a child,
// as the real one does, writes both pids to <developer>/pids and waits,
// and a build with an xctestrun in <developer>/products. The child has
// its own output, or the run's Wait would wait for it.
func fakeXcodebuild(t *testing.T) string {
	t.Helper()
	dev := t.TempDir()
	bin := filepath.Join(dev, "usr", "bin")
	products := filepath.Join(dev, "products")
	for _, d := range []string{bin, products} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\nsleep 600 >/dev/null 2>&1 &\necho \"$$ $!\" > \"$DEVELOPER_DIR/pids.tmp\"\nmv \"$DEVELOPER_DIR/pids.tmp\" \"$DEVELOPER_DIR/pids\"\nwait\n"
	if err := os.WriteFile(filepath.Join(bin, "xcodebuild"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(products, "WDA.xctestrun"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return dev
}

// runPids waits for the fake xcodebuild's pid and its child's.
func runPids(t *testing.T, dev string) (xcodebuild, child int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, err := os.ReadFile(filepath.Join(dev, "pids"))
		if err != nil {
			continue
		}
		f := strings.Fields(string(b))
		xcodebuild, _ = strconv.Atoi(f[0])
		child, _ = strconv.Atoi(f[1])
		// Whatever happens, they do not outlive the test.
		t.Cleanup(func() { syscall.Kill(-xcodebuild, syscall.SIGKILL) })
		return xcodebuild, child
	}
	t.Fatal("the fake xcodebuild did not start")
	return 0, 0
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// waitGone waits up to 15 s for ok: the guard gives the run 5 s after
// SIGTERM.
func waitGone(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); !ok(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

// TestRunGuardHelper is the Pinky that TestKilledPinkyEndsItsRun kills: it
// starts a run with the fake Xcode and waits.
func TestRunGuardHelper(t *testing.T) {
	dev := os.Getenv("PINKY_TEST_RUN_GUARD")
	if dev == "" {
		t.Skip("the helper of TestKilledPinkyEndsItsRun")
	}
	r, err := startRun(xcode{Developer: dev}, filepath.Join(dev, "products"), "SIM-UDID", filepath.Join(dev, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("dir: " + r.dir + "\n")
	time.Sleep(time.Minute)
}

// A Pinky that is killed takes its xcodebuild run with it: the run's
// guard stops the run's process group and removes its folder.
func TestKilledPinkyEndsItsRun(t *testing.T) {
	dev := fakeXcodebuild(t)
	pinky := exec.Command(os.Args[0], "-test.run=^TestRunGuardHelper$", "-test.v")
	pinky.Env = append(os.Environ(), "PINKY_TEST_RUN_GUARD="+dev)
	out, err := pinky.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := pinky.Start(); err != nil {
		t.Fatal(err)
	}
	defer pinky.Process.Kill()
	var dir string
	for sc := bufio.NewScanner(out); dir == "" && sc.Scan(); {
		dir, _ = strings.CutPrefix(sc.Text(), "dir: ")
		if dir == sc.Text() {
			dir = ""
		}
	}
	if dir == "" {
		t.Fatal("the helper started no run")
	}
	xcodebuild, child := runPids(t, dev)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}

	pinky.Process.Kill()
	pinky.Wait()
	waitGone(t, "xcodebuild outlived Pinky", func() bool { return !alive(xcodebuild) && !alive(child) })
	waitGone(t, "the run's folder outlived Pinky", func() bool {
		_, err := os.Stat(dir)
		return os.IsNotExist(err)
	})
}

// A run Pinky stops lets its guard go without the guard stopping anything.
func TestStoppedRunReleasesItsGuard(t *testing.T) {
	dev := fakeXcodebuild(t)
	r, err := startRun(xcode{Developer: dev}, filepath.Join(dev, "products"), "SIM-UDID", filepath.Join(dev, "runs"))
	if err != nil {
		t.Fatal(err)
	}
	xcodebuild, child := runPids(t, dev)
	guard := r.guard.cmd.Process.Pid
	r.stop()
	if alive(xcodebuild) {
		t.Fatal("xcodebuild runs on after stop")
	}
	if _, err := os.Stat(r.dir); !os.IsNotExist(err) {
		t.Fatalf("the run's folder: %v", err)
	}
	waitGone(t, "the guard runs on after stop", func() bool { return !alive(guard) })
	// stop sends SIGTERM to xcodebuild alone, so its child lives on unless
	// the guard stopped the group.
	if !alive(child) {
		t.Fatal("the guard stopped the run's group after a stop")
	}
}
