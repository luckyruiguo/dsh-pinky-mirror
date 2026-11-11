package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"howett.net/plist"
)

// TeamID is the Apple team whose Developer ID signs Pinky.app.
const TeamID = "492J8Q67PF"

// requirement is what a Pinky.app must satisfy to be installed: signed with
// a Developer ID Application certificate of Pinky's team, as Pinky.app.
const requirement = `anchor apple generic and identifier "` + BundleID + `"` +
	` and certificate 1[field.1.2.840.113635.100.6.2.6] exists` +
	` and certificate leaf[field.1.2.840.113635.100.6.1.13] exists` +
	` and certificate leaf[subject.OU] = "` + TeamID + `"`

// notarized is the code requirement that Apple notarized the code: its
// ticket is stapled to the bundle, which codesign reads without a network,
// or one the system already knows. Releases are stapled. (codesign's
// --check-notarization would look the ticket up online, which fails where
// Apple's servers cannot be reached.)
const notarized = "notarized"

// updateRequirement is what a downloaded Pinky.app must satisfy to be
// swapped in: requirement and, but in the e2e build, notarized.
func updateRequirement() string {
	if notarizationRequired {
		return requirement + " and " + notarized
	}
	return requirement
}

// codesign checks an app bundle's signature against a code requirement;
// deep also checks every nested binary (FFmpeg's libraries). It returns
// what codesign said when the check fails.
func codesign(ctx context.Context, app, req string, deep bool) (string, error) {
	args := []string{"--verify", "--strict"}
	if deep {
		args = append(args, "--deep")
	}
	args = append(args, "-R", "="+req, app)
	out, err := exec.CommandContext(ctx, "/usr/bin/codesign", args...).CombinedOutput()
	if err != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	return strings.TrimSpace(string(out)), err
}

// verifySignature checks that an app bundle is signed by Pinky's team
// (requirement); deep also checks every nested binary.
func verifySignature(ctx context.Context, app string, deep bool) error {
	out, err := codesign(ctx, app, requirement, deep)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		return fmt.Errorf("%w: %s", ErrSignature, out)
	}
	return nil
}

// verifyUpdate is the check a downloaded Pinky.app must pass: signed by
// Pinky's team, every nested binary too, and notarized by Apple (but in the
// e2e build, whose bundles the test signs and cannot notarize).
func verifyUpdate(ctx context.Context, app string) error {
	if err := verifySignature(ctx, app, true); err != nil {
		return err
	}
	if !notarizationRequired {
		return nil
	}
	out, err := codesign(ctx, app, notarized, false)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		return fmt.Errorf("%w: %s", ErrNotarized, out)
	}
	return nil
}

// BundleInstaller replaces Pinky.app (macOS).
type BundleInstaller struct {
	// Bundle is the installed Pinky.app.
	Bundle string
	// Verify checks a new bundle's signature; nil is codesign: Pinky's team
	// and Apple's notarization (tests replace it).
	Verify func(ctx context.Context, app string) error
	// Open starts the app after a swap; nil is /usr/bin/open (tests
	// replace it). The app's path and its arguments follow.
	Open []string
	// Log is the helper's log file; "" is pinky-update.log in the
	// temporary folder.
	Log string
	// PID is the process the swap waits for; 0 is this one.
	PID int
	// LaunchWait is how long the helper waits for the new version to
	// launch (take the gateway's lock) before it puts the old one back; 0
	// is three minutes, as the gateway may wait on adb as it starts.
	LaunchWait time.Duration
	Logger     *slog.Logger
}

// Stage unpacks a release's zip next to the installed bundle (so the swap
// is a rename on the same volume) and checks it: one app bundle, signed by
// Pinky's team, Pinky's identifier and the release's version.
func (b *BundleInstaller) Stage(ctx context.Context, file string, r Release) (Staged, error) {
	work, err := os.MkdirTemp(filepath.Dir(b.Bundle), workPrefix(b.Bundle))
	if err != nil {
		return nil, fmt.Errorf("cannot write next to %s: %w", b.Bundle, err)
	}
	s := &stagedBundle{b: b, work: work, version: r.Version}
	if err := s.unpack(ctx, file); err != nil {
		s.Discard()
		return nil, err
	}
	return s, nil
}

type stagedBundle struct {
	b       *BundleInstaller
	work    string
	app     string // the new bundle, inside work
	version string
}

func (s *stagedBundle) unpack(ctx context.Context, file string) error {
	dst := filepath.Join(s.work, "new")
	if out, err := exec.CommandContext(ctx, "/usr/bin/ditto", "-x", "-k", file, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("unpack the update: %v: %s", err, strings.TrimSpace(string(out)))
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	var apps []string
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			apps = append(apps, e.Name())
		}
	}
	if len(apps) != 1 {
		return fmt.Errorf("the update holds %d apps, not one Pinky.app", len(apps))
	}
	s.app = filepath.Join(dst, apps[0])
	info, err := readBundleInfo(s.app)
	if err != nil {
		return err
	}
	if info.ID != BundleID {
		return fmt.Errorf("the update is %q, not Pinky (%s)", info.ID, BundleID)
	}
	if Compare(info.Version, s.version) != 0 || !IsRelease(info.Version) {
		return fmt.Errorf("the update's app is version %q, the release says %s", info.Version, s.version)
	}
	if fi, err := os.Stat(filepath.Join(s.app, "Contents", "MacOS", info.Exe)); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("the update's app has no executable %q", info.Exe)
	}
	verify := s.b.Verify
	if verify == nil {
		verify = verifyUpdate
	}
	if err := verify(ctx, s.app); err != nil {
		return err
	}
	// Pinky checked the signature itself; a quarantine flag would have
	// macOS ask the person at the first start, which an unattended start
	// after an update must not wait for.
	_ = exec.CommandContext(ctx, "/usr/bin/xattr", "-dr", "com.apple.quarantine", s.app).Run()
	return nil
}

type bundleInfo struct {
	ID      string `plist:"CFBundleIdentifier"`
	Version string `plist:"CFBundleShortVersionString"`
	Exe     string `plist:"CFBundleExecutable"`
}

func readBundleInfo(app string) (bundleInfo, error) {
	var info bundleInfo
	data, err := os.ReadFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return info, fmt.Errorf("the update's app has no Info.plist: %w", err)
	}
	if _, err := plist.Unmarshal(data, &info); err != nil {
		return info, fmt.Errorf("the update's Info.plist: %w", err)
	}
	if info.Exe == "" || strings.ContainsAny(info.Exe, "/\x00") {
		return info, fmt.Errorf("the update's Info.plist names executable %q", info.Exe)
	}
	return info, nil
}

// Swap writes what the new version looks for (pending.json) and starts the
// helper, in a session of its own so that it outlives this process.
func (s *stagedBundle) Swap(opt StartOptions) error {
	b := s.b
	pid := b.PID
	if pid == 0 {
		pid = os.Getpid()
	}
	from := ""
	if info, err := readBundleInfo(b.Bundle); err == nil {
		from = info.Version
	}
	if err := writePending(s.work, from, s.version); err != nil {
		return err
	}
	logPath := b.Log
	if logPath == "" {
		logPath = filepath.Join(os.TempDir(), "pinky-update.log")
	}
	open := b.Open
	if open == nil {
		open = []string{"/usr/bin/open"}
		if opt.Background {
			open = append(open, "-g")
		}
		for _, e := range opt.Env {
			open = append(open, "--env", e)
		}
	}
	start := append(append([]string(nil), open...), b.Bundle)
	if len(opt.Args) > 0 {
		if b.Open == nil {
			start = append(start, "--args")
		}
		start = append(start, opt.Args...)
	}
	wait := b.LaunchWait
	if wait <= 0 {
		wait = 3 * time.Minute
	}
	tenths := strconv.Itoa(int(wait / (100 * time.Millisecond)))
	// The helper checks the signature (and the notarization) again right
	// before the swap: the new bundle may have waited next to the app for a
	// while since Stage.
	req := ""
	if b.Verify == nil {
		req = updateRequirement()
	}
	args := append([]string{"-c", swapScript, "pinky-update", strconv.Itoa(pid), b.Bundle, s.app, s.work, logPath, tenths, req}, start...)
	cmd := exec.Command("/bin/sh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the update helper: %w", err)
	}
	if b.Logger != nil {
		b.Logger.Info("update: staged; swapping once Pinky exits", "from", from, "to", s.version, "work", s.work, "helper", cmd.Process.Pid)
	}
	return cmd.Process.Release()
}

func (s *stagedBundle) Discard() { os.RemoveAll(s.work) }

// SwapNow puts the new version in place at once, for `pinky update` while
// Pinky is not running: it renames the installed bundle aside, the new one
// in, and removes the old one, putting it back should the second rename
// fail.
func (s *stagedBundle) SwapNow() error {
	old := filepath.Join(s.work, "old.app")
	if err := os.Rename(s.b.Bundle, old); err != nil {
		s.Discard()
		return fmt.Errorf("move %s aside: %w", s.b.Bundle, err)
	}
	if err := os.Rename(s.app, s.b.Bundle); err != nil {
		if rerr := os.Rename(old, s.b.Bundle); rerr != nil {
			return fmt.Errorf("move the new version in: %w (and putting the old one back failed: %v; it is in %s)", err, rerr, old)
		}
		s.Discard()
		return fmt.Errorf("move the new version in: %w", err)
	}
	s.Discard()
	return nil
}

// swapScript is the helper, for /bin/sh. Its arguments: the pid of the
// running Pinky, the installed bundle, the new bundle, the work folder, a
// log file, how long to wait for the new version to launch (in tenths of a
// second) and the code requirement to check the new bundle against (empty:
// none), then the command that starts Pinky.
//
// It waits for Pinky to exit (a zombie counts as exited), checks the new
// bundle's signature again, moves the installed bundle to work/old.app and
// the new one in its place, and starts it. The new version writes its pid
// to launched once it holds the gateway's lock (CheckSwap) and started
// once its window is up (Confirm); the helper then removes the work folder
// with the old bundle. A new version that exits before that, or never
// launches, is moved to work/failed.app and the old one put back and
// started again. Whenever the update does not happen, the helper leaves
// rolled-back in the work folder, with why ("didn't start", completing
// "Pinky <version> …"), so the old version knows and does not try the same
// version again by itself. It never removes a bundle that is not
// in place elsewhere: if the old one cannot be put back, both stay in the
// work folder and the log says where.
const swapScript = `pid=$1 target=$2 new=$3 work=$4 log=$5 wait=$6 req=$7
shift 7
trap '' HUP INT
old=$work/old.app
say() { echo "$(date '+%Y-%m-%d %H:%M:%S') pinky-update[$$]: $*" >>"$log" 2>/dev/null; }
alive() {
	kill -0 "$1" 2>/dev/null || return 1
	case $(ps -o stat= -p "$1" 2>/dev/null) in
	*Z*) return 1 ;;
	esac
	return 0
}
put_back() {
	i=0
	until mv "$old" "$target" 2>>"$log"; do
		i=$((i + 1))
		if [ "$i" -ge 5 ]; then
			return 1
		fi
		sleep 1
	done
}
n=0
while alive "$pid"; do
	n=$((n + 1))
	if [ "$n" -gt 6000 ]; then
		say "Pinky (pid $pid) still runs after 10 minutes; not updating"
		exit 1
	fi
	sleep 0.1
done
if [ -n "$req" ] && ! /usr/bin/codesign --verify --deep --strict -R "=$req" "$new" >>"$log" 2>&1; then
	say "the new version's signature does not check out; not installing it"
	rm -rf "$new"
	echo "didn't pass the signature check" >"$work/rolled-back"
	"$@" >>"$log" 2>&1
	exit 1
fi
if ! mv "$target" "$old" 2>>"$log"; then
	say "could not move $target aside; keeping it"
	rm -rf "$new"
	echo "couldn't replace $target" >"$work/rolled-back"
	"$@" >>"$log" 2>&1
	exit 1
fi
if ! mv "$new" "$target" 2>>"$log"; then
	say "could not move the new version to $target; putting the old one back"
	if put_back; then
		echo "couldn't be moved to $target" >"$work/rolled-back"
		"$@" >>"$log" 2>&1
	else
		say "could not put the old version back: it is at $old"
	fi
	exit 1
fi
say "installed the new version at $target; starting it"
"$@" >>"$log" 2>&1
n=0
while [ ! -e "$work/started" ]; do
	lp=$(cat "$work/launched" 2>/dev/null)
	if [ -n "$lp" ]; then
		alive "$lp" || break
	elif [ "$n" -gt "$wait" ]; then
		break
	fi
	n=$((n + 1))
	sleep 0.1
done
if [ -e "$work/started" ]; then
	say "the new version is up; removing the old one"
	rm -rf "$work"
	exit 0
fi
say "the new version did not start; putting the old one back"
if ! mv "$target" "$work/failed.app" 2>>"$log"; then
	say "could not move the new version aside; keeping it"
	exit 1
fi
if put_back; then
	echo "didn't start" >"$work/rolled-back"
	"$@" >>"$log" 2>&1
elif mv "$work/failed.app" "$target" 2>>"$log"; then
	say "could not put the old version back (it is at $old); kept the new one"
else
	say "could not put either version back: the old one is at $old, the new one at $work/failed.app"
fi
exit 1
`

// CheckSwap looks next to the running bundle for a swap that just
// happened. For the new version it writes launched (this process's pid),
// so the helper waits for it to come up; call it once the gateway's lock is
// held, so a second instance that exits does not count. After a failed
// swap it returns Failed and removes what is left. It also removes staged
// updates left behind by earlier runs. nil when there is none.
func CheckSwap(version string) *Swapped {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	bundle := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
	if !strings.HasSuffix(bundle, ".app") {
		return nil
	}
	return checkSwap(bundle, version, os.Getpid(), time.Now())
}

// cleanUpAfter has nothing to do on macOS: the helper removes the work
// folder with the old bundle.
func cleanUpAfter(string) {}

// RunHelper is for Windows: the helper on macOS is a shell script.
func RunHelper(string) error { return ErrHelperOS }

// errNotBundle is for builds that run outside an app bundle.
var errNotBundle = errors.New("Pinky does not run from Pinky.app")

// NewInstaller returns the installer for a location that can replace
// itself, or an error saying why it cannot; the swap's helper logs to
// logFile.
func NewInstaller(l Location, logFile string, log *slog.Logger) (Installer, error) {
	if !l.CanReplace() {
		if l.Problem != "" {
			return nil, errors.New(l.Problem)
		}
		return nil, errNotBundle
	}
	return &BundleInstaller{Bundle: l.App, Log: logFile, Logger: log}, nil
}
