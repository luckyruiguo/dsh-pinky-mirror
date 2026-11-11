package agents

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// This file changes one member of an object in a JSON settings file, which
// may have comments and trailing commas (VS Code's and Zed's do), and
// leaves every other byte of it as it was: the person's comments, order and
// formatting stay. Each change is checked by reading the file before and
// after it: everything but that member must read the same, or the change is
// refused.

// errNotJSON says a file is not JSON that Pinky can change safely.
type errNotJSON struct {
	line, col int // where, from 1; 0 when not known
	msg       string
}

func (e *errNotJSON) Error() string {
	if e.line == 0 {
		return "not valid JSON: " + e.msg
	}
	return fmt.Sprintf("not valid JSON at line %d, column %d: %s", e.line, e.col, e.msg)
}

// notJSON is an errNotJSON at off in src.
func notJSON(src []byte, off int, msg string) error {
	off = min(off, len(src))
	line := bytes.Count(src[:off], []byte("\n")) + 1
	col := off - (bytes.LastIndexByte(src[:off], '\n') + 1) + 1
	return &errNotJSON{line: line, col: col, msg: msg}
}

// jnode is a JSON value in the source, by its span.
type jnode struct {
	kind    byte // '{', '[', '"', or 'v' for numbers, true, false and null
	start   int  // its first byte
	end     int  // just after its last byte
	members []*jmember
}

// jmember is a member of an object.
type jmember struct {
	key      string
	keyStart int
	value    *jnode
	comma    int // the comma after the value, or -1
}

// jdoc is a parsed file: its value, and what to blank out to read it as
// plain JSON (comments and trailing commas).
type jdoc struct {
	src    []byte
	bom    int // 3 when the file starts with a UTF-8 byte order mark
	root   *jnode
	blanks [][2]int
}

type jparser struct {
	src    []byte
	pos    int
	blanks [][2]int
}

func parseJSONC(src []byte) (*jdoc, error) {
	p := &jparser{src: src}
	bom := 0
	if bytes.HasPrefix(src, []byte("\xef\xbb\xbf")) {
		bom = 3
		p.pos = 3
		p.blanks = append(p.blanks, [2]int{0, 3})
	}
	if err := p.skip(); err != nil {
		return nil, err
	}
	d := &jdoc{src: src, bom: bom}
	if p.pos == len(src) {
		d.blanks = p.blanks
		return d, nil // empty: no value
	}
	root, err := p.value(0)
	if err != nil {
		return nil, err
	}
	if err := p.skip(); err != nil {
		return nil, err
	}
	if p.pos != len(src) {
		return nil, p.fail("text after the end of the value")
	}
	d.root, d.blanks = root, p.blanks
	return d, nil
}

func (p *jparser) fail(msg string) error { return notJSON(p.src, p.pos, msg) }

// skip passes whitespace and comments.
func (p *jparser) skip() error {
	for p.pos < len(p.src) {
		switch c := p.src[p.pos]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			p.pos++
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/':
			start := p.pos
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
			p.blanks = append(p.blanks, [2]int{start, p.pos})
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '*':
			start := p.pos
			end := bytes.Index(p.src[p.pos+2:], []byte("*/"))
			if end < 0 {
				return p.fail("a comment that does not end")
			}
			p.pos += 2 + end + 2
			p.blanks = append(p.blanks, [2]int{start, p.pos})
		default:
			return nil
		}
	}
	return nil
}

func (p *jparser) value(depth int) (*jnode, error) {
	if depth > 200 {
		return nil, p.fail("nested too deeply")
	}
	if p.pos >= len(p.src) {
		return nil, p.fail("a value is missing")
	}
	switch c := p.src[p.pos]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"':
		start := p.pos
		if err := p.str(); err != nil {
			return nil, err
		}
		return &jnode{kind: '"', start: start, end: p.pos}, nil
	case c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z'):
		start := p.pos
		for p.pos < len(p.src) && strings.IndexByte("+-.0123456789eEabcdefghijklmnopqrstuvwxyz", p.src[p.pos]) >= 0 {
			p.pos++
		}
		return &jnode{kind: 'v', start: start, end: p.pos}, nil
	}
	return nil, p.fail(fmt.Sprintf("unexpected %q", p.src[p.pos]))
}

func (p *jparser) str() error {
	p.pos++ // the opening quote
	for p.pos < len(p.src) {
		switch p.src[p.pos] {
		case '\\':
			p.pos += 2
		case '"':
			p.pos++
			return nil
		case '\n':
			return p.fail("a line break in a string")
		default:
			p.pos++
		}
	}
	return p.fail("a string that does not end")
}

func (p *jparser) object(depth int) (*jnode, error) {
	n := &jnode{kind: '{', start: p.pos}
	p.pos++
	for {
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.src) {
			return nil, p.fail("an object that does not end")
		}
		if p.src[p.pos] == '}' {
			p.pos++
			n.end = p.pos
			return n, nil
		}
		if p.src[p.pos] != '"' {
			return nil, p.fail("expected a member's name in quotes")
		}
		m := &jmember{keyStart: p.pos, comma: -1}
		if err := p.str(); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(p.src[m.keyStart:p.pos], &m.key); err != nil {
			return nil, notJSON(p.src, m.keyStart, "a name that is not a valid string")
		}
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return nil, p.fail("expected a colon")
		}
		p.pos++
		if err := p.skip(); err != nil {
			return nil, err
		}
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		m.value = v
		n.members = append(n.members, m)
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.src) {
			return nil, p.fail("an object that does not end")
		}
		switch p.src[p.pos] {
		case ',':
			m.comma = p.pos
			p.pos++
			if err := p.skip(); err != nil {
				return nil, err
			}
			if p.pos < len(p.src) && p.src[p.pos] == '}' {
				p.blanks = append(p.blanks, [2]int{m.comma, m.comma + 1}) // a trailing comma
			}
		case '}':
		default:
			return nil, p.fail("expected a comma or the end of the object")
		}
	}
}

func (p *jparser) array(depth int) (*jnode, error) {
	n := &jnode{kind: '[', start: p.pos}
	p.pos++
	for {
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.src) {
			return nil, p.fail("a list that does not end")
		}
		if p.src[p.pos] == ']' {
			p.pos++
			n.end = p.pos
			return n, nil
		}
		if _, err := p.value(depth + 1); err != nil {
			return nil, err
		}
		if err := p.skip(); err != nil {
			return nil, err
		}
		if p.pos >= len(p.src) {
			return nil, p.fail("a list that does not end")
		}
		switch p.src[p.pos] {
		case ',':
			comma := p.pos
			p.pos++
			if err := p.skip(); err != nil {
				return nil, err
			}
			if p.pos < len(p.src) && p.src[p.pos] == ']' {
				p.blanks = append(p.blanks, [2]int{comma, comma + 1})
			}
		case ']':
		default:
			return nil, p.fail("expected a comma or the end of the list")
		}
	}
}

// plain is the file as plain JSON: comments, trailing commas and the byte
// order mark blanked out.
func (d *jdoc) plain() []byte {
	out := append([]byte(nil), d.src...)
	for _, b := range d.blanks {
		for i := b[0]; i < b[1]; i++ {
			if out[i] != '\n' && out[i] != '\r' {
				out[i] = ' '
			}
		}
	}
	return out
}

// decoded reads the file's value, numbers as json.Number; nil for an
// empty file.
func (d *jdoc) decoded() (any, error) {
	if d.root == nil {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(d.plain()))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, &errNotJSON{msg: err.Error()}
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, &errNotJSON{msg: "text after the end of the value"}
	}
	return v, nil
}

// readJSONC reads a settings file's value: nil for an empty one.
func readJSONC(src []byte) (any, error) {
	d, err := parseJSONC(src)
	if err != nil {
		return nil, err
	}
	return d.decoded()
}

// lookup follows keys through objects; ok is false where one is missing
// or a value on the way is not an object.
func lookup(v any, keys ...string) (any, bool) {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[k]; !ok {
			return nil, false
		}
	}
	return v, true
}

// member finds the one member named key; it is an error to have it twice,
// since the person could not tell which one counts.
func (n *jnode) member(key string) (*jmember, int, error) {
	idx := -1
	for i, m := range n.members {
		if m.key == key {
			if idx >= 0 {
				return nil, -1, fmt.Errorf("%q is there twice", key)
			}
			idx = i
		}
	}
	if idx < 0 {
		return nil, -1, nil
	}
	return n.members[idx], idx, nil
}

// field is a member of an object that keeps its order when written.
type field struct {
	Key   string
	Value any
}

// object is a JSON object written in the order of its fields.
type object []field

func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := marshal(f.Key)
		if err != nil {
			return nil, err
		}
		v, err := marshal(f.Value)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshal is json.Marshal without escaping <, > and &, which paths and
// commands may have.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// layout is how a file is written: its indentation unit and line ends.
type layout struct {
	unit, nl string
}

func (d *jdoc) layout() layout {
	l := layout{unit: "  ", nl: "\n"}
	if bytes.Contains(d.src, []byte("\r\n")) {
		l.nl = "\r\n"
	}
	// The unit: the indentation of the root object's first member, when it
	// starts a line.
	if d.root != nil && d.root.kind == '{' && len(d.root.members) > 0 {
		if ind, ok := d.lineIndent(d.root.members[0].keyStart); ok && ind != "" {
			l.unit = ind
		}
	}
	return l
}

// lineStart is the start of the line that holds off.
func (d *jdoc) lineStart(off int) int {
	return bytes.LastIndexByte(d.src[:off], '\n') + 1
}

// lineIndent is the whitespace before off on its line; ok is false when
// something else comes before it there.
func (d *jdoc) lineIndent(off int) (string, bool) {
	ls := d.lineStart(off)
	ind := d.src[ls:off]
	if len(bytes.TrimLeft(ind, " \t")) != 0 {
		return "", false
	}
	if ls < d.bom {
		ind = ind[d.bom-ls:]
	}
	return string(ind), true
}

// indentOf is the indentation of the line that holds off.
func (d *jdoc) indentOf(off int) string {
	ls := d.lineStart(off)
	if ls < d.bom {
		ls = d.bom
	}
	end := ls
	for end < len(d.src) && (d.src[end] == ' ' || d.src[end] == '\t') {
		end++
	}
	return string(d.src[ls:end])
}

// multiline reports whether a value spans lines.
func (d *jdoc) multiline(n *jnode) bool {
	return bytes.IndexByte(d.src[n.start:n.end], '\n') >= 0
}

// text writes v as the file would: indented under prefix, or on one line.
func (l layout) text(v any, prefix string, oneLine bool) (string, error) {
	if oneLine {
		b, err := marshal(v)
		if err != nil {
			return "", err
		}
		return spaced(string(b)), nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent(prefix, l.unit)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	s := strings.TrimRight(b.String(), "\n")
	if l.nl != "\n" {
		s = strings.ReplaceAll(s, "\n", l.nl)
	}
	return s, nil
}

// spaced puts a space after the colons and commas of compact JSON, outside
// strings, as people write it on one line.
func spaced(s string) string {
	var b strings.Builder
	in, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		b.WriteByte(c)
		switch {
		case esc:
			esc = false
		case in && c == '\\':
			esc = true
		case c == '"':
			in = !in
		case !in && (c == ':' || c == ','):
			b.WriteByte(' ')
		}
	}
	return b.String()
}

type edit struct {
	start, end int
	text       string
}

func apply(src []byte, edits []edit) []byte {
	// Last first, so the earlier offsets hold.
	for i := 1; i < len(edits); i++ {
		for j := i; j > 0 && edits[j].start > edits[j-1].start; j-- {
			edits[j], edits[j-1] = edits[j-1], edits[j]
		}
	}
	out := append([]byte(nil), src...)
	for _, e := range edits {
		out = append(out[:e.start], append([]byte(e.text), out[e.end:]...)...)
	}
	return out
}

// setJSONMember returns src with the member key of the object at path set
// to value, creating the objects on the path that are missing. An empty
// file becomes an object.
func setJSONMember(src []byte, path []string, key string, value any) ([]byte, error) {
	d, err := parseJSONC(src)
	if err != nil {
		return nil, err
	}
	before, err := d.decoded()
	if err != nil {
		return nil, err
	}
	if before == nil {
		before = map[string]any{}
	}
	l := d.layout()
	var out []byte
	if d.root == nil {
		// An empty file, or one of comments alone: the object goes after
		// them.
		var v any = object{{key, value}}
		for i := len(path) - 1; i >= 0; i-- {
			v = object{{path[i], v}}
		}
		t, err := l.text(v, "", false)
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(src[d.bom:])) == 0 {
			out = append(append(append([]byte(nil), src[:d.bom]...), t...), l.nl...)
		} else {
			out = append([]byte(nil), src...)
			if !bytes.HasSuffix(out, []byte("\n")) {
				out = append(out, l.nl...)
			}
			out = append(append(out, t...), l.nl...)
		}
	} else {
		if d.root.kind != '{' {
			return nil, errors.New("the file is not a JSON object")
		}
		obj := d.root
		rest := append([]string(nil), path...)
		for len(rest) > 0 {
			m, _, err := obj.member(rest[0])
			if err != nil {
				return nil, err
			}
			if m == nil {
				break
			}
			if m.value.kind != '{' {
				return nil, fmt.Errorf("%q is not an object", rest[0])
			}
			obj, rest = m.value, rest[1:]
		}
		var edits []edit
		if len(rest) == 0 {
			m, _, err := obj.member(key)
			if err != nil {
				return nil, err
			}
			if m != nil {
				ind := d.indentOf(m.keyStart)
				t, err := l.text(value, ind, !d.multiline(obj))
				if err != nil {
					return nil, err
				}
				edits = []edit{{m.value.start, m.value.end, t}}
			} else if edits, err = d.insert(obj, key, value, l); err != nil {
				return nil, err
			}
		} else {
			var v any = object{{key, value}}
			for i := len(rest) - 1; i >= 1; i-- {
				v = object{{rest[i], v}}
			}
			if edits, err = d.insert(obj, rest[0], v, l); err != nil {
				return nil, err
			}
		}
		out = apply(src, edits)
	}
	// Check the change: the member reads as value, and the rest as before.
	after, err := readJSONC(out)
	if err != nil {
		return nil, fmt.Errorf("the change would break the file: %w", err)
	}
	want, err := normalize(value)
	if err != nil {
		return nil, err
	}
	if got, ok := lookup(after, append(append([]string(nil), path...), key)...); !ok || !reflect.DeepEqual(got, want) {
		return nil, errors.New("the change did not read back")
	}
	if !reflect.DeepEqual(without(after, path, key, before), without(before, path, key, before)) {
		return nil, errors.New("the change would alter more than Pinky's entry")
	}
	return out, nil
}

// insert adds a member to an object, after its last one, in the object's
// own style: on a line of its own in an object that spans lines.
func (d *jdoc) insert(obj *jnode, key string, value any, l layout) ([]edit, error) {
	k, err := marshal(key)
	if err != nil {
		return nil, err
	}
	closing := obj.end - 1
	if len(obj.members) > 0 {
		last := obj.members[len(obj.members)-1]
		closeInd, closeAlone := d.lineIndent(closing)
		if d.multiline(obj) && closeAlone {
			ind, ok := d.lineIndent(last.keyStart)
			if !ok {
				ind = closeInd + l.unit
			}
			t, err := l.text(value, ind, false)
			if err != nil {
				return nil, err
			}
			edits := []edit{{d.lineStart(closing), d.lineStart(closing), ind + string(k) + ": " + t + l.nl}}
			if last.comma < 0 {
				edits = append(edits, edit{last.value.end, last.value.end, ","})
			}
			return edits, nil
		}
		t, err := l.text(value, "", true)
		if err != nil {
			return nil, err
		}
		if last.comma < 0 {
			return []edit{{last.value.end, last.value.end, ", " + string(k) + ": " + t}}, nil
		}
		return []edit{{last.comma + 1, last.comma + 1, " " + string(k) + ": " + t}}, nil
	}
	// No members: in a file on one line, the member goes on it too;
	// otherwise on a line of its own, and the closing brace on the next.
	if !d.multiline(d.root) {
		t, err := l.text(value, "", true)
		if err != nil {
			return nil, err
		}
		return []edit{{obj.start + 1, closing, string(k) + ": " + t}}, nil
	}
	closeInd := d.indentOf(obj.start)
	ind := closeInd + l.unit
	t, err := l.text(value, ind, false)
	if err != nil {
		return nil, err
	}
	at := closing
	for at > obj.start+1 && strings.IndexByte(" \t\r\n", d.src[at-1]) >= 0 {
		at--
	}
	return []edit{{at, closing, l.nl + ind + string(k) + ": " + t + l.nl + closeInd}}, nil
}

// deleteJSONMember returns src without the member key of the object at
// path; changed is false when it has none.
func deleteJSONMember(src []byte, path []string, key string) (out []byte, changed bool, err error) {
	d, err := parseJSONC(src)
	if err != nil {
		return nil, false, err
	}
	before, err := d.decoded()
	if err != nil {
		return nil, false, err
	}
	if d.root == nil || d.root.kind != '{' {
		return src, false, nil
	}
	obj := d.root
	for _, p := range path {
		m, _, err := obj.member(p)
		if err != nil {
			return nil, false, err
		}
		if m == nil || m.value.kind != '{' {
			return src, false, nil
		}
		obj = m.value
	}
	m, idx, err := obj.member(key)
	if err != nil || m == nil {
		return src, false, err
	}
	start, end := m.keyStart, m.value.end
	if m.comma >= 0 {
		end = m.comma + 1
	}
	var edits []edit
	if _, alone := d.lineIndent(m.keyStart); alone && d.multiline(obj) {
		// The member's lines go, with the line break after them when
		// nothing else follows on its last line.
		start = d.lineStart(m.keyStart)
		e := end
		for e < len(src) && (src[e] == ' ' || src[e] == '\t') {
			e++
		}
		switch {
		case e < len(src) && src[e] == '\n':
			end = e + 1
		case e+1 < len(src) && src[e] == '\r' && src[e+1] == '\n':
			end = e + 2
		}
	} else {
		for end < len(src) && (src[end] == ' ' || src[end] == '\t') && m.comma >= 0 {
			end++
		}
	}
	edits = append(edits, edit{start, end, ""})
	// The last member without a comma: the one before it loses its comma,
	// which would now trail.
	if idx == len(obj.members)-1 && m.comma < 0 && idx > 0 {
		if prev := obj.members[idx-1]; prev.comma >= 0 {
			edits = append(edits, edit{prev.comma, prev.comma + 1, ""})
		}
	}
	out = apply(src, edits)
	after, err := readJSONC(out)
	if err != nil {
		return nil, false, fmt.Errorf("the change would break the file: %w", err)
	}
	if _, ok := lookup(after, append(append([]string(nil), path...), key)...); ok {
		return nil, false, errors.New("the entry is still there")
	}
	if !reflect.DeepEqual(after, without(before, path, key, before)) {
		return nil, false, errors.New("the change would alter more than Pinky's entry")
	}
	return out, true, nil
}

// normalize is v as it reads back from JSON.
func normalize(v any) (any, error) {
	b, err := marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	err = dec.Decode(&out)
	return out, err
}

// without returns a copy of doc without the member key of the object at
// path, and without the objects on the path that orig lacked (the ones a
// change made for it).
func without(doc any, path []string, key string, orig any) any {
	if doc == nil {
		return nil
	}
	m, ok := doc.(map[string]any)
	if !ok {
		return doc
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	if len(path) == 0 {
		delete(out, key)
		return out
	}
	child, ok := out[path[0]]
	if !ok {
		return out
	}
	origChild, had := lookup(orig, path[0])
	if !had {
		// A change made this object for the member alone.
		delete(out, path[0])
		return out
	}
	out[path[0]] = without(child, path[1:], key, origChild)
	return out
}

// pruneEmpty returns a copy of doc without the empty objects on path,
// innermost first.
func pruneEmpty(doc any, path []string) any {
	m, ok := doc.(map[string]any)
	if !ok || len(path) == 0 {
		return doc
	}
	child, ok := m[path[0]]
	if !ok {
		return doc
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	child = pruneEmpty(child, path[1:])
	if cm, ok := child.(map[string]any); ok && len(cm) == 0 {
		delete(out, path[0])
	} else {
		out[path[0]] = child
	}
	return out
}
