package fake

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// AddApp adds an installed app, listed after Settings.
func (d *Device) AddApp(a driver.App) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.apps = append(d.apps, a)
}

// AddLogs adds log lines of an app, by its id.
func (d *Device) AddLogs(appID string, lines ...driver.LogLine) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.logs == nil {
		d.logs = map[string][]driver.LogLine{}
	}
	d.logs[appID] = append(d.logs[appID], lines...)
}

// AddCrash adds a crash report of an app, by its id.
func (d *Device) AddCrash(appID string, r driver.CrashReport) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.crashes == nil {
		d.crashes = map[string][]driver.CrashReport{}
	}
	d.crashes[appID] = append(d.crashes[appID], r)
}

// SetDevBuild marks an app, by its id, as a development build or not.
func (d *Device) SetDevBuild(appID string, dev bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.devBuilds == nil {
		d.devBuilds = map[string]bool{}
	}
	d.devBuilds[appID] = dev
}

// AppLogs implements driver.AppLogger.
func (d *Device) AppLogs(_ context.Context, q driver.LogQuery) (driver.LogResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.knownLocked(q.App); err != nil {
		return driver.LogResult{}, err
	}
	var keep []driver.LogLine
	for _, l := range d.logs[q.App.ID] {
		if !l.Time.Before(q.Since) && q.Keep(l) {
			keep = append(keep, l)
		}
	}
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].Time.Before(keep[j].Time) })
	res := driver.LogResult{From: q.Since}
	if q.Max > 0 && len(keep) > q.Max {
		res.Dropped = len(keep) - q.Max
		keep = keep[len(keep)-q.Max:]
	}
	res.Lines = keep
	return res, nil
}

// AppCrashes implements driver.AppLogger.
func (d *Device) AppCrashes(_ context.Context, q driver.LogQuery) ([]driver.CrashReport, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.knownLocked(q.App); err != nil {
		return nil, err
	}
	var out []driver.CrashReport
	for _, r := range d.crashes[q.App.ID] {
		if !r.Time.Before(q.Since) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if q.Max > 0 && len(out) > q.Max {
		out = out[:q.Max]
	}
	return out, nil
}

// DevBuild implements driver.AppLogger.
func (d *Device) DevBuild(_ context.Context, app driver.App) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.devBuilds[app.ID], nil
}

func (d *Device) knownLocked(app driver.App) error {
	for _, a := range d.apps {
		if a.ID == app.ID {
			return nil
		}
	}
	return fmt.Errorf("fake app %q: %w", app.ID, driver.ErrNotFound)
}

// LogLineAt is a log line for tests.
func LogLineAt(t time.Time, level driver.LogLevel, tag, text string) driver.LogLine {
	return driver.LogLine{Time: t, PID: 4321, Level: level, Tag: tag, Text: text}
}

var _ driver.AppLogger = (*Device)(nil)
