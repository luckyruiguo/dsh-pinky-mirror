package agents

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Env is the computer as Setup sees it: where the person's home is, which
// folders hold commands, and how commands run. Tests give one of their
// own, with a home folder of their own, so that no test reads or changes
// the person's agents.
type Env struct {
	GOOS string
	Home string
	// Getenv reads the environment (APPDATA, XDG_CONFIG_HOME, CODEX_HOME…).
	Getenv func(string) string
	// Path lists the folders to look for agents' commands in, in order;
	// nil asks the person's login shell, once, and adds the usual places.
	Path func() []string
	// Run runs a command with the PATH of Path and returns what it
	// printed, stdout and stderr together.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Apps are the folders that hold applications (on macOS
	// /Applications and ~/Applications).
	Apps []string
	Now  func() time.Time
}

// PathEnv, when set, is the whole search path for agents' commands, and
// only ~/Applications holds applications: for tests of the app, which give
// it a home folder of their own and fake agents.
const PathEnv = "PINKY_AGENTS_PATH"

// SystemEnv is this computer, for the app.
func SystemEnv() *Env {
	home, _ := os.UserHomeDir()
	e := &Env{GOOS: runtime.GOOS, Home: home, Getenv: os.Getenv, Now: time.Now}
	if runtime.GOOS == "darwin" {
		e.Apps = []string{"/Applications", filepath.Join(home, "Applications")}
	}
	var once sync.Once
	var path []string
	e.Path = func() []string {
		once.Do(func() { path = searchPath(e) })
		return path
	}
	e.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = withPath(os.Environ(), e.Path(), e.GOOS)
		cmd.Stdin = nil
		cmd.WaitDelay = 2 * time.Second // a child that keeps the output open
		hideWindow(cmd)
		return cmd.CombinedOutput()
	}
	if only := os.Getenv(PathEnv); only != "" {
		dirs := filepath.SplitList(only)
		e.Path = func() []string { return dirs }
		e.Apps = []string{filepath.Join(home, "Applications")}
		// Nor does it run anything else, such as open, which would open the
		// real Claude Desktop.
		run := e.Run
		e.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			for _, d := range dirs {
				if rel, err := filepath.Rel(d, name); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
					return run(ctx, name, args...)
				}
			}
			return nil, fmt.Errorf("%s runs only the commands in %s, not %s", PathEnv, only, name)
		}
	}
	return e
}

// withPath replaces PATH in env.
func withPath(env, path []string, goos string) []string {
	sep := string(os.PathListSeparator)
	if goos == "windows" {
		sep = ";"
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "PATH") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "PATH="+strings.Join(path, sep))
}

// searchPath is where agents' commands are: the PATH of the person's login
// shell, which an app opened from the Dock or Finder lacks (it has only the
// system's folders), then this process's PATH, then the folders that
// installers of agents use.
func searchPath(e *Env) []string {
	var dirs []string
	if e.GOOS != "windows" {
		dirs = append(dirs, loginShellPath(e)...)
	}
	dirs = append(dirs, filepath.SplitList(e.Getenv("PATH"))...)
	dirs = append(dirs, usualDirs(e)...)
	return unique(dirs)
}

// usualDirs are the folders where agents' installers put their commands.
func usualDirs(e *Env) []string {
	h := e.Home
	if e.GOOS == "windows" {
		var dirs []string
		if a := e.Getenv("APPDATA"); a != "" {
			dirs = append(dirs, filepath.Join(a, "npm"))
		}
		if l := e.Getenv("LOCALAPPDATA"); l != "" {
			dirs = append(dirs, filepath.Join(l, "Microsoft", "WinGet", "Links"), filepath.Join(l, "Programs", "Microsoft VS Code", "bin"))
		}
		return append(dirs, filepath.Join(h, ".local", "bin"), filepath.Join(h, ".bun", "bin"), filepath.Join(h, "scoop", "shims"))
	}
	dirs := []string{
		filepath.Join(h, ".local", "bin"), filepath.Join(h, ".claude", "local"),
		"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(h, ".npm-global", "bin"),
		filepath.Join(h, ".bun", "bin"), filepath.Join(h, ".volta", "bin"), filepath.Join(h, ".yarn", "bin"),
		filepath.Join(h, "Library", "pnpm"), filepath.Join(h, ".local", "share", "pnpm"),
		filepath.Join(h, ".cargo", "bin"), "/snap/bin",
	}
	// Node's version managers keep a folder per version: the newest last.
	for _, pat := range []string{filepath.Join(h, ".nvm", "versions", "node", "*", "bin"), filepath.Join(h, ".fnm", "node-versions", "*", "installation", "bin")} {
		if m, _ := filepath.Glob(pat); len(m) > 0 {
			for i := len(m) - 1; i >= 0; i-- {
				dirs = append(dirs, m[i])
			}
		}
	}
	return dirs
}

func unique(dirs []string) []string {
	seen := map[string]bool{}
	out := dirs[:0:0]
	for _, d := range dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// loginShellPath asks the person's shell for its PATH, as a terminal would
// have it: a login, interactive shell, with nothing to read and 5 s to
// answer. Shells that may print greetings get a marker to find the answer
// by.
func loginShellPath(e *Env) []string {
	shell := e.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
		if e.GOOS == "linux" {
			shell = "/bin/bash"
		}
	}
	switch filepath.Base(shell) {
	case "zsh", "bash", "fish", "sh", "ksh", "dash":
	default:
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const mark = "__PINKY_PATH__"
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", `printf '\n`+mark+`%s`+mark+`\n' "$PATH"`)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "TERM=dumb")
	cmd.WaitDelay = time.Second // what the shell's files start may keep its output open
	out, _ := cmd.Output()
	_, after, ok := bytes.Cut(out, []byte(mark))
	if !ok {
		return nil
	}
	path, _, ok := bytes.Cut(after, []byte(mark))
	if !ok {
		return nil
	}
	if filepath.Base(shell) == "fish" {
		// fish prints its PATH list with spaces.
		return strings.Fields(string(path))
	}
	return filepath.SplitList(string(path))
}

// lookPath finds a command in Path, with Windows' extensions there.
func (e *Env) lookPath(name string) string {
	exts := []string{""}
	if e.GOOS == "windows" {
		exts = []string{".exe", ".cmd", ".bat"}
	}
	if e.Path == nil {
		return ""
	}
	for _, dir := range e.Path() {
		// Windows' app execution aliases stand in for Store apps: a claude
		// there may open Claude Desktop.
		if e.GOOS == "windows" && strings.EqualFold(filepath.Base(filepath.Clean(dir)), "WindowsApps") {
			continue
		}
		for _, ext := range exts {
			p := filepath.Join(dir, name+ext)
			if fi, err := os.Stat(p); err == nil && e.runs(fi) {
				return p
			}
		}
	}
	return ""
}

// runs reports whether a file found on the search path may be run: one
// with an execute bit. Windows has no execute bits (Go reports none on
// it), so there, and for an Env of Windows, any file may.
func (e *Env) runs(fi os.FileInfo) bool {
	return !fi.IsDir() && (e.GOOS == "windows" || runtime.GOOS == "windows" || fi.Mode()&0o111 != 0)
}

// getenv reads the environment, never failing.
func (e *Env) getenv(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}

// exists reports whether path is there.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
