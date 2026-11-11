package android

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/DIYgod/pinky/internal/screen"
)

// uiNode is one node of a uiautomator dump.
type uiNode struct {
	Class, Package, Text, Desc, ResID, Hint string

	Checkable, Checked, Clickable, Enabled, Focusable, Focused bool
	Scrollable, LongClickable, Password, Selected              bool

	Bounds   screen.Rect
	Children []*uiNode
}

// uiTree is a parsed uiautomator dump.
type uiTree struct {
	Rotation int // 0 to 3, quarter turns from the natural orientation
	Roots    []*uiNode
}

// Package returns the package of the dumped window.
func (t *uiTree) Package() string {
	for _, r := range t.Roots {
		if r.Package != "" {
			return r.Package
		}
	}
	return ""
}

// errNoHierarchy means the dump output held no UI hierarchy.
var errNoHierarchy = errors.New("no UI hierarchy in uiautomator output")

// parseUIDump parses the XML that `uiautomator dump` writes. Anything around
// the <hierarchy> element (such as the "UI hierchary dumped to" line) is
// ignored, and characters XML does not allow are dropped.
func parseUIDump(out []byte) (*uiTree, error) {
	start := bytes.Index(out, []byte("<hierarchy"))
	if start < 0 {
		return nil, errNoHierarchy
	}
	end := bytes.LastIndex(out, []byte("</hierarchy>"))
	if end < 0 {
		// An empty hierarchy is written as <hierarchy rotation="0" />.
		if j := bytes.Index(out[start:], []byte("/>")); j >= 0 {
			end = start + j + 2
		} else {
			return nil, fmt.Errorf("truncated uiautomator dump")
		}
	} else {
		end += len("</hierarchy>")
	}
	dec := xml.NewDecoder(bytes.NewReader(sanitizeXML(out[start:end])))
	dec.Strict = false
	tree := &uiTree{}
	var stack []*uiNode
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse uiautomator dump: %w", err)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "hierarchy":
				for _, a := range el.Attr {
					if a.Name.Local == "rotation" {
						tree.Rotation, _ = strconv.Atoi(a.Value)
					}
				}
			case "node":
				n := nodeFromAttrs(el.Attr)
				if len(stack) == 0 {
					tree.Roots = append(tree.Roots, n)
				} else {
					p := stack[len(stack)-1]
					p.Children = append(p.Children, n)
				}
				stack = append(stack, n)
			}
		case xml.EndElement:
			if el.Name.Local == "node" && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return tree, nil
}

func nodeFromAttrs(attrs []xml.Attr) *uiNode {
	n := &uiNode{Enabled: true}
	for _, a := range attrs {
		v := a.Value
		switch a.Name.Local {
		case "class":
			n.Class = v
		case "package":
			n.Package = v
		case "text":
			n.Text = v
		case "content-desc":
			n.Desc = v
		case "resource-id":
			n.ResID = v
		case "hint":
			n.Hint = v
		case "checkable":
			n.Checkable = v == "true"
		case "checked":
			n.Checked = v == "true"
		case "clickable":
			n.Clickable = v == "true"
		case "enabled":
			n.Enabled = v == "true"
		case "focusable":
			n.Focusable = v == "true"
		case "focused":
			n.Focused = v == "true"
		case "scrollable":
			n.Scrollable = v == "true"
		case "long-clickable":
			n.LongClickable = v == "true"
		case "password":
			n.Password = v == "true"
		case "selected":
			n.Selected = v == "true"
		case "bounds":
			n.Bounds = parseBounds(v)
		}
	}
	return n
}

// parseBounds parses "[x1,y1][x2,y2]".
func parseBounds(s string) screen.Rect {
	var x1, y1, x2, y2 int
	if _, err := fmt.Sscanf(s, "[%d,%d][%d,%d]", &x1, &y1, &x2, &y2); err != nil {
		return screen.Rect{}
	}
	return screen.Rect{X: x1, Y: y1, W: x2 - x1, H: y2 - y1}
}

// sanitizeXML drops bytes that are not valid UTF-8 and characters XML 1.0
// forbids, which some apps put in their text.
func sanitizeXML(b []byte) []byte {
	clean := true
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 || !xmlChar(r) {
			clean = false
			break
		}
		i += size
	}
	if clean {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if !(r == utf8.RuneError && size <= 1) && xmlChar(r) {
			out = append(out, b[i:i+size]...)
		}
		i += size
	}
	return out
}

func xmlChar(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD || r >= 0x20 && r <= 0xD7FF ||
		r >= 0xE000 && r <= 0xFFFD || r >= 0x10000 && r <= 0x10FFFF
}

// Conversion from the dump to screen elements.

const (
	maxLabelRunes   = 200 // longer labels are cut with an ellipsis
	maxMergedLabels = 6   // a container takes at most this many child labels
	maxMergedRunes  = 160 // and at most this much text
	maxMergedArea   = 0.4 // share of the screen above which a container takes none
)

// convertTree turns a dump into screen elements in tree order. It keeps
// nodes that are interactive or carry text, merges the text and check state
// of plain children into the clickable container that owns them (the way
// TalkBack reads a row), and clips bounds to the w x h screen.
func convertTree(t *uiTree, w, h int) []screen.Element {
	c := &converter{w: w, h: h, absorbed: map[*uiNode]bool{}, merged: map[*uiNode]*merge{}}
	for _, r := range t.Roots {
		c.walk(r, nil, "")
	}
	return c.out
}

type converter struct {
	w, h     int
	absorbed map[*uiNode]bool
	merged   map[*uiNode]*merge
	out      []screen.Element
}

// merge is what a container took over from its plain descendants.
type merge struct {
	label   string
	role    string // from a check-state child: switch or checkbox
	checked bool
}

func (c *converter) walk(n *uiNode, path []string, parentLabel string) {
	if !c.absorbed[n] {
		if isActionable(n) && !n.Scrollable {
			c.mergeInto(n)
		}
		if e, ok := c.element(n, path, parentLabel); ok {
			c.out = append(c.out, e)
		}
	}
	label := parentLabel
	if isActionable(n) {
		label = c.labelOf(n)
	}
	path = append(path, signature(n))
	for _, ch := range n.Children {
		c.walk(ch, path, label)
	}
}

// mergeInto collects the labels and the check state of n's plain
// descendants, stopping at descendants that act on their own.
func (c *converter) mergeInto(n *uiNode) {
	var labels []*uiNode
	var carrier *uiNode
	var visit func(*uiNode)
	visit = func(x *uiNode) {
		for _, ch := range x.Children {
			if isActionable(ch) || ch.Scrollable || isInputNode(ch) {
				continue
			}
			if ch.Checkable && carrier == nil {
				carrier = ch
			}
			if ownLabel(ch) != "" {
				labels = append(labels, ch)
			}
			visit(ch)
		}
	}
	visit(n)
	m := &merge{}
	if carrier != nil && !n.Checkable {
		m.role = checkRole(carrier)
		m.checked = carrier.Checked
		c.absorbed[carrier] = true
	}
	own := ownLabel(n)
	switch {
	case own == "" && len(labels) > 0 && len(labels) <= maxMergedLabels && c.smallEnough(n):
		var parts []string
		seen := map[string]bool{}
		for _, l := range labels {
			s := ownLabel(l)
			if !seen[s] {
				seen[s] = true
				parts = append(parts, s)
			}
		}
		if joined := strings.Join(parts, " \u00b7 "); utf8.RuneCountInString(joined) <= maxMergedRunes {
			m.label = joined
			for _, l := range labels {
				c.absorbed[l] = true
			}
		}
	case own != "":
		// Drop children that only repeat the container's own label.
		for _, l := range labels {
			if ownLabel(l) == own {
				c.absorbed[l] = true
			}
		}
	}
	if m.label != "" || m.role != "" {
		c.merged[n] = m
	}
}

func (c *converter) smallEnough(n *uiNode) bool {
	if c.w <= 0 || c.h <= 0 {
		return true
	}
	return float64(n.Bounds.W)*float64(n.Bounds.H) <= maxMergedArea*float64(c.w)*float64(c.h)
}

// element builds the screen element for n, if n should be listed.
func (c *converter) element(n *uiNode, path []string, parentLabel string) (screen.Element, bool) {
	b := clip(n.Bounds, c.w, c.h)
	if b.Empty() {
		return screen.Element{}, false
	}
	interactive := isActionable(n) || n.Scrollable || n.Checkable || isInputNode(n)
	label := c.labelOf(n)
	if !interactive {
		if label == "" {
			return screen.Element{}, false
		}
		// A plain child that repeats its clickable parent adds nothing.
		if label == parentLabel {
			return screen.Element{}, false
		}
	}
	role := roleOf(n)
	checked := n.Checked
	if m := c.merged[n]; m != nil && m.role != "" {
		role = m.role
		checked = m.checked
	}
	e := screen.Element{
		Role:   role,
		Label:  label,
		Bounds: b,
		Source: screen.SourceTree,
		Path:   strings.Join(path, "/"),
		ID:     n.ResID,
	}
	if n.Focused {
		e.States = append(e.States, screen.StateFocused)
	}
	if checked {
		e.States = append(e.States, screen.StateChecked)
	}
	if n.Selected {
		e.States = append(e.States, screen.StateSelected)
	}
	if n.Scrollable {
		e.States = append(e.States, screen.StateScrollable)
	}
	if !n.Enabled {
		e.States = append(e.States, screen.StateDisabled)
	}
	if role == screen.RoleInput && n.Enabled {
		e.States = append(e.States, screen.StateEditable)
	}
	if n.Password {
		e.States = append(e.States, screen.StatePassword)
	}
	return e, true
}

// labelOf returns n's label: its own text or description, what it merged
// from its children, or the last part of its resource id.
func (c *converter) labelOf(n *uiNode) string {
	if l := ownLabel(n); l != "" {
		return l
	}
	if m := c.merged[n]; m != nil && m.label != "" {
		return m.label
	}
	if isActionable(n) || n.Scrollable || n.Checkable || isInputNode(n) {
		return idName(n.ResID)
	}
	return ""
}

// ownLabel is the node's own text, or its description. Inputs show their
// content, or their hint while empty; password fields never show content.
func ownLabel(n *uiNode) string {
	if isInputNode(n) {
		text := n.Text
		if n.Password || text == n.Hint {
			text = ""
		}
		for _, s := range []string{text, n.Hint, n.Desc} {
			if s = cleanLabel(s); s != "" {
				return s
			}
		}
		return ""
	}
	if s := cleanLabel(n.Text); s != "" {
		return s
	}
	return cleanLabel(n.Desc)
}

// cleanLabel collapses whitespace and cuts long text.
func cleanLabel(s string) string {
	if s == "" {
		return ""
	}
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
	if utf8.RuneCountInString(s) > maxLabelRunes {
		r := []rune(s)
		s = string(r[:maxLabelRunes-1]) + "\u2026"
	}
	return s
}

// idName is the part of a resource id after ":id/".
func idName(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// signature identifies a node in an element's path: its class and
// resource id, without sibling indices.
func signature(n *uiNode) string {
	s := simpleClass(n.Class)
	if id := idName(n.ResID); id != "" {
		s += "#" + id
	}
	return s
}

func simpleClass(cls string) string {
	if i := strings.LastIndexAny(cls, ".$"); i >= 0 {
		return cls[i+1:]
	}
	return cls
}

// isActionable reports whether a tap or long press on n does something.
func isActionable(n *uiNode) bool { return n.Clickable || n.LongClickable }

func isInputNode(n *uiNode) bool {
	s := simpleClass(n.Class)
	return strings.Contains(s, "EditText") || s == "AutoCompleteTextView" ||
		s == "MultiAutoCompleteTextView" || s == "SearchAutoComplete" || n.Password && n.Focusable
}

func checkRole(n *uiNode) string {
	s := simpleClass(n.Class)
	if strings.Contains(s, "Switch") || strings.Contains(s, "Toggle") {
		return screen.RoleSwitch
	}
	return screen.RoleCheckbox
}

// roleOf maps a node's class and flags onto the normalized roles.
func roleOf(n *uiNode) string {
	s := simpleClass(n.Class)
	switch {
	case isInputNode(n):
		return screen.RoleInput
	case strings.Contains(s, "Switch") || strings.Contains(s, "ToggleButton"):
		return screen.RoleSwitch
	case strings.Contains(s, "CheckBox") || strings.Contains(s, "RadioButton") || n.Checkable:
		return screen.RoleCheckbox
	case strings.Contains(s, "TabView") || s == "Tab" || strings.HasSuffix(s, "NavigationItemView") ||
		strings.HasSuffix(s, "NavigationBarItemView"):
		return screen.RoleTab
	case n.Scrollable || isListClass(s):
		return screen.RoleList
	case strings.Contains(s, "Button") || s == "Spinner" || s == "Chip" || s == "ActionMenuItemView" ||
		isActionable(n):
		return screen.RoleButton
	case strings.Contains(s, "Image"):
		return screen.RoleImage
	case n.Text != "":
		return screen.RoleText
	}
	return screen.RoleOther
}

func isListClass(s string) bool {
	switch s {
	case "RecyclerView", "ListView", "GridView", "ScrollView", "HorizontalScrollView",
		"NestedScrollView", "ViewPager", "ViewPager2", "ExpandableListView", "AbsListView":
		return true
	}
	return false
}

// clip intersects r with the w x h screen; w or h of 0 means no clipping.
func clip(r screen.Rect, w, h int) screen.Rect {
	if w <= 0 || h <= 0 {
		return r
	}
	x1, y1 := max(r.X, 0), max(r.Y, 0)
	x2, y2 := min(r.X+r.W, w), min(r.Y+r.H, h)
	if x2 <= x1 || y2 <= y1 {
		return screen.Rect{}
	}
	return screen.Rect{X: x1, Y: y1, W: x2 - x1, H: y2 - y1}
}
