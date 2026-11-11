package android

import (
	"errors"

	"golang.org/x/sys/windows"
)

// The device properties Windows keeps for every device: its parent's
// instance id, and the description the device reports over the bus (the
// product name a phone gives, "Pixel 7").
var (
	devpkeyParent = windows.DEVPROPKEY{
		FmtID: windows.DEVPROPGUID{Data1: 0x4340a6c5, Data2: 0x93fa, Data3: 0x4706, Data4: [8]byte{0x97, 0x2c, 0x7b, 0x64, 0x80, 0x08, 0xa5, 0xa7}},
		PID:   8,
	}
	devpkeyBusReportedDeviceDesc = windows.DEVPROPKEY{
		FmtID: windows.DEVPROPGUID{Data1: 0x540b947e, Data2: 0x8b40, Data3: 0x45bc, Data4: [8]byte{0xa8, 0xa2, 0x6a, 0x0b, 0x89, 0x4c, 0xbd, 0xa2}},
		PID:   4,
	}
)

// Device manager's problem codes for a device without a driver.
const (
	cmProbNotConfigured = 1  // CM_PROB_NOT_CONFIGURED
	cmProbFailedInstall = 28 // CM_PROB_FAILED_INSTALL: "The drivers for this device are not installed."
)

// systemUSBNodes lists the USB devices and interfaces that are present.
var systemUSBNodes = func() ([]usbNode, error) {
	set, err := windows.SetupDiGetClassDevsEx(nil, "USB", 0, windows.DIGCF_ALLCLASSES|windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return nil, err
	}
	defer set.Close()
	var out []usbNode
	for i := 0; ; i++ {
		data, err := set.EnumDeviceInfo(i)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			break
		}
		if err != nil {
			continue
		}
		n := usbNode{}
		if n.ID, err = set.DeviceInstanceID(data); err != nil {
			continue
		}
		if v, err := set.DeviceRegistryProperty(data, windows.SPDRP_COMPATIBLEIDS); err == nil {
			n.Compatible, _ = v.([]string)
		}
		if v, err := windows.SetupDiGetDeviceProperty(set, data, &devpkeyParent); err == nil {
			n.Parent, _ = v.(string)
		}
		if v, err := windows.SetupDiGetDeviceProperty(set, data, &devpkeyBusReportedDeviceDesc); err == nil {
			n.Name, _ = v.(string)
		}
		n.Driverless = noDriver(set, data)
		out = append(out, n)
	}
	return out, nil
}

// noDriver reports whether Windows has no driver for a device: Device
// Manager shows it under Other devices, with problem 28 (or 1), and it has
// no service.
func noDriver(set windows.DevInfo, data *windows.DevInfoData) bool {
	var status, problem uint32
	if err := windows.CM_Get_DevNode_Status(&status, &problem, data.DevInst, 0); err == nil &&
		status&windows.DN_HAS_PROBLEM != 0 && (problem == cmProbFailedInstall || problem == cmProbNotConfigured) {
		return true
	}
	v, err := set.DeviceRegistryProperty(data, windows.SPDRP_SERVICE)
	if err != nil {
		// No service at all: Windows found no driver.
		return errors.Is(err, windows.ERROR_INVALID_DATA)
	}
	s, _ := v.(string)
	return s == ""
}
