package ios

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"howett.net/plist"

	"github.com/DIYgod/pinky/assets"
)

// The WebDriverAgent helper's name and icon on the phone.
//
// Xcode makes the app that hosts WebDriverAgent's test,
// WebDriverAgentRunner-Runner.app with the bundle id <id>.xctrunner, from
// a template of its own (XCTRunner.app in the platform's
// Developer/Library/Xcode/Agents). Of WDA's project only the bundle id,
// the name and the test target's icon reach it, so the home screen shows
// "WebDriverAgen…" with Appium's logo, or a blank icon. Nothing in the
// project names the runner otherwise: its Info.plist is the template's.
//
// So Pinky brands the built app afterwards: a display name, "Pinky Helper",
// with Chinese and Japanese ones in InfoPlist.strings for phones in those
// languages, and Pinky's icon, compiled by Xcode's actool into the app as
// Xcode does for an app. The bundle id, CFBundleName and the executable
// stay, so Pinky finds the runner as before (pickRunnerApp) and policy
// still knows it for Pinky's own (policy.IsPinkyApp). A build for a phone
// is signed again afterwards (helperinstall.go); a Simulator build is not
// signed for one (CODE_SIGNING_ALLOWED=NO), and the Simulator runs it as
// it is.

// helperDisplayName is the helper's name on the phone.
const helperDisplayName = "Pinky Helper"

// helperNames are its names in the other languages Pinky speaks, by the
// phone's language. "Pinky 助手" is shorter than the app's 「Pinky 辅助应用」
// and fits under the icon; Japanese uses the app's own ヘルパー.
var helperNames = map[string]string{
	"zh-Hans": "Pinky 助手",
	"ja":      "Pinky ヘルパー",
}

// brandKey marks a branded runner in its Info.plist with brandVersion.
// A later Pinky that changes the name or the icon raises the version, and
// brands a build again.
const (
	brandKey     = "PinkyHelperBrand"
	brandVersion = 1
)

// runnerAppIn returns the runner app in a build's Products folder, or ""
// when there is none.
func runnerAppIn(products string) string {
	m, _ := filepath.Glob(filepath.Join(products, "*", "*-Runner.app"))
	if len(m) == 0 {
		return ""
	}
	sort.Strings(m)
	return m[0]
}

// readInfo reads an app's Info.plist and the format it is in.
func readInfo(app string) (map[string]any, int, error) {
	b, err := os.ReadFile(filepath.Join(app, "Info.plist"))
	if err != nil {
		return nil, 0, err
	}
	var info map[string]any
	format, err := plist.Unmarshal(b, &info)
	if err != nil {
		return nil, 0, fmt.Errorf("reading %s: %w", filepath.Join(app, "Info.plist"), err)
	}
	return info, format, nil
}

// branded reports whether an Info.plist is this Pinky's branded runner's.
func branded(info map[string]any) bool {
	v, ok := info[brandKey]
	if !ok {
		return false
	}
	switch n := v.(type) {
	case uint64:
		return n == brandVersion
	case int64:
		return n == brandVersion
	}
	return false
}

// brandInfo gives an Info.plist the helper's display name, the icon keys
// actool wrote (partial: CFBundleIcons and CFBundleIcons~ipad) and the
// brand mark. It leaves everything else alone.
func brandInfo(info, partial map[string]any) {
	for k, v := range partial {
		info[k] = v
	}
	info["CFBundleDisplayName"] = helperDisplayName
	info[brandKey] = brandVersion
}

// iconPlatform is actool's name for the platform an Info.plist was built
// for: the device's unless it names the Simulator.
func iconPlatform(info map[string]any) string {
	if ps, ok := info["CFBundleSupportedPlatforms"].([]any); ok {
		for _, p := range ps {
			if s, _ := p.(string); strings.EqualFold(s, "iPhoneSimulator") {
				return "iphonesimulator"
			}
		}
	}
	return "iphoneos"
}

// iconDevices are actool's target devices for an Info.plist's
// UIDeviceFamily: 1 the iPhone, 2 the iPad. Both when it names neither.
func iconDevices(info map[string]any) []string {
	var out []string
	if fs, ok := info["UIDeviceFamily"].([]any); ok {
		for _, f := range fs {
			var n int64
			switch v := f.(type) {
			case uint64:
				n = int64(v)
			case int64:
				n = v
			}
			switch n {
			case 1:
				out = append(out, "iphone")
			case 2:
				out = append(out, "ipad")
			}
		}
	}
	if len(out) == 0 {
		out = []string{"iphone", "ipad"}
	}
	return out
}

// actoolArgs are the arguments that compile catalog's AppIcon into out,
// for the platform, phones and iOS version an Info.plist names.
func actoolArgs(info map[string]any, catalog, out, partial string) []string {
	minOS, _ := info["MinimumOSVersion"].(string)
	if minOS == "" {
		minOS = "15.0"
	}
	args := []string{catalog, "--compile", out,
		"--app-icon", "AppIcon",
		"--platform", iconPlatform(info),
		"--minimum-deployment-target", minOS}
	for _, d := range iconDevices(info) {
		args = append(args, "--target-device", d)
	}
	return append(args,
		"--output-partial-info-plist", partial,
		"--output-format", "human-readable-text",
		"--errors", "--warnings",
		"--compress-pngs",
		"--development-region", "en")
}

// iconCatalog writes an asset catalog holding icon as the app icon, a
// single 1024-pixel one from which actool makes every size.
func iconCatalog(dir string, icon []byte) (string, error) {
	cat := filepath.Join(dir, "Pinky.xcassets")
	set := filepath.Join(cat, "AppIcon.appiconset")
	if err := os.MkdirAll(set, 0o755); err != nil {
		return "", err
	}
	files := map[string]string{
		filepath.Join(cat, "Contents.json"): `{"info":{"author":"pinky","version":1}}`,
		filepath.Join(set, "Contents.json"): `{"images":[{"filename":"icon-1024.png","idiom":"universal","platform":"ios","size":"1024x1024"}],"info":{"author":"pinky","version":1}}`,
	}
	for p, s := range files {
		if err := os.WriteFile(p, []byte(s+"\n"), 0o644); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(set, "icon-1024.png"), icon, 0o644); err != nil {
		return "", err
	}
	return cat, nil
}

// infoStrings is an InfoPlist.strings file giving the display name, as a
// binary property list, the form Xcode compiles .strings files to.
func infoStrings(name string) ([]byte, error) {
	return plist.Marshal(map[string]string{"CFBundleDisplayName": name}, plist.BinaryFormat)
}

// brandRunner gives the runner app at app Pinky's name and icon, and
// reports whether it changed the app; one branded by this Pinky stays as
// it is. It writes the icon files and the translations first and the
// Info.plist last, in one rename, so the app is never half branded for
// whoever installs it meanwhile. A device build must be signed again
// afterwards.
func brandRunner(ctx context.Context, x xcode, app string) (bool, error) {
	info, format, err := readInfo(app)
	if err != nil {
		return false, err
	}
	if branded(info) {
		return false, nil
	}
	if len(assets.IOSIcon) == 0 {
		return false, errors.New("this Pinky has no icon for the helper")
	}
	tmp, err := os.MkdirTemp("", "pinky-helper-icon-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	cat, err := iconCatalog(tmp, assets.IOSIcon)
	if err != nil {
		return false, err
	}
	out := filepath.Join(tmp, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return false, err
	}
	partialPath := filepath.Join(tmp, "partial.plist")
	cmd := exec.CommandContext(ctx, x.tool("actool"), actoolArgs(info, cat, out, partialPath)...)
	cmd.Env = x.env()
	if b, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("compiling the helper's icon with actool: %v: %s", err, strings.TrimSpace(string(b)))
	}
	pb, err := os.ReadFile(partialPath)
	if err != nil {
		return false, fmt.Errorf("actool wrote no icon keys: %w", err)
	}
	var partial map[string]any
	if _, err := plist.Unmarshal(pb, &partial); err != nil {
		return false, fmt.Errorf("reading actool's icon keys: %w", err)
	}
	if _, ok := partial["CFBundleIcons"]; !ok {
		return false, errors.New("actool made no app icon")
	}
	// The icon: Assets.car and the PNGs older iOS versions read.
	made, err := os.ReadDir(out)
	if err != nil {
		return false, err
	}
	for _, e := range made {
		if !e.Type().IsRegular() {
			continue
		}
		if err := copyFile(filepath.Join(out, e.Name()), filepath.Join(app, e.Name())); err != nil {
			return false, err
		}
	}
	for lang, name := range helperNames {
		b, err := infoStrings(name)
		if err != nil {
			return false, err
		}
		dir := filepath.Join(app, lang+".lproj")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return false, err
		}
		if err := os.WriteFile(filepath.Join(dir, "InfoPlist.strings"), b, 0o644); err != nil {
			return false, err
		}
	}
	brandInfo(info, partial)
	b, err := plist.Marshal(info, format)
	if err != nil {
		return false, err
	}
	if err := writeAtomic(filepath.Join(app, "Info.plist"), b); err != nil {
		return false, err
	}
	return true, nil
}

// copyFile copies a regular file over dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// writeAtomic replaces path with b in one rename, through a file next to
// it whose name starts with a dot.
func writeAtomic(path string, b []byte) error {
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".pinky")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
