package telemetry

import "golang.org/x/sys/unix"

// osVersion is the macOS version, such as "27.2".
func osVersion() string {
	v, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return ""
	}
	return v
}
