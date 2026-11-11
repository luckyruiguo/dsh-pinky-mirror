package gateway

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
)

// extraDev adds the optional driver features of round three to a demo
// phone: waking, unlocking with a PIN, a Pinky folder and background
// displays.
type extraDev struct {
	*demo.Device

	mu       sync.Mutex
	pin      string
	entered  []string // PINs entered
	screenOn bool
	wakes    int
	files    map[string]memFile
	pushes   int
	bgs      []*bgDisplay
	refuse   map[string]bool // apps that will not run on a background display
	bgErr    error           // OpenBackground fails with it

	pkgs      map[string]driver.Package // phone_app: a file holding a key is that package
	installed map[string]string         // phone_app: scripted packages installed, id to version
}

type memFile struct {
	data  []byte
	mtime time.Time
}

func newExtraDev(id, name string) *extraDev {
	return &extraDev{Device: demo.NewDevice(id, name, driver.Android), screenOn: true, files: map[string]memFile{},
		refuse: map[string]bool{}}
}

func (x *extraDev) ScreenOn(context.Context) (bool, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.screenOn, nil
}

func (x *extraDev) Wake(context.Context) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if !x.screenOn {
		x.wakes++
		x.screenOn = true
	}
	return nil
}

func (x *extraDev) setScreen(on bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.screenOn = on
}

func (x *extraDev) Unlock(_ context.Context, pin string) error {
	x.mu.Lock()
	x.entered = append(x.entered, pin)
	ok := pin == x.pin
	x.mu.Unlock()
	if !ok {
		return fmt.Errorf("still locked: %w", driver.ErrLocked)
	}
	x.SetLocked(false)
	return nil
}

func (x *extraDev) ListFiles(context.Context) ([]driver.FileInfo, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []driver.FileInfo
	dirs := map[string]bool{}
	for p, f := range x.files {
		out = append(out, driver.FileInfo{Path: p, Size: int64(len(f.data)), ModTime: f.mtime})
		for d := path.Dir(p); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	for d := range dirs {
		out = append(out, driver.FileInfo{Path: d, Dir: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (x *extraDev) Pull(_ context.Context, p string, w io.Writer) error {
	x.mu.Lock()
	f, ok := x.files[p]
	x.mu.Unlock()
	if !ok || strings.Contains(p, "..") {
		return fmt.Errorf("%s: %w", p, driver.ErrNotFound)
	}
	_, err := w.Write(f.data)
	return err
}

func (x *extraDev) Push(_ context.Context, p string, r io.Reader, size int64, mtime time.Time) error {
	if strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
		return fmt.Errorf("%s: %w", p, driver.ErrNotFound)
	}
	var b bytes.Buffer
	if _, err := io.Copy(&b, r); err != nil {
		return err
	}
	if int64(b.Len()) != size {
		return fmt.Errorf("pushed %d bytes, want %d", b.Len(), size)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.files[p] = memFile{data: b.Bytes(), mtime: mtime}
	x.pushes++
	return nil
}

func (x *extraDev) OpenBackground(context.Context, driver.BackgroundOptions) (driver.Device, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.bgErr != nil {
		return nil, x.bgErr
	}
	b := &bgDisplay{Device: demo.NewDevice(x.Info().ID+"-bg", "background", driver.Android), x: x}
	x.bgs = append(x.bgs, b)
	return b, nil
}

func (x *extraDev) backgrounds() []*bgDisplay {
	x.mu.Lock()
	defer x.mu.Unlock()
	return append([]*bgDisplay(nil), x.bgs...)
}

// bgDisplay is a background display: a demo phone of its own that is never
// locked, closed with Close.
type bgDisplay struct {
	*demo.Device
	x *extraDev

	mu     sync.Mutex
	closed bool
}

func (b *bgDisplay) Launch(ctx context.Context, app string) error {
	b.x.mu.Lock()
	refused := b.x.refuse[app]
	b.x.mu.Unlock()
	if refused {
		return fmt.Errorf("%s opened on the main display: %w", app, driver.ErrUnsupported)
	}
	return b.Device.Launch(ctx, app)
}

func (b *bgDisplay) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *bgDisplay) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// The demo's LockReporter would report the background display's own
// (never set) lock; a background display has none of its own.
var _ driver.LockReporter = (*bgDisplay)(nil)

type extraProvider struct {
	*demo.Provider
	x *extraDev
}

func (p extraProvider) Open(context.Context, string) (driver.Device, error) { return p.x, nil }

var (
	_ driver.Waker        = (*extraDev)(nil)
	_ driver.Unlocker     = (*extraDev)(nil)
	_ driver.Files        = (*extraDev)(nil)
	_ driver.Backgrounder = (*extraDev)(nil)
)

// newExtraGateway runs a gateway on one extraDev, with history in a
// temporary folder.
func newExtraGateway(t *testing.T, x *extraDev, opts ...func(*Options)) *Gateway {
	t.Helper()
	pol := testPolicy(t)
	o := Options{
		Providers: []driver.Provider{extraProvider{demo.NewProvider(driver.Android, x.Device), x}}, Policy: pol,
		TrustNewAgents: true, DataDir: t.TempDir(), ApprovalTimeout: 3 * time.Second, MaxBlock: 2 * time.Second,
		SettleMax: 200 * time.Millisecond, PollInterval: 5 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond,
		NoScreenshots: true,
	}
	for _, f := range opts {
		f(&o)
	}
	g, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

// waitNeedKind waits for a Needs you card of a kind.
func waitNeedKind(t *testing.T, g *Gateway, kind NeedKind) NeedView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, n := range g.Needs() {
			if n.Kind == kind {
				return n
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no %s card: %+v", kind, g.Needs())
	return NeedView{}
}

// taskSteps returns the steps of a session's task, running or ended.
func taskSteps(g *Gateway, s *Session) []StepView {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, l := range s.leases {
		return append([]StepView(nil), l.task.steps...)
	}
	for i := len(g.ended) - 1; i >= 0; i-- {
		if t := g.ended[i]; t.sessionID == s.id {
			return append([]StepView(nil), t.steps...)
		}
	}
	return nil
}

// newIPhoneGateway runs a gateway on a demo iPhone, which has neither a
// Pinky folder nor background displays.
func newIPhoneGateway(t *testing.T) *Gateway {
	t.Helper()
	dev := demo.NewDevice("iphone", "iPhone 17", driver.IOS)
	g, err := New(Options{Providers: []driver.Provider{demo.NewProvider(driver.IOS, dev)}, TrustNewAgents: true,
		DataDir: t.TempDir(), NoScreenshots: true, DiscoverEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}
