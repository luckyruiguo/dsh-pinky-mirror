package android

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// The phone's Pinky folder, the only place on the phone agents may reach.
// Every path is checked component by component with lstat before use, so
// neither ".." nor a symbolic link can lead out of it.
const pinkyDir = "/sdcard/Pinky"

// Limits of the folder walk, against runaway trees.
const (
	filesMaxDepth   = 32
	filesMaxEntries = 50_000
)

// ListFiles implements driver.Files. It lists the Pinky folder recursively,
// folders included, sorted by path, creating the folder when it does not
// exist. Symbolic links and special files are left out.
func (d *Device) ListFiles(ctx context.Context) ([]driver.FileInfo, error) {
	y, err := d.openSync(ctx)
	if err != nil {
		return nil, err
	}
	defer y.Close()
	if err := d.ensurePinkyDir(ctx, y); err != nil {
		return nil, err
	}
	var out []driver.FileInfo
	type dir struct {
		rel   string
		depth int
	}
	queue := []dir{{"", 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		entries, err := y.List(remotePath(cur.rel))
		if err != nil {
			return nil, wrapErr(err)
		}
		for _, e := range entries {
			if !validName(e.Name) {
				continue
			}
			rel := path.Join(cur.rel, e.Name)
			switch {
			case e.Mode.IsDir():
				out = append(out, driver.FileInfo{Path: rel, ModTime: e.ModTime, Dir: true})
				if cur.depth+1 < filesMaxDepth {
					queue = append(queue, dir{rel, cur.depth + 1})
				}
			case e.Mode.IsRegular():
				out = append(out, driver.FileInfo{Path: rel, Size: e.Size, ModTime: e.ModTime})
			}
			if len(out) > filesMaxEntries {
				return nil, fmt.Errorf("android: the Pinky folder has more than %d entries", filesMaxEntries)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Pull implements driver.Files: it copies a file from the Pinky folder.
func (d *Device) Pull(ctx context.Context, p string, w io.Writer) error {
	rel, err := cleanFilePath(p)
	if err != nil {
		return err
	}
	y, err := d.openSync(ctx)
	if err != nil {
		return err
	}
	fi, err := checkInside(y, rel)
	y.Close()
	if err != nil {
		return err
	}
	if !fi.Exists() || !fi.Mode.IsRegular() {
		return fmt.Errorf("android: %s is not a file in the Pinky folder: %w", rel, driver.ErrNotFound)
	}
	if _, err := d.client.Pull(ctx, d.info.ID, remotePath(rel), w); err != nil {
		return wrapErr(err)
	}
	return nil
}

// Push implements driver.Files: it writes a file into the Pinky folder,
// creating its folders, with mtime as its modification time. size, when
// not negative, is checked against what r delivered.
func (d *Device) Push(ctx context.Context, p string, r io.Reader, size int64, mtime time.Time) error {
	rel, err := cleanFilePath(p)
	if err != nil {
		return err
	}
	y, err := d.openSync(ctx)
	if err != nil {
		return err
	}
	err = d.ensurePinkyDir(ctx, y)
	var fi adb.FileInfo
	if err == nil {
		fi, err = checkInside(y, rel)
	}
	y.Close()
	if err != nil {
		return err
	}
	if fi.Exists() && !fi.Mode.IsRegular() {
		return fmt.Errorf("android: %s in the Pinky folder is not a file: %w", rel, driver.ErrNotFound)
	}
	if mtime.IsZero() {
		mtime = time.Now()
	}
	cr := &countReader{r: r}
	if err := d.client.Push(ctx, d.info.ID, cr, remotePath(rel), 0o644, mtime); err != nil {
		return wrapErr(err)
	}
	if size >= 0 && cr.n != size {
		return fmt.Errorf("android: pushed %d bytes of %s, expected %d", cr.n, rel, size)
	}
	// Android 11 and later index files written through /sdcard by
	// themselves; older versions need the media scanner told, or galleries
	// and pickers do not see the file.
	if d.props.SDK > 0 && d.props.SDK < 30 {
		uri := fileURI(remotePath(rel))
		if _, err := d.client.Shell(ctx, d.info.ID, "am broadcast -a android.intent.action.MEDIA_SCANNER_SCAN_FILE -d "+adb.Quote(uri)+" >/dev/null"); err != nil {
			d.cfg.logf("android: %s: media scan of %s: %v", d.info.ID, rel, err)
		}
	}
	return nil
}

// openSync opens a sync connection with the v2 requests when the phone
// has them (Android 9 and later): 64-bit sizes and lstat errors.
func (d *Device) openSync(ctx context.Context) (*adb.Sync, error) {
	d.stateMu.Lock()
	v2, known := d.syncV2, d.syncKnown
	d.stateMu.Unlock()
	if !known {
		f, err := d.client.Features(ctx, d.info.ID)
		if err != nil {
			return nil, wrapErr(err)
		}
		v2 = f["stat_v2"] && f["ls_v2"]
		d.stateMu.Lock()
		d.syncV2, d.syncKnown = v2, true
		d.stateMu.Unlock()
	}
	y, err := d.client.OpenSync(ctx, d.info.ID, v2)
	return y, wrapErr(err)
}

// ensurePinkyDir creates the Pinky folder when it is missing and refuses
// one that is not a real folder.
func (d *Device) ensurePinkyDir(ctx context.Context, y *adb.Sync) error {
	fi, err := y.Lstat(pinkyDir)
	if err != nil {
		return wrapErr(err)
	}
	if !fi.Exists() {
		if _, err := d.client.Shell(ctx, d.info.ID, "mkdir -p "+pinkyDir); err != nil {
			return wrapErr(err)
		}
		if fi, err = y.Lstat(pinkyDir); err != nil {
			return wrapErr(err)
		}
	}
	if !fi.Mode.IsDir() {
		return fmt.Errorf("android: %s is not a folder: %w", pinkyDir, driver.ErrNotFound)
	}
	return nil
}

// checkInside walks rel's components from the Pinky folder with lstat and
// returns the last one's info (Exists is false when it is missing). Every
// component before it that exists must be a real folder, never a link.
func checkInside(y lstater, rel string) (adb.FileInfo, error) {
	parts := strings.Split(rel, "/")
	cur := pinkyDir
	for i, part := range parts {
		cur += "/" + part
		fi, err := y.Lstat(cur)
		if err != nil {
			return adb.FileInfo{}, wrapErr(err)
		}
		if fi.Mode&os.ModeSymlink != 0 {
			return adb.FileInfo{}, fmt.Errorf("android: %s is a link, which Pinky does not follow: %w", path.Join(parts[:i+1]...), driver.ErrNotFound)
		}
		if i == len(parts)-1 {
			return fi, nil
		}
		if !fi.Exists() {
			return adb.FileInfo{}, nil // the rest is created by Push, missing for Pull
		}
		if !fi.Mode.IsDir() {
			return adb.FileInfo{}, fmt.Errorf("android: %s is not a folder: %w", path.Join(parts[:i+1]...), driver.ErrNotFound)
		}
	}
	return adb.FileInfo{}, nil
}

// lstater is the part of adb.Sync that checkInside uses.
type lstater interface {
	Lstat(path string) (adb.FileInfo, error)
}

// errBadPath is wrapped with driver.ErrNotFound for paths that would leave
// the Pinky folder or are not file paths at all.
var errBadPath = errors.New("not a path inside the Pinky folder")

// cleanFilePath checks a slash-separated path relative to the Pinky folder
// and returns it cleaned. It refuses absolute paths, any ".." component,
// backslashes, empty paths and names with control characters.
func cleanFilePath(p string) (string, error) {
	bad := func() (string, error) {
		return "", fmt.Errorf("android: %q: %w: %w", p, errBadPath, driver.ErrNotFound)
	}
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return bad()
	}
	var parts []string
	for _, part := range strings.Split(p, "/") {
		switch {
		case part == "" || part == ".":
			continue
		case part == "..":
			return bad()
		case !validName(part):
			return bad()
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return bad()
	}
	return strings.Join(parts, "/"), nil
}

// validName reports whether a file name is one Pinky reads or writes: no
// control characters, at most 255 bytes, and not "." or "..".
func validName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || strings.ContainsRune(name, '/') {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func remotePath(rel string) string {
	if rel == "" {
		return pinkyDir
	}
	return pinkyDir + "/" + rel
}

// countReader counts the bytes read through it.
type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// fileURI returns a file:// URI for an absolute path, escaping what a URI
// path cannot hold.
func fileURI(p string) string {
	var b strings.Builder
	b.WriteString("file://")
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("/-_.~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

var _ driver.Files = (*Device)(nil)
