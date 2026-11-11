//go:build device

package ios

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// probeOut opens the file a probe writes to: its output holds screen
// content, so it goes under /tmp/pinky-ios-probe (delete it when done)
// rather than into the test log.
func probeOut(t *testing.T, name string) *os.File {
	t.Helper()
	dir := "/tmp/pinky-ios-probe"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	t.Logf("writing %s", f.Name())
	return f
}

func dumpRAX(w *os.File, title string, els []raxElement) {
	fmt.Fprintf(w, "\n== %s: %d elements\n", title, len(els))
	for i, e := range els {
		fr := "-"
		if e.HasFrame {
			fr = fmt.Sprintf("%.0f,%.0f %.0fx%.0f", e.Frame.X, e.Frame.Y, e.Frame.W, e.Frame.H)
		}
		fmt.Fprintf(w, "%3d p%-3d d%-2d pid%-5d id%-20d t%-3d r%-3d tr%#x el%v vis%v en%v sel%v foc%v rem%v kids%d %s label=%q value=%q vtext=%q ident=%q ph=%q\n",
			i, e.Parent, e.Depth, e.Object.PID, e.Object.ID, e.Type, e.AXRole, e.Traits, b01(e.IsElement), b01(e.Visible), b01(e.Enabled), b01(e.Selected), b01(e.Focused), b01(e.Remote), e.Children, fr,
			e.Label, e.Value, e.ValueText, e.Ident, e.Placeholder)
	}
}

func b01(b bool) int {
	if b {
		return 1
	}
	return 0
}

func dumpScreen(w *os.File, title string, els []screen.Element) {
	fmt.Fprintf(w, "\n== %s: %d screen elements\n", title, len(els))
	for _, e := range els {
		fmt.Fprintf(w, "  %-8s %-40q id=%-24q %v %v path=%s\n", e.Role, e.Label, e.ID, e.Bounds, e.States, e.Path)
	}
}

// keysDiff lists elements whose ref key (role, label, id, path) differs
// between two conversions of the same screen, matched by label and place.
func keysDiff(w *os.File, title string, a, b []screen.Element) {
	fmt.Fprintf(w, "\n== key differences %s\n", title)
	type k struct {
		label string
		r     screen.Rect
	}
	idx := map[k]screen.Element{}
	for _, e := range b {
		idx[k{e.Label, e.Bounds}] = e
	}
	n := 0
	for _, e := range a {
		o, ok := idx[k{e.Label, e.Bounds}]
		if !ok {
			fmt.Fprintf(w, "  only in first: %s %q %v\n", e.Role, e.Label, e.Bounds)
			continue
		}
		if o.Role != e.Role || o.ID != e.ID || o.Path != e.Path {
			n++
			fmt.Fprintf(w, "  %q: role %s/%s id %q/%q\n    path %s\n    path %s\n", e.Label, e.Role, o.Role, e.ID, o.ID, e.Path, o.Path)
		}
	}
	fmt.Fprintf(w, "  %d differ\n", n)
}

// findEl finds an element by label, also when a value follows the label
// ("Alarms, 0"), and by role unless role is empty.
func findEl(els []screen.Element, role, label string) (screen.Element, bool) {
	for _, e := range els {
		if (role == "" || e.Role == role) && (e.Label == label || strings.HasPrefix(e.Label, label+", ")) && !e.Bounds.Empty() {
			return e, true
		}
	}
	return screen.Element{}, false
}

// pushRecorder keeps every tree the phone pushes until stopped.
type pushRecorder struct {
	mu    sync.Mutex
	trees []*raxTree
	stop  chan struct{}
	done  chan struct{}
}

func recordPushes(rax *raxSession) *pushRecorder {
	r := &pushRecorder{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		_, next := rax.pushed()
		for {
			select {
			case <-next:
				var t *raxTree
				t, next = rax.pushed()
				r.mu.Lock()
				r.trees = append(r.trees, t)
				r.mu.Unlock()
			case <-r.stop:
				return
			}
		}
	}()
	return r
}

func (r *pushRecorder) end() []*raxTree {
	close(r.stop)
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.trees
}

// TestDeviceRAXProbe looks into how remoteAXService reads Clock: a first
// push against its second push and a walk of the same screen (ref keys), the
// floating tab bar against the rows behind it, the Add Alarm sheet's
// picker wheels, and what SpringBoard's application element answers while
// Clock is in front. It opens the Add Alarm sheet and cancels it, and puts
// Clock back on the tab it was on.
func TestDeviceRAXProbe(t *testing.T) {
	d := testDevice(t)
	ctx := ctxFor(t, 3*time.Minute)
	w := probeOut(t, "rax-probe.txt")
	rax, err := d.raxReady(ctx)
	if err != nil {
		t.Fatal(err)
	}
	walk := func(title string) ([]raxElement, []screen.Element) {
		rax, err := d.raxReady(ctx)
		if err != nil {
			t.Fatal(err)
		}
		c, cancel := context.WithTimeout(ctx, raxLimits.Budget+raxRequestTimeout)
		defer cancel()
		start := time.Now()
		els, complete, err := rax.walk(c, raxLimits)
		if err != nil {
			t.Fatalf("%s: walk: %v", title, err)
		}
		fmt.Fprintf(w, "\n## %s: walk %v, complete %v\n", title, time.Since(start).Round(time.Millisecond), complete)
		dumpRAX(w, title+" raw", els)
		si := d.screen()
		scale := si.Scale
		if els[0].HasFrame && els[0].Frame.W > 0 {
			scale = float64(si.Width) / els[0].Frame.W
		}
		out, _, _ := raxScreenElements(els, scale, si.Width, si.Height, "app")
		dumpScreen(w, title, out)
		return els, out
	}
	convert := func(t *raxTree) []screen.Element {
		si := d.screen()
		scale := si.Scale
		if t.els[0].HasFrame && t.els[0].Frame.W > 0 {
			scale = float64(si.Width) / t.els[0].Frame.W
		}
		out, _, _ := raxScreenElements(t.els, scale, si.Width, si.Height, "app")
		return out
	}

	home(ctx, d)
	pause()
	sb, _ := walk("home screen")
	sbApp := sb[0].Object
	fmt.Fprintf(w, "\nSpringBoard app object: %+v\n", *sbApp)

	rec := recordPushes(rax)
	if err := d.Launch(ctx, "com.apple.mobiletimer"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)
	pushes := rec.end()
	_, clock := walk("clock after launch")
	for i, p := range pushes {
		fmt.Fprintf(w, "\n## push %d: first %v, %d elements, app pid %d, at +%v\n", i, p.first, len(p.els), p.appPID, p.at.Sub(pushes[0].at).Round(time.Millisecond))
		dumpRAX(w, fmt.Sprintf("push %d raw", i), p.els)
		els := convert(p)
		dumpScreen(w, fmt.Sprintf("push %d", i), els)
		keysDiff(w, fmt.Sprintf("push %d vs walk", i), els, clock)
	}
	_, again := walk("clock, second walk")
	keysDiff(w, "walk vs walk", clock, again)

	// SpringBoard while Clock is in front.
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	r, err := rax.request(c, axpRequest{Type: axpReqAttributes, Client: raxClientType, Object: sbApp, Params: map[string]any{"attributes": raxWalkAttrs}})
	cancel()
	fmt.Fprintf(w, "\n## SpringBoard app element while Clock is in front: err %v\n", err)
	if err == nil {
		if ad, ok := r.Result.(axpDict); ok {
			e, kids := raxElementFrom(sbApp, ad, -1, 0)
			dumpRAX(w, "springboard app", []raxElement{e})
			fmt.Fprintf(w, "children: %d\n", len(kids))
			// One level down: SpringBoard's windows or top elements.
			var sub []raxElement
			for i, k := range kids {
				if i >= 40 {
					break
				}
				c, cancel := context.WithTimeout(ctx, 3*time.Second)
				r, err := rax.request(c, axpRequest{Type: axpReqAttributes, Client: raxClientType, Object: k, Params: map[string]any{"attributes": raxWalkAttrs}})
				cancel()
				if err != nil {
					fmt.Fprintf(w, "child %d: %v\n", i, err)
					break
				}
				ad, _ := r.Result.(axpDict)
				e, _ := raxElementFrom(k, ad, 0, 1)
				sub = append(sub, e)
			}
			dumpRAX(w, "springboard children", sub)
		}
	}
	if !rax.alive() {
		fmt.Fprintf(w, "the session broke\n")
	}

	// The tab Clock was on, to go back to.
	var startTab screen.Element
	for _, e := range clock {
		if e.Has(screen.StateSelected) && e.Bounds.Y > d.screen().Height*3/4 {
			startTab = e
		}
	}
	fmt.Fprintf(w, "\nClock opened on tab %q\n", startTab.Label)

	tab, ok := findEl(clock, "", "Alarms")
	if !ok {
		t.Fatalf("no Alarms tab")
	}
	if err := d.Tap(ctx, tab.Bounds.Center(), driver.TapOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	_, alarms := walk("alarms")
	add, ok := findEl(alarms, screen.RoleButton, "Add")
	if !ok {
		t.Fatalf("no Add button")
	}
	if err := d.Tap(ctx, add.Bounds.Center(), driver.TapOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	sheet, sheetEls := walk("add alarm")
	// Every attribute the phone has for the picker wheels and their rows.
	for i, e := range sheet {
		if i >= 80 {
			break
		}
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		attrs := []int{1, 3, 8, 9, 21, 23, 25, 26, 33, 41, 42, 45, 53, 61, 64, 77, 78, 112, 124}
		r, err := rax.request(c, axpRequest{Type: axpReqAttributes, Client: raxClientType, Object: e.Object, Params: map[string]any{"attributes": attrs}})
		cancel()
		if err != nil {
			fmt.Fprintf(w, "picker attrs: %v\n", err)
			break
		}
		ad, _ := r.Result.(axpDict)
		keys := make([]int, 0, len(ad))
		for k := range ad {
			if n, ok := k.(int); ok {
				keys = append(keys, n)
			}
		}
		sort.Ints(keys)
		var b strings.Builder
		for _, k := range keys {
			v := ad[k]
			if list, ok := v.([]any); ok {
				fmt.Fprintf(&b, " %d=[%d items]", k, len(list))
				continue
			}
			fmt.Fprintf(&b, " %d=%v", k, v)
		}
		fmt.Fprintf(w, "picker element t%d %q:%s\n", e.Type, e.Label, b.String())
	}
	cancelBtn, ok := findEl(sheetEls, screen.RoleButton, "Cancel")
	if !ok {
		t.Fatalf("no Cancel button on the sheet")
	}
	pause()
	if err := d.Tap(ctx, cancelBtn.Bounds.Center(), driver.TapOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	_, after := walk("after cancel")
	if _, ok := findEl(after, screen.RoleButton, "Cancel"); ok {
		t.Errorf("the sheet is still open")
	}
	if startTab.Label != "" && !strings.HasPrefix(startTab.Label, "Alarms") {
		if back, ok := findEl(after, "", strings.SplitN(startTab.Label, ", ", 2)[0]); ok {
			pause()
			d.Tap(ctx, back.Bounds.Center(), driver.TapOptions{})
			time.Sleep(time.Second)
		}
	}
	home(ctx, d)
}
