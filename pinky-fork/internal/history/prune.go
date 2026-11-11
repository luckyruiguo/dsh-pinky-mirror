package history

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Pruned counts what Prune deleted.
type Pruned struct {
	Tasks, Steps, Approvals, Media, Recordings, Sessions int
}

// Prune deletes tasks that ended before cutoff (or, for tasks that never
// ended, as after a crash, started before it), with their steps, approvals,
// screenshots and recordings, and sessions that ended before cutoff with no
// task left. Tasks in keep stay whatever their age: the ones still running.
// A screenshot another task's step still shows stays too.
func (s *Store) Prune(ctx context.Context, cutoff time.Time, keep []string) (Pruned, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Pruned
	before := ms(cutoff)
	q := `SELECT id FROM tasks WHERE (CASE WHEN ended_at != 0 THEN ended_at ELSE started_at END) < ?`
	args := []any{before}
	if len(keep) > 0 {
		q += ` AND id NOT IN (` + placeholders(len(keep)) + `)`
		for _, k := range keep {
			args = append(args, k)
		}
	}
	ids, err := s.strings(ctx, q, args...)
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
	// Screenshots no step shows any more: those of the deleted steps, and
	// any left over from before the cutoff.
	orphans, err := txStrings(ctx, tx, `SELECT id FROM media WHERE created_at < ?
		AND id NOT IN (SELECT before_media FROM steps) AND id NOT IN (SELECT after_media FROM steps)`, before)
	if err != nil {
		return out, err
	}
	for _, id := range orphans {
		candidates[id] = true
	}
	media, err := s.dropMedia(ctx, tx, candidates, &out)
	if err != nil {
		return out, err
	}
	files = append(files, media...)
	res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE ended_at != 0 AND ended_at < ?
		AND id NOT IN (SELECT session_id FROM tasks)`, before)
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

// dropTasks deletes tasks with their steps, approvals and recordings in tx,
// counting them in out. It returns the screenshots their steps showed,
// which may be unused now (dropMedia), and the recordings' files, to remove
// once tx commits.
func (s *Store) dropTasks(ctx context.Context, tx *sql.Tx, ids []string, out *Pruned) (map[string]bool, []string, error) {
	var files []string
	candidates := map[string]bool{}
	for _, chunk := range chunks(ids, 500) {
		in, cargs := inList(chunk)
		rows, err := tx.QueryContext(ctx, `SELECT before_media, after_media FROM steps WHERE task_id IN (`+in+`)`, cargs...)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var b, a string
			if err := rows.Scan(&b, &a); err != nil {
				rows.Close()
				return nil, nil, err
			}
			for _, m := range []string{b, a} {
				if m != "" {
					candidates[m] = true
				}
			}
		}
		// Next stops on a read error as well as at the end. Deleting after a
		// short read would drop the tasks and leave the screenshots it never
		// saw, and the error would be gone.
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, err
		}
		rows.Close()
		recs, err := txStrings(ctx, tx, `SELECT path FROM recordings WHERE task_id IN (`+in+`)`, cargs...)
		if err != nil {
			return nil, nil, err
		}
		for _, rel := range recs {
			if !strings.Contains(rel, "..") {
				files = append(files, filepath.Join(s.dir, filepath.FromSlash(rel)))
			}
		}
		out.Recordings += len(recs)
		for _, del := range []struct {
			table string
			n     *int
		}{{"steps", &out.Steps}, {"approvals", &out.Approvals}, {"recordings", nil}, {"tasks", &out.Tasks}} {
			col := "task_id"
			if del.table == "tasks" {
				col = "id"
			}
			res, err := tx.ExecContext(ctx, `DELETE FROM `+del.table+` WHERE `+col+` IN (`+in+`)`, cargs...)
			if err != nil {
				return nil, nil, err
			}
			if del.n != nil {
				n, _ := res.RowsAffected()
				*del.n += int(n)
			}
		}
	}
	return candidates, files, nil
}

// dropMedia deletes the screenshots among candidates that no step shows
// any more in tx, counting them in out, and returns their files, to remove
// once tx commits.
func (s *Store) dropMedia(ctx context.Context, tx *sql.Tx, candidates map[string]bool, out *Pruned) ([]string, error) {
	var files []string
	for id := range candidates {
		var used int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM steps WHERE before_media = ? OR after_media = ?`, id, id).Scan(&used); err != nil {
			return nil, err
		}
		if used > 0 {
			continue
		}
		var rel string
		err := tx.QueryRowContext(ctx, `SELECT path FROM media WHERE id = ?`, id).Scan(&rel)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM media WHERE id = ?`, id); err != nil {
			return nil, err
		}
		out.Media++
		if !strings.Contains(rel, "..") {
			files = append(files, filepath.Join(s.mediaDir, filepath.FromSlash(rel)))
		}
	}
	return files, nil
}

// removeFiles removes files whose rows are gone; one already gone is fine.
func removeFiles(files []string) error {
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Store) strings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanStrings(rows)
}

func txStrings(ctx context.Context, tx *sql.Tx, q string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanStrings(rows)
}

func scanStrings(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func inList(ids []string) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return placeholders(len(ids)), args
}

func chunks(ids []string, n int) [][]string {
	var out [][]string
	for len(ids) > n {
		out = append(out, ids[:n])
		ids = ids[n:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}

// Task returns one task by id.
func (s *Store) Task(ctx context.Context, id string) (Task, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var t Task
	var started, ended int64
	err := s.db.QueryRowContext(ctx, `SELECT id, session_id, device_id, device_name, agent, title, state, outcome, summary,
			started_at, ended_at, step_count, approval_count FROM tasks WHERE id = ?`, id).
		Scan(&t.ID, &t.SessionID, &t.DeviceID, &t.DeviceName, &t.Agent, &t.Title, &t.State, &t.Outcome, &t.Summary,
			&started, &ended, &t.StepCount, &t.ApprovalCount)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	t.Started, t.Ended = fromMS(started), fromMS(ended)
	return t, true, nil
}
