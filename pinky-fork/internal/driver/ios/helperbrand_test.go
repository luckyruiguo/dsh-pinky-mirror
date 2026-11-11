package ios

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"howett.net/plist"
)

// runnerInfo is the Info.plist of a runner as Xcode builds it, cut down.
func runnerInfo(platform string) map[string]any {
	return map[string]any{
		"CFBundleIdentifier":         "com.example.WebDriverAgentRunner.xctrunner",
		"CFBundleName":               "WebDriverAgentRunner-Runner",
		"CFBundleExecutable":         "WebDriverAgentRunner-Runner",
		"CFBundleSupportedPlatforms": []any{platform},
		"MinimumOSVersion":           "17.0",
		"UIDeviceFamily":             []any{uint64(1), uint64(2)},
		"CFBundleIcons":              map[string]any{"CFBundlePrimaryIcon": map[string]any{"CFBundleIconName": "AppIcon"}},
	}
}

func TestBrandInfo(t *testing.T) {
	info := runnerInfo("iPhoneOS")
	if branded(info) {
		t.Fatal("an unbranded runner reads as branded")
	}
	icons := map[string]any{"CFBundlePrimaryIcon": map[string]any{
		"CFBundleIconFiles": []any{"AppIcon60x60"}, "CFBundleIconName": "AppIcon"}}
	brandInfo(info, map[string]any{"CFBundleIcons": icons, "CFBundleIcons~ipad": icons})
	if got := info["CFBundleDisplayName"]; got != "Pinky Helper" {
		t.Errorf("CFBundleDisplayName = %v", got)
	}
	if !reflect.DeepEqual(info["CFBundleIcons"], icons) || !reflect.DeepEqual(info["CFBundleIcons~ipad"], icons) {
		t.Errorf("icons = %v, %v", info["CFBundleIcons"], info["CFBundleIcons~ipad"])
	}
	// What Pinky and policy find the runner by stays.
	for k, want := range map[string]string{
		"CFBundleIdentifier": "com.example.WebDriverAgentRunner.xctrunner",
		"CFBundleName":       "WebDriverAgentRunner-Runner",
		"CFBundleExecutable": "WebDriverAgentRunner-Runner",
	} {
		if info[k] != want {
			t.Errorf("%s = %v, want %s", k, info[k], want)
		}
	}
	// The mark survives a round trip through the file, in both formats.
	for _, format := range []int{plist.XMLFormat, plist.BinaryFormat} {
		b, err := plist.Marshal(info, format)
		if err != nil {
			t.Fatal(err)
		}
		var back map[string]any
		if _, err := plist.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if !branded(back) {
			t.Errorf("format %d: branded runner reads as unbranded: %v", format, back[brandKey])
		}
		if !pickRunnerAppFinds(back) {
			t.Errorf("format %d: Pinky no longer finds the branded runner", format)
		}
	}
	info[brandKey] = uint64(brandVersion - 1)
	if branded(info) {
		t.Error("an older brand reads as this one")
	}
}

// pickRunnerAppFinds reports whether Pinky picks an app with this
// Info.plist as the runner, as the installation proxy lists it.
func pickRunnerAppFinds(info map[string]any) bool {
	return pickRunnerApp([]installationproxy.AppInfo{info}).BundleID != ""
}

func TestActoolArgs(t *testing.T) {
	sim := actoolArgs(runnerInfo("iPhoneSimulator"), "cat", "out", "p.plist")
	want := []string{"cat", "--compile", "out", "--app-icon", "AppIcon",
		"--platform", "iphonesimulator", "--minimum-deployment-target", "17.0",
		"--target-device", "iphone", "--target-device", "ipad",
		"--output-partial-info-plist", "p.plist", "--output-format", "human-readable-text",
		"--errors", "--warnings", "--compress-pngs", "--development-region", "en"}
	if !slices.Equal(sim, want) {
		t.Errorf("Simulator:\n got %q\nwant %q", sim, want)
	}
	phone := runnerInfo("iPhoneOS")
	phone["UIDeviceFamily"] = []any{int64(1)}
	delete(phone, "MinimumOSVersion")
	args := actoolArgs(phone, "cat", "out", "p.plist")
	for _, w := range [][2]string{{"--platform", "iphoneos"}, {"--minimum-deployment-target", "15.0"}, {"--target-device", "iphone"}} {
		i := slices.Index(args, w[0])
		if i < 0 || args[i+1] != w[1] {
			t.Errorf("phone: %s is not %s: %q", w[0], w[1], args)
		}
	}
	if slices.Contains(args, "ipad") {
		t.Errorf("phone: an iPhone-only runner gets iPad icons: %q", args)
	}
	if got := iconDevices(map[string]any{}); !slices.Equal(got, []string{"iphone", "ipad"}) {
		t.Errorf("no UIDeviceFamily: %q", got)
	}
}

func TestInfoStrings(t *testing.T) {
	b, err := infoStrings("Pinky 助手")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if _, err := plist.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["CFBundleDisplayName"] != "Pinky 助手" || len(m) != 1 {
		t.Errorf("strings = %v", m)
	}
}

func TestRunnerAppIn(t *testing.T) {
	products := t.TempDir()
	if got := runnerAppIn(products); got != "" {
		t.Errorf("empty Products: %q", got)
	}
	app := filepath.Join(products, "Debug-iphoneos", "WebDriverAgentRunner-Runner.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := runnerAppIn(products); got != app {
		t.Errorf("runnerAppIn = %q, want %q", got, app)
	}
}

// TestBrandRunner brands a runner with the Mac's own actool, where there
// is one: the icon files and translations land in the app, and branding
// again changes nothing.
func TestBrandRunner(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("actool runs on macOS")
	}
	x, err := findXcode()
	if err != nil {
		t.Skip("no Xcode:", err)
	}
	if _, err := os.Stat(x.tool("actool")); err != nil {
		t.Skip("no actool:", err)
	}
	app := filepath.Join(t.TempDir(), "WebDriverAgentRunner-Runner.app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := plist.Marshal(runnerInfo("iPhoneSimulator"), plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Info.plist"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	changed, err := brandRunner(ctx, x, app)
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Skip("actool cannot run on this runner:", err)
		}
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("brandRunner changed nothing")
	}
	for _, f := range []string{"Assets.car", "AppIcon60x60@2x.png", "zh-Hans.lproj/InfoPlist.strings", "ja.lproj/InfoPlist.strings"} {
		if _, err := os.Stat(filepath.Join(app, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	info, format, err := readInfo(app)
	if err != nil {
		t.Fatal(err)
	}
	if format != plist.XMLFormat {
		t.Errorf("Info.plist changed format to %d", format)
	}
	if !branded(info) || info["CFBundleDisplayName"] != "Pinky Helper" {
		t.Errorf("Info.plist = %v", info)
	}
	icons, _ := info["CFBundleIcons"].(map[string]any)
	primary, _ := icons["CFBundlePrimaryIcon"].(map[string]any)
	if primary["CFBundleIconName"] != "AppIcon" {
		t.Errorf("CFBundleIcons = %v", info["CFBundleIcons"])
	}
	if changed, err := brandRunner(ctx, x, app); err != nil || changed {
		t.Errorf("branding again: changed %v, %v", changed, err)
	}
}
