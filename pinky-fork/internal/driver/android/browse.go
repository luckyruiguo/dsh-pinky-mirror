package android

import (
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/driver/android/mdns"
)

// Browser finds the phones' wireless debugging services on the network
// without the adb server. The server's own discovery stops working after
// the computer's network changes (an interface comes or goes, as when an
// iPhone is plugged in, a VPN starts or the Mac wakes) and stays broken
// until the server restarts, which the driver never does: that would drop
// the connections of every other tool and emulator.
type Browser interface {
	// Browse starts searching for services of the types, such as
	// adb.ServicePairing, until the search is closed.
	Browse(types ...string) (Browse, error)
}

// Browse is a search in progress.
type Browse interface {
	// Services returns the services found so far that have an address.
	Services() []adb.MDNSService
	// Err says why the search cannot work, nil while it can. It wraps
	// mdns.ErrNotAllowed when the system does not let Pinky use the
	// local network.
	Err() error
	Close() error
}

// mdnsBrowser is the Browser of package mdns.
type mdnsBrowser struct{}

func (mdnsBrowser) Browse(types ...string) (Browse, error) {
	b, err := mdns.Browse(types, mdns.Options{})
	if err != nil {
		return nil, err
	}
	return mdnsBrowse{b}, nil
}

type mdnsBrowse struct{ *mdns.Browser }

func (b mdnsBrowse) Services() []adb.MDNSService {
	var out []adb.MDNSService
	for _, s := range b.Browser.Services() {
		if a := s.Addr(); a != "" {
			out = append(out, adb.MDNSService{Instance: s.Instance, Type: s.Type, Addr: a})
		}
	}
	return out
}

// browse starts a search with the configured Browser, or returns nil and
// the reason when it cannot start.
func (p *Provider) browse(types ...string) (Browse, error) {
	b, err := p.cfg.Browser.Browse(types...)
	if err != nil {
		p.cfg.logf("android: cannot search the network for phones: %v", err)
		return nil, err
	}
	return b, nil
}
