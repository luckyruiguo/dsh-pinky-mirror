package video

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestSearchDirsOrder(t *testing.T) {
	t.Setenv("PINKY_FFMPEG_DIR", "/opt/pinky-ffmpeg")
	dirs := searchDirs()
	if len(dirs) < 2 || dirs[0] != "/opt/pinky-ffmpeg" {
		t.Fatalf("searchDirs() = %q, want PINKY_FFMPEG_DIR first", dirs)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	exeDir := filepath.Dir(exe)
	i := 1
	if runtime.GOOS == "darwin" {
		// The bundle's own copy comes before anything installed.
		if want := filepath.Join(filepath.Dir(exeDir), "Frameworks"); dirs[1] != want {
			t.Fatalf("searchDirs()[1] = %q, want the bundle's Frameworks %q", dirs[1], want)
		}
		i = 2
	}
	if dirs[i] != exeDir {
		t.Fatalf("searchDirs()[%d] = %q, want the executable's folder %q", i, dirs[i], exeDir)
	}
	for _, d := range dirs[:i+1] {
		if strings.Contains(d, "homebrew") || strings.HasPrefix(d, "/usr/") {
			t.Fatalf("searchDirs() = %q: an installed FFmpeg comes before the app's", dirs)
		}
	}
}

// TestPackagedFFmpeg decodes the test streams with the FFmpeg that a package
// of Pinky carries, in a process that loads no other FFmpeg:
//
//	PINKY_FFMPEG_APP=build/pinky/darwin-arm64/Pinky.app go test -run TestPackagedFFmpeg ./internal/video/
//	PINKY_FFMPEG_APP=<the unpacked .tar.gz or Windows zip> go test -run TestPackagedFFmpeg ./internal/video/
//
// It runs TestDecodeTestStream again in a child process pointed at where
// the package keeps FFmpeg (Pinky.app's Frameworks folder, else the folder
// of the executable), and checks that the child loaded FFmpeg from there:
// on macOS every FFmpeg library that dyld reports, elsewhere the folders
// that Version names. (Making the test streams still takes an ffmpeg
// command, in processes of its own.)
func TestPackagedFFmpeg(t *testing.T) {
	app := os.Getenv("PINKY_FFMPEG_APP")
	if app == "" {
		t.Skip("set PINKY_FFMPEG_APP to a Pinky.app, or to the folder of a Windows or Linux Pinky, to check the FFmpeg it carries")
	}
	dir := app
	if runtime.GOOS == "darwin" {
		dir = filepath.Join(app, "Contents", "Frameworks")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	_, avcodec, _ := libNames()
	if _, err := os.Stat(filepath.Join(dir, avcodec)); err != nil {
		t.Fatalf("%s carries no FFmpeg: %v", app, err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestDecodeTestStream$", "-test.v", "-test.count=1")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DYLD_") && !strings.HasPrefix(kv, "PINKY_FFMPEG_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PINKY_FFMPEG_DIR="+dir)
	if runtime.GOOS == "darwin" {
		cmd.Env = append(cmd.Env, "DYLD_PRINT_LIBRARIES=1")
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	if err != nil {
		t.Fatalf("decode with the package's FFmpeg: %v\n%s", err, tail(out.String()))
	}
	text := out.String()
	if !strings.Contains(text, "--- PASS: TestDecodeTestStream") || strings.Contains(text, "--- SKIP: TestDecodeTestStream") {
		t.Fatalf("TestDecodeTestStream did not decode:\n%s", tail(text))
	}
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "video_test.go") {
			t.Log(strings.TrimSpace(l))
		}
	}

	if runtime.GOOS != "darwin" {
		// "FFmpeg: libavcodec 63.1.102 (<the folders it came from>)"
		m := regexp.MustCompile(`FFmpeg: libavcodec [0-9.]+ \((.*)\)`).FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("the decoding process did not say which FFmpeg it loaded:\n%s", tail(text))
		}
		if strings.TrimSpace(m[1]) != dir {
			t.Errorf("the decoding process loaded FFmpeg from %s, not only from the package's %s", m[1], dir)
		}
		return
	}

	// dyld[<pid>]: <uuid> <path>, for the child only: the ffmpeg commands
	// that make the test streams print theirs too.
	line := regexp.MustCompile(fmt.Sprintf(`^dyld\[%d\]: <[^>]*> (.+)$`, cmd.Process.Pid))
	lib := regexp.MustCompile(`/lib(avutil|avcodec|avformat|swscale|swresample|avfilter|avdevice)[.\-]`)
	found := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		m := line.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		path := m[1]
		if l := lib.FindStringSubmatch(path); l != nil {
			if filepath.Dir(path) != dir {
				t.Errorf("the decoding process loaded %s, not the app's", path)
			}
			found[l[1]] = true
		}
	}
	for _, want := range []string{"avutil", "avcodec", "swscale"} {
		if !found[want] {
			t.Errorf("lib%s was not loaded from %s (dyld reported %v)", want, dir, found)
		}
	}
}

func tail(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if !strings.HasPrefix(l, "dyld[") {
			keep = append(keep, l)
		}
	}
	if len(keep) > 40 {
		keep = keep[len(keep)-40:]
	}
	return strings.Join(keep, "\n")
}
