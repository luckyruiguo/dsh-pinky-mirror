//go:build device

package ios

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/danielpaulus/go-ios/ios/nskeyedarchiver"
)

// TestAXProbeDump dumps what the inspector sends for the first elements of
// the app in front (no actions). Run alone with PINKY_AXPROBE=dump and
// PINKY_AXAPP=<bundle id>.
func TestAXProbeDump(t *testing.T) {
	if os.Getenv("PINKY_AXPROBE") != "dump" {
		t.Skip("PINKY_AXPROBE=dump not set")
	}
	id := os.Getenv("PINKY_DEVICE")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	l, err := dialLink(ctx, id, false)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var names []string
	for name := range l.rsd.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	out := "/tmp/pinky-axprobe"
	os.MkdirAll(out, 0o700)
	os.WriteFile(filepath.Join(out, "rsd.txt"), []byte(joinLines(names)), 0o600)

	d := &Device{l: l}
	if app := os.Getenv("PINKY_AXAPP"); app != "" {
		if err := d.Launch(ctx, app); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Second)
	}
	ax, err := dialAX(ctx, l.base)
	if err != nil {
		t.Fatal(err)
	}
	defer ax.Close()

	ax.drain(ctx)
	ax.ch.MethodCallAsync("deviceInspectorFocusOnElement:", nskeyedarchiver.NewNSNull())
	msg, err := ax.recvFocus(ctx, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	dump(t, filepath.Join(out, "focus0.json"), unarchiveArg(msg))
	ax.ch.MethodCallAsync("deviceInspectorMoveWithOptions:", moveOptions(axFirst))
	for i := 1; i <= 6; i++ {
		msg, err := ax.recvFocus(ctx, 3*time.Second)
		if err != nil {
			t.Logf("move %d: %v", i, err)
			break
		}
		v := unarchiveArg(msg)
		n, _ := parseFocus(msg)
		t.Logf("element %d: caption %q ident %q handle %x", i, n.caption, n.ident, n.handle)
		dump(t, filepath.Join(out, "focus"+string(rune('0'+i))+".json"), v)
		ax.ch.MethodCallAsync("deviceInspectorMoveWithOptions:", moveOptions(axNext))
	}
	ax.ch.MethodCallAsync("deviceInspectorPreviewOnElement:", nskeyedarchiver.NewNSNull())
	nodes, complete, err := ax.walk(ctx, observeLimits)
	t.Logf("walk: %d nodes, complete %v, err %v", len(nodes), complete, err)
	for i, n := range nodes {
		t.Logf("%2d %q ident %q pid %d", i, n.caption, n.ident, handlePID(n.handle))
	}
}

func joinLines(s []string) string {
	b := ""
	for _, x := range s {
		b += x + "\n"
	}
	return b
}

// dump writes v as JSON, with byte slices as base64.
func dump(t *testing.T, path string, v any) {
	b, err := json.MarshalIndent(jsonable(v), "", " ")
	if err != nil {
		t.Logf("dump %s: %v", path, err)
		return
	}
	os.WriteFile(path, b, 0o600)
}

func jsonable(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := map[string]any{}
		for k, e := range x {
			m[k] = jsonable(e)
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jsonable(e)
		}
		return out
	case []byte:
		return "b64:" + base64.StdEncoding.EncodeToString(x)
	default:
		return x
	}
}

// TestAXProbeActivate tries the inspector's actions, the only input the
// path below iOS 27 has without the helper: Activate on a Calculator key
// and its Clear key, then Activate on Settings' search field and setting
// its value. It never presses anything but those: no switches. Run alone
// with PINKY_AXPROBE=act; it leaves the phone on its home screen.
func TestAXProbeActivate(t *testing.T) {
	if os.Getenv("PINKY_AXPROBE") != "act" {
		t.Skip("PINKY_AXPROBE=act not set")
	}
	id := os.Getenv("PINKY_DEVICE")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	l, err := dialLink(ctx, id, false)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	d := &Device{l: l}
	ax, err := dialAX(ctx, l.base)
	if err != nil {
		t.Fatal(err)
	}
	defer ax.Close()
	walk := func(what string) []axNode {
		t0 := time.Now()
		nodes, complete, err := ax.walk(ctx, observeLimits)
		t.Logf("%s: walk %v, %d nodes, complete %v, err %v", what, time.Since(t0).Round(time.Millisecond), len(nodes), complete, err)
		for i, n := range nodes {
			t.Logf("  %2d %q ident %q", i, n.caption, n.ident)
		}
		return nodes
	}
	find := func(nodes []axNode, ok func(cp captionParts, n axNode) bool) (axNode, bool) {
		for _, n := range nodes {
			if ok(parseCaption(n.caption), n) {
				return n, true
			}
		}
		return axNode{}, false
	}
	// Calculator's keys may read as just their label; nothing pressed here
	// is ever a switch.
	isButton := func(cp captionParts) bool { return !has(cp.traits, "toggle") && !has(cp.traits, "switch") }

	// Calculator: 7, then Clear.
	if err := d.Launch(ctx, "com.apple.calculator"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	nodes := walk("Calculator")
	seven, ok := find(nodes, func(cp captionParts, n axNode) bool { return cp.first == "7" && isButton(cp) })
	if !ok {
		t.Fatal("no 7 key")
	}
	t0 := time.Now()
	err = ax.activate(ctx, seven.handle)
	t.Logf("activate 7: %v in %v", err, time.Since(t0).Round(time.Millisecond))
	time.Sleep(time.Second)
	nodes = walk("Calculator after 7")
	if clr, ok := find(nodes, func(cp captionParts, n axNode) bool {
		f := strings.ToLower(cp.first)
		return (f == "clear" || f == "all clear" || f == "delete") && isButton(cp)
	}); ok {
		err = ax.activate(ctx, clr.handle)
		t.Logf("activate %q: %v", clr.caption, err)
		time.Sleep(time.Second)
		walk("Calculator after clear")
	}

	// Settings: the search field, then its value.
	if err := d.Launch(ctx, "com.apple.Preferences"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	nodes = walk("Settings")
	field, ok := find(nodes, func(cp captionParts, n axNode) bool { return cp.first == "Search" && has(cp.traits, "search field") })
	if !ok {
		t.Log("no search field; skipping")
		return
	}
	err = ax.activate(ctx, field.handle)
	t.Logf("activate the search field: %v", err)
	time.Sleep(1500 * time.Millisecond)
	for _, attr := range []string{"Value", "AXValue"} {
		c, cc := context.WithTimeout(ctx, 3*time.Second)
		_, err := ax.ch.MethodCallWithContext(c, "deviceElement:setValue:attribute:", elementArg(field.handle), "clock", attributeArg(attr))
		cc()
		t.Logf("setValue %q: %v", attr, err)
		time.Sleep(time.Second)
		nodes = walk("Settings after setValue " + attr)
	}
	if cancelBtn, ok := find(nodes, func(cp captionParts, n axNode) bool {
		return (cp.first == "Cancel" || cp.first == "close") && isButton(cp)
	}); ok {
		t.Logf("activate %q: %v", cancelBtn.caption, ax.activate(ctx, cancelBtn.handle))
		time.Sleep(time.Second)
	}
	// Home through the iOS 27 path, as every device test ends.
	full, err := Open(ctx, driver.Info{ID: id, Platform: driver.IOS})
	if err == nil {
		full.Key(ctx, driver.KeyHome)
		time.Sleep(time.Second)
		full.Close()
	}
}

// TestAXProbeMirror reads, without any input, what the phone's
// accessibility services see while the person mirrors it to a Mac with
// iPhone Mirroring (the phone locked): the lock and backlight state, the
// inspector's walk with each element's process, and remoteAXService's
// frontmost app. Run alone with PINKY_AXPROBE=mirror once the person has
// locked the phone, started iPhone Mirroring and opened Settings in it.
func TestAXProbeMirror(t *testing.T) {
	if os.Getenv("PINKY_AXPROBE") != "mirror" {
		t.Skip("PINKY_AXPROBE=mirror not set")
	}
	id := os.Getenv("PINKY_DEVICE")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	l, err := dialLink(ctx, id, false)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	d := &Device{l: l}
	locked, lerr := d.Locked(ctx)
	t.Logf("lockdown PasswordProtected: %v (%v)", locked, lerr)
	if svc, err := dialCoreDevice(l.entry, deviceInfoService); err == nil {
		si, err := readScreenInfo(ctx, svc)
		t.Logf("display: %dx%d, backlight %q, orientation %q (%v)", si.Width, si.Height, si.Backlight, si.Orientation, err)
		svc.Close()
	}
	procs, err := d.processPaths(ctx)
	if err != nil {
		t.Logf("process list: %v", err)
	}
	name := func(pid uint64) string {
		p := procs[pid]
		if i := strings.LastIndex(p, "/"); i >= 0 {
			p = p[i+1:]
		}
		return p
	}
	// remoteAXService's idea of the frontmost app.
	if rax, err := d.raxReady(ctx); err == nil {
		app, err := rax.frontmostApp(ctx)
		if err == nil {
			t.Logf("remoteAXService frontmost app: pid %d (%s)", app.PID, name(uint64(app.PID)))
		} else {
			t.Logf("remoteAXService frontmost app: %v", err)
		}
		rax.Close()
	} else {
		t.Logf("remoteAXService: %v", err)
	}
	// The inspector's walk.
	ax, err := dialAX(ctx, l.base)
	if err != nil {
		t.Fatal(err)
	}
	defer ax.Close()
	t0 := time.Now()
	nodes, complete, err := ax.walk(ctx, observeLimits)
	t.Logf("inspector walk: %v, %d elements, complete %v, err %v", time.Since(t0).Round(time.Millisecond), len(nodes), complete, err)
	byProc := map[string]int{}
	for i, n := range nodes {
		p := name(handlePID(n.handle))
		byProc[p]++
		t.Logf("  %2d %-24s %q ident %q", i, p, n.caption, n.ident)
	}
	fgName, fgID := ax.foreground()
	t.Logf("elements by process: %v; app-state foreground: %s (%s)", byProc, fgName, fgID)
}

// TestAXProbeFocusActivate is TestAXProbeActivate with the inspector's
// focus moved onto the element first, since the daemon answers attribute
// queries only for the focused element. PINKY_AXPROBE=focus.
func TestAXProbeFocusActivate(t *testing.T) {
	if os.Getenv("PINKY_AXPROBE") != "focus" {
		t.Skip("PINKY_AXPROBE=focus not set")
	}
	id := os.Getenv("PINKY_DEVICE")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	l, err := dialLink(ctx, id, false)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	d := &Device{l: l}
	ax, err := dialAX(ctx, l.base)
	if err != nil {
		t.Fatal(err)
	}
	defer ax.Close()
	display := func() string {
		nodes, _, _ := ax.walk(ctx, observeLimits)
		for _, n := range nodes {
			if strings.HasPrefix(n.ident, "StandardInputView") {
				return n.ident
			}
		}
		return "?"
	}
	focusThenActivate := func(label string, n axNode) {
		ax.drain(ctx)
		ax.ch.MethodCallAsync("deviceInspectorFocusOnElement:", elementArg(n.handle))
		msg, err := ax.recvFocus(ctx, 2*time.Second)
		got := ""
		if err == nil {
			f, _ := parseFocus(msg)
			got = f.caption
		}
		t0 := time.Now()
		aerr := ax.activate(ctx, n.handle)
		t.Logf("%s: focus -> %q (%v), activate %v in %v", label, got, err, aerr, time.Since(t0).Round(time.Millisecond))
		ax.ch.MethodCallAsync("deviceInspectorPreviewOnElement:", nskeyedarchiver.NewNSNull())
		time.Sleep(time.Second)
	}
	if err := d.Launch(ctx, "com.apple.calculator"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	nodes, _, _ := ax.walk(ctx, observeLimits)
	t.Logf("display before: %s", display())
	for _, n := range nodes {
		if parseCaption(n.caption).first == "7" {
			focusThenActivate("7", n)
			break
		}
	}
	t.Logf("display after 7: %s", display())
	nodes, _, _ = ax.walk(ctx, observeLimits)
	for _, n := range nodes {
		if n.ident == "AllClear" || n.ident == "Clear" {
			focusThenActivate(n.ident, n)
			break
		}
	}
	t.Logf("display after clear: %s", display())
	full, err := Open(ctx, driver.Info{ID: id, Platform: driver.IOS})
	if err == nil {
		full.Key(ctx, driver.KeyHome)
		time.Sleep(time.Second)
		full.Close()
	}
}

// activate performs an element's default action, as VoiceOver's double tap
// does (the inspector's "Activate", AXAction-2010), the way go-ios sends it.
// On iOS 27.0.1 it returns at once and presses nothing in Calculator.
func (a *axSession) activate(ctx context.Context, handle []byte) error {
	if len(handle) == 0 {
		return errors.New("accessibility: no element") // a null handle breaks the daemon's connection
	}
	pass := func(v any) any {
		return nskeyedarchiver.NewNSMutableDictionary(map[string]any{"ObjectType": "passthrough", "Value": v})
	}
	action := nskeyedarchiver.NewNSMutableDictionary(map[string]any{
		"ObjectType": "AXAuditElementAttribute_v1",
		"Value": pass(nskeyedarchiver.NewNSMutableDictionary(map[string]any{
			"AttributeNameValue_v1":     pass("AXAction-2010"),
			"HumanReadableNameValue_v1": pass("Activate"),
			"PerformsActionValue_v1":    pass(true),
			"SettableValue_v1":          pass(false),
			"DisplayAsTree_v1":          pass(false),
			"DisplayInlineValue_v1":     pass(false),
			"IsInternal_v1":             pass(false),
			"ValueTypeValue_v1":         pass(int32(1)),
		})),
	})
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := a.ch.MethodCallWithContext(c, "deviceElement:performAction:withValue:", elementArg(handle), action, nskeyedarchiver.NewNSMutableDictionary(map[string]any{})); err != nil {
		return fmt.Errorf("accessibility: activate: %w", err)
	}
	return nil
}
