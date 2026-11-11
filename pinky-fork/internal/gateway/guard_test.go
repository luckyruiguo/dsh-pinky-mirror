//go:build device

package gateway

import (
	"context"
	"fmt"
	"image"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/ios"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// lastStats returns the newest n read statistics.
func (g *guardDevice) lastStats(n int) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.stats) < n {
		n = len(g.stats)
	}
	return append([]string(nil), g.stats[len(g.stats)-n:]...)
}

// guardDevice wraps a real phone for the device tests. It is the last line
// of defence for the owner's phone and holds whatever the gateway decides:
//
//   - input reaches the phone only while it shows the home screen, Settings
//     or the Calculator, so an unexpected dialog is left as it is;
//   - taps reach the phone only in the Calculator, and in Settings only on
//     an allowlisted navigation row, never on a switch or checkbox;
//   - swipes only scroll Settings vertically; keys are only back and home;
//   - typing, raw touches, long presses, streams (unless the run records,
//     PINKY_RECORD) and launching anything but the Calculator and Settings
//     are refused.
//
// Every refusal is recorded, and the tests fail on any.
type guardDevice struct {
	driver.Device

	mu       sync.Mutex
	last     *screen.Screen
	navRows  []string // Settings rows the tests may open
	refused  []string
	inputs   []string
	timings  map[string][]time.Duration
	stats    []string // iOS read statistics, newest last
	settleOK int
	settleTO int
	// holdRead delays reads, untimed, so a test can act while one runs.
	holdRead time.Duration
}

// setHoldRead delays every read by d until set back to 0.
func (g *guardDevice) setHoldRead(d time.Duration) {
	g.mu.Lock()
	g.holdRead = d
	g.mu.Unlock()
}

func newGuard(dev driver.Device, navRows []string) *guardDevice {
	return &guardDevice{Device: dev, navRows: navRows, timings: map[string][]time.Duration{}}
}

func (g *guardDevice) time(what string, start time.Time) {
	g.mu.Lock()
	g.timings[what] = append(g.timings[what], time.Since(start))
	g.mu.Unlock()
}

func (g *guardDevice) refuse(format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	g.mu.Lock()
	g.refused = append(g.refused, msg)
	g.mu.Unlock()
	return fmt.Errorf("device test guard refused: %s: %w", msg, driver.ErrUnsupported)
}

func (g *guardDevice) record(in string) {
	g.mu.Lock()
	g.inputs = append(g.inputs, in)
	g.mu.Unlock()
}

// Refused returns what the guard refused so far.
func (g *guardDevice) Refused() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.refused...)
}

// Inputs returns the inputs that reached the phone.
func (g *guardDevice) Inputs() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.inputs...)
}

func isCalculator(a screen.App) bool {
	s := strings.ToLower(a.ID + " " + a.Name)
	return strings.Contains(s, "calculator") || strings.Contains(s, "calc") || strings.Contains(a.Name, "计算器")
}

func isSettings(a screen.App) bool {
	return a.ID == "com.android.settings" || a.ID == "com.apple.Preferences"
}

var safetyPolicy = policy.Default()

func isHome(a screen.App) bool {
	return a.ID != "" && safetyPolicy.IsSystem(a)
}

// allowedApp reports whether the tests may send input while app is in front.
func allowedApp(a screen.App) bool { return isHome(a) || isSettings(a) || isCalculator(a) }

// matchesRow reports whether an element is one of the allowed navigation
// rows: its label is the row name, or starts with it followed by the
// driver's separator for merged rows.
func matchesRow(e screen.Element, rows []string) bool {
	for _, r := range rows {
		if e.Label == r || strings.HasPrefix(e.Label, r+" · ") || strings.HasPrefix(e.Label, r+", ") {
			return true
		}
	}
	return false
}

func isToggle(e screen.Element) bool {
	return e.Role == screen.RoleSwitch || e.Role == screen.RoleCheckbox || e.Has(screen.StateChecked)
}

// statsObserver is the iOS driver's Observe with timings and OCR counts.
type statsObserver interface {
	ObserveWithStats(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, ios.ObserveStats, error)
}

func (g *guardDevice) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	g.mu.Lock()
	hold := g.holdRead
	g.mu.Unlock()
	if hold > 0 {
		select {
		case <-time.After(hold):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	start := time.Now()
	var s *screen.Screen
	var err error
	if so, ok := g.Device.(statsObserver); ok {
		var st ios.ObserveStats
		s, st, err = so.ObserveWithStats(ctx, opt)
		g.mu.Lock()
		g.stats = append(g.stats, fmt.Sprintf("elements %d placed %d ocr lines %d walk %v shot %v ocr %v ocrErr %v walkErr %v",
			st.Elements, st.Placed, st.OCRLines, st.Walk.Round(time.Millisecond), st.Shot.Round(time.Millisecond),
			st.OCR.Round(time.Millisecond), st.OCRError, st.WalkError))
		g.mu.Unlock()
	} else {
		s, err = g.Device.Observe(ctx, opt)
	}
	what := "observe"
	if opt.OCR {
		what = "observe+ocr"
	}
	g.time(what, start)
	if err == nil {
		cp := *s
		cp.Elements = append([]screen.Element(nil), s.Elements...)
		g.mu.Lock()
		g.last = &cp
		g.mu.Unlock()
	}
	return s, err
}

func (g *guardDevice) current() *screen.Screen {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

func (g *guardDevice) Screenshot(ctx context.Context) (image.Image, error) {
	start := time.Now()
	img, err := g.Device.Screenshot(ctx)
	g.time("screenshot", start)
	return img, err
}

func (g *guardDevice) Tap(ctx context.Context, p screen.Point, opt driver.TapOptions) error {
	cur := g.current()
	switch {
	case cur == nil:
		return g.refuse("tap before any observation")
	case cur.Locked:
		return g.refuse("tap on a locked phone")
	case opt.LongPress:
		return g.refuse("long press")
	case !allowedApp(cur.App):
		return g.refuse("tap in %s", cur.App.ID)
	}
	var hits []screen.Element
	for _, e := range cur.Elements {
		if e.Source != screen.SourceOCR && !e.Bounds.Empty() && e.Bounds.Contains(p) {
			hits = append(hits, e)
		}
	}
	for _, e := range hits {
		if isToggle(e) {
			return g.refuse("tap on a %s at %d,%d", e.Role, p.X, p.Y)
		}
	}
	if isSettings(cur.App) {
		ok := false
		for _, e := range hits {
			ok = ok || matchesRow(e, g.navRows)
		}
		if !ok {
			return g.refuse("tap in Settings outside the allowed rows at %d,%d", p.X, p.Y)
		}
	} else if !isCalculator(cur.App) {
		return g.refuse("tap outside the Calculator and Settings")
	}
	g.record(fmt.Sprintf("tap %d,%d", p.X, p.Y))
	return g.Device.Tap(ctx, p, opt)
}

func (g *guardDevice) Swipe(ctx context.Context, from, to screen.Point, dur time.Duration) error {
	cur := g.current()
	if cur == nil || cur.Locked || !isSettings(cur.App) {
		return g.refuse("swipe outside Settings")
	}
	dx, dy := to.X-from.X, to.Y-from.Y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dy < 200 || dx > dy/4 {
		return g.refuse("swipe that is not a vertical scroll")
	}
	for _, e := range cur.Elements {
		if isToggle(e) && e.Bounds.Contains(from) {
			return g.refuse("swipe starting on a %s", e.Role)
		}
	}
	g.record(fmt.Sprintf("swipe %d,%d to %d,%d", from.X, from.Y, to.X, to.Y))
	return g.Device.Swipe(ctx, from, to, dur)
}

func (g *guardDevice) Key(ctx context.Context, k driver.Key) error {
	cur := g.current()
	if cur == nil || cur.Locked || !allowedApp(cur.App) {
		return g.refuse("key %s outside the home screen, Settings and the Calculator", k)
	}
	if k != driver.KeyBack && k != driver.KeyHome {
		return g.refuse("key %s", k)
	}
	g.record("key " + string(k))
	return g.Device.Key(ctx, k)
}

func (g *guardDevice) Type(ctx context.Context, text string, opt driver.TypeOptions) error {
	return g.refuse("typing")
}

func (g *guardDevice) Launch(ctx context.Context, app string) error {
	cur := g.current()
	if cur == nil || cur.Locked || !allowedApp(cur.App) {
		return g.refuse("launch while an unexpected app is in front")
	}
	apps, err := g.Device.Apps(ctx)
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.ID == app || strings.EqualFold(a.Name, app) {
			sa := screen.App{ID: a.ID, Name: a.Name}
			if isSettings(sa) || isCalculator(sa) {
				g.record("launch " + a.ID)
				return g.Device.Launch(ctx, app)
			}
		}
	}
	return g.refuse("launch %s", app)
}

func (g *guardDevice) Stream(ctx context.Context, opt driver.StreamOptions) (driver.Stream, error) {
	// A stream only reads the screen; the recording stays in the run's
	// temporary folder.
	if os.Getenv("PINKY_RECORD") != "" {
		return g.Device.Stream(ctx, opt)
	}
	return nil, g.refuse("stream")
}

// WaitSettled forwards to the driver's Settler, so the gateway takes the
// real settle path, and times it.
func (g *guardDevice) WaitSettled(ctx context.Context, max time.Duration) (bool, error) {
	s, ok := g.Device.(driver.Settler)
	if !ok {
		return false, driver.ErrUnsupported
	}
	start := time.Now()
	settled, err := s.WaitSettled(ctx, max)
	g.time("settle", start)
	g.mu.Lock()
	if settled {
		g.settleOK++
	} else {
		g.settleTO++
	}
	g.mu.Unlock()
	return settled, err
}

// Locked forwards to the driver's LockReporter.
func (g *guardDevice) Locked(ctx context.Context) (bool, error) {
	if lr, ok := g.Device.(driver.LockReporter); ok {
		return lr.Locked(ctx)
	}
	return false, driver.ErrUnsupported
}

// guardProvider shows only the phone under test, wrapped in the guard.
type guardProvider struct {
	driver.Provider
	id      string
	navRows []string

	mu    sync.Mutex
	guard *guardDevice
}

func (p *guardProvider) List(ctx context.Context) ([]driver.Info, error) {
	infos, err := p.Provider.List(ctx)
	var out []driver.Info
	for _, info := range infos {
		if info.ID == p.id {
			out = append(out, info)
		}
	}
	return out, err
}

func (p *guardProvider) Open(ctx context.Context, id string) (driver.Device, error) {
	if id != p.id {
		return nil, fmt.Errorf("%s is not under test: %w", id, driver.ErrNotFound)
	}
	dev, err := p.Provider.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	g := newGuard(dev, p.navRows)
	p.mu.Lock()
	p.guard = g
	p.mu.Unlock()
	return g, nil
}

func (p *guardProvider) Guard() *guardDevice {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.guard
}
