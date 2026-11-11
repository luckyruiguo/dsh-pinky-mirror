package telemetry

import (
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// osVersion is the distribution and its version from os-release, such as
// "ubuntu 24.04", or the kernel's release without one.
func osVersion() string {
	for _, path := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var id, ver string
		for _, line := range strings.Split(string(data), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			v = strings.Trim(v, `"'`)
			switch k {
			case "ID":
				id = v
			case "VERSION_ID":
				ver = v
			}
		}
		if id != "" {
			return strings.TrimSpace(id + " " + ver)
		}
	}
	var u unix.Utsname
	if unix.Uname(&u) != nil {
		return ""
	}
	return unix.ByteSliceToString(u.Release[:])
}
