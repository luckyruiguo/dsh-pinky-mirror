package android

import (
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

func TestDriverless(t *testing.T) {
	nodes := []usbNode{
		// A Pixel without a driver: the composite device and its two
		// interfaces, MTP (with a driver) and USB debugging (without).
		{ID: `USB\VID_18D1&PID_4EE7\26051FDF6002R9`, Name: "Pixel 7"},
		{ID: `USB\VID_18D1&PID_4EE7&MI_00\6&2D1B3B5&0&0000`, Parent: `USB\VID_18D1&PID_4EE7\26051FDF6002R9`,
			Compatible: []string{`USB\Class_06&SubClass_01&Prot_01`, `USB\Class_06&SubClass_01`, `USB\Class_06`}, Name: "Pixel 7"},
		{ID: `USB\VID_18D1&PID_4EE7&MI_01\6&2D1B3B5&0&0001`, Parent: `USB\VID_18D1&PID_4EE7\26051FDF6002R9`,
			Compatible: []string{`USB\Class_ff&SubClass_42&Prot_01`, `USB\Class_ff&SubClass_42`, `USB\Class_ff`}, Name: "ADB Interface", Driverless: true},
		// A Samsung phone with a driver: adb sees it.
		{ID: `USB\VID_04E8&PID_6860&MI_02\7&1A2B3C4D&0&0002`, Parent: `USB\VID_04E8&PID_6860\R58N12345AB`,
			Compatible: []string{`USB\Class_ff&SubClass_42&Prot_01`}},
		// A Xiaomi phone without a driver, whose parent Windows did not
		// list, and which reports no serial number.
		{ID: `USB\VID_2717&PID_FF48&MI_01\6&AAAA&0&0001`, Parent: `USB\VID_2717&PID_FF48\5&BBBB&0&3`,
			Compatible: []string{`usb\class_ff&subclass_42&prot_01`}, Driverless: true},
		// A device of one interface, without a driver: an unknown maker.
		{ID: `USB\VID_1234&PID_0001\ABC123`, Compatible: []string{`USB\Class_ff&SubClass_42&Prot_01`}, Driverless: true},
		// Some other device without a driver.
		{ID: `USB\VID_0BDA&PID_8153\000001`, Compatible: []string{`USB\Class_ff&SubClass_ff&Prot_00`}, Driverless: true},
	}
	got := driverless(nodes, nil)
	if len(got) != 3 {
		t.Fatalf("got %d phones: %+v", len(got), got)
	}
	pixel, xiaomi, other := got[0], got[1], got[2]
	if pixel.ID != driverlessPrefix+`USB\VID_18D1&PID_4EE7\26051FDF6002R9` || pixel.Name != "Pixel 7" ||
		pixel.State != driver.StateNeedsSetup || pixel.Setup != setupUSBDriver || pixel.Platform != driver.Android || pixel.Connection != "usb" ||
		!strings.Contains(pixel.Detail, "Google USB Driver") {
		t.Errorf("the Pixel: %+v", pixel)
	}
	if xiaomi.Name != "Xiaomi phone" || !strings.Contains(xiaomi.Detail, "oem-usb") {
		t.Errorf("the Xiaomi phone: %+v", xiaomi)
	}
	if other.Name != "Android phone" || other.ID != driverlessPrefix+`USB\VID_1234&PID_0001\ABC123` {
		t.Errorf("the other phone: %+v", other)
	}

	// The Pixel is online over Wi-Fi, by its hardware serial: not listed.
	got = driverless(nodes, []driver.Info{{ID: "26051FDF6002R9", State: driver.StateOnline, Connection: "wifi"}})
	if len(got) != 2 || got[0].Name != "Xiaomi phone" {
		t.Errorf("with the Pixel on Wi-Fi: %+v", got)
	}

	if got := driverless(nil, nil); len(got) != 0 {
		t.Errorf("no devices: %+v", got)
	}
}

func TestUSBIDs(t *testing.T) {
	for id, want := range map[string][2]string{
		`USB\VID_18D1&PID_4EE7\26051FDF6002R9`:      {"26051FDF6002R9", "18D1"},
		`USB\VID_2717&PID_FF48\5&BBBB&0&3`:          {"", "2717"},
		`USB\VID_04e8&PID_6860&MI_02\7&1A2B&0&0002`: {"", "04E8"},
		`USB\ROOT_HUB30\4&1234&0&0`:                 {"", ""},
		`garbage`:                                   {"", ""},
	} {
		if s, v := usbSerial(id), usbVendor(id); s != want[0] || v != want[1] {
			t.Errorf("%s: serial %q vendor %q, want %q", id, s, v, want)
		}
	}
}
