package gateway

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// blankAfterLaunch is a background display that shows no app for a while
// after each launch, as Android may for an app just started on a virtual
// display (GitHub issue #30): reads give the display's size only.
type blankAfterLaunch struct {
	*bgDisplay
	mu    sync.Mutex
	until time.Time // reads before this show no app; the zero time: none
	never bool      // reads after a launch never show the app
}

func (b *blankAfterLaunch) Launch(ctx context.Context, app string) error {
	if err := b.bgDisplay.Launch(ctx, app); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.until = time.Now().Add(700 * time.Millisecond)
	return nil
}

func (b *blankAfterLaunch) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, err := b.bgDisplay.Observe(ctx, opt)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	blank := !b.until.IsZero() && (b.never || time.Now().Before(b.until))
	b.mu.Unlock()
	if blank {
		return &screen.Screen{Width: s.Width, Height: s.Height, Orientation: s.Orientation, CapturedAt: s.CapturedAt}, nil
	}
	return s, nil
}

type blankDev struct {
	*extraDev
	bg *blankAfterLaunch
}

func (x *blankDev) OpenBackground(ctx context.Context, opt driver.BackgroundOptions) (driver.Device, error) {
	d, err := x.extraDev.OpenBackground(ctx, opt)
	if err != nil {
		return nil, err
	}
	x.bg.bgDisplay = d.(*bgDisplay)
	return x.bg, nil
}

func blankGateway(t *testing.T, never bool) (*Gateway, *blankDev) {
	t.Helper()
	x := newExtraDev("pixel", "Pixel 9")
	bd := &blankDev{extraDev: x, bg: &blankAfterLaunch{never: never}}
	g := newExtraGateway(t, x, func(o *Options) {
		o.Providers = []driver.Provider{extraProvider2{demo.NewProvider(driver.Android, x.Device), bd}}
	})
	g.SetBackground("pixel", true)
	old := launchReadWait
	launchReadWait = 1500 * time.Millisecond
	t.Cleanup(func() { launchReadWait = old })
	return g, bd
}

// An app that shows on the background display a moment after its start:
// the launch waits for it and returns its screen.
func TestLaunchWaitsForTheBackgroundApp(t *testing.T) {
	g, _ := blankGateway(t, false)
	s, _ := connect(t, g, "Lunch")
	res := s.Act(context.Background(), Call{}, Action{Tool: "launch", App: "Dinely"})
	mustStatus(t, res, StatusSettled)
	if res.Screen == nil || res.Screen.App.ID != demo.Dinely || len(res.Screen.Elements) == 0 {
		t.Fatalf("launch result: %+v", res.Screen)
	}
}

// An app that never shows: the launch says so instead of an empty settled
// screen, and so do observe and a screenshot.
func TestLaunchSaysTheBackgroundAppCannotBeRead(t *testing.T) {
	g, _ := blankGateway(t, true)
	s, _ := connect(t, g, "Lunch")
	ctx := context.Background()
	res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"})
	mustStatus(t, res, StatusUncertain)
	if !strings.Contains(res.Message, "Dinely was started on the background display, but Android does not show its screen there yet") {
		t.Fatalf("launch message: %q", res.Message)
	}
	obs := s.Observe(ctx, ObserveRequest{Screenshot: true})
	mustStatus(t, obs, StatusSettled)
	if !strings.Contains(obs.Message, "The background display shows no app") {
		t.Fatalf("observe message: %q", obs.Message)
	}
	if obs.Screenshot != nil || !strings.Contains(obs.Message, "No screenshot: Pinky cannot tell which app is on the screen yet") ||
		strings.Contains(obs.Message, "the screen changed") {
		t.Fatalf("screenshot message: %q", obs.Message)
	}
}
