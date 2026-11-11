package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// ErrChecksum means a download is not the file the feed describes.
var ErrChecksum = errors.New("the download does not match the release's checksum")

// ErrStalled means a download received nothing for StallTimeout.
var ErrStalled = errors.New("the download stalled")

// StallTimeout ends a download that receives nothing for this long, such
// as one through a proxy whose far side hangs.
var StallTimeout = time.Minute

// Download fetches an asset into dir and checks its size and SHA-256,
// reporting progress (bytes so far, total) at most ten times a second. It
// returns the file's path; nothing is left in dir when it fails.
func Download(ctx context.Context, client *http.Client, a Asset, dir, version string, progress func(done, total int64)) (string, error) {
	return download(ctx, client, a, dir, version, progress, nil)
}

// opener starts a download, through one route.
type opener func(ctx context.Context) (*http.Response, error)

// download is Download that tries alt when the asset's URL fails in a way
// another route may not (retryable); alt may be nil.
func download(ctx context.Context, client *http.Client, a Asset, dir, version string, progress func(done, total int64), alt opener) (string, error) {
	if err := checkAsset(a); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := downloadFrom(ctx, a, dir, progress, func(ctx context.Context) (*http.Response, error) {
		return get(ctx, client, a.URL, version)
	})
	if err == nil || alt == nil || !retryable(ctx, err) {
		return file, err
	}
	file, err2 := downloadFrom(ctx, a, dir, progress, alt)
	if err2 != nil {
		return "", fmt.Errorf("%w (through GitHub's API, after the link failed: %v)", err2, err)
	}
	return file, nil
}

// downloadFrom is one try of download, through open.
func downloadFrom(ctx context.Context, a Asset, dir string, progress func(done, total int64), open opener) (string, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stall := time.AfterFunc(StallTimeout, func() { cancel(ErrStalled) })
	defer stall.Stop()
	resp, err := open(ctx)
	if err != nil {
		return "", stalled(ctx, err)
	}
	defer resp.Body.Close()
	if resp.ContentLength > 0 && resp.ContentLength != a.Size {
		return "", fmt.Errorf("the server offers %d bytes, the release says %d", resp.ContentLength, a.Size)
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", err
	}
	defer func() {
		if tmp != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	pr := &progressReader{r: io.LimitReader(resp.Body, a.Size+1), total: a.Size, fn: progress,
		got: func() { stall.Reset(StallTimeout) }}
	n, err := io.Copy(io.MultiWriter(tmp, h), pr)
	if err != nil {
		return "", fmt.Errorf("download: %w", stalled(ctx, err))
	}
	if n != a.Size {
		return "", fmt.Errorf("download: got %d bytes, the release says %d", n, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return "", fmt.Errorf("%w (sha256 %s, want %s)", ErrChecksum, got, a.SHA256)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, assetName(a.URL))
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	tmp = nil
	return dst, nil
}

// assetName is the file name a download gets: the last part of its URL's
// path, or "pinky-update" when that is no usable name.
func assetName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "pinky-update"
	}
	name := path.Base(u.Path)
	if name == "" || name == "." || name == "/" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `\:`) {
		return "pinky-update"
	}
	return name
}

// VerifyFile checks a file already on disk against an asset, for a download
// kept from an earlier run.
func VerifyFile(file string, a Asset) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Size() != a.Size {
		return fmt.Errorf("%s has %d bytes, the release says %d", filepath.Base(file), fi.Size(), a.Size)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return ErrChecksum
	}
	return nil
}

// stalled reports ErrStalled for an error that the stall timer caused.
func stalled(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); errors.Is(cause, ErrStalled) {
		return fmt.Errorf("%w: nothing for %s", ErrStalled, StallTimeout)
	}
	return err
}

type progressReader struct {
	r     io.Reader
	n     int64
	total int64
	fn    func(done, total int64)
	got   func() // after each read that got bytes
	last  time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if n > 0 && p.got != nil {
		p.got()
	}
	if p.fn != nil && (err != nil || p.n >= p.total || time.Since(p.last) > 100*time.Millisecond) {
		p.last = time.Now()
		p.fn(min(p.n, p.total), p.total)
	}
	return n, err
}
