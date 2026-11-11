package update

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Method is how an update reaches this installation.
type Method int

const (
	// MethodSelf: Pinky replaces itself and starts again (macOS; on
	// Windows through the release's installer, which runs quietly).
	MethodSelf Method = iota
	// MethodInstaller: Pinky downloads the release's installer and opens
	// it for the person (a copy of Pinky on Windows that its installer did
	// not install).
	MethodInstaller
	// MethodDownload: Pinky downloads the release and shows the file, to be
	// installed by hand.
	MethodDownload
	// MethodManager: what installed Pinky updates it (Homebrew, a Linux
	// package); Pinky only says so.
	MethodManager
)

func (m Method) String() string {
	switch m {
	case MethodSelf:
		return "self"
	case MethodInstaller:
		return "installer"
	case MethodDownload:
		return "download"
	case MethodManager:
		return "manager"
	}
	return "method " + strconv.Itoa(int(m))
}

// Location is where the running Pinky is installed, and how it can be
// updated there.
type Location struct {
	// Exe is the running executable, symlinks resolved.
	Exe string
	// App is what an update replaces: the app bundle on macOS, the
	// executable elsewhere.
	App string
	// Method is how this installation gets updates.
	Method Method
	// Manager names what installed Pinky, for MethodManager ("Homebrew"),
	// and Command is how it updates it ("brew upgrade --cask pinky").
	Manager, Command string
	// Problem says, in words for people, why Pinky cannot replace itself
	// here; "" with MethodSelf.
	Problem string
	// Setup: updates run the release's installer (Windows), by themselves
	// (MethodSelf) or with the person (MethodInstaller).
	Setup bool
}

// CanReplace reports whether Pinky can replace itself here and start again.
func (l Location) CanReplace() bool { return l.Method == MethodSelf && l.Problem == "" }

// Locate finds the running installation.
func Locate() Location {
	exe, err := os.Executable()
	if err != nil {
		return Location{Method: MethodDownload, Problem: "Pinky could not find where it is installed."}
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return locate(exe)
}

// setupLocation is locate on Windows, where registered is the folder that
// Settings › Apps says Pinky's installer installed into: a Pinky.exe there
// replaces itself by running the next installer quietly, into the same
// folder; any other copy (the portable zip, a build) downloads the installer
// and opens it for the person.
func setupLocation(exe, registered string, writable func(dir string) error) Location {
	l := Location{Exe: exe, App: exe, Method: MethodInstaller, Setup: true}
	dir := filepath.Dir(exe)
	if registered == "" || !strings.EqualFold(filepath.Clean(registered), filepath.Clean(dir)) {
		return l
	}
	l.App, l.Method = dir, MethodSelf
	l.Problem = folderProblem(dir, writable)
	return l
}

// archiveLocation is locate on Linux for a Pinky unpacked from the
// release's archive into a folder of its own, which the archive's file list
// (.mygo-files, MyGo's) shows: Pinky replaces the folder's files with the
// next archive's and starts again. Any other copy (a build, a pinky copied
// into a folder of other programs) downloads the release for the person.
func archiveLocation(exe string, writable func(dir string) error) Location {
	l := Location{Exe: exe, App: exe, Method: MethodDownload}
	dir := filepath.Dir(exe)
	if !listsFile(filepath.Join(dir, fileList), filepath.Base(exe)) {
		return l
	}
	l.App, l.Method = dir, MethodSelf
	l.Problem = folderProblem(dir, writable)
	return l
}

// folderProblem says why Pinky cannot replace the files of its install
// folder dir, or "": the helper works in a folder next to it.
func folderProblem(dir string, writable func(dir string) error) string {
	for _, d := range []string{dir, filepath.Dir(dir)} {
		if writable(d) != nil {
			return "Pinky can't write to " + d + ", where it is installed."
		}
	}
	return ""
}

// fileList is the list of the files of a release's archive, which MyGo
// writes into it: one name a line.
const fileList = ".mygo-files"

// listsFile reports whether the file list at path names name.
func listsFile(path, name string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == name {
			return true
		}
	}
	return false
}

// listedEntries are the top-level entries that the file list at path
// names ("lib" for lib/libavutil.so.61); none when it cannot be read.
func listedEntries(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		name := filepath.Clean(filepath.FromSlash(strings.TrimSpace(line)))
		if name == "." || !filepath.IsLocal(name) {
			continue
		}
		top, _, _ := strings.Cut(name, string(filepath.Separator))
		out = append(out, top)
	}
	return out
}

// writableDir reports whether a folder can take new entries, by making
// and removing one.
func writableDir(dir string) error {
	probe, err := os.MkdirTemp(dir, ".pinky-write-")
	if err != nil {
		return err
	}
	return os.Remove(probe)
}
