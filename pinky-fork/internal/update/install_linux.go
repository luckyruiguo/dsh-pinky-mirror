package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ArchiveInstaller updates a Pinky unpacked from the release's archive into
// a folder of its own (archiveLocation). It unpacks the next archive next to
// the folder, and a copy of Pinky's executable, the helper of helper.go,
// swaps the files once Pinky has quit: it moves the old ones aside and the
// new ones in, starts the new version and keeps the old files until it is
// up, or puts them back when it does not start.
type ArchiveInstaller struct {
	// Dir is the install folder, Exe the running pinky in it, which the
	// helper runs a copy of.
	Dir, Exe string
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
		return nil, errors.New("this copy of Pinky wasn't unpacked from Pinky's archive, so it can't replace itself")
	}
	return &ArchiveInstaller{Dir: l.App, Exe: l.Exe, Log: logFile, Logger: log}, nil
}

// Stage unpacks the archive (its checksum, from the feed, already matched)
// into a work folder next to the install folder, on its volume, so that the
// helper moves files with renames, and puts a copy of the running pinky
// there to run the helper.
func (s *ArchiveInstaller) Stage(ctx context.Context, file string, r Release) (Staged, error) {
	work, err := os.MkdirTemp(filepath.Dir(s.Dir), workPrefix(s.Dir))
	if err != nil {
		return nil, fmt.Errorf("cannot write next to %s: %w", s.Dir, err)
	}
	st := &stagedArchive{in: s, work: work, version: r.Version,
		files: filepath.Join(work, "new"), helper: filepath.Join(work, "pinky-update")}
	if err := unpackArchive(file, st.files); err != nil {
		st.Discard()
		return nil, fmt.Errorf("unpack the update: %w", err)
	}
	if name := filepath.Base(s.Exe); !listsFile(filepath.Join(st.files, fileList), name) {
		st.Discard()
		return nil, fmt.Errorf("the update %s has no %s", filepath.Base(file), name)
	}
	if err := copyFile(s.Exe, st.helper); err != nil {
		st.Discard()
		return nil, fmt.Errorf("copy the update helper: %w", err)
	}
	return st, nil
}

type stagedArchive struct {
	in                  *ArchiveInstaller
	work, files, helper string
	version             string
}

// Swap writes the helper's job and what the new version looks for
// (pending.json), and starts the helper, which outlives this process.
func (st *stagedArchive) Swap(opt StartOptions) error {
	s := st.in
	pid := s.PID
	if pid == 0 {
		pid = os.Getpid()
	}
	from, _ := exeVersion(s.Exe)
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
	if err := writeJob(job, helperJob{PID: pid, Dir: s.Dir, Exe: filepath.Base(s.Exe), Setup: st.files,
		Version: st.version, Args: opt.Args, Env: opt.Env, Log: logPath, Wait: wait, Listed: true}); err != nil {
		return err
	}
	cmd := exec.Command(st.helper, "update-helper", job)
	cmd.Dir = st.work
	// The helper is a pinky: MyGo's automation, in e2e builds, would take
	// the socket the new version listens on.
	cmd.Env = withoutVar(os.Environ(), "MYGO_AUTOMATION")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the update helper: %w", err)
	}
	if s.Logger != nil {
		s.Logger.Info("update: staged; installing once Pinky exits", "from", from, "to", st.version, "work", st.work, "helper", cmd.Process.Pid)
	}
	return cmd.Process.Release()
}

func (st *stagedArchive) Discard() { os.RemoveAll(st.work) }

// SwapNow swaps the files at once, while Pinky does not run (`pinky
// update`): Pinky's own entries of the folder (archiveEntries) go aside,
// the new ones in, and the rest of the folder stays where it is.
func (st *stagedArchive) SwapNow() error {
	dir, old := st.in.Dir, filepath.Join(st.work, "old")
	release, err := entries(st.files)
	if err == nil {
		err = moveNames(dir, old, archiveEntries(dir, release))
	}
	if err != nil {
		if errors.Is(err, errMoveBack) {
			return fmt.Errorf("move the installed version aside: %w; what moved is in %s", err, old)
		}
		st.Discard()
		return fmt.Errorf("move the installed version aside: %w", err)
	}
	if err := moveAll(st.files, dir); err != nil {
		if errors.Is(err, errMoveBack) || moveAll(old, dir) != nil {
			return fmt.Errorf("install the update: %w; the old version is in %s", err, old)
		}
		st.Discard()
		return fmt.Errorf("install the update: %w", err)
	}
	st.Discard()
	return nil
}

// maxUnpacked bounds what an archive may unpack to.
const maxUnpacked = 4 << 30

// unpackArchive unpacks a .tar.gz of files and folders into dir, which it
// creates; anything else (links, paths that leave dir) is refused.
func unpackArchive(file, dir string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if name == "." {
			continue
		}
		if !filepath.IsLocal(name) {
			return fmt.Errorf("the archive has %q", h.Name)
		}
		path := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if total += h.Size; total > maxUnpacked {
				return errors.New("the archive is too large")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(tr, h.Size))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("the archive has %q, which is not a file or folder", h.Name)
		}
	}
}

// exeVersion is what `pinky version` says, "2026.1107.1".
func exeVersion(exe string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "version")
	cmd.Env = withoutVar(os.Environ(), "MYGO_AUTOMATION")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s version: %w", filepath.Base(exe), err)
	}
	f := strings.Fields(string(out))
	if len(f) != 2 || f[0] != "pinky" {
		return "", fmt.Errorf("%s version says %q", filepath.Base(exe), bytes.TrimSpace(out))
	}
	return f[1], nil
}

// RunHelper is `pinky update-helper <job.json>`: it swaps in an update the
// app staged, once the app has quit (helper.go). The release's files take
// the place of the installer: installing moves them into the folder.
func RunHelper(jobFile string) error {
	j, err := readJob(jobFile)
	if err != nil {
		return err
	}
	return runHelper(j, helperOps{
		exited:   processExited,
		install:  func(_ context.Context, files, dir string) error { return moveAll(files, dir) },
		version:  exeVersion,
		start:    startPinky,
		watch:    watchProcess,
		snapshot: func() func() error { return nil },
	})
}

// processExited waits up to d for the process pid to exit; a zombie, which
// its parent has not reaped yet, has.
func processExited(pid int, d time.Duration) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(100 * time.Millisecond) {
		if !alive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

func alive(pid int) bool {
	if unix.Kill(pid, 0) == unix.ESRCH {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return unix.Kill(pid, 0) != unix.ESRCH
	}
	// The state follows the command, which is in parentheses.
	i := bytes.LastIndexByte(stat, ')')
	return i < 0 || i+2 >= len(stat) || stat[i+2] != 'Z'
}

// startPinky starts pinky from its folder, in a session of its own, with
// its arguments and Pinky's own variables.
func startPinky(exe string, args, env []string) (helperProc, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = withVars(os.Environ(), env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &unixProc{done: make(chan struct{}), kill: cmd.Process.Kill}
	go func() { cmd.Wait(); close(p.done) }()
	return p, nil
}

// watchProcess follows a process by its pid; one that is gone is done.
func watchProcess(pid int) helperProc {
	p := &unixProc{done: make(chan struct{}), kill: func() error { return unix.Kill(pid, unix.SIGKILL) }}
	go func() {
		for alive(pid) {
			time.Sleep(100 * time.Millisecond)
		}
		close(p.done)
	}()
	return p
}

type unixProc struct {
	done chan struct{}
	kill func() error
}

func (p *unixProc) Done() <-chan struct{} { return p.done }
func (p *unixProc) Kill() error           { return p.kill() }
