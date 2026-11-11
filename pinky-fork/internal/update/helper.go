package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The update helper of Windows and Linux. On Linux the release's archive,
// unpacked next to the install folder, takes the installer's place: the
// helper moves its files into the folder (install_linux.go), and the rest
// is as below, except that the folder is the person's: only Pinky's
// entries of it move (Listed), and none has to come back. On Windows,
// Pinky's installer replaces the files in the install folder
// (%LOCALAPPDATA%\Programs\Pinky), which it cannot do while Pinky runs
// from there, so the app leaves the work to a copy of itself in the work
// folder next to the install folder (`Pinky.exe update-helper <job.json>`),
// which outlives it. The helper
//
//  1. waits for the app to exit;
//  2. moves everything in the install folder to work\old, so that the
//     installer finds it empty and the old version stays whole for a
//     rollback (a rename works even on a Pinky.exe that an agent's `pinky
//     mcp` still runs from);
//  3. runs the release's installer quietly (/S) into the folder and checks
//     that what it installed is the release's version;
//  4. starts the new version, which writes launched and started as on
//     macOS (CheckSwap, Confirm);
//  5. once it is up, moves back what only the old folder had (FFmpeg's
//     DLLs that the person put next to Pinky.exe, say) and removes the old
//     version. When the installer fails, or the new version exits before
//     it is up or never launches, it stops the new version, moves its files
//     to work\failed, puts the old ones back with the uninstall entry as it
//     was, leaves rolled-back with why and starts the old version again.
//
// It never removes files that are not in place elsewhere: when something
// cannot be put back, both versions stay in the work folder and the log
// says where.

// helperJob is what the app asks of the helper, in job.json in the work
// folder.
type helperJob struct {
	PID     int           `json:"pid"`     // the Pinky to wait for
	Dir     string        `json:"dir"`     // the install folder
	Exe     string        `json:"exe"`     // Pinky's executable in it, "Pinky.exe"
	Setup   string        `json:"setup"`   // the release's installer
	Version string        `json:"version"` // the release's version
	Args    []string      `json:"args,omitempty"`
	Env     []string      `json:"env,omitempty"` // Pinky's own variables, for both versions
	Log     string        `json:"log"`
	Wait    time.Duration `json:"wait"` // how long the new version may take to launch
	// Listed is a folder that is the person's, with Pinky's files in it, as
	// an archive unpacked on Linux leaves it: only the entries its file
	// list names, and the release's, move; the rest of the folder stays.
	Listed bool `json:"listed,omitempty"`

	work string // the folder of job.json
}

// helperProc is a process the helper started or follows.
type helperProc interface {
	// Done is closed once the process has exited.
	Done() <-chan struct{}
	Kill() error
}

// helperOps are what the helper does to the system; tests replace them.
type helperOps struct {
	// exited waits up to d for the process pid to exit, reporting whether
	// it did (or never ran).
	exited func(pid int, d time.Duration) bool
	// install runs the installer quietly into dir and waits for it.
	install func(ctx context.Context, setup, dir string) error
	// version is the version of an installed executable.
	version func(exe string) (string, error)
	// start starts Pinky with its arguments and Pinky's own variables.
	start func(exe string, args, env []string) (helperProc, error)
	// watch follows a running process by its pid.
	watch func(pid int) helperProc
	// snapshot saves what the installer changes besides the files (the
	// uninstall entry in Settings › Apps) and returns what puts it back.
	snapshot func() (restore func() error)
}

const (
	helperAppWait = 10 * time.Minute // for the app to exit
	helperSetup   = 5 * time.Minute  // for the installer
	helperKill    = 10 * time.Second // for a stopped process to go
)

// Tests shorten these.
var (
	// helperGrace is how long launched may take to appear after the
	// process the helper started exited (another start of Pinky, such as
	// an agent's, may have taken the gateway).
	helperGrace = 15 * time.Second
	// A rename is tried renameTries times, renameWait apart: a scanner may
	// hold a file a moment.
	renameTries = 25
	renameWait  = 200 * time.Millisecond
)

// ErrHelperOS is RunHelper on a system whose updates need no helper.
var ErrHelperOS = errors.New("the update helper runs on Windows and Linux only")

// readJob reads job.json.
func readJob(path string) (helperJob, error) {
	var j helperJob
	data, err := os.ReadFile(path)
	if err != nil {
		return j, err
	}
	if err := json.Unmarshal(data, &j); err != nil {
		return j, fmt.Errorf("%s: %w", path, err)
	}
	j.work = filepath.Dir(path)
	if j.Dir == "" || j.Exe == "" || j.Setup == "" || !IsRelease(j.Version) || strings.ContainsAny(j.Exe, `/\`) {
		return j, fmt.Errorf("%s: an incomplete job", path)
	}
	if j.Wait <= 0 {
		j.Wait = 3 * time.Minute
	}
	return j, nil
}

func writeJob(path string, j helperJob) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

type helper struct {
	job     helperJob
	ops     helperOps
	restore func() error
	// release is what the installer puts in the folder, for a Listed
	// folder: the entries of the release's files.
	release []string
}

func (h *helper) say(format string, args ...any) {
	line := time.Now().Format("2006-01-02 15:04:05") + " pinky-update[" + strconv.Itoa(os.Getpid()) + "]: " + fmt.Sprintf(format, args...) + "\n"
	if f, err := os.OpenFile(h.job.Log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
		f.WriteString(line)
		f.Close()
	}
}

func (h *helper) path(name string) string { return filepath.Join(h.job.work, name) }

// mark leaves a file for CheckSwap in the work folder.
func (h *helper) mark(name, content string) {
	if err := os.WriteFile(h.path(name), []byte(content), 0o600); err != nil {
		h.say("could not write %s: %v", name, err)
	}
}

// runHelper does a job (see above) and returns why the update did not
// happen, if it did not. It leaves done in the work folder once nothing
// there is needed any more, so CheckSwap removes the folder; where files
// could not be put back it leaves the folder as it is.
func runHelper(j helperJob, ops helperOps) error {
	h := &helper{job: j, ops: ops}
	exe := filepath.Join(j.Dir, j.Exe)
	h.say("waiting for Pinky (pid %d) to quit, to install %s", j.PID, j.Version)
	if !ops.exited(j.PID, helperAppWait) {
		h.say("Pinky (pid %d) still runs after %s; not updating", j.PID, helperAppWait)
		h.mark("done", "")
		return errors.New("Pinky did not quit")
	}
	old := h.path("old")
	ours, err := entries(j.Dir)
	if j.Listed {
		if h.release, err = entries(j.Setup); err == nil {
			ours = archiveEntries(j.Dir, h.release)
		}
	}
	if err == nil {
		err = moveNames(j.Dir, old, ours)
	}
	if err != nil {
		if errors.Is(err, errMoveBack) {
			h.say("could not move the installed version aside, nor all of it back (%v): what moved is in %s", err, old)
			return err
		}
		h.say("could not move the installed version aside (%v); keeping it", err)
		return h.giveUp("couldn't replace the files in " + j.Dir)
	}
	h.restore = ops.snapshot()
	h.say("installing %s into %s", j.Version, j.Dir)
	ctx, cancel := context.WithTimeout(context.Background(), helperSetup)
	err = ops.install(ctx, j.Setup, j.Dir)
	cancel()
	if err != nil {
		return h.rollBack("didn't install: "+err.Error(), nil)
	}
	if v, err := ops.version(exe); err != nil || Compare(v, j.Version) != 0 {
		if err == nil {
			err = fmt.Errorf("the installer put version %s there", v)
		}
		return h.rollBack("didn't install: "+err.Error(), nil)
	}
	h.say("installed %s; starting it", j.Version)
	p, err := ops.start(exe, j.Args, j.Env)
	if err != nil {
		return h.rollBack("didn't start: "+err.Error(), nil)
	}
	if launched, why := h.waitUp(p); why != "" {
		return h.rollBack(why, p, launched)
	}
	h.say("the new version is up; removing the old one")
	// What the person keeps in the installer's folder on Windows moved
	// with the old version; a Listed folder's never moved.
	if !j.Listed {
		if left := keepExtras(old, j.Dir); len(left) > 0 {
			// Not ours to remove: leave them, and the folder, for the person.
			h.say("could not move these back into %s; they are in %s: %s", j.Dir, old, strings.Join(left, ", "))
			h.mark("kept", strings.Join(left, "\n"))
			return nil
		}
	}
	if err := os.RemoveAll(old); err != nil {
		// A Pinky.exe that an agent's `pinky mcp` still runs; CheckSwap
		// removes it at a later start.
		h.say("left some of the old version: %v", err)
	}
	os.Remove(j.Setup)
	h.mark("done", "")
	return nil
}

// waitUp waits for the new version to come up: started appears once its
// window (or the daemon) is up. It gives up when the version that
// launched (took the gateway's lock) exits first, or when nothing launches
// within the job's wait, or soon after the process it started exits
// without anyone having launched. It returns the launched process.
func (h *helper) waitUp(p helperProc) (helperProc, string) {
	deadline := time.Now().Add(h.job.Wait)
	var launched helperProc
	var exitedAt time.Time
	for {
		if exists(h.path("started")) {
			return launched, ""
		}
		if launched == nil {
			if data, err := os.ReadFile(h.path("launched")); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
					launched = h.ops.watch(pid)
				}
			}
		}
		now := time.Now()
		switch {
		case launched != nil:
			select {
			case <-launched.Done():
				if !exists(h.path("started")) {
					return launched, "didn't start: it exited before it was up"
				}
			default:
			}
		case now.After(deadline):
			return nil, fmt.Sprintf("didn't start within %s", h.job.Wait)
		default:
			select {
			case <-p.Done():
				if exitedAt.IsZero() {
					exitedAt = now
				} else if now.Sub(exitedAt) > helperGrace {
					return nil, "didn't start: it exited"
				}
			default:
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// rollBack puts the old version back after the new one did not work: it
// stops the new version's processes, moves its files to work\failed, the
// old ones back and the uninstall entry as it was, and starts the old
// version, which learns why from rolled-back.
func (h *helper) rollBack(why string, procs ...helperProc) error {
	j := h.job
	h.say("Pinky %s %s; putting the old version back", j.Version, why)
	for _, p := range procs {
		if p == nil {
			continue
		}
		p.Kill()
		select {
		case <-p.Done():
		case <-time.After(helperKill):
			h.say("the new version did not stop")
		}
	}
	old, failed := h.path("old"), h.path("failed")
	release, err := entries(j.Dir)
	if j.Listed {
		release, err = h.release, nil
	}
	if err == nil {
		err = moveNames(j.Dir, failed, release)
	}
	if err != nil {
		if errors.Is(err, errMoveBack) {
			h.say("could not move the new version aside, nor all of it back (%v): the old version is in %s, what moved of the new one in %s", err, old, failed)
		} else {
			h.say("could not move the new version aside (%v); keeping it: the old version is in %s", err, old)
		}
		return errors.New(why)
	}
	if err := moveAll(old, j.Dir); err != nil {
		if err2 := moveAll(failed, j.Dir); err2 == nil {
			h.say("could not put the old version back (%v); kept the new one: the old version is in %s", err, old)
		} else {
			h.say("could not put either version back: the old one is in %s, the new one in %s", old, failed)
		}
		return errors.New(why)
	}
	if h.restore != nil {
		if err := h.restore(); err != nil {
			h.say("could not put the uninstall entry back: %v", err)
		}
	}
	return h.giveUp(why)
}

// giveUp leaves rolled-back with why and starts the old version, which
// is in place again; the old version's CheckSwap records why and removes
// the work folder.
func (h *helper) giveUp(why string) error {
	h.mark("rolled-back", why+"\n")
	h.mark("done", "")
	h.say("starting the old version again")
	if _, err := h.ops.start(filepath.Join(h.job.Dir, h.job.Exe), h.job.Args, h.job.Env); err != nil {
		h.say("could not start the old version: %v", err)
	}
	return errors.New(why)
}

// errMoveBack is moveAll failing to put back what it had moved.
var errMoveBack = errors.New("and could not move everything back")

// moveAll moves every entry of src into dst, which it creates, and puts
// back what it moved when one of them will not move (errMoveBack when
// that fails too).
func moveAll(src, dst string) error {
	names, err := entries(src)
	if err != nil {
		return err
	}
	return moveNames(src, dst, names)
}

// moveNames is moveAll for the entries of src that names names; those
// src lacks are skipped.
func moveNames(src, dst string, names []string) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	var moved []string
	for _, name := range names {
		if _, err := os.Lstat(filepath.Join(src, name)); os.IsNotExist(err) {
			continue
		}
		if err := rename(filepath.Join(src, name), filepath.Join(dst, name)); err != nil {
			for i := len(moved) - 1; i >= 0; i-- {
				if rerr := rename(filepath.Join(dst, moved[i]), filepath.Join(src, moved[i])); rerr != nil {
					return fmt.Errorf("%w: %w (%s: %v)", err, errMoveBack, moved[i], rerr)
				}
			}
			return err
		}
		moved = append(moved, name)
	}
	return nil
}

// entries are the names of the entries of dir.
func entries(dir string) ([]string, error) {
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(list))
	for i, e := range list {
		names[i] = e.Name()
	}
	return names, nil
}

// archiveEntries are the entries of the install folder dir that are
// Pinky's, as MyGo's file list has it: the top-level entries that its
// .mygo-files names, the list itself, and those of the next release
// (release), which take the place of what the person has there by those
// names.
func archiveEntries(dir string, release []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range append(append(listedEntries(filepath.Join(dir, fileList)), fileList), release...) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// keepExtras moves back into dir what only old has, in subfolders too, as
// an installer that writes over the folder would have left it. It returns
// what would not move.
func keepExtras(old, dir string) []string {
	entries, err := os.ReadDir(old)
	if err != nil {
		return nil
	}
	var left []string
	for _, e := range entries {
		from, to := filepath.Join(old, e.Name()), filepath.Join(dir, e.Name())
		fi, err := os.Lstat(to)
		switch {
		case os.IsNotExist(err):
			if err := rename(from, to); err != nil {
				left = append(left, e.Name())
			}
		case err == nil && fi.IsDir() && e.IsDir():
			for _, l := range keepExtras(from, to) {
				left = append(left, filepath.Join(e.Name(), l))
			}
		}
	}
	return left
}

// rename tries for a few seconds: a scanner may hold a file a moment.
func rename(from, to string) error {
	var err error
	for i := 0; i < renameTries; i++ {
		if i > 0 {
			time.Sleep(renameWait)
		}
		if err = os.Rename(from, to); err == nil {
			return nil
		}
	}
	return err
}

// checkSetup checks that a download is a Windows program, as the release's
// installer is (its checksum, from the feed, already matched).
func checkSetup(file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 2)
	if _, err := io.ReadFull(f, head); err != nil || string(head) != "MZ" {
		return fmt.Errorf("the update %s is not an installer", filepath.Base(file))
	}
	return nil
}

// withoutVar drops a variable from an environment (names are case
// insensitive on Windows).
func withoutVar(env []string, name string) []string {
	var out []string
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !strings.EqualFold(k, name) {
			out = append(out, kv)
		}
	}
	return out
}

// withVars sets variables (NAME=value) in an environment.
func withVars(env, vars []string) []string {
	out := append([]string(nil), env...)
	for _, kv := range vars {
		k, _, _ := strings.Cut(kv, "=")
		out = append(withoutVar(out, k), kv)
	}
	return out
}
