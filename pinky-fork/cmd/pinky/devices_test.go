package main

import (
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// TestDevicesStatus: without a gateway, a phone that waits for a step
// needs setup, as the gateway lists it; the STATUS column says the
// driver's detail once, also when the gateway's line already ends with it.
func TestDevicesStatus(t *testing.T) {
	allow := "Unlock the phone and allow USB debugging for this computer."
	for _, c := range []struct {
		info driver.Info
		want gateway.DeviceStatus
		col  string
	}{
		{driver.Info{State: driver.StateOnline}, gateway.DeviceIdle, "online"},
		{driver.Info{State: driver.StateUnauthorized, Detail: allow}, gateway.DeviceNeedsSetup, "unauthorized · " + allow},
		{driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupDeveloperMode, Detail: "Turn on Developer Mode."}, gateway.DeviceNeedsSetup, "needs_setup · Turn on Developer Mode."},
		{driver.Info{State: driver.StateOffline, Detail: "The phone is in recovery mode."}, gateway.DeviceOffline, "offline · The phone is in recovery mode."},
	} {
		v := listedView(c.info)
		if v.Status != c.want || statusColumn(v) != c.col {
			t.Errorf("%s: status %q, column %q; want %q, %q", c.info.State, v.Status, statusColumn(v), c.want, c.col)
		}
	}
	// From the gateway.
	for _, c := range []struct {
		v   gateway.DeviceView
		col string
	}{
		{gateway.DeviceView{Info: driver.Info{State: driver.StateOffline, Detail: "Unplugged"}, Status: gateway.DeviceOffline, StatusLine: "Offline · Unplugged"}, "Offline · Unplugged"},
		{gateway.DeviceView{Info: driver.Info{State: driver.StateOffline}, Status: gateway.DeviceOffline, StatusLine: "Offline"}, "Offline"},
		{gateway.DeviceView{Info: driver.Info{State: driver.StateUnauthorized, Detail: allow}, Status: gateway.DeviceNeedsSetup, StatusLine: "Needs setup"}, "Needs setup · " + allow},
		{gateway.DeviceView{Info: driver.Info{State: driver.StateOnline}, Status: gateway.DeviceIdle, StatusLine: "Idle"}, "Idle"},
	} {
		if got := statusColumn(c.v); got != c.col {
			t.Errorf("%q: column %q, want %q", c.v.StatusLine, got, c.col)
		}
	}
}
