package android

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

// sentTo returns the serials of the devices the fake server passed
// requests to.
func (f *fakeADB) sentTo() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.transports)
}

// TestLimitLeavesOthersAlone: limited to some phones (PINKY_DEVICES), the
// driver lists and opens only those, and sends the others nothing: no
// getprop, and no connection over the network.
func TestLimitLeavesOthersAlone(t *testing.T) {
	f, p := newFakeADB(t)
	const (
		named      = "adb-PHONE2-a1b2c3._adb-tls-connect._tcp" // Wireless debugging's discovery
		otherNamed = "adb-OTHER2-d4e5f6._adb-tls-connect._tcp"
		kept       = "192.168.1.20:5555" // where Pinky keeps NETPHONE (network.go)
		stranger   = "192.168.1.9:5555"  // a connection Pinky did not make
	)
	serials := []string{"usb-ok", "usb-other", "emulator-5554", named, otherNamed, kept, stranger}
	hw := map[string]string{named: "PHONE2", otherNamed: "OTHER2", kept: "NETPHONE", stranger: "STRANGER"}
	f.set(func(f *fakeADB) {
		f.props = map[string]string{}
		for i, s := range serials {
			f.devices = append(f.devices, fmt.Sprintf("%s\tdevice product:x model:Pixel_9 device:x transport_id:%d", s, i+1))
			f.props[s] = "Pixel 9\n16\n36\nPixel\n" + cmpOr(hw[s], s) + "\npinky-boot=\n"
		}
	})
	for id, lan := range map[string]string{"NETPHONE": "192.168.1.20", "NETOTHER": "192.168.1.21"} {
		if err := p.net.update(id, func(n *netPhone) { n.LAN = lan }); err != nil {
			t.Fatal(err)
		}
	}
	p.link.probe = func(_ context.Context, serial string) error {
		f.set(func(f *fakeADB) { f.probes = append(f.probes, serial) })
		return nil
	}
	allowed := map[string]bool{"usb-ok": true, "PHONE2": true, "NETPHONE": true}
	p.Limit(func(id string) bool { return allowed[id] })

	ctx := context.Background()
	infos, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var online []string
	for _, in := range infos {
		if in.State == driver.StateOnline {
			online = append(online, in.ID)
		}
	}
	slices.Sort(online)
	if !slices.Equal(online, []string{"NETPHONE", "PHONE2", "usb-ok"}) {
		t.Errorf("online %q", online)
	}
	for _, id := range []string{"usb-other", "emulator-5554", "OTHER2", "STRANGER", stranger} {
		if _, err := p.OpenDevice(ctx, id); !errors.Is(err, driver.ErrNotFound) {
			t.Errorf("open %s: %v", id, err)
		}
	}
	// The kept phone Pinky may not use is not connected to.
	entries, err := p.devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.link.reconnect(ctx, p.net.list(), entries)
	if tried, conns := f.tried(), f.connected(); len(tried) != 0 || len(conns) != 0 {
		t.Errorf("probed %q, connected %q", tried, conns)
	}
	for _, s := range f.sentTo() {
		if !slices.Contains([]string{"usb-ok", named, kept}, s) {
			t.Errorf("sent a request to %s", s)
		}
	}
	if !slices.Contains(f.sentTo(), "usb-ok") {
		t.Errorf("did not read usb-ok: %q", f.sentTo())
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// TestLimitLetsAddedPhoneIn: limited to no phone at all, a phone the
// person connects by address is used all the same.
func TestLimitLetsAddedPhoneIn(t *testing.T) {
	f, p := newFakeADB(t)
	const addr = "192.168.1.30:41235"
	f.set(func(f *fakeADB) {
		f.props = map[string]string{addr: "Pixel 9\n16\n36\nPixel\nNEWPHONE\npinky-boot=\n"}
	})
	p.Limit(func(string) bool { return false })
	info, err := p.ConnectWiFi(context.Background(), addr)
	if err != nil || info.ID != "NEWPHONE" || info.State != driver.StateOnline {
		t.Fatalf("connected %+v, %v", info, err)
	}
}

// TestLimitEmulators: limited, the driver asks no emulator it did not
// start about itself, starts one on a console port it picks, and uses
// that one until it quits. A launch on the port it picked that fails
// tries the next free one, never one the emulator would pick.
func TestLimitEmulators(t *testing.T) {
	f, p := newFakeADB(t)
	fx := newAVDFixture(t, p)
	emulators := func(pairs ...string) {
		f.set(func(f *fakeADB) {
			f.devices, f.props = nil, map[string]string{}
			for i := 0; i < len(pairs); i += 2 {
				s, avd := pairs[i], pairs[i+1]
				f.devices = append(f.devices, s+"\tdevice product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a transport_id:"+s[len("emulator-"):])
				f.props[s] = "sdk_gphone64_arm64\n16\n36\nsdk_gphone64_arm64\nEMULATOR36X1X11X0\npinky-boot=\npinky-avd=" + avd + "\n"
			}
		})
	}
	// Another tool's emulator runs pinky-api29.
	emulators("emulator-5554", "pinky-api29")
	p.Limit(func(string) bool { return false })
	p.avd.portFree = func(port int) bool { return port != 5556 }
	ctx := context.Background()

	if v := avdNamed(t, p, "pinky-api29"); v.Running {
		t.Errorf("another tool's emulator: %+v", v)
	}
	if err := p.StartVirtual(ctx, "pinky-api36"); err != nil {
		t.Fatal(err)
	}
	emulators("emulator-5554", "pinky-api29", "emulator-5558", "pinky-api36")
	infos, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != "emulator-5558" || infos[0].VirtualID != "pinky-api36" {
		t.Errorf("listed %+v", infos)
	}
	if v := avdNamed(t, p, "pinky-api36"); !v.Running || v.DeviceID != "emulator-5558" {
		t.Errorf("Pinky's emulator: %+v", v)
	}

	// It quits: no longer Pinky's, even should another take its port.
	fx.mu.Lock()
	quit := fx.quits[0]
	fx.mu.Unlock()
	quit(nil)
	emulators("emulator-5554", "pinky-api29", "emulator-5558", "pinky-api36")
	if infos, _ := p.List(ctx); len(infos) != 0 {
		t.Errorf("listed after it quit: %+v", infos)
	}

	emulators("emulator-5554", "pinky-api29")
	fx.portErr = errors.New("the emulator quit: ERROR | port used")
	if err := p.StartVirtual(ctx, "pinky-api36"); err == nil {
		t.Error("started on a port Pinky did not pick")
	}
	fx.mu.Lock()
	ports := slices.Clone(fx.ports)
	fx.mu.Unlock()
	if !slices.Equal(ports, []int{5558, 5558, 5560}) {
		t.Errorf("ports %v", ports)
	}
	if slices.Contains(f.sentTo(), "emulator-5554") {
		t.Errorf("asked another tool's emulator: %q", f.sentTo())
	}
}
