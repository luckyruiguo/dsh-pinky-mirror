package android

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/ocr"
	"github.com/DIYgod/pinky/internal/screen"
)

// OCR on Android. The tree is read first. When the gateway asks for OCR
// (driver.ObserveOptions.OCR) and the tree misses text somewhere
// (textareas.go), or the gateway asks for all the text (OCRAll), the
// driver captures the screen, reads it with the computer's recognizer
// (internal/ocr) and adds the lines no element shows as OCR elements, the
// way the iOS driver does: agents tap them by x,y. Only lines in the areas
// the tree misses are added, unless all were asked for; never the bars'
// or the keyboard's. The picture is `screencap` over USB, and a frame of
// the live video over Wi-Fi, where a screenshot's PNG takes seconds to
// come, and on a background display, which has no screenshots. It stays in
// memory and is dropped once read.
//
// Nothing is captured of a locked phone, or of a screen the gateway does
// not allow (driver.ObserveOptions.MayCapture: a blocked app), and lines
// are dropped when another window came to the front while the picture was
// taken. On a computer without a recognizer (Linux), the areas the tree
// misses carry stateNoOCR instead.

// stateNoOCR marks an area whose text the tree misses, on a computer that
// cannot read text from pixels: the agent knows that the screen shows
// more than its list.
const stateNoOCR = "no_ocr"

// The recognizer, as variables for tests.
var (
	ocrSupported = ocr.Supported
	ocrImage     = ocr.RecognizeImage
	ocrEncoded   = ocr.Recognize
	ocrWarm      = ocr.Warm
)

// frameSlack is how far a frame's aspect ratio may be from the screen's:
// the encoder rounds the video's size to a multiple of 8 or 16.
const frameSlack = 0.02

// frameWait bounds the wait for a frame. The gateway waits for a key frame
// it asks for, but the phone's encoder restarts at most once a second, and
// a screen that keeps moving (a game, a video) then sends no pause either:
// at frameWait the newest picture decoded is taken, or a screenshot.
var frameWait = 1500 * time.Millisecond

// textSource captures a screen for OCR.
type textSource struct {
	// frame returns a frame of the screen's live video (the gateway's
	// driver.ObserveOptions.Frame); nil when there is none.
	frame func(ctx context.Context) (image.Image, error)
	// shot returns a screenshot as a PNG; nil where there is none.
	shot func(ctx context.Context) ([]byte, error)
	// moved reports whether another window may have come to the front
	// since the tree was read, at since, from app.
	moved func(ctx context.Context, since time.Time, app screen.App) bool
}

// TextStats say what a read did about OCR, for diagnostics (androidctl).
type TextStats struct {
	Areas   int    // areas whose text the tree misses
	Skipped string // why no OCR ran, when it did not
	From    string // what OCR read: "video" or "screenshot"
	Lines   int    // lines OCR read
	Added   int    // OCR elements added
	Capture time.Duration
	OCR     time.Duration
	Err     error // why capturing or reading failed
}

// addText adds OCR elements to s, as the package comment says. sc is what
// the tree read at since says about where text is missing.
func addText(ctx context.Context, s *screen.Screen, sc textScan, opt driver.ObserveOptions, since time.Time, src textSource) TextStats {
	st := TextStats{Areas: len(sc.areas)}
	if err := ocrSupported(); err != nil {
		markNoOCR(s, sc)
		st.Skipped = "this computer has no OCR"
		return st
	}
	switch {
	case !opt.OCR:
		st.Skipped = "not asked"
	case !opt.OCRAll && len(sc.areas) == 0:
		st.Skipped = "the tree misses no text"
	case s.Locked:
		st.Skipped = "locked"
	case s.Width <= 0 || s.Height <= 0:
		st.Skipped = "no screen size"
	case opt.MayCapture != nil && !opt.MayCapture(s):
		st.Skipped = "the gateway takes no picture of this screen"
	}
	if st.Skipped != "" {
		return st
	}
	t := time.Now()
	pic, err := capture(ctx, s, src)
	st.Capture, st.From = time.Since(t), pic.from
	if err != nil {
		st.Err = err
		return st
	}
	if src.moved != nil && src.moved(ctx, since, s.App) {
		st.Skipped = "another window came to the front"
		return st
	}
	t = time.Now()
	var lines []ocr.Line
	if pic.img != nil {
		lines, err = ocrImage(pic.img, ocr.Options{Languages: ocr.Languages})
	} else {
		lines, err = ocrEncoded(pic.png, ocr.Options{Languages: ocr.Languages})
	}
	st.OCR = time.Since(t)
	if err != nil {
		st.Err = fmt.Errorf("ocr: %w", err)
		return st
	}
	st.Lines = len(lines)
	lines = sc.keep(lines, s.Width, s.Height, opt.OCRAll, s.Keyboard)
	extra := ocr.Extras(s.Elements, lines, s.Width, s.Height)
	s.Elements = append(s.Elements, extra...)
	st.Added = len(extra)
	return st
}

// picture is a capture of the screen: a decoded frame or a PNG.
type picture struct {
	img  image.Image
	png  []byte
	from string
}

// capture takes the picture OCR reads: a frame of the live video when
// there is one of the screen's shape, else a screenshot.
func capture(ctx context.Context, s *screen.Screen, src textSource) (picture, error) {
	var errs []error
	if src.frame != nil {
		fctx, cancel := context.WithTimeout(ctx, frameWait)
		img, err := src.frame(fctx)
		cancel()
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("video: %w", err))
		case img == nil || !fits(img.Bounds(), s.Width, s.Height):
			errs = append(errs, errors.New("video: the frame is not of this screen"))
		default:
			return picture{img: img, from: "video"}, nil
		}
		if ctx.Err() != nil {
			return picture{}, ctx.Err()
		}
	}
	if src.shot != nil {
		png, err := src.shot(ctx)
		if err == nil && len(png) > 0 {
			return picture{png: png, from: "screenshot"}, nil
		}
		if err == nil {
			err = errors.New("empty")
		}
		errs = append(errs, fmt.Errorf("screenshot: %w", err))
	}
	if len(errs) == 0 {
		return picture{}, fmt.Errorf("no picture of this screen: %w", driver.ErrUnsupported)
	}
	return picture{}, fmt.Errorf("no picture for OCR: %w", errors.Join(errs...))
}

// fits reports whether a picture has the screen's shape.
func fits(b image.Rectangle, w, h int) bool {
	if b.Empty() || w <= 0 || h <= 0 {
		return false
	}
	pic := float64(b.Dx()) / float64(b.Dy())
	scr := float64(w) / float64(h)
	d := pic/scr - 1
	return d < frameSlack && d > -frameSlack
}

// keep returns the lines OCR elements may come from: none from the bars or
// the keyboard, and only those in the areas the tree misses unless all
// were asked for.
func (sc textScan) keep(lines []ocr.Line, w, h int, all, keyboard bool) []ocr.Line {
	if keyboard && !sc.keyboard {
		// The tree does not say where the keyboard is (uiautomator's):
		// find it by its keys.
		if top, ok := ocr.KeyboardTop(lines); ok {
			kept := lines[:0:0]
			for _, l := range lines {
				if l.Y < top {
					kept = append(kept, l)
				}
			}
			lines = kept
		}
	}
	var out []ocr.Line
	for _, l := range lines {
		c := ocr.Rect(l, w, h).Center()
		if in(sc.bars, c) {
			continue
		}
		if !all && !sc.holds(c) {
			continue
		}
		out = append(out, l)
	}
	return out
}

func (sc textScan) holds(p screen.Point) bool {
	for _, a := range sc.areas {
		if a.holds(p) {
			return true
		}
	}
	return false
}

func in(rs []screen.Rect, p screen.Point) bool {
	for _, r := range rs {
		if r.Contains(p) {
			return true
		}
	}
	return false
}

// markNoOCR gives the areas the tree misses stateNoOCR: the element that
// is the area when the tree lists one, else an element of its own.
func markNoOCR(s *screen.Screen, sc textScan) {
	for _, a := range sc.areas {
		found := false
		for i := range s.Elements {
			e := &s.Elements[i]
			if e.Source == screen.SourceTree && near(e.Bounds, a.rect) {
				if !e.Has(stateNoOCR) {
					e.States = append(e.States, stateNoOCR)
				}
				found = true
				break
			}
		}
		if !found {
			s.Elements = append(s.Elements, screen.Element{
				Role: screen.RoleOther, Bounds: a.rect, States: []string{stateNoOCR},
				Source: screen.SourceTree, Path: "no_ocr:" + a.path, ID: a.id,
			})
		}
	}
}

// near reports whether two rectangles are the same but for a few pixels.
func near(a, b screen.Rect) bool {
	const px = 4
	d := func(x, y int) bool { return x-y <= px && y-x <= px }
	return d(a.X, b.X) && d(a.Y, b.Y) && d(a.W, b.W) && d(a.H, b.H)
}

// textLog logs a device's OCR failures, once until the reason changes.
type textLog struct {
	mu   sync.Mutex
	last string
}

func (l *textLog) note(logf func(string, ...any), id string, st TextStats) {
	msg := ""
	if st.Err != nil {
		msg = st.Err.Error()
	}
	l.mu.Lock()
	changed := msg != l.last
	l.last = msg
	l.mu.Unlock()
	if changed && msg != "" && !strings.Contains(msg, context.Canceled.Error()) {
		logf("android: %s: no OCR for this read: %s", id, msg)
	}
}
