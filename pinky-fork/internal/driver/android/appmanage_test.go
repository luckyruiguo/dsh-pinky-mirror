package android

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// scriptedPM answers package manager commands by what they hold.
type scriptedPM struct {
	answers []pmAnswer
	ran     []string
	pushed  map[string]string // remote to local
}

type pmAnswer struct {
	prefix string
	out    string
	code   int
}

func (s *scriptedPM) run(_ context.Context, cmd string) (string, int, error) {
	s.ran = append(s.ran, cmd)
	for _, a := range s.answers {
		if strings.Contains(cmd, a.prefix) {
			return a.out, a.code, nil
		}
	}
	return "", 0, nil
}

func (s *scriptedPM) push(_ context.Context, local, remote string) error {
	if s.pushed == nil {
		s.pushed = map[string]string{}
	}
	s.pushed[remote] = local
	return nil
}

func pmDevice(pm *scriptedPM) *Device {
	d := newDevice(Config{}, driver.Info{ID: "phone"}, deviceProps{SDK: 34})
	d.pkgTest = pm
	return d
}

const (
	testAPK      = "../../apppkg/testdata/pinky-test-1.apk"
	testAPKSplit = "../../apppkg/testdata/pinky-test-1-de.apk"
)

func TestInspectPackageAndroid(t *testing.T) {
	ctx := context.Background()
	pm := &scriptedPM{}
	d := pmDevice(pm)
	p, err := d.InspectPackage(ctx, []string{testAPK})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "app.pinky.testapp" || p.Name != "Pinky Test" || p.Version != "1.0 (1)" || p.Installed != "" ||
		!strings.HasPrefix(p.Signer, "Pinky Test (292579aa") || !strings.HasPrefix(p.SignerKey, "292579aa") {
		t.Fatalf("package %+v", p)
	}
	if len(pm.ran) != 1 || !strings.HasPrefix(pm.ran[0], "u=$(am get-current-user 2>/dev/null);") ||
		!strings.Contains(pm.ran[0], `pm list packages --user "$u" --show-versioncode app.pinky.testapp;`) {
		t.Fatalf("ran %q", pm.ran)
	}

	pm.answers = []pmAnswer{{prefix: "pm list packages", out: "package:app.pinky.testapp.extra versionCode:9\npackage:app.pinky.testapp versionCode:3\n" +
		versionMark + "\n    versionName=0.9-beta\n    versionName=0.1\n"}}
	if p, err = d.InspectPackage(ctx, []string{testAPK}); err != nil || p.Installed != "0.9-beta (3)" {
		t.Fatalf("installed: %+v, %v", p, err)
	}
	pm.answers = []pmAnswer{{prefix: "pm list packages", out: "package:app.pinky.testapp.extra versionCode:9\n" + versionMark + "\n"}}
	if p, err = d.InspectPackage(ctx, []string{testAPK}); err != nil || p.Installed != "" {
		t.Fatalf("another package only: %+v, %v", p, err)
	}

	notes := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(notes, []byte("hi"), 0o600)
	for _, paths := range [][]string{nil, {notes}, {testAPKSplit}} {
		if _, err := d.InspectPackage(ctx, paths); !errors.Is(err, driver.ErrPackage) {
			t.Errorf("%v: %v, want ErrPackage", paths, err)
		}
	}
}

func TestInstallPackageAndroid(t *testing.T) {
	ctx := context.Background()
	pm := &scriptedPM{answers: []pmAnswer{{prefix: "pm install ", out: "Performing Streamed Install\nSuccess"}}}
	d := pmDevice(pm)
	d.apps, d.appsAt = []driver.App{{ID: "x"}}, time.Now()
	if err := d.InstallPackage(ctx, []string{testAPK}); err != nil {
		t.Fatal(err)
	}
	if len(pm.ran) != 2 || !strings.HasPrefix(pm.ran[0], "pm install -r -t --user current /data/local/tmp/pinky-app-") ||
		!strings.HasPrefix(pm.ran[1], "rm -f /data/local/tmp/pinky-app-") || len(pm.pushed) != 1 {
		t.Fatalf("ran %q, pushed %v", pm.ran, pm.pushed)
	}
	if !d.appsAt.IsZero() {
		t.Fatal("the app list was not read again")
	}

	// Split APKs go in one session.
	pm = &scriptedPM{answers: []pmAnswer{
		{prefix: "pm install-create", out: "Success: created install session [1234]"},
		{prefix: "pm install-write", out: "Success: streamed 100 bytes"},
		{prefix: "pm install-commit", out: "Success"},
	}}
	d = pmDevice(pm)
	if err := d.InstallPackage(ctx, []string{testAPK, testAPKSplit}); err != nil {
		t.Fatal(err)
	}
	want := []string{"pm install-create -r -t --user current -S ", "pm install-write -S ", "pm install-write -S ", "pm install-commit 1234", "rm -f "}
	if len(pm.ran) != len(want) {
		t.Fatalf("ran %q", pm.ran)
	}
	for i, w := range want {
		if !strings.HasPrefix(pm.ran[i], w) {
			t.Fatalf("ran %q", pm.ran)
		}
	}
	if !strings.Contains(pm.ran[1], " 1234 0.apk /data/local/tmp/pinky-app-") || !strings.Contains(pm.ran[2], " 1234 1.apk ") {
		t.Fatalf("writes %q", pm.ran[1:3])
	}

	// A failed write abandons the session.
	pm = &scriptedPM{answers: []pmAnswer{
		{prefix: "pm install-create", out: "Success: created install session [77]"},
		{prefix: "pm install-write", out: "Failure [INSTALL_FAILED_INSUFFICIENT_STORAGE]", code: 1},
	}}
	d = pmDevice(pm)
	err := d.InstallPackage(ctx, []string{testAPK, testAPKSplit})
	if !errors.Is(err, driver.ErrPackage) || !strings.Contains(err.Error(), "not enough free storage for “Pinky Test”") {
		t.Fatalf("storage: %v", err)
	}
	if got := strings.Join(pm.ran, "\n"); !strings.Contains(got, "pm install-abandon 77") || !strings.Contains(got, "rm -f ") {
		t.Fatalf("ran %q", pm.ran)
	}
}

func TestInstallErrorAndroid(t *testing.T) {
	for out, want := range map[string]string{
		"Failure [INSTALL_FAILED_UPDATE_INCOMPATIBLE: Existing package app.pinky.testapp signatures do not match newer version; ignoring!]": "signed with another key",
		"Failure [INSTALL_FAILED_VERSION_DOWNGRADE]":                                                  "does not install an older one",
		"Failure [INSTALL_FAILED_USER_RESTRICTED: Install canceled by user]":                          "Install via USB",
		"Failure [INSTALL_FAILED_OLDER_SDK: Requires newer sdk version #36 (current version is #34)]": "newer Android",
		"Failure [INSTALL_FAILED_SOMETHING_NEW: odd]":                                                 "The phone did not install “Pinky Test”: Failure [INSTALL_FAILED_SOMETHING_NEW: odd].",
		"Error: java.lang.IllegalStateException\nat x":                                                "The phone did not install “Pinky Test”: Error: java.lang.IllegalStateException",
		"": "it gave no reason",
	} {
		err := installError("Pinky Test", out)
		if !errors.Is(err, driver.ErrPackage) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", out, err, want)
		}
	}
}

func TestUninstallAndStopAndroid(t *testing.T) {
	ctx := context.Background()
	pm := &scriptedPM{answers: []pmAnswer{{prefix: "pm uninstall", out: "Success"}}}
	d := pmDevice(pm)
	if err := d.UninstallApp(ctx, "app.pinky.testapp"); err != nil {
		t.Fatal(err)
	}
	if err := d.UninstallApp(ctx, "com.tencent.mm@999"); err != nil {
		t.Fatal(err)
	}
	if err := d.StopApp(ctx, "com.tencent.mm@10"); err != nil {
		t.Fatal(err)
	}
	if err := d.StopApp(ctx, "app.pinky.testapp"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"pm uninstall --user current app.pinky.testapp",
		"pm uninstall --user 999 com.tencent.mm",
		"am force-stop --user 10 com.tencent.mm",
		"am force-stop --user current app.pinky.testapp",
	}
	if strings.Join(pm.ran, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran %q", pm.ran)
	}

	pm.answers = []pmAnswer{{prefix: "pm uninstall", out: "Failure [DELETE_FAILED_INTERNAL_ERROR]", code: 1}}
	if err := d.UninstallApp(ctx, "app.gone"); !errors.Is(err, driver.ErrNotFound) {
		t.Fatalf("not installed: %v", err)
	}
	pm.answers = []pmAnswer{{prefix: "pm uninstall", out: "Failure [DELETE_FAILED_INTERNAL_ERROR]", code: 1},
		{prefix: "pm list packages", out: "package:app.stuck"}}
	if err := d.UninstallApp(ctx, "app.stuck"); !errors.Is(err, driver.ErrPackage) {
		t.Fatalf("internal error: %v", err)
	}
	pm.answers = []pmAnswer{{prefix: "pm uninstall", out: "Failure [DELETE_FAILED_DEVICE_POLICY_MANAGER]", code: 1}}
	if err := d.UninstallApp(ctx, "com.corp.mdm"); !errors.Is(err, driver.ErrPackage) || !strings.Contains(err.Error(), "device administrator") {
		t.Fatalf("admin: %v", err)
	}
}
