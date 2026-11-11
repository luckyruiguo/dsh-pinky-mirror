package android

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/apppkg"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// driver.AppManager: installing APKs from the computer, uninstalling and
// force-stopping apps, for phone_app (docs/notes/apps.md). The gateway
// asks the person and refuses Pinky's own apps and system apps; this only
// runs the package manager.

// pkgShell runs the package manager on the phone; tests script it.
type pkgShell interface {
	// run runs cmd and returns its stdout and stderr together and its
	// exit status.
	run(ctx context.Context, cmd string) (out string, code int, err error)
	// push copies a file on the computer to the phone.
	push(ctx context.Context, local, remote string) error
}

type adbPkgShell struct {
	c      *adb.Client
	serial string
}

func (s adbPkgShell) run(ctx context.Context, cmd string) (string, int, error) {
	res, err := s.c.Run(ctx, s.serial, cmd)
	if err != nil {
		return "", 0, wrapErr(err)
	}
	return strings.TrimSpace(string(res.Stdout) + "\n" + string(res.Stderr)), res.ExitCode, nil
}

func (s adbPkgShell) push(ctx context.Context, local, remote string) error {
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	return wrapErr(s.c.Push(ctx, s.serial, f, remote, 0o644, time.Now()))
}

func (d *Device) pkgShell() pkgShell {
	if d.pkgTest != nil {
		return d.pkgTest
	}
	return adbPkgShell{c: d.client, serial: d.info.ID}
}

// InspectPackage implements driver.AppManager: the app in one APK or the
// split APKs of one app, and the version of it the phone's user has now.
func (d *Device) InspectPackage(ctx context.Context, paths []string) (driver.Package, error) {
	a, err := readAPKs(paths)
	if err != nil {
		return driver.Package{}, err
	}
	p := driver.Package{ID: a.Package, Name: a.Name(), Version: a.Version(), Signer: a.Signer.Text(), SignerKey: a.Signer.SHA256}
	p.Installed, err = d.installedVersion(ctx, a.Package)
	return p, err
}

func readAPKs(paths []string) (apppkg.APK, error) {
	if len(paths) == 0 {
		return apppkg.APK{}, driver.PackageError("No APK was given.")
	}
	for _, p := range paths {
		if !apppkg.IsAPK(p) {
			return apppkg.APK{}, driver.PackageError("An Android phone installs .apk files, and “%s” is not one.", filepath.Base(p))
		}
	}
	a, err := apppkg.ReadAPKs(paths)
	if err != nil {
		return apppkg.APK{}, driver.PackageError("%v", err)
	}
	return a, nil
}

// installedVersion is the version of pkg the phone's current user has,
// "1.3 (14)", or "" when that user does not have it.
func (d *Device) installedVersion(ctx context.Context, pkg string) (string, error) {
	q := adb.Quote(pkg)
	out, _, err := d.pkgShell().run(ctx, listUser("current")+`pm list packages --user "$u" --show-versioncode `+q+"; echo '"+versionMark+"'; dumpsys package "+q+" | grep -E 'versionName='")
	if err != nil {
		return "", err
	}
	return parseInstalledVersion(out, pkg), nil
}

const versionMark = "--pinky-version--"

// listUser sets $u to the user `pm list packages` should read. Android 10's
// pm lists nothing for `--user current` (its install and uninstall take
// it), so the current user goes by number.
func listUser(user string) string {
	if user != "current" {
		return "u=" + adb.Quote(user) + "; "
	}
	return `u=$(am get-current-user 2>/dev/null); case "$u" in ''|*[!0-9]*) u=0;; esac; `
}

// parseInstalledVersion reads what installedVersion runs: pm's line for
// the package ("package:com.example versionCode:14"; pm matches by
// substring, so others may be listed too), then dumpsys's versionName.
func parseInstalledVersion(out, pkg string) string {
	list, dump, _ := strings.Cut(out, versionMark)
	code := ""
	found := false
	for _, line := range strings.Split(list, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) == 0 || f[0] != "package:"+pkg {
			continue
		}
		found = true
		for _, x := range f[1:] {
			if v, ok := strings.CutPrefix(x, "versionCode:"); ok {
				code = v
			}
		}
	}
	if !found {
		return ""
	}
	name := ""
	for _, line := range strings.Split(dump, "\n") {
		if _, v, ok := strings.Cut(strings.TrimSpace(line), "versionName="); ok {
			name = strings.TrimSpace(v)
			break
		}
	}
	if v := apppkg.VersionText(name, code); v != "" {
		return v
	}
	return "installed"
}

// InstallPackage implements driver.AppManager. The APKs go to
// /data/local/tmp and the package manager installs them for the phone's
// current user, the one its screen and background displays run as:
// replacing the installed build (-r), taking test-only builds as Android
// Studio's Run makes them (-t), without downgrades or granting the app's
// permissions. The copies on the phone are removed either way.
func (d *Device) InstallPackage(ctx context.Context, paths []string) error {
	a, err := readAPKs(paths)
	if err != nil {
		return err
	}
	sh := d.pkgShell()
	var b [6]byte
	_, _ = rand.Read(b[:])
	tag := hex.EncodeToString(b[:])
	var remotes []string
	defer func() {
		if len(remotes) == 0 {
			return
		}
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		q := make([]string, len(remotes))
		for i, r := range remotes {
			q[i] = adb.Quote(r)
		}
		if _, _, err := sh.run(cctx, "rm -f "+strings.Join(q, " ")); err != nil {
			d.cfg.logf("android: %s: removing the copied APKs: %v", d.info.ID, err)
		}
	}()
	sizes := make([]int64, len(paths))
	for i, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return err
		}
		sizes[i] = fi.Size()
		remote := fmt.Sprintf("/data/local/tmp/pinky-app-%s-%d.apk", tag, i)
		remotes = append(remotes, remote)
		if err := sh.push(ctx, p, remote); err != nil {
			return fmt.Errorf("android: copying %s to the phone: %w", filepath.Base(p), err)
		}
	}
	if len(remotes) == 1 {
		out, code, err := sh.run(ctx, "pm install -r -t --user current "+adb.Quote(remotes[0]))
		if err != nil {
			return err
		}
		if code != 0 || !strings.Contains(out, "Success") {
			return installError(a.Name(), out)
		}
	} else if err := d.installSplits(ctx, sh, a.Name(), remotes, sizes); err != nil {
		return err
	}
	d.cfg.logf("android: %s: installed %s %s from %d APK(s)", d.info.ID, a.Package, a.Version(), len(paths))
	d.appsChanged()
	return nil
}

var sessionRe = regexp.MustCompile(`\[(\d+)\]`)

// installSplits installs the split APKs of one app in one package
// installer session, which it abandons when a step fails.
func (d *Device) installSplits(ctx context.Context, sh pkgShell, name string, remotes []string, sizes []int64) error {
	var total int64
	for _, n := range sizes {
		total += n
	}
	out, code, err := sh.run(ctx, fmt.Sprintf("pm install-create -r -t --user current -S %d", total))
	if err != nil {
		return err
	}
	m := sessionRe.FindStringSubmatch(out)
	if code != 0 || m == nil {
		return installError(name, out)
	}
	session := m[1]
	committed := false
	defer func() {
		if !committed {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			sh.run(cctx, "pm install-abandon "+session)
		}
	}()
	for i, r := range remotes {
		out, code, err := sh.run(ctx, fmt.Sprintf("pm install-write -S %d %s %d.apk %s", sizes[i], session, i, adb.Quote(r)))
		if err != nil {
			return err
		}
		if code != 0 || !strings.Contains(out, "Success") {
			return installError(name, out)
		}
	}
	out, code, err = sh.run(ctx, "pm install-commit "+session)
	if err != nil {
		return err
	}
	committed = true // a failed commit ends the session too
	if code != 0 || !strings.Contains(out, "Success") {
		return installError(name, out)
	}
	return nil
}

var failureRe = regexp.MustCompile(`Failure \[([A-Z_]+)(?::\s*([^\]]*))?\]`)

// installFailures says in plain words why the package manager refused an
// install, by its failure code.
var installFailures = map[string]string{
	"INSTALL_FAILED_UPDATE_INCOMPATIBLE":    "The installed “%s” is signed with another key, so the phone refused the update. Uninstalling it first lets this build install, but deletes its data",
	"INSTALL_FAILED_VERSION_DOWNGRADE":      "The phone has a newer build of “%s” (a higher version code) and does not install an older one over it. Raise the version code, or uninstall it first, which deletes its data",
	"INSTALL_FAILED_USER_RESTRICTED":        "The phone refused to install “%s” from the computer. On Xiaomi, Redmi and POCO phones the person turns on Install via USB in Developer options; other phones may have asked on their screen and been declined",
	"INSTALL_FAILED_ABORTED":                "The install of “%s” was cancelled on the phone. Some phones ask on their screen before installing from the computer; the person did not accept in time",
	"INSTALL_FAILED_DEPRECATED_SDK_VERSION": "“%s” targets an Android version too old for this phone to install. Raise its targetSdkVersion",
	"INSTALL_FAILED_NO_MATCHING_ABIS":       "“%s” has native code only for other processors than this phone's. Build it for the phone's ABI too",
	"INSTALL_FAILED_OLDER_SDK":              "“%s” needs a newer Android than this phone has (its minSdkVersion)",
	"INSTALL_PARSE_FAILED_NO_CERTIFICATES":  "The phone found no signature it accepts in “%s”. Sign it, as Android Studio's debug and release builds are",
	"INSTALL_FAILED_INSUFFICIENT_STORAGE":   "The phone has not enough free storage for “%s”",
	"INSTALL_FAILED_MISSING_SPLIT":          "“%s” needs split APKs that were not given. Pass all of them in paths",
	"INSTALL_FAILED_INVALID_APK":            "The phone found “%s” broken or incomplete",
	"INSTALL_FAILED_DUPLICATE_PERMISSION":   "“%s” declares a permission another installed app already owns",
	"INSTALL_FAILED_CONFLICTING_PROVIDER":   "“%s” declares a content provider another installed app already owns",
	"INSTALL_FAILED_VERIFICATION_FAILURE":   "The phone's package verification refused “%s”",
}

// installError turns the package manager's refusal into a plain-words
// driver.ErrPackage error.
func installError(name, out string) error {
	m := failureRe.FindStringSubmatch(out)
	if m == nil {
		return driver.PackageError("The phone did not install “%s”: %s", name, pmLine(out))
	}
	if msg, ok := installFailures[m[1]]; ok {
		return driver.PackageError(msg+" (%s).", name, m[1])
	}
	return driver.PackageError("The phone did not install “%s”: %s.", name, strings.TrimSpace(m[0]))
}

func pmLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "it gave no reason"
	}
	return s
}

// pkgUser is the package and the user an app's id stands for: the
// phone's current user, or a clone's own.
func pkgUser(id string) (pkg, user string) {
	if p, u, ok := driver.ParseCloneID(id); ok {
		return p, strconv.Itoa(u)
	}
	return id, "current"
}

// UninstallApp implements driver.AppManager: the app goes for the current
// user, or a clone for its own user; Android removes it entirely when no
// other user has it.
func (d *Device) UninstallApp(ctx context.Context, id string) error {
	pkg, user := pkgUser(id)
	out, code, err := d.pkgShell().run(ctx, "pm uninstall --user "+user+" "+adb.Quote(pkg))
	if err != nil {
		return err
	}
	defer d.appsChanged()
	if code == 0 && strings.Contains(out, "Success") {
		d.cfg.logf("android: %s: uninstalled %s for user %s", d.info.ID, pkg, user)
		return nil
	}
	m := failureRe.FindStringSubmatch(out)
	switch {
	case strings.Contains(out, "not installed for") || strings.Contains(out, "Unknown package") || m != nil && m[1] == "DELETE_FAILED_INTERNAL_ERROR" && !d.hasPackage(ctx, pkg, user):
		return fmt.Errorf("android: %s is not installed: %w", id, driver.ErrNotFound)
	case m != nil && m[1] == "DELETE_FAILED_DEVICE_POLICY_MANAGER":
		return driver.PackageError("The phone keeps %s: it is a device administrator, or the phone's management does not let it go (DELETE_FAILED_DEVICE_POLICY_MANAGER).", pkg)
	case m != nil && (m[1] == "DELETE_FAILED_OWNER_BLOCKED" || m[1] == "DELETE_FAILED_USER_RESTRICTED"):
		return driver.PackageError("The phone does not let %s be uninstalled (%s).", pkg, m[1])
	}
	return driver.PackageError("The phone did not uninstall %s: %s", pkg, pmLine(out))
}

// hasPackage reports whether user has pkg; on a failure to ask, true.
func (d *Device) hasPackage(ctx context.Context, pkg, user string) bool {
	out, _, err := d.pkgShell().run(ctx, listUser(user)+`pm list packages --user "$u" `+adb.Quote(pkg))
	if err != nil {
		return true
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "package:"+pkg {
			return true
		}
	}
	return false
}

// StopApp implements driver.AppManager with `am force-stop`, in the
// clone's user for a clone.
func (d *Device) StopApp(ctx context.Context, id string) error {
	pkg, user := pkgUser(id)
	out, code, err := d.pkgShell().run(ctx, "am force-stop --user "+user+" "+adb.Quote(pkg))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("android: force-stop %s: exit status %d: %s", id, code, pmLine(out))
	}
	d.cfg.logf("android: %s: stopped %s for user %s", d.info.ID, pkg, user)
	return nil
}

// appsChanged makes the next app list read the phone again.
func (d *Device) appsChanged() {
	d.appsMu.Lock()
	d.appsAt = time.Time{}
	d.appsMu.Unlock()
}

var _ driver.AppManager = (*Device)(nil)
