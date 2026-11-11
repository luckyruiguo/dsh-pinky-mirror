package android

import (
	"context"
	"slices"
	"testing"
)

func TestGuidSerial(t *testing.T) {
	for guid, want := range map[string]string{
		"adb-3e8be48e-kIY2HE":            "3e8be48e",
		"adb-R5CN20ABC-x1y2z3":           "R5CN20ABC",
		"adb-EMULATOR36X4X9X0-nW9LNN":    "EMULATOR36X4X9X0",
		"adb-with-dash-in-serial-abc123": "with-dash-in-serial",
		"adb--abc123":                    "",
		"adb-noSuffix":                   "",
		"192.168.1.5:5555":               "",
		"pinky-abc123":                   "",
		"":                               "",
	} {
		if got := guidSerial(guid); got != want {
			t.Errorf("guidSerial(%q) = %q, want %q", guid, got, want)
		}
	}
}

// TestDisconnectRemovedPhone: removing the Mi drops its connections over
// Wi-Fi made by address, whatever name adb lists them under, and leaves
// its cable, the one the adb server's discovery made (which adb cannot
// drop by name) and every other phone alone.
func TestDisconnectRemovedPhone(t *testing.T) {
	f, p := newFakeADB(t)
	ctx := context.Background()
	const mi = "3e8be48e"
	f.set(func(f *fakeADB) {
		f.devices = []string{
			mi + "\tdevice usb:1048576X product:x model:Mi_10_Pro device:x transport_id:1",
			"192.168.1.5:41234\tdevice product:x model:Mi_10_Pro device:x transport_id:2",
			"adb-" + mi + "-kIY2HE._adb-tls-connect._tcp\tdevice product:x model:Mi_10_Pro device:x transport_id:3",
			"192.168.1.9:5555\tdevice product:x model:Pixel_9 device:x transport_id:4",
			"192.168.1.5:5555\toffline transport_id:5", // the Mi's earlier address, which List names by its serial
		}
		f.props = map[string]string{
			mi:                  "Mi 10 Pro\n17\n37\nMi 10 Pro\n" + mi + "\n",
			"192.168.1.5:41234": "Mi 10 Pro\n17\n37\nMi 10 Pro\n" + mi + "\n",
			"adb-" + mi + "-kIY2HE._adb-tls-connect._tcp": "Mi 10 Pro\n17\n37\nMi 10 Pro\n" + mi + "\n",
			"192.168.1.9:5555": "Pixel 9\n16\n36\nPixel 9\nR5CN20ABC\n",
		}
	})
	if err := p.Disconnect(ctx, mi); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	disconnects, devices := slices.Clone(f.disconnects), slices.Clone(f.devices)
	f.mu.Unlock()
	if !slices.Equal(disconnects, []string{"192.168.1.5:41234"}) {
		t.Errorf("disconnected %q, want only the Mi's address", disconnects)
	}
	if len(devices) != 4 {
		t.Errorf("left %q", devices)
	}
	// The offline connection is a phone of its own to Pinky, by its serial.
	if err := p.Disconnect(ctx, "192.168.1.5:5555"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	disconnects = slices.Clone(f.disconnects)
	f.mu.Unlock()
	if !slices.Equal(disconnects, []string{"192.168.1.5:41234", "192.168.1.5:5555"}) {
		t.Errorf("disconnected %q", disconnects)
	}
	for _, d := range []string{"", "nobody"} {
		if err := p.Disconnect(ctx, d); err != nil {
			t.Errorf("Disconnect(%q): %v", d, err)
		}
	}
	f.mu.Lock()
	n := len(f.disconnects)
	f.mu.Unlock()
	if n != 2 {
		t.Errorf("disconnected %d connections for phones that have none", n-2)
	}
}

// TestReconnectLeavesRemovedPhones: a phone paired over Wi-Fi that the
// person removed is neither searched for nor connected, until it is
// remembered.
func TestReconnectLeavesRemovedPhones(t *testing.T) {
	f, p := newReconnectFake(t)
	ctx := context.Background()
	f.set(func(f *fakeADB) {
		f.known = []string{guidPixel}
		f.own = []string{guidPixel + "\t_adb-tls-connect._tcp\t192.168.1.42:41000"}
	})
	p.Forget("R5CN20ABC") // guidPixel's hardware serial
	if away, connected := p.wifi.reconnect(ctx, entries(t, p)); away || connected {
		t.Errorf("reconnect = away %v, connected %v; want nothing to do", away, connected)
	}
	if browses, connects := f.counts(); browses != 0 || len(connects) != 0 {
		t.Fatalf("browses %d, connects %q for a removed phone", browses, connects)
	}
	p.Remember("R5CN20ABC")
	if _, connected := p.wifi.reconnect(ctx, entries(t, p)); !connected {
		t.Error("a phone added back was not reconnected")
	}
	if _, connects := f.counts(); !slices.Equal(connects, []string{"192.168.1.42:41000"}) {
		t.Errorf("connects %q", connects)
	}
}
