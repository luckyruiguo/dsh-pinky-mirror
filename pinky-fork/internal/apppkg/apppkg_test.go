package apppkg

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"howett.net/plist"
)

// The fixtures are built by testdata/testapp/build.sh (version 1, "1.0")
// with a throwaway key whose certificate has this SHA-256.
const testSigner = "292579aa7257b0bd7ca40ffec13f8e8dbdca0fc992cd6d77de7fd424cb46dab1"

func TestReadAPK(t *testing.T) {
	for _, file := range []string{"testdata/pinky-test-1.apk", "testdata/pinky-test-1-v1.apk"} {
		a, err := ReadAPK(file)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if a.Package != "app.pinky.testapp" || a.VersionCode != 1 || a.VersionName != "1.0" || a.Label != "Pinky Test" || a.Split != "" {
			t.Errorf("%s: %+v", file, a)
		}
		if a.Signer.Name != "Pinky Test" || a.Signer.SHA256 != testSigner {
			t.Errorf("%s: signer %+v", file, a.Signer)
		}
		if a.Version() != "1.0 (1)" || a.Name() != "Pinky Test" {
			t.Errorf("%s: Version %q Name %q", file, a.Version(), a.Name())
		}
	}
}

func TestReadAPKs(t *testing.T) {
	base, err := ReadAPKs([]string{"testdata/pinky-test-1-de.apk", "testdata/pinky-test-1.apk"})
	if err != nil {
		t.Fatal(err)
	}
	if base.File != "testdata/pinky-test-1.apk" || base.Package != "app.pinky.testapp" {
		t.Errorf("base %+v", base)
	}
	split, err := ReadAPK("testdata/pinky-test-1-de.apk")
	if err != nil || split.Split != "config.de" {
		t.Fatalf("split %+v %v", split, err)
	}
	for _, c := range []struct {
		files []string
		want  string
	}{
		{[]string{"testdata/pinky-test-1-de.apk"}, "without their base"},
		{[]string{"testdata/pinky-test-1.apk", "testdata/pinky-test-1-v1.apk"}, "both a base APK"},
		{[]string{"testdata/pinky-test-1.apk", "testdata/pinky-test-1-de.apk", "testdata/pinky-test-1-de.apk"}, "same split"},
		{nil, "no APK"},
	} {
		if _, err := ReadAPKs(c.files); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.files, err, c.want)
		}
	}
}

func TestReadAPKRefusesOtherFiles(t *testing.T) {
	dir := t.TempDir()
	text := filepath.Join(dir, "notes.apk")
	os.WriteFile(text, []byte("not a zip"), 0o644)
	if _, err := ReadAPK(text); err == nil || !strings.Contains(err.Error(), "not a ZIP") {
		t.Errorf("text: %v", err)
	}
	zipped := filepath.Join(dir, "other.apk")
	writeZip(t, zipped, map[string]string{"hello.txt": "hi"})
	if _, err := ReadAPK(zipped); err == nil || !strings.Contains(err.Error(), "no AndroidManifest.xml") {
		t.Errorf("zip: %v", err)
	}
}

func TestReadAPKUnsigned(t *testing.T) {
	// The fixture's entries without its signature files or signing block.
	src, err := zip.OpenReader("testdata/pinky-test-1-v1.apk")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	files := map[string]string{}
	for _, f := range src.File {
		if strings.HasPrefix(f.Name, "META-INF/") {
			continue
		}
		b, err := readEntry(f, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = string(b)
	}
	out := filepath.Join(t.TempDir(), "unsigned.apk")
	writeZip(t, out, files)
	if _, err := ReadAPK(out); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Errorf("unsigned: %v", err)
	}
}

func infoPlist(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := plist.Marshal(m, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReadIOSApp(t *testing.T) {
	dir := t.TempDir()
	info := infoPlist(t, map[string]any{
		"CFBundleIdentifier":         "com.example.dinely",
		"CFBundleDisplayName":        "Dinely",
		"CFBundleName":               "DinelyApp",
		"CFBundleShortVersionString": "1.3",
		"CFBundleVersion":            "14",
		"CFBundleSupportedPlatforms": []string{"iPhoneOS"},
		"MinimumOSVersion":           "17.0",
	})
	ipa := filepath.Join(dir, "Dinely.ipa")
	writeZip(t, ipa, map[string]string{
		"Payload/Dinely.app/Info.plist":               info,
		"Payload/Dinely.app/embedded.mobileprovision": "profile",
		"Payload/Dinely.app/Dinely":                   "binary",
	})
	a, err := ReadIOSApp(ipa)
	if err != nil {
		t.Fatal(err)
	}
	if a.BundleID != "com.example.dinely" || a.Name != "Dinely" || a.VersionText() != "1.3 (14)" || string(a.Profile) != "profile" || a.BundleFolder != "Dinely.app" || a.ForSimulator() {
		t.Errorf("ipa %+v", a)
	}

	app := filepath.Join(dir, "Sim.app")
	os.MkdirAll(app, 0o755)
	os.WriteFile(filepath.Join(app, "Info.plist"), []byte(infoPlist(t, map[string]any{
		"CFBundleIdentifier":         "com.example.sim",
		"CFBundleExecutable":         "Sim",
		"CFBundleSupportedPlatforms": []string{"iPhoneSimulator"},
	})), 0o644)
	a, err = ReadIOSApp(app)
	if err != nil {
		t.Fatal(err)
	}
	if a.BundleID != "com.example.sim" || a.Name != "Sim" || !a.ForSimulator() || a.Profile != nil {
		t.Errorf("app %+v", a)
	}

	two := filepath.Join(dir, "two.ipa")
	writeZip(t, two, map[string]string{"Payload/A.app/Info.plist": info, "Payload/B.app/Info.plist": info})
	if _, err := ReadIOSApp(two); err == nil || !strings.Contains(err.Error(), "more than one app") {
		t.Errorf("two apps: %v", err)
	}
	none := filepath.Join(dir, "none.ipa")
	writeZip(t, none, map[string]string{"Payload/readme.txt": "x"})
	if _, err := ReadIOSApp(none); err == nil || !strings.Contains(err.Error(), "no Payload") {
		t.Errorf("no app: %v", err)
	}
}

func TestPackApp(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "Dinely.app")
	os.MkdirAll(filepath.Join(app, "Frameworks"), 0o755)
	os.WriteFile(filepath.Join(app, "Info.plist"), []byte(infoPlist(t, map[string]any{"CFBundleIdentifier": "com.example.dinely"})), 0o644)
	os.WriteFile(filepath.Join(app, "Dinely"), []byte("binary"), 0o755)
	os.WriteFile(filepath.Join(app, "Frameworks", "lib.dylib"), []byte("lib"), 0o644)
	ipa := filepath.Join(dir, "out.ipa")
	if err := PackApp(app, ipa); err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(ipa)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range z.File {
		names = append(names, f.Name)
		// Windows keeps no executable bit to pack.
		if f.Name == "Payload/Dinely.app/Dinely" && runtime.GOOS != "windows" && f.Mode().Perm()&0o100 == 0 {
			t.Errorf("executable lost its mode: %v", f.Mode())
		}
	}
	z.Close()
	got := strings.Join(names, " ")
	for _, want := range []string{"Payload/Dinely.app/Info.plist", "Payload/Dinely.app/Dinely", "Payload/Dinely.app/Frameworks/lib.dylib"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if a, err := ReadIOSApp(ipa); err != nil || a.BundleID != "com.example.dinely" {
		t.Errorf("read back %+v %v", a, err)
	}

	os.Symlink("/etc/passwd", filepath.Join(app, "link"))
	bad := filepath.Join(dir, "bad.ipa")
	if err := PackApp(app, bad); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("symlink: %v", err)
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Errorf("a refused pack left %s behind", bad)
	}
}

func TestIsKind(t *testing.T) {
	if !IsAPK("a/B.APK") || IsAPK("a.ipa") || !IsIOSApp("x/Dinely.app/") || !IsIOSApp("d.IPA") || IsIOSApp("a.apk") {
		t.Error("kinds")
	}
}

func writeZip(t *testing.T, file string, files map[string]string) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, body := range files {
		e, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		e.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}
