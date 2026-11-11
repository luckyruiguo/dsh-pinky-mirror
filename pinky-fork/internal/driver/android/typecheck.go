package android

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DIYgod/pinky/internal/driver/android/companion"
)

// Notes for the agent about typed text Pinky could not check
// (driver.InputNoter). They name no text: the field may hold a one-time
// code that the policy hides.
const (
	noteHidden = "The field hides its text, so Pinky could not check what was typed."
	noteUnread = "Pinky could not read the field back to check the text; look at the screen to be sure."
)

// How long the field may take to show text typed as keys or pasted, and
// how often it is read meanwhile.
const (
	fieldLandWait = 1500 * time.Millisecond
	fieldPoll     = 150 * time.Millisecond
)

// setInputNote keeps a note about the current Type for TakeInputNote.
func (d *Device) setInputNote(note string) {
	d.stateMu.Lock()
	d.inputNote = note
	d.stateMu.Unlock()
}

// TakeInputNote implements driver.InputNoter: what the agent should know
// about the last Type, such as a field Pinky could not read back.
func (d *Device) TakeInputNote() string {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	n := d.inputNote
	d.inputNote = ""
	return n
}

// TakeInputNote implements driver.InputNoter for the background display.
func (b *Background) TakeInputNote() string { return b.d.TakeInputNote() }

// typedOtherwise is the error for a field that took the text but reads
// otherwise afterwards. how says how it was typed, "" for the companion.
func typedOtherwise(v, text string, appended, submit bool, how string) error {
	got, typed := utf8.RuneCountInString(v), utf8.RuneCountInString(text)
	counts := fmt.Sprintf("%d characters where %d were typed", got, typed)
	if appended {
		counts = fmt.Sprintf("%d characters after %d were added", got, typed)
	}
	if submit {
		counts += "; it was not submitted"
	}
	if how != "" {
		how = " " + how
	}
	return fmt.Errorf("type: after typing%s, the field reads otherwise than the text (%s): the app may format, cut or change what it takes; read the field before going on", how, counts)
}

// fieldCheck reads the focused field of a display through the companion
// before and after text is typed as keys or pasted, which report nothing
// back, to tell whether the text landed.
type fieldCheck struct {
	d       *Device
	c       *companion.Client
	display int
	prior   *string // the field's text before typing; nil when unreadable
}

// checkField starts a fieldCheck; nil (which checks nothing) without a
// companion that can read the display.
func (d *Device) checkField(ctx context.Context, c *companion.Client, display int) *fieldCheck {
	if c == nil || (display != 0 && !d.comp.displays()) {
		return nil
	}
	f := &fieldCheck{d: d, c: c, display: display}
	if n := f.read(ctx); n != nil && !n.Password {
		v := n.Text
		f.prior = &v
	}
	return f
}

// read returns the focused editable node of the display, or nil.
func (f *fieldCheck) read(ctx context.Context) *companion.Node {
	t, err := f.c.Dump(ctx, companion.DumpOptions{Display: f.display})
	if err != nil {
		return nil
	}
	return focusedField(t)
}

// focusedField is the focused editable node of a tree, top-most window
// first; the keyboard's windows are skipped.
func focusedField(t *companion.Tree) *companion.Node {
	for _, w := range t.Windows {
		if w.Type == companion.WindowInputMethod || w.Root == nil {
			continue
		}
		if n := findNode(w.Root, func(n *companion.Node) bool { return n.Focused && n.Editable }); n != nil {
			return n
		}
	}
	return nil
}

func findNode(n *companion.Node, match func(*companion.Node) bool) *companion.Node {
	if match(n) {
		return n
	}
	for _, c := range n.Children {
		if m := findNode(c, match); m != nil {
			return m
		}
	}
	return nil
}

// typedAs reports whether a field that reads v holds text as typed: the
// whole of it, or, appended, the text within no more than the old text
// and the new (keys go in at the cursor, not always at the end).
func typedAs(v, text string, appended bool, prior *string) bool {
	if !appended {
		return v == text
	}
	if !strings.Contains(v, text) {
		return false
	}
	return prior == nil || utf8.RuneCountInString(v) <= utf8.RuneCountInString(*prior)+utf8.RuneCountInString(text)
}

// after reads the field once text was typed as keys (keys) or pasted, and
// returns an error when it did not land or reads otherwise. The field may
// be unreadable: then the agent gets a note. refused is why the companion
// could not write the text, composing whether the keyboard composes what
// it is given.
func (f *fieldCheck) after(ctx context.Context, text string, appended, submit, keys bool, refused error, composing bool) error {
	if f == nil {
		return nil
	}
	var n *companion.Node
	for deadline := time.Now().Add(fieldLandWait); ; {
		n = f.read(ctx)
		if n != nil && (n.Password || typedAs(n.Text, text, appended, f.prior)) {
			break
		}
		if !time.Now().Before(deadline) {
			break
		}
		if err := sleepCtx(ctx, fieldPoll); err != nil {
			return err
		}
	}
	switch {
	case n == nil:
		f.d.setInputNote(noteUnread)
		return nil
	case n.Password:
		f.d.setInputNote(noteHidden)
		return nil
	case typedAs(n.Text, text, appended, f.prior):
		return nil
	}
	how := "by pasting"
	if keys {
		how = "as keys"
	}
	if (f.prior != nil && n.Text == *f.prior) || (f.prior == nil && n.Text == "") {
		return notLanded(how, refused, composing)
	}
	return typedOtherwise(n.Text, text, appended, submit, how)
}

// notLanded is the error for text that did not reach the field.
func notLanded(how string, refused error, composing bool) error {
	var b strings.Builder
	b.WriteString("type: the text did not reach the field, which reads as it did before typing. ")
	var ce *companion.Error
	if errors.As(refused, &ce) {
		fmt.Fprintf(&b, "Pinky's companion could not write it (%s), and typing it %s did not work either", ce.Message, how)
	} else {
		fmt.Fprintf(&b, "Typing it %s did not work", how)
	}
	if composing {
		b.WriteString(": the keyboard composes what it is given")
	}
	b.WriteString(". Tap the field so it has focus and type again; if that fails too, this field does not take text from Pinky.")
	return errors.New(b.String())
}
