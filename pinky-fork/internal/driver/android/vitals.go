package android

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/DIYgod/pinky/internal/driver"
)

// vitalsSplit separates the outputs of the commands of one Vitals shell.
const vitalsSplit = "--pinky-vitals--"

// Vitals implements driver.VitalsReader with `dumpsys battery` and `df`,
// in one shell. Neither wakes the screen nor touches the scrcpy session.
// An emulator's battery is made up, so it reports none.
func (d *Device) Vitals(ctx context.Context, parts driver.VitalParts) (driver.Vitals, error) {
	battery := parts&driver.VitalBattery != 0 && !d.info.Virtual
	storage := parts&driver.VitalStorage != 0
	var cmds []string
	if battery {
		cmds = append(cmds, "dumpsys battery")
	}
	if storage {
		cmds = append(cmds, "df -k /data")
	}
	if len(cmds) == 0 {
		return driver.Vitals{}, nil
	}
	out, err := d.client.Shell(ctx, d.info.ID, strings.Join(cmds, "; echo "+vitalsSplit+"; "))
	if err != nil {
		return driver.Vitals{}, wrapErr(err)
	}
	outs := strings.Split(string(out), vitalsSplit)
	var v driver.Vitals
	if battery {
		v.Battery = parseBattery(outs[0])
		outs = outs[1:]
	}
	if storage && len(outs) > 0 {
		v.Storage = parseDF(outs[0])
	}
	if v.Battery == nil && v.Storage == nil {
		return v, fmt.Errorf("android: cannot read the battery or storage from %q", strings.TrimSpace(string(out)))
	}
	return v, nil
}

// parseBattery reads `dumpsys battery`; nil without a level.
func parseBattery(out string) *driver.Battery {
	vals := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok {
			vals[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	level, err := strconv.Atoi(vals["level"])
	if err != nil || level < 0 {
		return nil
	}
	if scale, err := strconv.Atoi(vals["scale"]); err == nil && scale > 0 && scale != 100 {
		level = level * 100 / scale
	}
	if level > 100 {
		return nil
	}
	b := &driver.Battery{Level: level}
	for _, k := range []string{"ac powered", "usb powered", "wireless powered", "dock powered"} {
		if vals[k] == "true" {
			b.Plugged = true
		}
	}
	// BatteryManager's BATTERY_STATUS_*: 2 charging, 5 full.
	switch vals["status"] {
	case "2":
		b.Charging = true
	case "5":
		b.Full = true
	}
	if t, err := strconv.Atoi(vals["temperature"]); err == nil && t > 0 {
		b.TempC = float64(t) / 10
	}
	return b
}

// parseDF reads `df -k /data`: the partition's size, rounded the way
// Android's Settings round it to the capacity the phone is sold with, and
// what is available on it.
func parseDF(out string) *driver.Storage {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return nil
	}
	// A long filesystem name may stand on a line of its own: read the
	// first three numbers after the header.
	var nums []int64
	for _, f := range strings.Fields(strings.Join(lines[1:], " ")) {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			if len(nums) > 0 {
				break
			}
			continue
		}
		nums = append(nums, n)
		if len(nums) == 3 {
			break
		}
	}
	if len(nums) < 3 || nums[0] <= 0 {
		return nil
	}
	size, avail := nums[0]*1024, nums[2]*1024
	return &driver.Storage{Total: roundStorageSize(size), Free: min(avail, size)}
}

// roundStorageSize is Android's FileUtils.roundStorageSize: the next power
// of two of a power of 1000, so a 115 GB data partition reads as 128 GB,
// as the phone's Settings and its box say.
func roundStorageSize(size int64) int64 {
	val, pow := int64(1), int64(1)
	for val*pow < size {
		val <<= 1
		if val > 512 {
			val = 1
			pow *= 1000
		}
	}
	return val * pow
}

var _ driver.VitalsReader = (*Device)(nil)
