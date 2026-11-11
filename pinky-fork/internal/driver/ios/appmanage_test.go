package ios

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"howett.net/plist"
)

// writeIPA writes an .ipa of a Tool app with the given platform and
// profile (none when nil).
func writeIPA(t *testing.T, file, platform string, profile []byte) {
	t.Helper()
	info, err := plist.Marshal(map[string]any{
		"CFBundleIdentifier":         "com.example.tool",
		"CFBundleDisplayName":        "Tool",
		"CFBundleExecutable":         "Tool",
		"CFBundleShortVersionString": "2.1",
		"CFBundleVersion":            "7",
		"CFBundleSupportedPlatforms": []string{platform},
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	files := map[string][]byte{"Payload/Tool.app/Info.plist": info, "Payload/Tool.app/Tool": []byte("binary")}
	if profile != nil {
		files["Payload/Tool.app/embedded.mobileprovision"] = profile
	}
	for name, b := range files {
		e, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		e.Write(b)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestReadIOSPackage(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	udid := "00008110-001E30C9262A801E"
	dir := t.TempDir()
	profile := func(devices []string, expires time.Time) []byte {
		return berProfile(profileXML(t, "A1B2C3D4E5.com.example.tool", devices, expires), 1000)
	}
	good := filepath.Join(dir, "Tool.ipa")
	writeIPA(t, good, "iPhoneOS", profile([]string{"00008030-000000000000000A", strings.ToLower(udid)}, now.Add(72*time.Hour)))
	_, p, err := readIOSPackage([]string{good}, udid, now)
	if err != nil {
		t.Fatal(err)
	}
	want := driver.Package{ID: "com.example.tool", Name: "Tool", Version: "2.1 (7)", Signer: "Someone (Personal Team) (A1B2C3D4E5)", SignerKey: "A1B2C3D4E5"}
	if p != want {
		t.Fatalf("package %+v", p)
	}

	enterprise, err := plist.Marshal(map[string]any{
		"Name": "In House", "TeamName": "Example Corp", "TeamIdentifier": []string{"EXAMPLE123"},
		"ExpirationDate": now.Add(300 * 24 * time.Hour), "ProvisionsAllDevices": true,
		"Entitlements": map[string]any{"application-identifier": "EXAMPLE123.com.example.tool"},
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	all := filepath.Join(dir, "all.ipa")
	writeIPA(t, all, "iPhoneOS", derProfile(t, enterprise))
	if _, p, err := readIOSPackage([]string{all}, udid, now); err != nil || p.Signer != "Example Corp (EXAMPLE123)" {
		t.Fatalf("enterprise: %+v, %v", p, err)
	}

	cases := map[string]struct {
		platform string
		profile  []byte
		want     string
	}{
		"sim.ipa":     {"iPhoneSimulator", nil, "built for the iOS Simulator"},
		"bare.ipa":    {"iPhoneOS", nil, "has no provisioning profile"},
		"old.ipa":     {"iPhoneOS", profile([]string{udid}, now.Add(-time.Hour)), "ran out on"},
		"other.ipa":   {"iPhoneOS", profile([]string{"00008030-000000000000000A"}, now.Add(time.Hour)), "does not list this iPhone (UDID " + udid + ")"},
		"store.ipa":   {"iPhoneOS", profile(nil, now.Add(time.Hour)), "signed for the App Store"},
		"garbled.ipa": {"iPhoneOS", []byte("not a profile"), "cannot be read"},
	}
	for name, c := range cases {
		file := filepath.Join(dir, name)
		writeIPA(t, file, c.platform, c.profile)
		_, _, err := readIOSPackage([]string{file}, udid, now)
		if !errors.Is(err, driver.ErrPackage) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}

	notes := filepath.Join(dir, "notes.txt")
	os.WriteFile(notes, []byte("hi"), 0o600)
	notIPA := filepath.Join(dir, "fake.ipa")
	os.WriteFile(notIPA, []byte("hi"), 0o600)
	for _, paths := range [][]string{nil, {good, good}, {notes}, {"app-debug.apk"}, {notIPA}} {
		if _, _, err := readIOSPackage(paths, udid, now); !errors.Is(err, driver.ErrPackage) {
			t.Errorf("%v: %v, want ErrPackage", paths, err)
		}
	}
}

func TestInstallErrorIOS(t *testing.T) {
	for in, want := range map[string]string{
		"your app is not properly signed for this device, check your codesigning and provisioningprofile. original error: 'ApplicationVerificationFailed' errorDescription:'Failed to verify code signature of /var/installd/Tool.app : 0xe8008015 (A valid provisioning profile for this executable was not found.)'": "does not let it run on this phone. Sign it with a development profile that lists the phone, then build again (ApplicationVerificationFailed: Failed to verify code signature",
		"failed installing: 'DeviceOSVersionTooLow' errorDescription:'The system version is lower than the minimum OS version specified for bundle at path'":                                                                                                                                                           "“Tool” needs a newer iOS than this iPhone has (DeviceOSVersionTooLow",
		"failed installing: 'MismatchedApplicationIdentifierEntitlement' errorDescription:''": "signed by another team. Uninstall it first, or sign this build with the same team (MismatchedApplicationIdentifierEntitlement).",
		"failed installing: 'SomethingNew' errorDescription:'Odd things.'":                    "The iPhone did not install “Tool”: Odd things (SomethingNew).",
	} {
		err := installErrorIOS("Tool", errors.New(in))
		if !errors.Is(err, driver.ErrPackage) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q:\n%v\nwant %q", in, err, want)
		}
	}
	err := installErrorIOS("Tool", errors.New("read tcp: connection reset by peer"))
	if errors.Is(err, driver.ErrPackage) || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("transport: %v", err)
	}
}

func TestAppProcesses(t *testing.T) {
	bundles := []string{"/var/containers/Bundle/Application/AB12/Tool.app/"}
	procs := map[uint64]string{
		10: "/private/var/containers/Bundle/Application/AB12/Tool.app/Tool",
		11: "/private/var/containers/Bundle/Application/AB12/Tool.app/PlugIns/Widget.appex/Widget",
		12: "/var/containers/Bundle/Application/CD34/Other.app/Other",
		13: "/var/containers/Bundle/Application/AB12/Tool.app/Tool",
	}
	got := appProcesses(bundles, procs)
	slices.Sort(got)
	if !slices.Equal(got, []uint64{10, 13}) {
		t.Fatalf("pids %v", got)
	}
}
