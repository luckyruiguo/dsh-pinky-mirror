package android

import (
	"context"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/scrcpy"
)

// A drag goes through the scrcpy session like a swipe, so it works on the
// main display and on background displays alike.

// Drag implements driver.Dragger.
func (d *Device) Drag(ctx context.Context, g driver.Drag) error {
	return d.DragReplan(ctx, g, -1, nil)
}

// DragReplan implements driver.DragReplanner. replan runs while the drag
// holds the session's input, so it may take screenshots (adb) but must not
// open a video stream, which waits for that input.
func (d *Device) DragReplan(ctx context.Context, g driver.Drag, at int, replan func(context.Context) []driver.DragStop) error {
	d.used()
	err := d.withInput(ctx, true, func(l *live) error { return l.drag(ctx, g, at, replan) })
	if err == nil {
		d.markAwake()
	}
	return err
}

// Drag implements driver.Dragger.
func (b *Background) Drag(ctx context.Context, g driver.Drag) error {
	return b.DragReplan(ctx, g, -1, nil)
}

// DragReplan implements driver.DragReplanner, as on the main display.
func (b *Background) DragReplan(ctx context.Context, g driver.Drag, at int, replan func(context.Context) []driver.DragStop) error {
	b.d.used()
	if err := b.ready(ctx); err != nil {
		return err
	}
	return b.l.input(func() error { return b.l.drag(ctx, g, at, replan) })
}

// drag moves one finger through g, a move every swipeStep and no events
// while it rests. Each step keeps to the drag's own clock, so a long drag
// takes as long as it says. With replan, the finger stops at g.Stops[at]
// and replan may change the stops after it (driver.DragReplanner). The
// finger always lifts, also when ctx ends during the drag.
func (l *live) drag(ctx context.Context, g driver.Drag, at int, replan func(context.Context) []driver.DragStop) error {
	first, rest := g, []driver.DragStop(nil)
	if replan != nil && at >= 0 && at < len(g.Stops) {
		first.Stops, rest = g.Stops[:at+1], g.Stops[at+1:]
	} else {
		replan = nil
	}
	steps := first.Steps(swipeStep)
	finger := scrcpy.PointerGenericFinger
	pos, err := l.position(ctx, steps[0].P)
	if err != nil {
		return err
	}
	if err := l.send(scrcpy.InjectTouch(scrcpy.ActionDown, finger, pos, 1, 0, 0)); err != nil {
		return err
	}
	pos, werr, serr := l.dragSteps(ctx, steps[1:], pos)
	if serr != nil {
		return serr
	}
	if werr == nil && replan != nil {
		if stops := replan(ctx); stops != nil {
			rest = stops
		}
		if werr = ctx.Err(); werr == nil && len(rest) > 0 {
			tail := driver.Drag{From: first.To(), Stops: rest}
			pos, werr, serr = l.dragSteps(ctx, tail.Steps(swipeStep)[1:], pos)
			if serr != nil {
				return serr
			}
		}
	}
	if err := l.send(scrcpy.InjectTouch(scrcpy.ActionUp, finger, pos, 0, 0, 0)); err != nil {
		return err
	}
	return werr
}

// dragSteps plays steps with the finger down at pos, keeping to their
// clock from now, and returns where the finger is. werr is why it stopped
// early (ctx, or a point it could not place); serr a failed send, after
// which the finger cannot be lifted either.
func (l *live) dragSteps(ctx context.Context, steps []driver.DragStep, pos scrcpy.Position) (_ scrcpy.Position, werr, serr error) {
	finger := scrcpy.PointerGenericFinger
	next := time.Now()
	for _, s := range steps {
		next = next.Add(s.Wait)
		if werr = sleepCtx(ctx, time.Until(next)); werr != nil {
			return pos, werr, nil
		}
		p, err := l.position(ctx, s.P)
		if err != nil {
			return pos, err, nil
		}
		if p == pos {
			continue
		}
		pos = p
		if err := l.send(scrcpy.InjectTouch(scrcpy.ActionMove, finger, pos, 1, 0, 0)); err != nil {
			return pos, nil, err
		}
	}
	return pos, nil, nil
}

var (
	_ driver.DragReplanner = (*Device)(nil)
	_ driver.DragReplanner = (*Background)(nil)
)
