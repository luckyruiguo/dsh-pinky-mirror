package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// failDev is a demo phone whose next read or tap fails with an error.
type failDev struct {
	*demo.Device
	mu      sync.Mutex
	readErr error
	tapErr  error
}

func (d *failDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	d.mu.Lock()
	err := d.readErr
	d.readErr = nil
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return d.Device.Observe(ctx, opt)
}

func (d *failDev) Tap(ctx context.Context, p screen.Point, opt driver.TapOptions) error {
	d.mu.Lock()
	err := d.tapErr
	d.tapErr = nil
	d.mu.Unlock()
	if err != nil {
		return err
	}
	return d.Device.Tap(ctx, p, opt)
}

// clientTimeout is the error an HTTP client gives up with, as
// WebDriverAgent's does after 30 s.
func clientTimeout(t *testing.T) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()
	_, err := (&http.Client{Timeout: 10 * time.Millisecond}).Get(srv.URL + "/source")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the client's timeout is %v", err)
	}
	return fmt.Errorf("WebDriverAgent /source: %w", err)
}

func failGateway(t *testing.T) (*Session, *failDev, *Result) {
	t.Helper()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &failDev{Device: base}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Timeouts")
	return s, dev, first
}

// A read or an action that ran out of time says so; only a cancelled call
// says it was cancelled.
func TestTimeoutIsNotCancelled(t *testing.T) {
	ctx := context.Background()
	timeout := clientTimeout(t)
	t.Run("read timed out", func(t *testing.T) {
		s, dev, _ := failGateway(t)
		dev.mu.Lock()
		dev.readErr = timeout
		dev.mu.Unlock()
		res := s.Observe(ctx, ObserveRequest{})
		if res.Error != ErrInternal || !strings.Contains(res.Message, "timed out") || strings.Contains(res.Message, "cancelled") {
			t.Fatalf("a read that timed out: %q %q", res.Error, res.Message)
		}
	})
	t.Run("read cancelled", func(t *testing.T) {
		s, dev, _ := failGateway(t)
		dev.mu.Lock()
		dev.readErr = fmt.Errorf("WebDriverAgent /source: %w", context.Canceled)
		dev.mu.Unlock()
		res := s.Observe(ctx, ObserveRequest{})
		if res.Error != ErrInternal || res.Message != "The call was cancelled." {
			t.Fatalf("a cancelled read: %q %q", res.Error, res.Message)
		}
	})
	t.Run("action timed out", func(t *testing.T) {
		s, dev, first := failGateway(t)
		dev.mu.Lock()
		dev.tapErr = timeout
		dev.mu.Unlock()
		res := tap(s, ref(t, first, "Dinely"))
		mustStatus(t, res, StatusUncertain)
		if !strings.Contains(res.Message, "did not answer in time") {
			t.Fatalf("an action that timed out: %q", res.Message)
		}
	})
	t.Run("other calls", func(t *testing.T) {
		s, _, _ := failGateway(t)
		l, res := s.acquire("")
		if res != nil {
			t.Fatalf("acquire: %+v", res)
		}
		defer s.done(l)
		g := s.g
		for name, f := range map[string]func(error) *Result{
			"phone_app":    func(err error) *Result { return g.appError(l, err, "Installing failed") },
			"phone_files":  func(err error) *Result { return g.filesError(l, err) },
			"phone_device": func(err error) *Result { return g.switchError(l, err) },
		} {
			if res := f(timeout); !strings.Contains(res.Message, "did not answer in time") {
				t.Errorf("%s timed out: %q", name, res.Message)
			}
			if res := f(context.Canceled); res.Message != "The call was cancelled." {
				t.Errorf("%s cancelled: %q", name, res.Message)
			}
		}
	})
}
