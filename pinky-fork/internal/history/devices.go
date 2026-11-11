package history

import (
	"context"
	"time"
)

// Removing a phone from Pinky: the store forgets the phone's record and
// settings, remembers that the person removed it (so a phone that a driver
// keeps listing by itself, over the network, stays off the list until the
// person adds it back), and, when the person asks, deletes its history.

// deviceTables are the tables that hold a phone's record and settings, with
// the column that names the phone. Its history (tasks and what hangs off
// them) is not here: DeleteDeviceHistory deletes that.
var deviceTables = []struct{ table, column string }{
	{"devices", "id"},
	{"device_settings", "device_id"},
	{"device_connections", "device_id"},
	{"paused_devices", "device_id"},
}

// ForgetDevice deletes the phone's record and its settings: whether agents
// work on its background display, and how Pinky connects to it. Its
// history stays.
func (s *Store) ForgetDevice(ctx context.Context, deviceID string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range deviceTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t.table+` WHERE `+t.column+` = ?`, deviceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetDeviceRemoved records that the person removed the phone from Pinky,
// or, with removed false, that they added it back.
func (s *Store) SetDeviceRemoved(ctx context.Context, deviceID string, removed bool) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !removed {
		_, err := s.db.ExecContext(ctx, `DELETE FROM removed_devices WHERE device_id = ?`, deviceID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO removed_devices (device_id, removed_at) VALUES (?, ?)
		ON CONFLICT(device_id) DO UPDATE SET removed_at=excluded.removed_at`, deviceID, ms(time.Now()))
	return err
}

// SetDevicePaused records that the person paused Pinky's connection to the
// phone, or, with paused false, that they resumed it.
func (s *Store) SetDevicePaused(ctx context.Context, deviceID string, paused bool) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !paused {
		_, err := s.db.ExecContext(ctx, `DELETE FROM paused_devices WHERE device_id = ?`, deviceID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO paused_devices (device_id, paused_at) VALUES (?, ?)
		ON CONFLICT(device_id) DO UPDATE SET paused_at=excluded.paused_at`, deviceID, ms(time.Now()))
	return err
}

// PausedDevices returns the phones the person paused and has not resumed.
func (s *Store) PausedDevices(ctx context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.strings(ctx, `SELECT device_id FROM paused_devices ORDER BY paused_at, device_id`)
}

// RemovedDevices returns the phones the person removed and has not added
// back.
func (s *Store) RemovedDevices(ctx context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.strings(ctx, `SELECT device_id FROM removed_devices ORDER BY removed_at, device_id`)
}

// DeviceIDs returns the phones Pinky has seen and the person has not
// removed: those it opened, and those listed on a cable.
func (s *Store) DeviceIDs(ctx context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.strings(ctx, `SELECT id FROM devices WHERE id NOT IN (SELECT device_id FROM removed_devices) ORDER BY id`)
}

// SeenDevices returns the records of the phones Pinky has seen and the
// person has not removed, by id.
func (s *Store) SeenDevices(ctx context.Context) ([]Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, platform, name, model, os_version, backend, pairing_ref, last_seen FROM devices
		WHERE id NOT IN (SELECT device_id FROM removed_devices) ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var seen int64
		if err := rows.Scan(&d.ID, &d.Platform, &d.Name, &d.Model, &d.OSVersion, &d.Backend, &d.PairingRef, &seen); err != nil {
			return nil, err
		}
		d.LastSeen = fromMS(seen)
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDeviceHistory deletes the phone's tasks that started before
// before, with their steps, approvals, screenshots and recordings, and the
// sessions that used the phone, ended and have no task left. A screenshot
// another phone's step still shows stays.
func (s *Store) DeleteDeviceHistory(ctx context.Context, deviceID string, before time.Time) (Pruned, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Pruned
	ids, err := s.strings(ctx, `SELECT id FROM tasks WHERE device_id = ? AND started_at < ?`, deviceID, ms(before))
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	candidates, files, err := s.dropTasks(ctx, tx, ids, &out)
	if err != nil {
		return out, err
	}
	media, err := s.dropMedia(ctx, tx, candidates, &out)
	if err != nil {
		return out, err
	}
	files = append(files, media...)
	res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE ended_at != 0
		AND id NOT IN (SELECT session_id FROM tasks)
		AND EXISTS (SELECT 1 FROM json_each(sessions.devices) WHERE json_each.value = ?)`, deviceID)
	if err != nil {
		return out, err
	}
	n, _ := res.RowsAffected()
	out.Sessions = int(n)
	if err := tx.Commit(); err != nil {
		return out, err
	}
	return out, removeFiles(files)
}
