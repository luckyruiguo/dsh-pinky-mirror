package fakeapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Tailscale (driver.Tailnet): this computer's Tailscale runs, signed in to
// someone@example.com, with a Pixel 8a on the tailnet that does not take
// connections until the person sets it up (SetAway) or gives the port
// Wireless debugging shows. SetTailnet changes what Tailscale reports.

type fakeTailnet struct {
	st        driver.TailnetStatus
	listening map[string]bool // addresses where adb listens, "addr:port"
	away      map[string]driver.Away
	wifi      map[string]driver.WiFiState // phones moved to Wi-Fi (wifiswitch)
}

// tailnetLocked returns the fake's Tailscale, set up on first use.
func (a *API) tailnetLocked() *fakeTailnet {
	if a.tn == nil {
		a.tn = &fakeTailnet{
			st: driver.TailnetStatus{State: driver.TailnetRunning, Tailnet: "someone@example.com", Self: "Someone's MacBook",
				Phones: []driver.TailnetPhone{{Name: "pixel-8a", Addr: "100.101.2.2", Online: true}}},
			listening: map[string]bool{},
			away:      map[string]driver.Away{},
			wifi:      map[string]driver.WiFiState{},
		}
	}
	return a.tn
}

// SetTailnet sets what Tailscale on this computer reports.
func (a *API) SetTailnet(st driver.TailnetStatus) {
	a.mu.Lock()
	a.tailnetLocked().st = st
	a.mu.Unlock()
}

// SetPhoneAway sets what Away says about a phone, as Tailscale off on it
// (driver.AwayNoTailscaleThere) or on another tailnet.
func (a *API) SetPhoneAway(deviceID string, aw driver.Away) {
	a.mu.Lock()
	a.tailnetLocked().away[deviceID] = aw
	a.mu.Unlock()
}

// Tailnet implements the client's tailnetter.
func (a *API) Tailnet(ctx context.Context) (driver.TailnetStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.tailnetLocked().st
	st.Phones = append([]driver.TailnetPhone(nil), st.Phones...)
	return st, nil
}

// ConnectTailnet connects the fake phone on the tailnet: at port 5555 once
// it is set up, or at any other port (Wireless debugging's).
func (a *API) ConnectTailnet(ctx context.Context, addr string) (driver.Info, error) {
	select {
	case <-ctx.Done():
		return driver.Info{}, ctx.Err()
	case <-time.After(time.Duration(float64(900*time.Millisecond) / a.opt.Speed)):
	}
	host, port, found := strings.Cut(addr, ":")
	if !found {
		port = "5555"
	}
	a.mu.Lock()
	tn := a.tailnetLocked()
	i := -1
	for j, ph := range tn.st.Phones {
		if ph.Addr == host {
			i = j
		}
	}
	if i < 0 {
		a.mu.Unlock()
		return driver.Info{}, fmt.Errorf("android: no device at %s on the tailnet", host)
	}
	if port == "5555" && !tn.listening[host+":5555"] {
		a.mu.Unlock()
		return driver.Info{}, fmt.Errorf("android: %s:5555: failed to connect: Connection refused: %w", host, driver.ErrNotListening)
	}
	a.nextID++
	d := &device{
		info: driver.Info{ID: fmt.Sprintf("pixel-8a-tailnet-%d", a.nextID), Platform: driver.Android, Name: "Pixel 8a",
			Model: "Pixel 8a", OSVersion: "16", Connection: "wifi", Via: driver.ViaTailscale, Backend: "scrcpy", State: driver.StateOnline},
		width: 1080, height: 2400, codec: driver.H264, source: "rgbtestsrc", companion: driver.CompanionOn,
	}
	a.devices = append(a.devices, d)
	tn.st.Phones[i].ID, tn.st.Phones[i].Connected = d.info.ID, true
	if port == "5555" {
		tn.away[d.info.ID] = driver.Away{On: true, Listening: true, Addr: host, Port: 5555}
	}
	info := d.info
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: info.ID})
	return info, nil
}

// Away implements the client's tailnetter: an Android phone connected by
// cable or Wi-Fi can be set up while Tailscale runs here.
func (a *API) Away(ctx context.Context, deviceID string) (driver.Away, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	d := a.device(deviceID)
	if d == nil {
		return driver.Away{}, fmt.Errorf("fakeapi: no device %q: %w", deviceID, driver.ErrNotFound)
	}
	if d.info.Platform != driver.Android {
		return driver.Away{}, fmt.Errorf("%s cannot be reached through Tailscale: %w", d.info.Name, driver.ErrUnsupported)
	}
	tn := a.tailnetLocked()
	if aw, ok := tn.away[deviceID]; ok {
		return aw, nil
	}
	aw := driver.Away{Port: 5555, Addr: "100.101.3.3"}
	switch {
	case tn.st.State != driver.TailnetRunning:
		aw.Addr, aw.Missing = "", driver.AwayNoTailscaleHere
	case d.info.State != driver.StateOnline:
		aw.Missing = driver.AwayNotConnected
	}
	return aw, nil
}

// SetAway implements the client's tailnetter, taking a moment as adb
// restarts.
func (a *API) SetAway(ctx context.Context, deviceID string, on bool) (driver.Away, error) {
	aw, err := a.Away(ctx, deviceID)
	if err != nil {
		return aw, err
	}
	if on && aw.Missing != "" {
		return aw, fmt.Errorf("fakeapi: %s: %w", aw.Missing, driver.ErrUnsupported)
	}
	select {
	case <-ctx.Done():
		return driver.Away{}, ctx.Err()
	case <-time.After(time.Duration(float64(1500*time.Millisecond) / a.opt.Speed)):
	}
	a.mu.Lock()
	tn := a.tailnetLocked()
	if on {
		aw = driver.Away{On: true, Listening: true, Addr: aw.Addr, Port: 5555}
		tn.away[deviceID] = aw
		tn.listening[aw.Addr+":5555"] = true
	} else {
		delete(tn.away, deviceID)
		delete(tn.listening, aw.Addr+":5555")
		aw = driver.Away{Port: 5555, Addr: aw.Addr}
	}
	a.mu.Unlock()
	return aw, nil
}

// AddAndroid10 adds an online Android 10 phone on a cable, which has no
// Wireless debugging, and returns its id.
func (a *API) AddAndroid10(name string) string {
	a.mu.Lock()
	a.nextID++
	id := fmt.Sprintf("%s-%d", strings.ToLower(strings.ReplaceAll(name, " ", "-")), a.nextID)
	d := &device{
		info: driver.Info{ID: id, Platform: driver.Android, Name: name, Model: name, OSVersion: "10",
			Connection: "usb", Backend: "scrcpy", State: driver.StateOnline},
		width: 1080, height: 1920, codec: driver.H264, source: "rgbtestsrc", companion: driver.CompanionOn,
	}
	a.devices = append(a.devices, d)
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: id})
	return id
}

// RestartOnWiFi has a phone moved to Wi-Fi restart off its cable: its adb
// no longer listens on the network, so it is offline until plugged in.
func (a *API) RestartOnWiFi(deviceID string) {
	a.mu.Lock()
	if d := a.device(deviceID); d != nil {
		d.info.State, d.info.Connection, d.info.Setup = driver.StateOffline, "wifi", driver.SetupNetworkOff
		tn := a.tailnetLocked()
		st := tn.wifi[deviceID]
		st.Listening, st.Connected, st.Stopped = false, false, true
		tn.wifi[deviceID] = st
	}
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: deviceID})
}

// PhoneWiFi implements the client's wifiSwitcher: an Android phone on a
// cable can be moved to Wi-Fi.
func (a *API) PhoneWiFi(ctx context.Context, deviceID string) (driver.WiFiState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	d := a.device(deviceID)
	if d == nil {
		return driver.WiFiState{}, fmt.Errorf("fakeapi: no device %q: %w", deviceID, driver.ErrNotFound)
	}
	if d.info.Platform != driver.Android {
		return driver.WiFiState{}, fmt.Errorf("%s cannot move to Wi-Fi this way: %w", d.info.Name, driver.ErrUnsupported)
	}
	tn := a.tailnetLocked()
	if st, ok := tn.wifi[deviceID]; ok {
		return st, nil
	}
	st := driver.WiFiState{Port: 5555, Addr: "192.168.1.42"}
	if d.info.Connection != "usb" || d.info.State != driver.StateOnline {
		st.Addr, st.Missing = "", driver.WiFiNeedsCable
	}
	return st, nil
}

// SetPhoneWiFi implements the client's wifiSwitcher, taking a moment as adb
// restarts.
func (a *API) SetPhoneWiFi(ctx context.Context, deviceID string, on bool) (driver.WiFiState, error) {
	st, err := a.PhoneWiFi(ctx, deviceID)
	if err != nil {
		return st, err
	}
	if on && st.Missing != "" {
		return st, fmt.Errorf("fakeapi: %s: %w", st.Missing, driver.ErrOffline)
	}
	select {
	case <-ctx.Done():
		return driver.WiFiState{}, ctx.Err()
	case <-time.After(time.Duration(float64(1500*time.Millisecond) / a.opt.Speed)):
	}
	a.mu.Lock()
	tn := a.tailnetLocked()
	if on {
		st = driver.WiFiState{On: true, Connected: true, Listening: true, Addr: nonEmpty(st.Addr, "192.168.1.42"), Port: 5555}
		tn.wifi[deviceID] = st
		if d := a.device(deviceID); d != nil {
			d.info.Setup = ""
		}
	} else {
		delete(tn.wifi, deviceID)
		st = driver.WiFiState{Port: 5555}
	}
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: deviceID})
	return st, nil
}

func nonEmpty(s, or string) string {
	if s == "" {
		return or
	}
	return s
}
