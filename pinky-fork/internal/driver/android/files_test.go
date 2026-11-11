package android

import (
	"errors"
	"os"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

func TestCleanFilePath(t *testing.T) {
	good := map[string]string{
		"a.txt":            "a.txt",
		"a/b c/d.bin":      "a/b c/d.bin",
		"./a//b/":          "a/b",
		"\u7167\u7247.jpg": "\u7167\u7247.jpg",
		"...":              "...",
		"a..b":             "a..b",
	}
	for in, want := range good {
		got, err := cleanFilePath(in)
		if err != nil || got != want {
			t.Errorf("cleanFilePath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", ".", "/", "/etc/hosts", "../x", "a/../b", "a/..", "..", `a\b`, "a\x00b", "a/\nb", "./"} {
		if got, err := cleanFilePath(in); !errors.Is(err, driver.ErrNotFound) {
			t.Errorf("cleanFilePath(%q) = %q, %v; want ErrNotFound", in, got, err)
		}
	}
}

type fakeLstat map[string]os.FileMode

func (f fakeLstat) Lstat(p string) (adb.FileInfo, error) {
	m, ok := f[p]
	if !ok {
		return adb.FileInfo{}, nil
	}
	return adb.FileInfo{Mode: m, Size: 1}, nil
}

func TestCheckInside(t *testing.T) {
	fs := fakeLstat{
		"/sdcard/Pinky/dir":          os.ModeDir | 0o770,
		"/sdcard/Pinky/dir/f.txt":    0o660,
		"/sdcard/Pinky/link":         os.ModeSymlink | 0o777,
		"/sdcard/Pinky/dir/sub-link": os.ModeSymlink | 0o777,
		"/sdcard/Pinky/file":         0o660,
	}
	fi, err := checkInside(fs, "dir/f.txt")
	if err != nil || !fi.Exists() || !fi.Mode.IsRegular() {
		t.Errorf("dir/f.txt = %+v, %v", fi, err)
	}
	if fi, err := checkInside(fs, "dir/new/deeper.txt"); err != nil || fi.Exists() {
		t.Errorf("a missing path = %+v, %v; want missing, no error", fi, err)
	}
	for _, p := range []string{"link", "link/x", "dir/sub-link", "dir/sub-link/x", "file/x"} {
		if _, err := checkInside(fs, p); !errors.Is(err, driver.ErrNotFound) {
			t.Errorf("checkInside(%q) = %v; want ErrNotFound", p, err)
		}
	}
}

func TestFileURI(t *testing.T) {
	if got := fileURI("/sdcard/Pinky/a b/\u00e9#.jpg"); got != "file:///sdcard/Pinky/a%20b/%C3%A9%23.jpg" {
		t.Errorf("fileURI = %q", got)
	}
}
