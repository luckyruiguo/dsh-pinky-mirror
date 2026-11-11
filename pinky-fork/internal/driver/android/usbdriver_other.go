//go:build !windows

package android

// systemUSBNodes: only Windows needs a driver for a phone's USB debugging.
var systemUSBNodes func() ([]usbNode, error)
