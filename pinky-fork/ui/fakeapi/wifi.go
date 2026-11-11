package fakeapi

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// iPhones and iPads over Wi-Fi, as the gateway's (gateway.SetWiFi,
// UseFoundPhone, IgnoreFoundPhone): a phone found on Wi-Fi waits for Use
// or Ignore; Use takes it through being prepared to online.

// FindOnWiFi puts an iPad on the list that Pinky found on Wi-Fi and has
// not used, as the iOS driver lists one (driver.SetupConfirmWiFi). It
// returns its id. Nothing shows while Wi-Fi is off.
func (a *API) FindOnWiFi() string {
	a.mu.Lock()
	a.nextID++
	id := fmt.Sprintf("ipad-%d", a.nextID)
	d := &device{
		info: driver.Info{ID: id, Platform: driver.IOS, Name: "Sam's iPad", Model: "iPad Pro 11-inch (3rd generation)", OSVersion: "27.0.1",
			Connection: "wifi", Backend: "coredevice", State: driver.StateNeedsSetup, Setup: driver.SetupConfirmWiFi,
			Detail: "Found on Wi-Fi: this iPhone or iPad was paired with this computer before, in Finder or Xcode, and Pinky has not used it. Pinky leaves it alone until you choose Use in Pinky."},
		width: 1668, height: 2388, codec: driver.HEVC, source: "testsrc",
	}
	if !a.wifiOff {
		a.devices = append(a.devices, d)
	}
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: id})
	return id
}

// SetWiFi implements the client's switch: off, phones on Wi-Fi leave the
// list.
func (a *API) SetWiFi(on bool) {
	a.mu.Lock()
	a.wifiOff = !on
	if !on {
		a.devices = slices.DeleteFunc(a.devices, func(d *device) bool {
			return d.info.Connection == "wifi" && d.info.Platform == driver.IOS
		})
	}
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices})
}

// WiFi reports whether iPhones over Wi-Fi are on.
func (a *API) WiFi() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.wifiOff
}

// UseFoundPhone implements the client's Use: the phone is prepared, then
// online, a step every couple of seconds at Speed (or at once with
// HoldSetup: then AdvanceIPhone moves it on).
func (a *API) UseFoundPhone(deviceID string) error {
	a.mu.Lock()
	d := a.device(deviceID)
	if d == nil {
		a.mu.Unlock()
		return fmt.Errorf("fakeapi: no device %q: %w", deviceID, gateway.ErrUnknownDevice)
	}
	gen := a.gen
	setStep(&d.info, iphoneSteps[2]) // preparing
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: deviceID})
	if a.opt.HoldSetup {
		return nil
	}
	go func() {
		if a.sleepGen(gen, 2*time.Second) {
			a.SetIPhone(deviceID, iphoneSteps[3])
		}
	}()
	return nil
}

// IgnoreFoundPhone implements the client's Ignore: the phone leaves the
// list, as RemoveDevice does.
func (a *API) IgnoreFoundPhone(ctx context.Context, deviceID string) error {
	return a.RemoveDevice(ctx, deviceID, gateway.RemoveOptions{})
}

// SetConnecting shows a phone as being opened, with the gateway's status
// line ("Connecting", "Moving to Wi-Fi", "Moving to the cable"), or, with
// "", as it was.
func (a *API) SetConnecting(deviceID, line string) {
	a.mu.Lock()
	if d := a.device(deviceID); d != nil {
		d.connecting = line
	}
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: deviceID})
}

// LoseOnWiFi shows a phone as one macOS's device service lost on Wi-Fi
// (driver.SetupDeviceService).
func (a *API) LoseOnWiFi(deviceID string) {
	a.mu.Lock()
	if d := a.device(deviceID); d != nil {
		d.info.Connection = "wifi"
		setStep(&d.info, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupDeviceService,
			Detail: "This Mac's device service (usbmuxd) lost the iPhone on Wi-Fi."})
	}
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: deviceID})
}

// RestartDeviceService implements the client's restart: the phones the
// service lost come back online, or it fails with what SetRestartError set.
func (a *API) RestartDeviceService(ctx context.Context, deviceID, reason string) error {
	a.mu.Lock()
	a.restarts = append(a.restarts, reason)
	err := a.restartErr
	var back []string
	if err == nil {
		for _, d := range a.devices {
			if d.info.Setup == driver.SetupDeviceService {
				setStep(&d.info, driver.Info{State: driver.StateOnline})
				back = append(back, d.info.ID)
			}
		}
	}
	a.mu.Unlock()
	for _, id := range back {
		a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: id})
	}
	return err
}

// Restarts returns the reasons RestartDeviceService was called with.
func (a *API) Restarts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.restarts)
}

// SetRestartError makes RestartDeviceService fail with err.
func (a *API) SetRestartError(err error) {
	a.mu.Lock()
	a.restartErr = err
	a.mu.Unlock()
}

// ChangeOnWiFi shows an Android phone as the driver lists one whose
// connection over the network leads to a device that lacks the mark Pinky
// left on the phone (driver.SetupPhoneChanged).
func (a *API) ChangeOnWiFi(deviceID string) {
	a.mu.Lock()
	if d := a.device(deviceID); d != nil {
		d.info.Connection = "wifi"
		setStep(&d.info, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupPhoneChanged,
			Detail: "This is not the phone Pinky set up under this name: it lacks the mark Pinky left on it. Trust it if you reset it or paired it again, or plug it in."})
	}
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: deviceID})
}

// TrustPhone implements the client's Trust this phone: the phone comes
// back online.
func (a *API) TrustPhone(ctx context.Context, deviceID string) error {
	a.mu.Lock()
	d := a.device(deviceID)
	if d == nil {
		a.mu.Unlock()
		return fmt.Errorf("fakeapi: no device %q: %w", deviceID, gateway.ErrUnknownDevice)
	}
	a.trusted = append(a.trusted, deviceID)
	setStep(&d.info, driver.Info{State: driver.StateOnline})
	a.mu.Unlock()
	a.emit(gateway.Event{Kind: gateway.EventDevices, DeviceID: deviceID})
	return nil
}

// Trusted returns the phones TrustPhone was called for.
func (a *API) Trusted() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.trusted)
}
