package adb

import (
	"bufio"
	"context"
	"fmt"
	"strings"
)

// Device states as the adb server reports them.
const (
	StateDevice       = "device" // online and authorized
	StateOffline      = "offline"
	StateUnauthorized = "unauthorized"
	StateAuthorizing  = "authorizing"
	StateConnecting   = "connecting"
	StateNoPerm       = "no permissions" // Linux without udev rules
	StateRecovery     = "recovery"
	StateBootloader   = "bootloader"
	StateSideload     = "sideload"
	StateRescue       = "rescue"
)

// DeviceEntry is one line of `adb devices -l`.
type DeviceEntry struct {
	Serial      string
	State       string
	Detail      string // extra text after the state, e.g. the no-permissions hint
	USB         string // usb:<port path>, empty over Wi-Fi
	Product     string
	Model       string // with underscores for spaces, as adb reports it
	Device      string
	TransportID string
}

// Wireless reports whether the device is connected over the network rather
// than USB.
func (d DeviceEntry) Wireless() bool {
	if d.USB != "" {
		return false
	}
	s := d.Serial
	return strings.Contains(s, "._adb-tls-connect.") || strings.Contains(s, "._adb._tcp") ||
		strings.Count(s, ":") == 1 || strings.HasPrefix(s, "[")
}

// Devices lists the devices the server knows, with details.
func (c *Client) Devices(ctx context.Context) ([]DeviceEntry, error) {
	s, err := c.query(ctx, "host:devices-l")
	if err != nil {
		return nil, err
	}
	return ParseDevices(s), nil
}

// TrackDevices calls fn with the full device list now and after every
// change, until ctx ends or the server goes away. It returns the reason it
// stopped; an adb server restart shows up as an error here.
func (c *Client) TrackDevices(ctx context.Context, fn func([]DeviceEntry)) error {
	conn, stop, err := c.hostConn(ctx, "host:track-devices-l")
	if err != nil {
		return err
	}
	defer stop()
	defer conn.Close()
	for {
		s, err := readString(conn)
		if err != nil {
			return ctxErr(ctx, fmt.Errorf("adb: track devices: %w", err))
		}
		fn(ParseDevices(s))
	}
}

// ParseDevices parses the text of host:devices-l.
func ParseDevices(text string) []DeviceEntry {
	var out []DeviceEntry
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "*") {
			continue
		}
		// The serial is separated from the rest by whitespace; the state may
		// contain spaces ("no permissions (...)").
		i := strings.IndexAny(line, " \t")
		if i < 0 {
			out = append(out, DeviceEntry{Serial: line})
			continue
		}
		d := DeviceEntry{Serial: line[:i]}
		rest := strings.TrimSpace(line[i:])
		if strings.HasPrefix(rest, StateNoPerm) {
			d.State = StateNoPerm
			rest = strings.TrimSpace(strings.TrimPrefix(rest, StateNoPerm))
			// The hint is in parentheses and may contain spaces and colons.
			if strings.HasPrefix(rest, "(") {
				if j := strings.Index(rest, ")"); j >= 0 {
					d.Detail = rest[1:j]
					rest = strings.TrimSpace(rest[j+1:])
				}
			}
		} else {
			f := strings.Fields(rest)
			d.State = f[0]
			rest = strings.TrimSpace(strings.TrimPrefix(rest, f[0]))
		}
		for _, kv := range strings.Fields(rest) {
			k, v, ok := strings.Cut(kv, ":")
			if !ok {
				continue
			}
			switch k {
			case "usb":
				d.USB = v
			case "product":
				d.Product = v
			case "model":
				d.Model = v
			case "device":
				d.Device = v
			case "transport_id":
				d.TransportID = v
			}
		}
		out = append(out, d)
	}
	return out
}
