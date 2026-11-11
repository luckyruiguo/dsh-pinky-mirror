package gateway

import "github.com/DIYgod/pinky/internal/driver"

// HostChecks says, by platform, what the computer lacks for that
// platform's phones (driver.HostChecker), such as usbmuxd for iPhones on
// Windows and Linux. Platforms that lack nothing are left out. It asks the
// providers, which answer from what their last List found.
func (g *Gateway) HostChecks() map[driver.Platform]driver.HostCheck {
	out := map[driver.Platform]driver.HostCheck{}
	for _, p := range g.opt.Providers {
		if hc, ok := p.(driver.HostChecker); ok {
			if c := hc.CheckHost(); c.Missing != "" {
				out[p.Platform()] = c
			}
		}
	}
	return out
}
