// Package local drives the Android phone Pinky itself runs on, through the
// standalone prototype's accessibility service (android/standalone) over
// its local socket: no adb and no computer. Taps, swipes and long presses
// are gestures the service dispatches, keys are its global actions, text
// goes in with set_text and pictures come from its screenshots.
//
// It is a feasibility prototype, kept apart from the shipped drivers; see
// docs/notes/standalone-phone.md, "Prototype results (Android)".
package local

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/screen"
)

// Standalone is the prototype's build of the companion. It is not one of
// companion.AppFor's builds: Pinky on a computer never drives it.
var Standalone = companion.App{Package: "app.pinky.standalone", Socket: "pinky_standalone", Label: "Pinky Standalone"}

// ID is the one phone's id: the phone the core runs on.
const ID = "this-phone"

// ErrOwnUI means the service refused input that would land on Pinky's own
// controls: an approval, the Working pill or Pinky's own screen. Nothing
// was done. It counts as driver.ErrUnsupported, which the gateway reports
// as an action that was not done, never as one that may have happened.
var ErrOwnUI error = ownUIError{}

type ownUIError struct{}

func (ownUIError) Error() string {
	return "it would press Pinky's own controls, which only the person may use"
}

func (ownUIError) Is(target error) bool { return target == driver.ErrUnsupported }

// Durations of a gesture's one stroke.
const (
	tapDuration       = 50 * time.Millisecond
	longPressDuration = 800 * time.Millisecond
	swipeDuration     = 300 * time.Millisecond
)

// Provider lists the one phone. It opens the client lazily.
type Provider struct {
	app companion.App
	mu  sync.Mutex
	dev *Device
}

// NewProvider returns the provider for the phone the caller runs on,
// reached through app's socket.
func NewProvider(app companion.App) *Provider { return &Provider{app: app} }

func (p *Provider) Platform() driver.Platform { return driver.Android }

func (p *Provider) List(ctx context.Context) ([]driver.Info, error) {
	d, err := p.device(ctx)
	if err != nil {
		return []driver.Info{{
			ID: ID, Platform: driver.Android, Name: "This phone", Backend: "accessibility",
			State: driver.StateNeedsSetup, Detail: "Turn on Pinky Standalone in Accessibility settings.",
		}}, nil
	}
	return []driver.Info{d.Info()}, nil
}

func (p *Provider) Open(ctx context.Context, id string) (driver.Device, error) {
	if id != ID {
		return nil, fmt.Errorf("local: no phone %q: %w", id, driver.ErrNotFound)
	}
	return p.device(ctx)
}

func (p *Provider) device(ctx context.Context) (*Device, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dev != nil {
		return p.dev, nil
	}
	d, err := Open(ctx, p.app)
	if err != nil {
		return nil, err
	}
	p.dev = d
	return d, nil
}

// Device is the phone the caller runs on.
type Device struct {
	c    *companion.Client
	app  companion.App
	info driver.Info

	appsMu sync.Mutex
	apps   []driver.App
	appsAt time.Time
}

// Open connects to the service and reads the phone's details.
func Open(ctx context.Context, app companion.App) (*Device, error) {
	c := companion.NewLocal(app)
	info, err := c.Ping(ctx)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("local: %w: %w", driver.ErrOffline, err)
	}
	name := strings.TrimSpace(info.Manufacturer + " " + info.Model)
	return &Device{c: c, app: app, info: driver.Info{
		ID: ID, Platform: driver.Android, Name: name, Model: info.Model,
		OSVersion: info.Release, Connection: "local", Backend: "accessibility",
		State: driver.StateOnline,
	}}, nil
}

// Client is the companion client, for commands the driver has no method
// for.
func (d *Device) Client() *companion.Client { return d.c }

func (d *Device) Info() driver.Info { return d.info }

func (d *Device) Close() error { return d.c.Close() }

// Observe reads every window's tree. The lock state and the keyboard come
// with the tree; there is no OCR on the phone, so a screen the tree does
// not describe (a game, a canvas) comes back sparse.
func (d *Device) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	t, err := d.c.Dump(ctx, companion.DumpOptions{})
	if err != nil {
		return nil, d.wrap(err)
	}
	s := agentScreen(t, d.app.Package)
	s.CapturedAt = d.c.HostTime(t.Time)
	if s.App.Name == "" {
		s.App.Name = d.appName(s.App.ID)
	}
	return s, nil
}

// agentScreen is the screen the agent reads from t. Pinky's own controls
// over other apps (the Working pill, an approval card) are left out, and
// what they cover counts as covered: the agent never sees them to press
// them. They hide nothing of the app below, so the screen reads the same
// whether they show or not: an approval card that appears while the
// approval waits must not make the approved step stale. Not even the
// service reads their content (it is accessibilityDataSensitive), so they
// come without a package and are known by their title, the app's package.
func agentScreen(t *companion.Tree, pkg string) *screen.Screen {
	var own []screen.Rect
	windows := make([]companion.Window, 0, len(t.Windows))
	for _, w := range t.Windows {
		if w.Type == companion.WindowAccessibilityOverlay && (w.Package == pkg || w.Title == pkg) {
			own = append(own, ownRects(w)...)
			continue
		}
		windows = append(windows, w)
	}
	tt := *t
	tt.Windows = windows
	s := tt.Screen(companion.ScreenOptions{})
	s.Covered = append(s.Covered, own...)
	return s
}

// ownRects is what one of Pinky's own windows covers: its touchable
// region when the service sent one, else its bounds.
func ownRects(w companion.Window) []screen.Rect {
	bs := w.Region
	if len(bs) == 0 {
		bs = []companion.Bounds{w.Bounds}
	}
	var out []screen.Rect
	for _, b := range bs {
		if r := (screen.Rect{X: b[0], Y: b[1], W: b[2] - b[0], H: b[3] - b[1]}); r.W > 0 && r.H > 0 {
			out = append(out, r)
		}
	}
	return out
}

// Locked reports whether the keyguard shows, from a tree read without its
// nodes' text.
func (d *Device) Locked(ctx context.Context) (bool, error) {
	t, err := d.c.Dump(ctx, companion.DumpOptions{MaxNodes: 1})
	if err != nil {
		return false, d.wrap(err)
	}
	return t.Locked, nil
}

// Shot is a screenshot as the service sent it, with its timings.
type Shot struct {
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Format    string `json:"format"`
	CaptureMS int64  `json:"capture_ms"`
	EncodeMS  int64  `json:"encode_ms"`
	Bytes     int    `json:"bytes"`
	Data      string `json:"data"`
}

// Capture takes a screenshot as JPEG (quality 1 to 100) or, with quality
// 0, as PNG.
func (d *Device) Capture(ctx context.Context, quality int) (*Shot, error) {
	params := map[string]any{"format": "jpeg", "quality": quality}
	if quality == 0 {
		params = map[string]any{"format": "png"}
	}
	var s Shot
	if err := d.c.Call(ctx, "screenshot", params, &s); err != nil {
		return nil, d.wrap(err)
	}
	return &s, nil
}

// Decode decodes the picture.
func (s *Shot) Decode() (image.Image, error) {
	raw, err := base64.StdEncoding.DecodeString(s.Data)
	if err != nil {
		return nil, fmt.Errorf("local: screenshot: %w", err)
	}
	if s.Format == "png" {
		return png.Decode(bytes.NewReader(raw))
	}
	return jpeg.Decode(bytes.NewReader(raw))
}

func (d *Device) Screenshot(ctx context.Context) (image.Image, error) {
	s, err := d.Capture(ctx, 80)
	if err != nil {
		return nil, err
	}
	return s.Decode()
}

// Stroke is one finger's path in a gesture.
type Stroke struct {
	Points   []screen.Point
	Start    time.Duration
	Duration time.Duration
}

// Gesture dispatches strokes as one gesture and waits until Android has
// played it. It returns how long the service took.
func (d *Device) Gesture(ctx context.Context, strokes ...Stroke) (time.Duration, error) {
	ss := make([]map[string]any, len(strokes))
	for i, s := range strokes {
		pts := make([][2]int, len(s.Points))
		for j, p := range s.Points {
			pts[j] = [2]int{p.X, p.Y}
		}
		ss[i] = map[string]any{"points": pts, "start_ms": s.Start.Milliseconds(), "duration_ms": max(1, s.Duration.Milliseconds())}
	}
	var r struct {
		TookMS int64 `json:"took_ms"`
	}
	if err := d.c.Call(ctx, "gesture", map[string]any{"strokes": ss}, &r); err != nil {
		return 0, d.wrap(err)
	}
	return time.Duration(r.TookMS) * time.Millisecond, nil
}

func (d *Device) Tap(ctx context.Context, p screen.Point, opt driver.TapOptions) error {
	dur := tapDuration
	if opt.LongPress {
		dur = longPressDuration
	}
	_, err := d.Gesture(ctx, Stroke{Points: []screen.Point{p}, Duration: dur})
	return err
}

func (d *Device) Swipe(ctx context.Context, from, to screen.Point, dur time.Duration) error {
	if dur <= 0 {
		dur = swipeDuration
	}
	_, err := d.Gesture(ctx, Stroke{Points: []screen.Point{from, to}, Duration: dur})
	return err
}

// Type writes text into the focused field with set_text: any script, no
// clipboard and no keyboard.
func (d *Device) Type(ctx context.Context, text string, opt driver.TypeOptions) error {
	r, err := d.c.SetText(ctx, companion.Target{}, text, companion.SetTextOptions{Append: opt.Append, Submit: opt.Submit})
	if err != nil {
		return d.wrap(err)
	}
	if opt.Submit && !r.Submitted {
		return d.Key(ctx, driver.KeyEnter)
	}
	return nil
}

// Key presses back, home, recents or the keyboard's action key (enter).
// Other keys need a keyboard the prototype does not have.
func (d *Device) Key(ctx context.Context, k driver.Key) error {
	switch k {
	case driver.KeyBack, driver.KeyHome, driver.KeyRecents, driver.KeyEnter:
	default:
		return fmt.Errorf("local: key %q: %w", k, driver.ErrUnsupported)
	}
	return d.Global(ctx, string(k))
}

// Global runs one of the service's global actions: back, home, recents,
// notifications or enter.
func (d *Device) Global(ctx context.Context, action string) error {
	if err := d.c.Call(ctx, "global", map[string]any{"action": action}, nil); err != nil {
		return d.wrap(err)
	}
	return nil
}

// Launch opens an app by package or by the name its icon shows.
func (d *Device) Launch(ctx context.Context, app string) error {
	pkg, err := d.resolve(ctx, app)
	if err != nil {
		return err
	}
	if err := d.c.Call(ctx, "launch", map[string]any{"package": pkg}, nil); err != nil {
		return d.wrap(err)
	}
	return nil
}

func (d *Device) Apps(ctx context.Context) ([]driver.App, error) {
	var r struct {
		Apps []struct {
			Package string `json:"package"`
			Label   string `json:"label"`
			System  bool   `json:"system"`
		} `json:"apps"`
	}
	if err := d.c.Call(ctx, "apps", nil, &r); err != nil {
		return nil, d.wrap(err)
	}
	apps := make([]driver.App, 0, len(r.Apps))
	for _, a := range r.Apps {
		apps = append(apps, driver.App{ID: a.Package, Name: a.Label, System: a.System})
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	d.appsMu.Lock()
	d.apps, d.appsAt = apps, time.Now()
	d.appsMu.Unlock()
	return apps, nil
}

func (d *Device) cachedApps(ctx context.Context) ([]driver.App, error) {
	d.appsMu.Lock()
	apps, at := d.apps, d.appsAt
	d.appsMu.Unlock()
	if apps != nil && time.Since(at) < time.Minute {
		return apps, nil
	}
	return d.Apps(ctx)
}

func (d *Device) appName(pkg string) string {
	d.appsMu.Lock()
	defer d.appsMu.Unlock()
	for _, a := range d.apps {
		if a.ID == pkg {
			return a.Name
		}
	}
	return ""
}

// resolve turns a package or an app's name into its package.
func (d *Device) resolve(ctx context.Context, query string) (string, error) {
	query = strings.TrimSpace(query)
	apps, err := d.cachedApps(ctx)
	if err != nil {
		return "", err
	}
	q := strings.ToLower(query)
	for _, test := range []func(a driver.App) bool{
		func(a driver.App) bool { return a.ID == query },
		func(a driver.App) bool { return strings.ToLower(a.Name) == q },
		func(a driver.App) bool { return strings.HasPrefix(strings.ToLower(a.Name), q) },
	} {
		var hits []string
		for _, a := range apps {
			if test(a) {
				hits = append(hits, a.ID)
			}
		}
		if len(hits) == 1 {
			return hits[0], nil
		}
		if len(hits) > 1 {
			return "", fmt.Errorf("local: launch %q: several apps match: %s", query, strings.Join(hits, ", "))
		}
	}
	return "", fmt.Errorf("local: launch %q: %w", query, driver.ErrNotFound)
}

// Stream is not part of the prototype: the person watches the phone itself.
func (d *Device) Stream(ctx context.Context, opt driver.StreamOptions) (driver.Stream, error) {
	return nil, fmt.Errorf("local: live video: %w", driver.ErrUnsupported)
}

// WaitSettled waits until the screen has been free of window and content
// events for a moment.
func (d *Device) WaitSettled(ctx context.Context, max time.Duration) (bool, error) {
	ok, err := d.c.WaitSettled(ctx, max)
	return ok, d.wrap(err)
}

func (d *Device) wrap(err error) error {
	var ce *companion.Error
	if errors.As(err, &ce) && ce.Code == "own_ui" {
		return ErrOwnUI
	}
	if errors.Is(err, companion.ErrNotRunning) {
		return fmt.Errorf("local: %w: %w", driver.ErrOffline, err)
	}
	return err
}

var (
	_ driver.Device       = (*Device)(nil)
	_ driver.Provider     = (*Provider)(nil)
	_ driver.Settler      = (*Device)(nil)
	_ driver.LockReporter = (*Device)(nil)
)
