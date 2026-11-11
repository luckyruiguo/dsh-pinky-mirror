package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// uninstallKey is the entry of Pinky's installer in Settings › Apps (MyGo's
// NSIS installer names it by the app's identifier).
const uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + BundleID

// installLocation is the folder Pinky's installer installed into, as
// Settings › Apps knows it, or "".
var installLocation = func() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	dir, _, err := k.GetStringValue("InstallLocation")
	if err != nil {
		return ""
	}
	dir = strings.Trim(strings.TrimSpace(dir), `"`)
	// The installer keeps the folder as it was given, which may use short
	// names (C:\Users\RUNNER~1\…); the running Pinky.exe's path has the long
	// ones.
	if long, err := filepath.EvalSymlinks(dir); err == nil {
		dir = long
	}
	return dir
}

// locate on Windows: a Pinky that Pinky's installer installed (where
// Settings › Apps says) updates itself by running the release's installer
// quietly; any other copy, such as the portable zip, downloads the
// installer for the person.
func locate(exe string) Location {
	return setupLocation(exe, installLocation(), writableDir)
}

// SetupInstaller updates a Pinky that Pinky's installer put in Dir: it runs
// the release's installer quietly once Pinky has quit, starts the new
// version and keeps the old files until it is up (helper.go).
type SetupInstaller struct {
	// Dir is the install folder, %LOCALAPPDATA%\Programs\Pinky.
	Dir string
	// Exe is the running Pinky.exe, which the helper runs a copy of.
	Exe string
	// Log is the helper's log file; "" is pinky-update.log in the
	// temporary folder.
	Log string
	// PID is the process the helper waits for; 0 is this one.
	PID int
	// LaunchWait is how long the helper waits for the new version to
	// launch before it puts the old one back; 0 is three minutes.
	LaunchWait time.Duration
	Logger     *slog.Logger
}

// NewInstaller returns the installer for a location that can replace
// itself, or an error saying why it cannot; the helper logs to logFile.
func NewInstaller(l Location, logFile string, log *slog.Logger) (Installer, error) {
	if !l.CanReplace() {
		if l.Problem != "" {
			return nil, errors.New(l.Problem)
		}
		return nil, errors.New("this copy of Pinky wasn't installed by Pinky's installer, so it can't replace itself")
	}
	return &SetupInstaller{Dir: l.App, Exe: l.Exe, Log: logFile, Logger: log}, nil
}

// Stage puts the release's installer and a copy of this Pinky.exe, which
// runs the helper, in a work folder next to the install folder (on its
// volume, so that the helper moves files with renames).
func (s *SetupInstaller) Stage(ctx context.Context, file string, r Release) (Staged, error) {
	if err := checkSetup(file); err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp(filepath.Dir(s.Dir), workPrefix(s.Dir))
	if err != nil {
		return nil, fmt.Errorf("cannot write next to %s: %w", s.Dir, err)
	}
	st := &stagedSetup{in: s, work: work, version: r.Version,
		setup: filepath.Join(work, filepath.Base(file)), helper: filepath.Join(work, "pinky-update.exe")}
	if err := moveFile(file, st.setup); err != nil {
		st.Discard()
		return nil, err
	}
	if err := copyFile(s.Exe, st.helper); err != nil {
		st.Discard()
		return nil, fmt.Errorf("copy the update helper: %w", err)
	}
	return st, nil
}

type stagedSetup struct {
	in                  *SetupInstaller
	work, setup, helper string
	version             string
}

// Swap writes the helper's job and what the new version looks for
// (pending.json), and starts the helper, which outlives this process.
func (st *stagedSetup) Swap(opt StartOptions) error {
	s := st.in
	pid := s.PID
	if pid == 0 {
		pid = os.Getpid()
	}
	from, _ := fileVersion(s.Exe)
	if err := writePending(st.work, from, st.version); err != nil {
		return err
	}
	logPath := s.Log
	if logPath == "" {
		logPath = filepath.Join(os.TempDir(), "pinky-update.log")
	}
	wait := s.LaunchWait
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	job := filepath.Join(st.work, "job.json")
	if err := writeJob(job, helperJob{PID: pid, Dir: s.Dir, Exe: filepath.Base(s.Exe), Setup: st.setup,
		Version: st.version, Args: opt.Args, Env: opt.Env, Log: logPath, Wait: wait}); err != nil {
		return err
	}
	p, err := startDetached(func() *exec.Cmd {
		cmd := exec.Command(st.helper, "update-helper", job)
		cmd.Dir = st.work
		// The helper is a Pinky.exe: MyGo's automation, in e2e builds,
		// would take the socket the new version listens on.
		cmd.Env = withoutVar(os.Environ(), "MYGO_AUTOMATION")
		return cmd
	}, true)
	if err != nil {
		return fmt.Errorf("start the update helper: %w", err)
	}
	if s.Logger != nil {
		s.Logger.Info("update: staged; installing once Pinky exits", "from", from, "to", st.version, "work", st.work, "helper", p.Pid)
	}
	return p.Release()
}

func (st *stagedSetup) Discard() { os.RemoveAll(st.work) }

// RunHelper is `pinky update-helper <job.json>`: it installs an update
// the app staged, once the app has quit (helper.go).
func RunHelper(jobFile string) error {
	j, err := readJob(jobFile)
	if err != nil {
		return err
	}
	return runHelper(j, helperOps{
		exited:   processExited,
		install:  runSetup,
		version:  fileVersion,
		start:    startPinky,
		watch:    watchProcess,
		snapshot: snapshotUninstall,
	})
}

// startDetached starts a command that outlives this process and the job
// object it may run in (a terminal's, an agent's), where the job allows
// that. hide starts it hidden, which suits the helper; Pinky itself must
// not get it, as Windows would apply it to its window's first showing.
func startDetached(mk func() *exec.Cmd, hide bool) (*os.Process, error) {
	flags := []uint32{windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB, windows.CREATE_NEW_PROCESS_GROUP}
	var err error
	for _, f := range flags {
		cmd := mk()
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: f, HideWindow: hide}
		if err = cmd.Start(); err == nil {
			return cmd.Process, nil
		}
	}
	return nil, err
}

// processExited waits up to d for the process pid to exit.
func processExited(pid int, d time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return true // no such process
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, uint32(d/time.Millisecond))
	return err == nil && ev == windows.WAIT_OBJECT_0
}

// runSetup runs Pinky's installer quietly into dir. /D= must come last and
// unquoted, even with spaces, so the command line is written out.
func runSetup(ctx context.Context, setup, dir string) error {
	cmd := exec.CommandContext(ctx, setup)
	cmd.Dir = filepath.Dir(setup)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: syscall.EscapeArg(setup) + " /S /D=" + dir, HideWindow: true}
	err := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("the installer did not finish within %s", helperSetup)
	case err != nil:
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("the installer exited with %d", ee.ExitCode())
		}
		return err
	}
	return nil
}

// fileVersion is the version in an executable's version resource: 2026.1107.1
// for 2026.1107.1.0.
func fileVersion(exe string) (string, error) {
	size, err := windows.GetFileVersionInfoSize(exe, nil)
	if err != nil {
		return "", err
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(exe, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return "", err
	}
	var fixed *windows.VS_FIXEDFILEINFO
	var n uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&fixed), &n); err != nil {
		return "", err
	}
	if fixed == nil || n == 0 {
		return "", fmt.Errorf("%s has no version", filepath.Base(exe))
	}
	v := fmt.Sprintf("%d.%d.%d", fixed.FileVersionMS>>16, fixed.FileVersionMS&0xffff, fixed.FileVersionLS>>16)
	if b := fixed.FileVersionLS & 0xffff; b != 0 {
		v += fmt.Sprintf(".%d", b)
	}
	return v, nil
}

// startPinky starts Pinky.exe from its folder with its arguments and
// Pinky's own variables.
func startPinky(exe string, args, env []string) (helperProc, error) {
	var cmd *exec.Cmd
	p, err := startDetached(func() *exec.Cmd {
		cmd = exec.Command(exe, args...)
		cmd.Dir = filepath.Dir(exe)
		cmd.Env = withVars(os.Environ(), env)
		return cmd
	}, false)
	if err != nil {
		return nil, err
	}
	w := &winProc{pid: p.Pid, done: make(chan struct{}), kill: p.Kill}
	go func() { cmd.Wait(); close(w.done) }()
	return w, nil
}

// watchProcess follows a process by its pid; one that is gone is done.
func watchProcess(pid int) helperProc {
	w := &winProc{pid: pid, done: make(chan struct{})}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		close(w.done)
		w.kill = func() error { return nil }
		return w
	}
	w.kill = func() error { return windows.TerminateProcess(h, 1) }
	go func() {
		// The handle stays open for Kill: the helper is short-lived.
		windows.WaitForSingleObject(h, windows.INFINITE)
		close(w.done)
	}()
	return w
}

type winProc struct {
	pid  int
	done chan struct{}
	kill func() error
}

func (w *winProc) Done() <-chan struct{} { return w.done }
func (w *winProc) Kill() error           { return w.kill() }

// snapshotUninstall saves the values of Pinky's entry in Settings › Apps,
// which the installer writes (DisplayVersion among them), and returns what
// writes them back.
func snapshotUninstall() func() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, uninstallKey, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	names, err := k.ReadValueNames(-1)
	if err != nil {
		return nil
	}
	type value struct {
		name string
		kind uint32
		s    string
		n    uint64
	}
	var values []value
	for _, name := range names {
		if s, kind, err := k.GetStringValue(name); err == nil {
			values = append(values, value{name: name, kind: kind, s: s})
		} else if n, kind, err := k.GetIntegerValue(name); err == nil {
			values = append(values, value{name: name, kind: kind, n: n})
		}
	}
	return func() error {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		var errs []error
		for _, v := range values {
			switch v.kind {
			case registry.SZ:
				errs = append(errs, k.SetStringValue(v.name, v.s))
			case registry.EXPAND_SZ:
				errs = append(errs, k.SetExpandStringValue(v.name, v.s))
			case registry.DWORD:
				errs = append(errs, k.SetDWordValue(v.name, uint32(v.n)))
			case registry.QWORD:
				errs = append(errs, k.SetQWordValue(v.name, v.n))
			}
		}
		return errors.Join(errs...)
	}
}

// moveFile renames a file, or copies it where it is on another volume.
func moveFile(from, to string) error {
	if os.Rename(from, to) == nil {
		return nil
	}
	if err := copyFile(from, to); err != nil {
		return err
	}
	os.Remove(from)
	return nil
}
