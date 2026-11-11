package gateway

import (
	"context"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

// A page in a web view is shown before it is in the accessibility tree: on
// the iPhone 17 Simulator (iOS 26.5, WebDriverAgent), a Wikipedia page that
// Safari had just opened was on the screen with no element of the page in
// the tree a second after the link opened, and with 51 of them 2.5 s
// later. A read in between has the page's text from OCR alone, without
// its links and buttons (field test 2026-11-10, an iPhone 13 mini: a page
// opened in an app read as OCR lines only). After an action, a read that
// looks like that is read again until the tree has the page, for treeWait
// at most and within the call's budget.

// treeWait is how long an action waits for the tree of a page that OCR
// read before it. Tests shorten it.
var treeWait = 4 * time.Second

// treeReserve is what the wait leaves of the call's budget.
const treeReserve = 5 * time.Second

// sparseTree reports whether OCR read much more of s than its tree has: a
// page whose tree is still loading. A screen of text with a full tree has
// more elements in its tree than OCR lines beside it.
func sparseTree(s *screen.Screen) bool {
	if s == nil || s.Locked {
		return false
	}
	ocr, tree := 0, 0
	for _, e := range s.Elements {
		if e.Source == screen.SourceOCR {
			ocr++
		} else {
			tree++
		}
	}
	return ocr >= 8 && ocr >= 2*tree
}

// awaitsTree reports whether the result of an action, next, should wait
// for its tree: it is sparse, and its app's was not sparse for a whole
// wait before (a game or a canvas, whose text is only ever in the picture).
func (g *Gateway) awaitsTree(d *device, next *screen.Screen) bool {
	if !sparseTree(next) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return !sameApp(d.treeless, next.App)
}

// awaitTree reads the screen again until its tree is no longer sparse, or
// another app is in front, for treeWait at most. It returns the last read,
// or next when none succeeded.
func (g *Gateway) awaitTree(ctx context.Context, d *device, next *screen.Screen) *screen.Screen {
	end := time.Now().Add(treeWait)
	deadline := budgetBefore(ctx, end, treeReserve)
	for time.Now().Before(deadline) {
		if !sleepCtx(ctx, max(g.opt.PollInterval, 300*time.Millisecond), nil) {
			return next
		}
		s, err := g.observe(ctx, d, true)
		if err != nil {
			return next
		}
		if !sameApp(s.App, next.App) || !sparseTree(s) {
			return s
		}
		next = s
	}
	if deadline.Equal(end) {
		// The whole wait passed: this app shows its text in pictures.
		g.mu.Lock()
		d.treeless = next.App
		g.mu.Unlock()
	}
	return next
}
