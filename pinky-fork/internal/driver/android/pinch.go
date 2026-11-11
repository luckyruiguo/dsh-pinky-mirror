package android

import (
	"context"
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/scrcpy"
	"github.com/DIYgod/pinky/internal/screen"
)

// A pinch goes through the scrcpy session like a swipe, with two pointers:
// the server turns the second pointer's down and up into
// ACTION_POINTER_DOWN and ACTION_POINTER_UP and injects every event on
// the session's display, so pinches work on the main display and on
// background displays alike.
const (
	pinchFirst  = scrcpy.PointerGenericFinger
	pinchSecond = scrcpy.PointerVirtualFinger
	pinchStep   = 8 * time.Millisecond // interval between moves
)

// Pinch implements driver.Pincher.
func (d *Device) Pinch(ctx context.Context, g driver.Pinch) error {
	d.used()
	err := d.withInput(ctx, true, func(l *live) error { return l.pinch(ctx, g) })
	if err == nil {
		d.markAwake()
	}
	return err
}

// Touch2 implements driver.MultiToucher, for the person's pinch on the
// mirror.
func (d *Device) Touch2(ctx context.Context, a driver.TouchAction, p, q screen.Point) error {
	d.used()
	return d.withInput(ctx, a == driver.TouchDown, func(l *live) error { return l.touch2(ctx, a, p, q) })
}

// Pinch implements driver.Pincher.
func (b *Background) Pinch(ctx context.Context, g driver.Pinch) error {
	b.d.used()
	if err := b.ready(ctx); err != nil {
		return err
	}
	return b.l.input(func() error { return b.l.pinch(ctx, g) })
}

// Touch2 implements driver.MultiToucher.
func (b *Background) Touch2(ctx context.Context, a driver.TouchAction, p, q screen.Point) error {
	if a == driver.TouchDown {
		if err := b.ready(ctx); err != nil {
			return err
		}
	}
	return b.l.input(func() error { return b.l.touch2(ctx, a, p, q) })
}

// pinch moves two fingers along g. Both fingers always lift, also when
// ctx ends during the gesture.
func (l *live) pinch(ctx context.Context, g driver.Pinch) error {
	at := func(t float64) (scrcpy.Position, scrcpy.Position, error) {
		a, b := g.At(t)
		pa, err := l.position(ctx, a)
		if err != nil {
			return pa, pa, err
		}
		pb, err := l.position(ctx, b)
		return pa, pb, err
	}
	a, b, err := at(0)
	if err != nil {
		return err
	}
	if err := l.send(scrcpy.InjectTouch(scrcpy.ActionDown, pinchFirst, a, 1, 0, 0)); err != nil {
		return err
	}
	if err := l.send(scrcpy.InjectTouch(scrcpy.ActionDown, pinchSecond, b, 1, 0, 0)); err != nil {
		l.send(scrcpy.InjectTouch(scrcpy.ActionUp, pinchFirst, a, 0, 0, 0))
		return err
	}
	start := time.Now()
	var werr error
	for {
		if werr = sleepCtx(ctx, pinchStep); werr != nil {
			break
		}
		f := float64(time.Since(start)) / float64(g.Duration)
		if g.Duration <= 0 || f >= 1 {
			break
		}
		if a, b, err = at(f); err != nil {
			werr = err
			break
		}
		if err := l.move2(a, b); err != nil {
			return err
		}
	}
	if werr == nil {
		if a, b, err = at(1); err != nil {
			werr = err
		} else if err := l.move2(a, b); err != nil {
			return err
		}
	}
	if err := l.send(scrcpy.InjectTouch(scrcpy.ActionUp, pinchSecond, b, 0, 0, 0)); err != nil {
		return err
	}
	if err := l.send(scrcpy.InjectTouch(scrcpy.ActionUp, pinchFirst, a, 0, 0, 0)); err != nil {
		return err
	}
	return werr
}

// move2 moves both fingers of a pinch.
func (l *live) move2(a, b scrcpy.Position) error {
	if err := l.send(scrcpy.InjectTouch(scrcpy.ActionMove, pinchFirst, a, 1, 0, 0)); err != nil {
		return err
	}
	return l.send(scrcpy.InjectTouch(scrcpy.ActionMove, pinchSecond, b, 1, 0, 0))
}

// touch2 puts down, moves or lifts both fingers of the person's pinch.
func (l *live) touch2(ctx context.Context, a driver.TouchAction, p, q screen.Point) error {
	pp, err := l.position(ctx, p)
	if err != nil {
		return err
	}
	pq, err := l.position(ctx, q)
	if err != nil {
		return err
	}
	switch a {
	case driver.TouchDown:
		if err := l.send(scrcpy.InjectTouch(scrcpy.ActionDown, pinchFirst, pp, 1, 0, 0)); err != nil {
			return err
		}
		return l.send(scrcpy.InjectTouch(scrcpy.ActionDown, pinchSecond, pq, 1, 0, 0))
	case driver.TouchMove:
		return l.move2(pp, pq)
	case driver.TouchUp:
		if err := l.send(scrcpy.InjectTouch(scrcpy.ActionUp, pinchSecond, pq, 0, 0, 0)); err != nil {
			return err
		}
		return l.send(scrcpy.InjectTouch(scrcpy.ActionUp, pinchFirst, pp, 0, 0, 0))
	}
	return fmt.Errorf("android: touch action %q: %w", a, driver.ErrUnsupported)
}

var (
	_ driver.Pincher      = (*Device)(nil)
	_ driver.Pincher      = (*Background)(nil)
	_ driver.MultiToucher = (*Device)(nil)
	_ driver.MultiToucher = (*Background)(nil)
)
