package gateway

import (
	"context"
	"errors"
	"fmt"
	"image"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// Following a home screen's page during a drag. Pixel Launcher (and other
// Launcher3 builds) shrinks the page toward a point near the middle once an
// app is picked up, so a drop point read from the screen before the drag
// is no longer over what it was. When a drag starts on a home screen, the
// gateway takes a picture as the finger touches down, lets the finger move
// a little to pick the app up and rest, takes another, finds where the
// drop point went (findDrop, dragmatch.go) and lets go there. The stops on
// the way stay where they are: a rest at the screen's edge still turns the
// page. It costs about a second on a phone on a cable: the short move and
// rest (0.4 s), a screenshot (0.13 to 0.35 s) and the match (30 to 40 ms).

const (
	// followMinHold is the least hold that picks an app up: a shorter one
	// scrolls the page instead.
	followMinHold = 300 * time.Millisecond
	// followPickMove and followPickPause are the short move that lifts the
	// app out of its place and the rest after it, for the page to finish
	// shrinking.
	followPickMove  = 120 * time.Millisecond
	followPickPause = 250 * time.Millisecond
	// followShotTimeout bounds each picture.
	followShotTimeout = 4 * time.Second
	// followMaxMark is the largest element, as a fraction of the screen,
	// that counts as something on the page.
	followMaxMark = 6
)

// dragFollow follows the page under one drag.
type dragFollow struct {
	g     *Gateway
	d     *device
	dev   driver.Device
	onBG  bool
	w, h  int
	to    screen.Point
	marks []screen.Rect

	// What happened, for the result.
	note    string
	dropped screen.Point
	moved   bool
}

// followDrag returns how to follow the page under drag p from cur, or nil
// when the drag does not start on a home screen with an app held up.
func (g *Gateway) followDrag(d *device, dev driver.Device, onBG bool, cur *screen.Screen, p *prep) *dragFollow {
	if p.drag == nil || len(p.drag.Stops) == 0 || p.drag.Hold < followMinHold || cur == nil || cur.Locked {
		return nil
	}
	if _, ok := dev.(driver.DragReplanner); !ok {
		return nil
	}
	if cur.App.ID == "com.android.systemui" || !g.policy.IsSystem(cur.App) {
		return nil
	}
	if cur.Width <= 0 || cur.Height <= 0 || !g.mayCapture(d, cur) {
		return nil
	}
	marks := dropMarks(cur, p.target)
	if len(marks) == 0 {
		return nil
	}
	return &dragFollow{g: g, d: d, dev: dev, onBG: onBG, w: cur.Width, h: cur.Height, to: p.drag.To(), marks: marks}
}

// dropMarks lists where the elements on cur are that may show where a drop
// point went: what the tree shows with a name, but the status bar, the
// element picked up and anything that covers much of the screen.
func dropMarks(cur *screen.Screen, held *screen.Element) []screen.Rect {
	var out []screen.Rect
	for _, e := range cur.Elements {
		b := e.Bounds
		if e.Source == screen.SourceOCR || b.Empty() || e.Label == "" || e.Hidden || e.Masked {
			continue
		}
		if b.W*b.H*followMaxMark > cur.Width*cur.Height || inStatusBar(cur, e) {
			continue
		}
		if held != nil && b == held.Bounds {
			continue
		}
		out = append(out, b)
	}
	return out
}

// withPickup adds the short move that picks the app up before the drag's
// first stop: a tenth of the screen's width toward it, kept on the screen
// away from its edges, which would turn the page.
func withPickup(g driver.Drag, w, h int) driver.Drag {
	dx, dy := float64(g.Stops[0].Point.X-g.From.X), float64(g.Stops[0].Point.Y-g.From.Y)
	n := dist(g.Stops[0].Point, g.From)
	if n < 1 {
		dx, dy, n = 0, -1, 1
	}
	step := float64(w) / 10
	pt := screen.Point{X: g.From.X + int(dx/n*step), Y: g.From.Y + int(dy/n*step)}
	pt.X = min(max(pt.X, w/12), w-w/12)
	pt.Y = min(max(pt.Y, h*8/100), h*95/100)
	out := g
	out.Stops = append([]driver.DragStop{{Point: pt, Move: followPickMove, Pause: followPickPause}}, g.Stops...)
	return out
}

// dropAt is the stops after the pickup with the last one at to.
func dropAt(stops []driver.DragStop, to screen.Point) []driver.DragStop {
	out := append([]driver.DragStop(nil), stops...)
	out[len(out)-1].Point = to
	return out
}

type shotResult struct {
	img image.Image
	err error
}

// run performs drag g with dr, following the page.
func (f *dragFollow) run(ctx context.Context, dr driver.DragReplanner, g driver.Drag) error {
	start := time.Now()
	route := withPickup(g, f.w, f.h)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	before := make(chan shotResult, 1)
	go func() {
		img, err := f.shoot(sctx)
		before <- shotResult{img, err}
	}()
	var shotAt, matchAt time.Duration
	err := dr.DragReplan(ctx, route, 0, func(ctx context.Context) []driver.DragStop {
		var b shotResult
		select {
		case b = <-before:
		case <-ctx.Done():
			return nil
		}
		if b.err != nil {
			f.fail("no picture from before the drag")
			f.g.log.Info("drag: no picture before", "device", f.d.id, "err", b.err)
			return nil
		}
		t := time.Now()
		mid, err := f.shoot(ctx)
		shotAt = time.Since(t)
		if err != nil {
			f.fail("no picture while the app was held")
			f.g.log.Info("drag: no picture while held", "device", f.d.id, "err", err)
			return nil
		}
		t = time.Now()
		found := findDrop(b.img, mid, f.w, f.h, f.to, f.marks)
		matchAt = time.Since(t)
		f.g.log.Info("drag: follow", "device", f.d.id, "to", f.to, "found", found.To, "scale", found.Scale,
			"score", found.Score, "why", found.Why, "shot", shotAt, "match", matchAt)
		switch {
		case found.Why != "":
			f.fail(found.Why)
			return nil
		case !found.Shrunk:
			return nil
		}
		f.moved, f.dropped = true, found.To
		f.note = fmt.Sprintf("The launcher shrank the home screen page when it picked the app up; Pinky let go at %d,%d, where %d,%d had moved.",
			found.To.X, found.To.Y, f.to.X, f.to.Y)
		return dropAt(route.Stops[1:], found.To)
	})
	f.g.log.Info("drag: followed", "device", f.d.id, "took", time.Since(start), "drag", g.Duration())
	return err
}

func (f *dragFollow) fail(why string) {
	f.note = fmt.Sprintf("The launcher may have shrunk the home screen page when it picked the app up, but Pinky could not see where %d,%d went (%s), so it let go there as given. Observe to check where the app landed.",
		f.to.X, f.to.Y, why)
}

// shoot takes a picture of the screen without waiting for the phone's
// input, which the drag holds: a screenshot of the phone's own screen on a
// cable, else a picture from the video when it is open.
func (f *dragFollow) shoot(ctx context.Context) (image.Image, error) {
	ctx, cancel := context.WithTimeout(ctx, followShotTimeout)
	defer cancel()
	if !f.onBG && cabled(f.d.info) {
		return f.dev.Screenshot(ctx)
	}
	img, err := f.g.frameShotOpen(ctx, hubKey(f.d.id, f.onBG))
	if err == nil || f.onBG || !errors.Is(err, errNoStream) {
		return img, err
	}
	return f.dev.Screenshot(ctx)
}
