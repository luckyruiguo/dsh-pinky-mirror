package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// loadingDev is a demo phone that shows a page before its tree has it:
// after a tap, its next reads with OCR have two elements of the tree and
// ten lines of OCR, as a web view's page right after it opened.
type loadingDev struct {
	*demo.Device
	mu      sync.Mutex
	loading int  // sparse reads still to come after the tap
	forever bool // a canvas: every read after a tap is sparse
	next    struct {
		loading int
		forever bool
	} // what the next tap starts
	sparse int // sparse reads returned
}

func (d *loadingDev) Tap(ctx context.Context, p screen.Point, opt driver.TapOptions) error {
	d.mu.Lock()
	d.loading, d.forever = d.next.loading, d.next.forever
	d.mu.Unlock()
	return d.Device.Tap(ctx, p, opt)
}

func (d *loadingDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, err := d.Device.Observe(ctx, opt)
	if err != nil || !opt.OCR {
		return s, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loading == 0 && !d.forever {
		return s, nil
	}
	if d.loading > 0 {
		d.loading--
	}
	d.sparse++
	els := append([]screen.Element(nil), s.Elements[:min(2, len(s.Elements))]...)
	for i := range 10 {
		els = append(els, screen.Element{Role: screen.RoleText, Label: fmt.Sprintf("Page line %d", i),
			Bounds: screen.Rect{X: 20, Y: 600 + 40*i, W: 300, H: 30}, Source: screen.SourceOCR})
	}
	s.Elements = els
	return s, nil
}

func (d *loadingDev) set(loading int, forever bool) {
	d.mu.Lock()
	d.next.loading, d.next.forever = loading, forever
	d.loading, d.forever, d.sparse = 0, false, 0
	d.mu.Unlock()
}

func (d *loadingDev) sparseReads() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sparse
}

// After an action, a read whose text OCR found while the tree has little
// waits for the tree; an app whose tree stays sparse is waited for once.
func TestAwaitTree(t *testing.T) {
	defer func(w time.Duration) { treeWait = w }(treeWait)
	treeWait = time.Second
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &loadingDev{Device: base}
	g := newDevGateway(t, base, dev, func(o *Options) { o.PollInterval = time.Millisecond })
	s, first := connect(t, g, "Tree")
	r := ref(t, first, "Dinely")

	dev.set(2, false)
	res := tap(s, r)
	mustStatus(t, res, StatusSettled)
	if text := body(res.Screen); strings.Contains(text, "Page line") {
		t.Fatalf("the page was read before its tree: %s", text)
	}
	if n := dev.sparseReads(); n != 2 {
		t.Fatalf("%d sparse reads, want 2", n)
	}

	// A canvas: the wait ends after treeWait with the sparse read, and the
	// next action in the same app does not wait.
	obs := s.Observe(context.Background(), ObserveRequest{})
	r = obs.Screen.Elements[0].Ref
	dev.set(0, true)
	start := time.Now()
	res = tap(s, r)
	if took := time.Since(start); took < treeWait {
		t.Fatalf("no wait: %v", took)
	}
	if !strings.Contains(body(res.Screen), "Page line") {
		t.Fatalf("the sparse read was lost: %s", body(res.Screen))
	}
	waited := dev.sparseReads()
	if waited < 2 {
		t.Fatalf("%d sparse reads during the wait", waited)
	}
	dev.set(0, true)
	r = ref(t, res, "Page line 0")
	tap(s, r)
	if n := dev.sparseReads(); n >= waited {
		t.Fatalf("waited again in the same app: %d reads", n)
	}
}
