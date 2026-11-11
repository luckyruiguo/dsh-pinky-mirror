package ios

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

// fakeMirror serves a developer disk image made by writeDiskImage, and
// returns its base URL and the pinned list of its files.
func fakeMirror(t *testing.T, tamper string) (string, []mirrorFile) {
	t.Helper()
	src := filepath.Join(writeDiskImage(t, t.TempDir(), mirrorBuild, "27.1.5228.8"), "Restore")
	var files []mirrorFile
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		files = append(files, mirrorFile{from: "half/" + rel, to: rel, size: int64(len(b)), sha256: hex.EncodeToString(sum[:])})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/half/")
		b, err := os.ReadFile(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if rel == tamper {
			b = append([]byte("not "), b...)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/", files
}

func TestDownloadMirror(t *testing.T) {
	defer func(d string) { xcodeDDI = d }(xcodeDDI)
	xcodeDDI = filepath.Join(t.TempDir(), "none")
	t.Setenv(ddiEnv, "")
	own := t.TempDir()
	base, files := fakeMirror(t, "")
	var done, total int64
	err := downloadMirror(context.Background(), http.DefaultClient, base, files, own, func(d, t int64) { done, total = d, t })
	if err != nil {
		t.Fatal(err)
	}
	if done != total || total != mirrorSize(files) {
		t.Errorf("progress ended at %d of %d, want %d", done, total, mirrorSize(files))
	}
	img, err := findDiskImage(own)
	if err != nil || img.Source != sourceMirror || img.Version != "27.1.5228.8" || img.Build != mirrorBuild {
		t.Fatalf("findDiskImage after the download = %+v, %v", img, err)
	}
	b, _ := os.ReadFile(filepath.Join(own, mirrorFolder, "version.plist"))
	if !strings.Contains(string(b), mirrorName) || !strings.Contains(string(b), mirrorCommit) {
		t.Errorf("version.plist does not say where the image came from:\n%s", b)
	}
	// An official copy wins over the download, even an older one.
	writeDiskImage(t, own, "26A1", "26.0.1.0")
	if img, err := findDiskImage(own); err != nil || img.Source != sourcePinky {
		t.Fatalf("with an official copy too: %+v, %v", img, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(own, ".download-*")); len(leftovers) > 0 {
		t.Errorf("left behind: %v", leftovers)
	}
}

func TestDownloadMirrorRefusesChangedFiles(t *testing.T) {
	own := t.TempDir()
	// An earlier download stays when the new one fails.
	base, files := fakeMirror(t, "")
	if err := downloadMirror(context.Background(), http.DefaultClient, base, files, own, nil); err != nil {
		t.Fatal(err)
	}
	base, files = fakeMirror(t, "a.dmg")
	err := downloadMirror(context.Background(), http.DefaultClient, base, files, own, nil)
	if !errors.Is(err, driver.ErrDiskImageMismatch) || !strings.Contains(err.Error(), "a.dmg") {
		t.Fatalf("a changed file: %v, want ErrDiskImageMismatch naming it", err)
	}
	if _, err := readDiskImage(filepath.Join(own, mirrorFolder)); err != nil {
		t.Errorf("the earlier download is gone: %v", err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(own, ".download-*")); len(leftovers) > 0 {
		t.Errorf("left behind: %v", leftovers)
	}
	// A file the mirror no longer has.
	base, files = fakeMirror(t, "")
	files = append(files, mirrorFile{from: "half/gone", to: "gone", size: 1, sha256: strings.Repeat("0", 64)})
	if err := downloadMirror(context.Background(), http.DefaultClient, base, files, own, nil); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("a missing file: %v", err)
	}
}

func TestMirrorFilesPinned(t *testing.T) {
	if got := mirrorSize(mirrorFiles); got != 32437683 {
		t.Errorf("the pinned files add up to %d bytes", got)
	}
	seen := map[string]bool{}
	for _, f := range mirrorFiles {
		if len(f.sha256) != 64 || !filepath.IsLocal(filepath.FromSlash(f.to)) || seen[f.to] {
			t.Errorf("bad pin %+v", f)
		}
		seen[f.to] = true
	}
	if !strings.Contains(mirrorBase, mirrorCommit) {
		t.Errorf("the mirror's URL %s is not pinned to %s", mirrorBase, mirrorCommit)
	}
	m := NewProvider().DiskImageMirror()
	if m.Name != mirrorName || m.Size != 32437683 || !strings.Contains(m.Image, mirrorBuild) {
		t.Errorf("DiskImageMirror = %+v", m)
	}
}

// TestDownloadMirrorLive downloads the real image from the mirror (about
// 32 MB) and checks that Pinky assembles a usable image from the pinned
// files. It needs the network, so it runs only with PINKY_MIRROR_LIVE=1.
func TestDownloadMirrorLive(t *testing.T) {
	if os.Getenv("PINKY_MIRROR_LIVE") != "1" {
		t.Skip("set PINKY_MIRROR_LIVE=1 to download the image from the mirror")
	}
	own := t.TempDir()
	p := NewProvider()
	p.DiskImageDir = own
	if err := p.DownloadDiskImage(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	img, err := readDiskImage(filepath.Join(own, mirrorFolder))
	t.Logf("downloaded: %+v err=%v", img, err)
	if err != nil || img.Build != mirrorBuild || img.Version != "27.1.5228.8" {
		t.Fatalf("the downloaded image = %+v, %v", img, err)
	}
}
