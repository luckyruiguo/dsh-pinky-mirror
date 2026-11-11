package telemetry

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// osVersion is the Windows version and build, such as "10.0.26100".
func osVersion() string {
	v := windows.RtlGetVersion()
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}
