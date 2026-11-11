package companion

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/DIYgod/pinky/internal/screen"
)

// ScreenOptions tune Tree.Screen.
type ScreenOptions struct {
	// Keyboard lists the keyboard's keys (with DumpOptions.IME). Without
	// it, an input method window is listed only when the app walked it on
	// its own: a sheet that covers the screen (Gboard's stylus tutorial,
	// a setup screen), which is what the person sees there.
	Keyboard   bool
	SystemBars bool // list the status and navigation bars (needs DumpOptions.Bars)
}

const (
	maxLabel       = 1000 // runes in one label
	maxMergedLabel = 500  // runes in a label merged from descendants
	// A clickable container takes its descendants' text as its label only
	// while it is this small, so a clickable root never swallows a screen.
	maxMergeChildren = 12
	maxMergeArea     = 0.5  // of the screen
	maxUnlabeledArea = 0.25 // larger unlabeled targets are left out
	minUnlabeledDP   = 12   // and so are slivers thinner than this
	systemBarMax     = 0.12 // a system window this thin along an edge is a bar
)

// Screen converts the tree into the screen model: one element per
// meaningful node, windows top-most first. Refs are left to the gateway.
func (t *Tree) Screen(opt ScreenOptions) *screen.Screen {
	s := &screen.Screen{
		Width:  t.Display.Width,
		Height: t.Display.Height,
		Locked: t.Locked,
	}
	if s.Width > 0 && s.Height > 0 {
		s.Orientation = "portrait"
		if s.Width > s.Height {
			s.Orientation = "landscape"
		}
	}
	// Where touches go to the keyboard or a system bar instead of the app:
	// the keyboard window's largest touchable part is the keyboard, and
	// its other parts (a stylus toolbar's strips) count as covered. A
	// keyboard whose own nodes came (DumpOptions.IME, or a sheet the app
	// walked unasked) is listed and is what the agent acts on, so it lies
	// over nothing.
	full := screen.Rect{W: s.Width, H: s.Height}
	for _, w := range t.Windows {
		ime := w.Type == WindowInputMethod
		if ime {
			s.Keyboard = true
		}
		if (ime && w.Root != nil) || (!ime && !t.isSystemBar(w)) {
			continue
		}
		for _, r := range w.covers() {
			r = clipTo(r, full)
			if ime && r.W*r.H > s.KeyboardFrame.W*s.KeyboardFrame.H {
				r, s.KeyboardFrame = s.KeyboardFrame, r
			}
			if !r.Empty() {
				s.Covered = append(s.Covered, r)
			}
		}
	}
	s.App = t.foreground()

	dpi := t.Display.DensityDPI
	if dpi <= 0 {
		dpi = 160
	}
	cv := &converter{screenArea: float64(s.Width) * float64(s.Height), minTarget: minUnlabeledDP * dpi / 160}
	var above []screen.Rect // what the windows above the current one cover
	for _, w := range t.Windows {
		wb := w.Bounds.rect()
		// The app sends an input method window's nodes only when asked
		// (DumpOptions.IME) or when the window covers the screen like a
		// sheet; either way, list what it sent.
		skip := (t.isSystemBar(w) && !opt.SystemBars) || w.Type == WindowMagnificationOverlay
		if !skip && w.Root != nil {
			cv.clip = intersect(wb, full)
			if full.Empty() {
				cv.clip = wb
			}
			cv.occluders = above
			cv.prefix = w.Type + ":" + w.Package
			cv.node(w.Root, cv.prefix, false)
		}
		above = append(above, w.covers()...)
	}
	s.Elements = cv.out
	return s
}

// covers returns what a window hides from the windows below it: its
// touchable region when the app sent one, else its bounds.
func (w Window) covers() []screen.Rect {
	var out []screen.Rect
	for _, r := range w.Region {
		if rr := r.rect(); !rr.Empty() {
			out = append(out, rr)
		}
	}
	if len(out) > 0 {
		return out
	}
	if wb := w.Bounds.rect(); !wb.Empty() {
		return []screen.Rect{wb}
	}
	return nil
}

// foreground picks the app the person is looking at: the top-most focused
// or active application window, or a focused full-screen system window
// such as the notification shade, else the top-most application window.
func (t *Tree) foreground() screen.App {
	for _, w := range t.Windows {
		if !(w.Focused || w.Active) || w.Package == "" {
			continue
		}
		if w.Type == WindowApplication || (w.Type == WindowSystem && !t.isSystemBar(w)) {
			return appOf(w)
		}
	}
	for _, w := range t.Windows {
		if w.Type == WindowApplication && w.Package != "" {
			return appOf(w)
		}
	}
	return screen.App{}
}

// appOf names a window's app. Until the phone has loaded the app's display
// name, an application window's title (usually the activity label) stands
// in for it.
func appOf(w Window) screen.App {
	name := w.App
	if name == "" && w.Type == WindowApplication {
		name = w.Title
	}
	return screen.App{Name: name, ID: w.Package}
}

// isSystemBar reports whether a window is the status bar or the navigation
// bar: a system window that spans one edge of the screen and is thin.
func (t *Tree) isSystemBar(w Window) bool {
	if w.Type != WindowSystem {
		return false
	}
	W, H := t.Display.Width, t.Display.Height
	if W <= 0 || H <= 0 {
		return false
	}
	l, tp, r, b := w.Bounds[0], w.Bounds[1], w.Bounds[2], w.Bounds[3]
	wide := r-l >= W*9/10
	tall := b-tp >= H*9/10
	switch {
	case wide && float64(b-tp) <= systemBarMax*float64(H):
		return tp <= 0 || b >= H
	case tall && float64(r-l) <= systemBarMax*float64(W):
		return l <= 0 || r >= W
	}
	return false
}

type converter struct {
	out        []screen.Element
	screenArea float64
	minTarget  int           // smallest side of an unlabeled target, in pixels
	clip       screen.Rect   // the window's bounds on the screen
	occluders  []screen.Rect // windows above this one
	prefix     string
}

// node converts n and its subtree. merged means an ancestor already took
// this node's text as its label, so plain text is not listed again.
func (cv *converter) node(n *Node, path string, merged bool) {
	if n.Invisible {
		return
	}
	self := path + ">" + segment(n)
	switch {
	case n.Editable:
		cv.emit(n, path, inputLabel(n), nil)
		cv.children(n, self, true)
	case isTarget(n):
		label := ownLabel(n)
		var check *Node
		mergedChildren := false
		if label == "" && cv.mergeable(n) {
			var parts []string
			parts, check = mergedLabels(n, nil, nil)
			label = truncate(strings.Join(parts, ", "), maxMergedLabel)
			mergedChildren = true
		}
		// An unlabeled target is listed only when it is small and mostly on
		// screen: a big one is a clickable backdrop, and a row scrolled almost
		// out of view (Android reports only the visible sliver) has its text
		// off screen.
		if label != "" || n.Checkable || check != nil ||
			(cv.area(n) <= maxUnlabeledArea*cv.screenArea && cv.mostlyVisible(n)) {
			cv.emit(n, path, label, check)
		}
		cv.children(n, self, mergedChildren)
	case n.Scrollable:
		label := ownLabel(n)
		// Scrolling containers often nest (a scroll view around a list of the
		// same size); list the outer one only.
		if prev := cv.last(); label != "" || prev == nil || prev.Role != screen.RoleList || prev.Label != "" ||
			prev.Bounds != intersect(n.Bounds.rect(), cv.clip) || !strings.HasPrefix(path, prev.Path) {
			cv.emit(n, path, label, nil)
		}
		cv.children(n, self, false)
	default:
		if !merged {
			if label := ownLabel(n); label != "" {
				cv.emit(n, path, label, nil)
			}
		}
		cv.children(n, self, merged)
	}
}

func (cv *converter) children(n *Node, path string, merged bool) {
	for _, c := range n.Children {
		cv.node(c, path, merged)
	}
}

// emit adds n as an element. check is the node whose checkable state the
// element shows, when it differs from n (a row that holds a switch).
func (cv *converter) emit(n *Node, path, label string, check *Node) {
	b := intersect(n.Bounds.rect(), cv.clip)
	if b.Empty() {
		return
	}
	b, ok := cv.unoccluded(b)
	if !ok {
		return
	}
	if check == nil && n.Checkable {
		check = n
	}
	role := roleOf(n, check)
	cv.out = append(cv.out, screen.Element{
		Role:   role,
		Label:  label,
		Bounds: b,
		States: states(n, role, check),
		Source: screen.SourceTree,
		Path:   path,
		ID:     n.ID,
	})
}

// unoccluded drops an element whose center lies under a window above it,
// unless part of it stays uncovered, which then becomes its bounds.
func (cv *converter) unoccluded(b screen.Rect) (screen.Rect, bool) {
	for _, o := range cv.occluders {
		if !o.Contains(b.Center()) {
			continue
		}
		rest := largestOutside(b, o)
		if rest.Empty() {
			return b, false
		}
		b = rest
	}
	return b, true
}

// last returns the element emitted last in this window, if any.
func (cv *converter) last() *screen.Element {
	if len(cv.out) == 0 || cv.out[len(cv.out)-1].Path == "" || !strings.HasPrefix(cv.out[len(cv.out)-1].Path, cv.prefix) {
		return nil
	}
	return &cv.out[len(cv.out)-1]
}

// mostlyVisible reports whether at least half of the node lies inside the
// window and the visible part is not a thin sliver.
func (cv *converter) mostlyVisible(n *Node) bool {
	full := n.Bounds.rect()
	if full.Empty() {
		return false
	}
	vis := intersect(full, cv.clip)
	if vis.W < cv.minTarget || vis.H < cv.minTarget {
		return false
	}
	return 2*cv.area(n) >= float64(full.W)*float64(full.H)
}

func (cv *converter) area(n *Node) float64 {
	b := intersect(n.Bounds.rect(), cv.clip)
	return float64(b.W) * float64(b.H)
}

func (cv *converter) mergeable(n *Node) bool {
	if cv.screenArea > 0 && cv.area(n) > maxMergeArea*cv.screenArea {
		return false
	}
	count := 0
	var walk func(*Node) bool
	walk = func(m *Node) bool {
		for _, c := range m.Children {
			if c.Invisible || c.Editable || isTarget(c) || c.Scrollable {
				continue
			}
			if ownLabel(c) != "" {
				count++
				if count > maxMergeChildren {
					return false
				}
			}
			if !walk(c) {
				return false
			}
		}
		return true
	}
	return walk(n)
}

// mergedLabels collects the labels of n's descendants that are not targets
// themselves, in order, and the first checkable one among them.
func mergedLabels(n *Node, parts []string, check *Node) ([]string, *Node) {
	for _, c := range n.Children {
		if c.Invisible || c.Editable || isTarget(c) || c.Scrollable {
			continue
		}
		if c.Checkable && check == nil {
			check = c
		}
		if l := ownLabel(c); l != "" {
			parts = append(parts, l)
		}
		parts, check = mergedLabels(c, parts, check)
	}
	return parts, check
}

// isTarget reports whether a person can act on the node by tapping it.
func isTarget(n *Node) bool {
	return n.Clickable || n.LongClickable || (n.Checkable && !n.Disabled && n.Focusable)
}

// ownLabel is what a screen reader would say for the node itself: the
// content description, else the text.
func ownLabel(n *Node) string {
	if n.Password {
		return clean(firstNonEmpty(n.Hint, n.Desc))
	}
	if n.Editable {
		return inputLabel(n)
	}
	return clean(firstNonEmpty(n.Desc, n.Text))
}

// inputLabel is the field's text, or its hint or description when empty.
// Password text never reaches the client.
func inputLabel(n *Node) string {
	if n.Password {
		return clean(firstNonEmpty(n.Hint, n.Desc))
	}
	return clean(firstNonEmpty(n.Text, n.Hint, n.Desc))
}

// roleOf maps a node onto the screen model's roles. check is the node whose
// checkable state the element shows, if any.
func roleOf(n *Node, check *Node) string {
	cls := shortClass(n.Class)
	role := strings.ToLower(strings.TrimSpace(n.Role))
	switch {
	case n.Editable || strings.Contains(cls, "EditText") || strings.HasSuffix(cls, "AutoCompleteTextView"):
		return screen.RoleInput
	case role == "switch" || role == "toggle":
		return screen.RoleSwitch
	case role == "checkbox" || role == "check box" || role == "radio button":
		return screen.RoleCheckbox
	case role == "tab":
		return screen.RoleTab
	case role == "link":
		return screen.RoleLink
	case role == "button":
		return screen.RoleButton
	}
	if check != nil {
		if c := shortClass(check.Class); strings.Contains(c, "Switch") || strings.Contains(c, "Toggle") {
			return screen.RoleSwitch
		}
		return screen.RoleCheckbox
	}
	switch {
	case strings.Contains(cls, "Switch") || strings.Contains(cls, "ToggleButton"):
		return screen.RoleSwitch
	case strings.Contains(cls, "CheckBox") || strings.Contains(cls, "RadioButton"):
		return screen.RoleCheckbox
	case strings.Contains(cls, "TabView") || cls == "Tab" || strings.HasSuffix(cls, "$Tab"):
		return screen.RoleTab
	case n.Scrollable || isListClass(cls):
		return screen.RoleList
	case n.Clickable || n.LongClickable || strings.Contains(cls, "Button") || cls == "Spinner":
		return screen.RoleButton
	case strings.Contains(cls, "Image"):
		return screen.RoleImage
	case strings.Contains(cls, "SeekBar") || strings.Contains(cls, "ProgressBar"):
		return screen.RoleOther
	}
	return screen.RoleText
}

func isListClass(cls string) bool {
	switch cls {
	case "ListView", "GridView", "RecyclerView", "ScrollView", "HorizontalScrollView",
		"NestedScrollView", "ViewPager", "ViewPager2", "AbsListView", "ExpandableListView":
		return true
	}
	return false
}

// states lists the element's states in the screen model's order.
func states(n *Node, role string, check *Node) []string {
	var st []string
	if n.Focused {
		st = append(st, screen.StateFocused)
	}
	if n.Selected {
		st = append(st, screen.StateSelected)
	}
	if check != nil && check.Checked {
		st = append(st, screen.StateChecked)
	}
	if n.Disabled {
		st = append(st, screen.StateDisabled)
	}
	if n.Scrollable {
		st = append(st, screen.StateScrollable)
	}
	if n.Editable && role != screen.RoleInput {
		st = append(st, screen.StateEditable)
	}
	if n.Password {
		st = append(st, screen.StatePassword)
	}
	return st
}

// segment is one step of an element's Path: the class's short name and the
// resource id's name, never a sibling index.
func segment(n *Node) string {
	s := shortClass(n.Class)
	if n.ID != "" {
		id := n.ID
		if i := strings.LastIndexByte(id, '/'); i >= 0 {
			id = id[i+1:]
		}
		s += "#" + id
	}
	return s
}

// shortClass returns "Switch" for "android.widget.Switch" and keeps inner
// class markers ("Toolbar$TabView" stays as it is after the last dot).
func shortClass(cls string) string {
	if i := strings.LastIndexByte(cls, '.'); i >= 0 {
		return cls[i+1:]
	}
	return cls
}

func (b Bounds) rect() screen.Rect {
	return screen.Rect{X: b[0], Y: b[1], W: b[2] - b[0], H: b[3] - b[1]}
}

func intersect(a, b screen.Rect) screen.Rect {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	if x1 <= x0 || y1 <= y0 {
		return screen.Rect{}
	}
	return screen.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// clipTo returns the part of r inside full, or r when full is unknown.
func clipTo(r, full screen.Rect) screen.Rect {
	if full.Empty() {
		return r
	}
	return intersect(r, full)
}

// largestOutside returns the largest part of a that lies above, below, left
// or right of o.
func largestOutside(a, o screen.Rect) screen.Rect {
	candidates := []screen.Rect{
		{X: a.X, Y: a.Y, W: a.W, H: o.Y - a.Y},                     // above
		{X: a.X, Y: o.Y + o.H, W: a.W, H: a.Y + a.H - (o.Y + o.H)}, // below
		{X: a.X, Y: a.Y, W: o.X - a.X, H: a.H},                     // left
		{X: o.X + o.W, Y: a.Y, W: a.X + a.W - (o.X + o.W), H: a.H}, // right
	}
	var best screen.Rect
	for _, c := range candidates {
		c = intersect(c, a)
		if !c.Empty() && c.W*c.H > best.W*best.H {
			best = c
		}
	}
	return best
}

// clean collapses whitespace runs, including newlines, into single spaces
// and caps the length.
func clean(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return truncate(b.String(), maxLabel)
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
