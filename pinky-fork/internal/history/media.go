package history

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxShotSide is the longest side of a stored screenshot. Larger screens are
// scaled down by a whole factor, which keeps a 1080x2400 phone at 540x1200.
const MaxShotSide = 1280

// Media is a stored file.
type Media struct {
	ID      string
	Kind    string // "screenshot"
	Path    string // relative to the media folder
	Size    int64
	Hash    string
	Created time.Time
}

// ErrNoMedia means the media id is unknown or its file is gone.
var ErrNoMedia = errors.New("media not found")

// SaveScreenshot stores a screenshot as JPEG and returns its record.
func (s *Store) SaveScreenshot(ctx context.Context, img image.Image) (Media, error) {
	return s.SaveScreenshotAs(ctx, NewID("m_"), img)
}

// SaveScreenshotAs stores a screenshot under an id chosen beforehand, so a
// caller can refer to it before the write finishes.
func (s *Store) SaveScreenshotAs(ctx context.Context, id string, img image.Image) (Media, error) {
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return Media{}, errors.New("bad media id")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, Downscale(img, MaxShotSide), &jpeg.Options{Quality: 75}); err != nil {
		return Media{}, err
	}
	now := time.Now()
	m := Media{ID: id, Kind: "screenshot", Size: int64(buf.Len()), Hash: digest(buf.Bytes()), Created: now}
	m.Path = filepath.ToSlash(filepath.Join(now.Format("2006-01"), m.ID+".jpg"))
	abs := filepath.Join(s.mediaDir, filepath.FromSlash(m.Path))
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return Media{}, err
	}
	if err := os.WriteFile(abs, buf.Bytes(), 0o600); err != nil {
		return Media{}, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO media (id, kind, path, size, hash, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		m.ID, m.Kind, m.Path, m.Size, m.Hash, ms(m.Created)); err != nil {
		os.Remove(abs)
		return Media{}, err
	}
	return m, nil
}

// MediaPath returns the absolute path of a media file.
func (s *Store) MediaPath(ctx context.Context, id string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var rel string
	err := s.db.QueryRowContext(ctx, `SELECT path FROM media WHERE id = ?`, id).Scan(&rel)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoMedia
	}
	if err != nil {
		return "", err
	}
	if strings.Contains(rel, "..") {
		return "", ErrNoMedia
	}
	abs := filepath.Join(s.mediaDir, filepath.FromSlash(rel))
	if _, err := os.Stat(abs); err != nil {
		return "", ErrNoMedia
	}
	return abs, nil
}

// Downscale shrinks img by the smallest whole factor that brings its longest
// side to max or less, averaging each block of pixels.
func Downscale(img image.Image, max int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	long := w
	if h > long {
		long = h
	}
	if max <= 0 || long <= max || w == 0 || h == 0 {
		return img
	}
	f := (long + max - 1) / max
	src, ok := img.(*image.RGBA)
	if !ok {
		src = image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	} else if b.Min != (image.Point{}) {
		src = src.SubImage(b).(*image.RGBA)
	}
	ow, oh := w/f, h/f
	dst := image.NewRGBA(image.Rect(0, 0, ow, oh))
	n := uint32(f * f)
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			var r, g, bl, a uint32
			for dy := 0; dy < f; dy++ {
				row := src.PixOffset(src.Rect.Min.X+x*f, src.Rect.Min.Y+y*f+dy)
				for dx := 0; dx < f; dx++ {
					p := src.Pix[row+dx*4 : row+dx*4+4 : row+dx*4+4]
					r += uint32(p[0])
					g += uint32(p[1])
					bl += uint32(p[2])
					a += uint32(p[3])
				}
			}
			o := dst.PixOffset(x, y)
			dst.Pix[o] = uint8(r / n)
			dst.Pix[o+1] = uint8(g / n)
			dst.Pix[o+2] = uint8(bl / n)
			dst.Pix[o+3] = uint8(a / n)
		}
	}
	return dst
}
