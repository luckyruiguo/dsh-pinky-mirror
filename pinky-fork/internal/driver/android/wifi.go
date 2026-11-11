package android

import (
	"context"
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// Wi-Fi for phones without Wireless debugging (driver.WiFiSwitch), which
// Android 10 lacks: a phone plugged in by cable has its adb listen on the
// network (network.go), and the driver connects to it at its Wi-Fi
// address, so the person can unplug it. The driver connects to it again
// whenever it is back on the network, at the address it keeps or at a new
// one mDNS finds. Once the phone restarts its adb no longer listens: the
// phone is offline with driver.SetupNetworkOff until it is plugged in and
// moved to Wi-Fi again.

// PhoneWiFi implements driver.WiFiSwitch.
func (p *Provider) PhoneWiFi(ctx context.Context, id string) (driver.WiFiState, error) {
	n, kept := p.net.get(id)
	on := kept && n.LAN != ""
	out := driver.WiFiState{On: on, Port: n.port(), Away: n.Tailnet != ""}
	if on {
		out.Addr = n.LAN
	}
	entries, err := p.devices(ctx)
	if err != nil {
		return out, wrapErr(err)
	}
	out.Connected = on && online(entries, n.lanSerial())
	c := p.conns(ctx, entries, id)
	switch {
	case c.usb != "":
		wifi, _, err := p.phoneAddrs(ctx, c.usb)
		if err != nil {
			return out, err
		}
		if !on && wifi.IsValid() {
			out.Addr = wifi.String()
		}
		out.Listening = out.Connected || p.listenPort(ctx, c.usb) == AwayPort
		if !wifi.IsValid() {
			out.Missing = driver.WiFiNoWiFi
			out.Detail = "The phone is not on Wi-Fi: connect it to the same Wi-Fi network as this computer."
		}
	case out.Connected:
		out.Listening = true
	case !on && c.local == "":
		out.Missing = driver.WiFiNeedsCable
		out.Detail = "Plug the phone in to move it to Wi-Fi."
	}
	out.Stopped = on && !out.Listening && (c.usb != "" || p.link.refused(n))
	return out, nil
}

// SetPhoneWiFi implements driver.WiFiSwitch. On has the phone's adb listen
// on the network over its cable (or another connection it has) and connects
// at its Wi-Fi address; a phone that cannot be reached there stops
// listening again. Off has its adb stop listening, which also ends its
// use away from home, and forgets its addresses.
func (p *Provider) SetPhoneWiFi(ctx context.Context, id string, on bool) (driver.WiFiState, error) {
	if !on {
		n, _ := p.net.get(id)
		if err := p.net.update(id, func(m *netPhone) { *m = netPhone{} }); err != nil {
			return driver.WiFiState{}, err
		}
		if err := p.stopListening(ctx, id, n); err != nil {
			return driver.WiFiState{}, err
		}
		// adbd restarts: what the phone says may have to wait, and its
		// cable's connection may be gone for a moment.
		st, err := p.PhoneWiFi(ctx, id)
		if err != nil {
			return driver.WiFiState{Port: AwayPort}, nil
		}
		if st.Missing == driver.WiFiNeedsCable {
			st.Missing, st.Detail = "", ""
		}
		return st, nil
	}
	entries, err := p.devices(ctx)
	if err != nil {
		return driver.WiFiState{}, wrapErr(err)
	}
	c := p.conns(ctx, entries, id)
	src := c.usb
	if src == "" {
		src = c.local
	}
	if src == "" {
		return driver.WiFiState{}, fmt.Errorf("android: %s: plug the phone in to move it to Wi-Fi: %w", id, driver.ErrOffline)
	}
	wifi, _, err := p.phoneAddrs(ctx, src)
	if err != nil {
		return driver.WiFiState{}, err
	}
	if !wifi.IsValid() {
		return driver.WiFiState{}, fmt.Errorf("android: %s is not on Wi-Fi: connect it to the same Wi-Fi network as this computer: %w", id, driver.ErrUnsupported)
	}
	name := id
	if pr, err := p.deviceProps(ctx, entryOf(entries, src)); err == nil {
		name = pr.Name
	}
	was := p.listenPort(ctx, src) == AwayPort
	if err := p.listen(ctx, id, src); err != nil {
		return driver.WiFiState{}, err
	}
	serial := joinAddr(wifi.String(), AwayPort)
	if err := p.connectWhenUp(ctx, serial); err != nil {
		if !was {
			// Not reachable there (another network, or one that keeps
			// its devices apart): leave nothing listening.
			p.undoListen(ctx, id, src)
		}
		return driver.WiFiState{}, fmt.Errorf("android: %s: connecting over Wi-Fi at %s (is it on the same Wi-Fi network as this computer?): %w", id, serial, err)
	}
	if err := p.net.update(id, func(n *netPhone) { n.Name, n.Port, n.LAN = name, AwayPort, wifi.String() }); err != nil {
		return driver.WiFiState{}, err
	}
	p.link.forgive(serial)
	p.link.soon()
	return p.PhoneWiFi(ctx, id)
}

// undoListen has the phone's adb stop listening on the network again after
// it could not be used, over the connection it was turned on with (which
// comes back once adbd restarted).
func (p *Provider) undoListen(ctx context.Context, id, serial string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	for {
		err := p.cfg.Client.USB(ctx, serial)
		if err == nil {
			p.cfg.logf("android: %s: adb stopped listening on the network again", id)
			return
		}
		if sleepCtx(ctx, time.Second) != nil {
			p.cfg.logf("android: %s: adb still listens on the network (adb usb: %v); it stops when the phone restarts", id, err)
			return
		}
	}
}

// online reports whether the connection with this serial is online.
func online(entries []adb.DeviceEntry, serial string) bool {
	for _, e := range entries {
		if e.Serial == serial && e.State == adb.StateDevice {
			return true
		}
	}
	return false
}

var _ driver.WiFiSwitch = (*Provider)(nil)
