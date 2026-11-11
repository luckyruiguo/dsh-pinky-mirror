package gateway

import (
	"sync/atomic"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

// closableProvider is a provider that holds something of its own.
type closableProvider struct {
	listedProvider
	closed atomic.Int32
}

func (p *closableProvider) Close() error {
	p.closed.Add(1)
	return nil
}

// TestCloseClosesProviders: closing the gateway closes the providers that
// can be closed, once.
func TestCloseClosesProviders(t *testing.T) {
	p := &closableProvider{listedProvider: listedProvider{info: driver.Info{ID: "p1", Platform: driver.IOS, State: driver.StateOnline}}}
	g := checkupGateway(t, p)
	g.Close()
	g.Close()
	if n := p.closed.Load(); n != 1 {
		t.Fatalf("provider closed %d times, want once", n)
	}
}
