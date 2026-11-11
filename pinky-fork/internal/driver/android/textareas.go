package android

import (
	"strings"

	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/screen"
)

// Where the tree misses text. Some screens draw their text as pixels the
// accessibility tree does not describe: mini-programs and other pages in
// super-apps' own web views, games and other canvases, maps, charts. The
// tree then has a large area with little or nothing labelled in it, and
// OCR reads the text there when the gateway asks for OCR (text.go).
//
// Class names tell only web views apart: a SurfaceView, a TextureView or a
// custom view reports android.view.View or the layout it extends. So an
// area counts when it is
//
//   - a web view, at least webMinArea of the screen;
//   - a view with nothing in it (a leaf) that is not an image, a field or
//     a list and has no text, at least canvasMinArea of the screen: a
//     canvas, a surface, a Flutter view without semantics, Chrome's page
//     when it exposes none of it (a FrameLayout described "Web View"). A
//     description names such an area; it is not its text;
//
// and the window's labelled nodes over it, its own or drawn on top of it,
// are fewer than minLabels or cover less than minCover of it: a
// launcher's full-screen backdrop has its icons over it, a game's canvas
// a score or two. And an application window counts, at least
// canvasMinArea of the screen, with fewer than minLabels labelled nodes
// in all of it.
const (
	webMinArea    = 1.0 / 8 // of the screen, for a web view to count
	canvasMinArea = 1.0 / 6 // of the screen
	minLabels     = 3
	minCover      = 0.2
	coverCells    = 32 // grid cells along an area's longer side, for its cover
)

// textNode is a node of either tree, the companion's or uiautomator's, as
// the search needs it.
type textNode struct {
	class    string
	id       string // resource id
	bounds   screen.Rect
	labelled bool // text, a description or a hint
	text     bool // text of its own, not only a description
	editable bool
	list     bool // scrollable
	kids     []*textNode
}

// textWindow is a window of the tree, top-most first in a textScan.
type textWindow struct {
	app    bool // an application window; else a system bar, the keyboard, an overlay
	bar    bool // a status or navigation bar, or the keyboard
	bounds screen.Rect
	covers []screen.Rect // what it hides of the windows below it
	prefix string        // the window's part of element paths
	root   *textNode
}

// textArea is an area of the screen whose text the tree misses.
type textArea struct {
	rect  screen.Rect
	above []screen.Rect // what windows above it cover of it
	kind  string        // "web", "canvas" or "window"
	path  string        // the node's ancestry and itself, for an element that marks it
	id    string
}

// holds reports whether p lies in the area and in no window above it.
func (a textArea) holds(p screen.Point) bool {
	if !a.rect.Contains(p) {
		return false
	}
	for _, r := range a.above {
		if r.Contains(p) {
			return false
		}
	}
	return true
}

// textScan is what a screen's tree says about where text may be missing.
type textScan struct {
	areas []textArea
	// bars are the status and navigation bars and the keyboard: OCR text
	// from there is never the app's.
	bars []screen.Rect
	// keyboard: the keyboard's window is among bars. Without it (a
	// uiautomator tree), OCR finds the keyboard by its letters.
	keyboard bool
}

// scanWindows finds the areas of a w x h screen whose text the tree misses.
func scanWindows(windows []textWindow, w, h int) textScan {
	var sc textScan
	full := screen.Rect{W: w, H: h}
	area := float64(w) * float64(h)
	var above []screen.Rect
	for _, win := range windows {
		if win.bar {
			sc.bars = append(sc.bars, win.covers...)
		}
		if win.app && win.root != nil && area > 0 {
			clip := intersect(win.bounds, full)
			if win.bounds.Empty() {
				clip = full
			}
			sc.areas = append(sc.areas, scanWindow(win, clip, area, append([]screen.Rect(nil), above...))...)
		}
		above = append(above, win.covers...)
	}
	return sc
}

func scanWindow(win textWindow, clip screen.Rect, screenArea float64, above []screen.Rect) []textArea {
	labelled := labelledIn(win.root, clip)
	if len(labelled) < minLabels && rectArea(clip) >= canvasMinArea*screenArea {
		return []textArea{{rect: clip, above: above, kind: "window", path: win.prefix}}
	}
	var out []textArea
	var walk func(n *textNode, path string)
	walk = func(n *textNode, path string) {
		vis := intersect(n.bounds, clip)
		self := path + ">" + nodeSegment(n)
		switch {
		case vis.Empty():
			// Off screen; its children may still show (a scrolled list's
			// container can report odd bounds), so go on.
		case isWebClass(n.class):
			if rectArea(vis) >= webMinArea*screenArea && sparse(n, vis, labelled) {
				out = append(out, textArea{rect: vis, above: above, kind: "web", path: self, id: n.id})
			}
			return // its insides belong to the page
		case len(visibleKids(n, clip)) == 0:
			if !n.text && !n.editable && !n.list && !isImageClass(n.class) && rectArea(vis) >= canvasMinArea*screenArea &&
				sparse(n, vis, labelled) {
				out = append(out, textArea{rect: vis, above: above, kind: "canvas", path: self, id: n.id})
			}
			return
		}
		for _, k := range n.kids {
			walk(k, self)
		}
	}
	walk(win.root, win.prefix)
	return out
}

// labelledNode is a labelled node's visible bounds.
type labelledNode struct {
	n   *textNode
	vis screen.Rect
}

// labelledIn collects the labelled nodes of a window whose middle shows in
// clip.
func labelledIn(root *textNode, clip screen.Rect) []labelledNode {
	var out []labelledNode
	var walk func(m *textNode)
	walk = func(m *textNode) {
		if m.labelled {
			if vis := intersect(m.bounds, clip); !vis.Empty() && clip.Contains(m.bounds.Center()) {
				out = append(out, labelledNode{m, vis})
			}
		}
		for _, k := range m.kids {
			walk(k)
		}
	}
	walk(root)
	return out
}

// sparse reports whether the window's labelled nodes over an area (vis of
// node n) are too few, or cover too little of it, for its text to be read
// from the tree. n's own label (a web view's page title, "Web View") and
// labelled nodes that span half of the area or more (a container with an
// aria-label, a screen-wide backdrop) say nothing about where its text
// is, so they do not cover it.
func sparse(n *textNode, vis screen.Rect, labelled []labelledNode) bool {
	half := rectArea(vis) / 2
	var over []screen.Rect
	count := 0
	for _, l := range labelled {
		if l.n == n || !vis.Contains(l.vis.Center()) {
			continue
		}
		count++
		if r := intersect(l.vis, vis); rectArea(r) < half {
			over = append(over, r)
		}
	}
	if count < minLabels {
		return true
	}
	// Cover on a grid: which cells' middles lie in a labelled node. It
	// stops once enough are covered.
	step := max(1, max(vis.W, vis.H)/coverCells)
	cols := (vis.W + step - 1 - step/2) / step
	rows := (vis.H + step - 1 - step/2) / step
	need := minCover * float64(cols*rows)
	covered := 0
	for y := vis.Y + step/2; y < vis.Y+vis.H; y += step {
		for x := vis.X + step/2; x < vis.X+vis.W; x += step {
			p := screen.Point{X: x, Y: y}
			for _, r := range over {
				if r.Contains(p) {
					covered++
					break
				}
			}
			if float64(covered) >= need {
				return false
			}
		}
	}
	return true
}

func visibleKids(n *textNode, clip screen.Rect) []*textNode {
	var out []*textNode
	for _, k := range n.kids {
		if !intersect(k.bounds, clip).Empty() {
			out = append(out, k)
		}
	}
	return out
}

// isWebClass reports whether a class is a web view: Android's, Chromium's,
// or a super-app's own engine (Tencent's X5 and XWeb, UC's U4 in Alipay).
func isWebClass(cls string) bool {
	if strings.Contains(shortName(cls), "WebView") {
		return true
	}
	for _, p := range []string{"org.chromium.", "com.uc.webview.", "com.uc.webkit.", "com.tencent.smtt.", "com.tencent.xweb."} {
		if strings.HasPrefix(cls, p) {
			return true
		}
	}
	return false
}

// isImageClass reports whether a class draws an image: pixels, but no text
// worth reading on every read.
func isImageClass(cls string) bool {
	return strings.Contains(shortName(cls), "Image")
}

func shortName(cls string) string {
	if i := strings.LastIndexByte(cls, '.'); i >= 0 {
		return cls[i+1:]
	}
	return cls
}

func nodeSegment(n *textNode) string {
	s := shortName(n.class)
	if n.id != "" {
		id := n.id
		if i := strings.LastIndexByte(id, '/'); i >= 0 {
			id = id[i+1:]
		}
		s += "#" + id
	}
	return s
}

func rectArea(r screen.Rect) float64 { return float64(r.W) * float64(r.H) }

func intersect(a, b screen.Rect) screen.Rect {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	if x1 <= x0 || y1 <= y0 {
		return screen.Rect{}
	}
	return screen.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// scanCompanion scans a companion tree.
func scanCompanion(t *companion.Tree) textScan {
	if t == nil {
		return textScan{}
	}
	var windows []textWindow
	keyboard := false
	for _, w := range t.Windows {
		tw := textWindow{
			bounds: companionRect(w.Bounds),
			prefix: w.Type + ":" + w.Package,
		}
		switch w.Type {
		case companion.WindowApplication:
			tw.app = true
		case companion.WindowInputMethod:
			tw.bar, keyboard = true, true
		case companion.WindowSystem:
			tw.bar = isBar(tw.bounds, t.Display.Width, t.Display.Height)
		}
		for _, r := range w.Region {
			if rr := companionRect(r); !rr.Empty() {
				tw.covers = append(tw.covers, rr)
			}
		}
		if len(tw.covers) == 0 && !tw.bounds.Empty() {
			tw.covers = []screen.Rect{tw.bounds}
		}
		if w.Root != nil {
			tw.root = companionNode(w.Root)
		}
		windows = append(windows, tw)
	}
	sc := scanWindows(windows, t.Display.Width, t.Display.Height)
	sc.keyboard = keyboard
	return sc
}

func companionNode(n *companion.Node) *textNode {
	tn := &textNode{
		class:    n.Class,
		id:       n.ID,
		bounds:   companionRect(n.Bounds),
		labelled: strings.TrimSpace(n.Text) != "" || strings.TrimSpace(n.Desc) != "" || strings.TrimSpace(n.Hint) != "",
		text:     strings.TrimSpace(n.Text) != "",
		editable: n.Editable,
		list:     n.Scrollable,
	}
	for _, c := range n.Children {
		if c != nil && !c.Invisible {
			tn.kids = append(tn.kids, companionNode(c))
		}
	}
	return tn
}

func companionRect(b companion.Bounds) screen.Rect {
	return screen.Rect{X: b[0], Y: b[1], W: b[2] - b[0], H: b[3] - b[1]}
}

// isBar reports whether a system window is the status or navigation bar:
// it spans an edge of the screen and is thin, as the companion's own
// conversion decides.
func isBar(r screen.Rect, w, h int) bool {
	if w <= 0 || h <= 0 || r.Empty() {
		return false
	}
	const thin = 0.12
	wide, tall := r.W >= w*9/10, r.H >= h*9/10
	switch {
	case wide && float64(r.H) <= thin*float64(h):
		return r.Y <= 0 || r.Y+r.H >= h
	case tall && float64(r.W) <= thin*float64(w):
		return r.X <= 0 || r.X+r.W >= w
	}
	return false
}

// scanDump scans a uiautomator tree: the app's window only, without the
// bars or the keyboard.
func scanDump(t *uiTree, w, h int) textScan {
	if t == nil {
		return textScan{}
	}
	var windows []textWindow
	for _, r := range t.Roots {
		windows = append(windows, textWindow{app: true, bounds: r.Bounds, prefix: "dump:" + r.Package, root: dumpNode(r)})
	}
	return scanWindows(windows, w, h)
}

func dumpNode(n *uiNode) *textNode {
	tn := &textNode{
		class:    n.Class,
		id:       n.ResID,
		bounds:   n.Bounds,
		labelled: strings.TrimSpace(n.Text) != "" || strings.TrimSpace(n.Desc) != "" || strings.TrimSpace(n.Hint) != "",
		text:     strings.TrimSpace(n.Text) != "",
		editable: strings.Contains(n.Class, "EditText"),
		list:     n.Scrollable,
	}
	for _, c := range n.Children {
		tn.kids = append(tn.kids, dumpNode(c))
	}
	return tn
}
