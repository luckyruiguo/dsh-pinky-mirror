package scrcpy

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// App is an installed app as the server lists it: apps with a launcher
// entry, labeled in the phone's language.
type App struct {
	Name    string
	Package string
	System  bool
}

// ListApps runs the server once in list mode and returns the apps that
// have a launcher entry. It takes a few seconds, since the server loads
// every app's label.
func ListApps(ctx context.Context, c *adb.Client, serial string) ([]App, error) {
	scid, err := randomSCID()
	if err != nil {
		return nil, err
	}
	jar := fmt.Sprintf("/data/local/tmp/pinky-scrcpy-%08x.jar", scid)
	if err := c.Push(ctx, serial, bytes.NewReader(serverJar), jar, 0o644, time.Now()); err != nil {
		return nil, fmt.Errorf("scrcpy: push server: %w", err)
	}
	// In list mode the server deletes its jar, prints the list and exits.
	cmd := "CLASSPATH=" + jar + " app_process / com.genymobile.scrcpy.Server " +
		adb.Command(Version, "log_level=info", "list_apps=true")
	out, err := c.Shell(ctx, serial, cmd)
	if err != nil {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = c.Shell(cctx, serial, "rm -f "+jar)
		return nil, fmt.Errorf("scrcpy: list apps: %w", err)
	}
	apps, ok := ParseAppList(string(out))
	if !ok {
		return nil, fmt.Errorf("scrcpy: list apps: unexpected output: %s", firstLines(string(out), 5))
	}
	return apps, nil
}

// ParseAppList parses the server's "List of apps:" output. Each app is
// " * Name<padding> package" for system apps or " - ..." for others, where
// the padding fills the name to 30 characters and a space follows; a
// longer name puts the package alone on the next line.
func ParseAppList(out string) ([]App, bool) {
	out = strings.ReplaceAll(out, "\r\n", "\n")
	i := strings.Index(out, "List of apps:")
	if i < 0 {
		return nil, false
	}
	var apps []App
	var pending *App
	for _, line := range strings.Split(out[i+len("List of apps:"):], "\n") {
		if strings.HasPrefix(line, "[server]") {
			break // the next log message
		}
		if pending != nil {
			if pkg := strings.TrimSpace(line); looksLikePackage(pkg) {
				pending.Package = pkg
				apps = append(apps, *pending)
			}
			pending = nil
			continue
		}
		var system bool
		switch {
		case strings.HasPrefix(line, " * "):
			system = true
		case strings.HasPrefix(line, " - "):
		default:
			continue
		}
		body := strings.TrimRight(line[3:], " \t")
		// One-line form: at least the separator and one padding space
		// precede the package.
		if j := strings.LastIndex(body, "  "); j >= 0 {
			if pkg := strings.TrimSpace(body[j:]); looksLikePackage(pkg) {
				apps = append(apps, App{Name: strings.TrimSpace(body[:j]), Package: pkg, System: system})
				continue
			}
		}
		pending = &App{Name: strings.TrimSpace(body), System: system}
	}
	return apps, true
}

func looksLikePackage(s string) bool {
	if !strings.Contains(s, ".") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_') {
			return false
		}
	}
	return true
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " | ")
}
