package android

import (
	"strings"

	"github.com/DIYgod/pinky/internal/driver"
)

// Phones without a driver. On Windows, a phone's USB debugging interface
// needs a driver (WinUSB through Google's USB driver or the maker's, or
// one Windows Update brings), and adb cannot see a phone whose interface
// has none. List shows such a phone as needing setup, from what Windows
// says about its USB devices, so that the person is told to install a
// driver rather than left waiting.

// driverlessPrefix starts the id of a phone listed for its missing driver.
// It cannot be opened.
const driverlessPrefix = "usbdriver:"

// adbInterface is the compatible id of a USB debugging interface: class
// 0xff, subclass 0x42, protocol 0x01.
const adbInterface = `USB\CLASS_FF&SUBCLASS_42&PROT_01`

// usbNode is a USB device or interface as Windows lists it.
type usbNode struct {
	ID         string   // instance id: USB\VID_18D1&PID_4EE7&MI_01\6&2D1B3B5&0&0001
	Parent     string   // the parent's instance id: USB\VID_18D1&PID_4EE7\<serial>
	Compatible []string // compatible ids, such as adbInterface
	Name       string   // what the device calls itself ("Pixel 7"), if Windows knows
	Driverless bool     // no driver is installed for it
}

// driverMaker names the makers whose drivers people look for, by USB
// vendor id, with where their driver is.
var driverMaker = map[string]struct{ name, driver string }{
	"18D1": {"Google", "the Google USB Driver (developer.android.com/studio/run/win-usb)"},
	"04E8": {"Samsung", "Samsung's Android USB Driver (developer.samsung.com/android-usb-driver)"},
	"2717": {"Xiaomi", ""},
	"22D9": {"OPPO", ""},
	"2A70": {"OnePlus", ""},
	"2D95": {"vivo", ""},
	"12D1": {"Huawei", ""},
	"339B": {"Honor", ""},
	"22B8": {"Motorola", ""},
	"0FCE": {"Sony", ""},
	"1004": {"LG", ""},
	"0B05": {"ASUS", ""},
	"17EF": {"Lenovo", ""},
	"19D2": {"ZTE", ""},
	"2B4C": {"Nothing", ""},
}

// driverless lists the phones on USB whose debugging interface has no
// driver, but those that are online already (over Wi-Fi, by the serial
// their USB device reports).
func driverless(nodes []usbNode, online []driver.Info) []driver.Info {
	byID := map[string]usbNode{}
	for _, n := range nodes {
		byID[strings.ToUpper(n.ID)] = n
	}
	have := map[string]bool{}
	for _, info := range online {
		have[strings.ToUpper(info.ID)] = true
	}
	var out []driver.Info
	seen := map[string]bool{}
	for _, n := range nodes {
		if !n.Driverless || !hasID(n.Compatible, adbInterface) {
			continue
		}
		// An interface of a composite device (…&MI_01\…) belongs to its
		// parent, the phone.
		dev := n
		if strings.Contains(strings.ToUpper(n.ID), "&MI_") && n.Parent != "" {
			if p, ok := byID[strings.ToUpper(n.Parent)]; ok {
				dev = p
			} else {
				dev = usbNode{ID: n.Parent}
			}
		}
		key := strings.ToUpper(dev.ID)
		if seen[key] {
			continue
		}
		seen[key] = true
		serial := usbSerial(dev.ID)
		if serial != "" && have[strings.ToUpper(serial)] {
			continue
		}
		vid := usbVendor(dev.ID)
		maker := driverMaker[vid]
		name := dev.Name
		if name == "" || strings.EqualFold(name, "ADB Interface") {
			name = n.Name
		}
		if name == "" || strings.EqualFold(name, "ADB Interface") {
			name = "Android phone"
			if maker.name != "" {
				name = maker.name + " phone"
			}
		}
		where := maker.driver
		if where == "" {
			where = "the Google USB Driver or the USB driver from the phone's maker (developer.android.com/studio/run/oem-usb lists them)"
		}
		out = append(out, driver.Info{
			ID:         driverlessPrefix + dev.ID,
			Platform:   driver.Android,
			Name:       name,
			Model:      name,
			Connection: "usb",
			Backend:    "scrcpy",
			State:      driver.StateNeedsSetup,
			Setup:      setupUSBDriver,
			// Add phone shows "Found <phone>, but <detail>", the
			// first letter lowered.
			Detail: "The phone's USB debugging has no driver on this computer. Install " + where + ", then unplug the phone and plug it in again.",
		})
	}
	return out
}

func hasID(ids []string, want string) bool {
	for _, id := range ids {
		if strings.EqualFold(strings.TrimSpace(id), want) {
			return true
		}
	}
	return false
}

// usbSerial is the serial number in a USB device's instance id
// (USB\VID_18D1&PID_4EE7\<serial>), or "" when Windows made the last part
// up (it holds an &) because the device reports none.
func usbSerial(id string) string {
	i := strings.LastIndex(id, `\`)
	if i < 0 {
		return ""
	}
	s := id[i+1:]
	if s == "" || strings.Contains(s, "&") {
		return ""
	}
	return s
}

// usbVendor is the vendor id in an instance id: 18D1 in USB\VID_18D1&….
func usbVendor(id string) string {
	u := strings.ToUpper(id)
	i := strings.Index(u, "VID_")
	if i < 0 || len(u) < i+8 {
		return ""
	}
	return u[i+4 : i+8]
}

// driverlessPhones lists the phones whose driver is missing, for List. An
// error from Windows leaves them out.
func (p *Provider) driverlessPhones(online []driver.Info) []driver.Info {
	if p.usbNodes == nil {
		return nil
	}
	nodes, err := p.usbNodes()
	if err != nil {
		p.cfg.logf("android: listing the USB devices: %v", err)
		return nil
	}
	return driverless(nodes, online)
}
