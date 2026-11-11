package ios

import (
	goios "github.com/danielpaulus/go-ios/ios"

	"github.com/DIYgod/pinky/internal/driver"
)

// Phones the person paused in Pinky (driver.Pauser). The gateway neither
// opens them nor keeps their sessions, so no display stream runs and no
// tunnel stays up. List still lists them, by usbmuxd's entry alone: it
// does not look at their developer services, which takes a CoreDevice
// tunnel each time, and it ends the helper's runner that waited for the
// passcode and the session that waited for the camera and microphone. Once
// the person resumes one, List looks at it afresh.

// SetPaused implements driver.Pauser.
func (p *Provider) SetPaused(id string, paused bool) {
	p.mu.Lock()
	var stop *wdaStart
	if paused {
		if p.paused == nil {
			p.paused = map[string]bool{}
		}
		p.paused[id] = true
		stop = p.wdaWaits[id]
		delete(p.wdaWaits, id)
		delete(p.wdaHolds, id)
		p.stopMediaWaitLocked(id)
	} else if p.paused[id] {
		delete(p.paused, id)
		delete(p.known, id) // inspected again at the next List
	}
	p.mu.Unlock()
	if stop != nil {
		go stop.stop()
	}
	if paused {
		p.logger().Info("iphone: paused", "device", id)
	}
}

// pausedInfo is what List says about a paused phone: what it knew of the
// phone before, or its id alone, over the connection usbmuxd lists it on
// now. ok is false for a phone that is not paused.
func (p *Provider) pausedInfo(e goios.DeviceEntry) (driver.Info, bool) {
	id := e.Properties.SerialNumber
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.paused[id] {
		return driver.Info{}, false
	}
	info := driver.Info{ID: id, Platform: driver.IOS, State: driver.StateOffline}
	if k, ok := p.known[id]; ok {
		info = k.info
	}
	info.Connection = "usb"
	if e.Properties.ConnectionType == "Network" {
		info.Connection = "wifi"
	}
	return info, true
}

var _ driver.Pauser = (*Provider)(nil)
