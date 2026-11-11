// Package history keeps what agents did: sessions, tasks, steps with before
// and after screenshots, and approvals. Everything lives in one SQLite
// database (pure Go, modernc.org/sqlite) and a media folder next to it, on
// the user's machine. Blocked apps are never recorded; the gateway does not
// pass them here.
package history

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/DIYgod/pinky/internal/screen"
)

// schemaVersion is stored in PRAGMA user_version. Version 2 added the
// recordings and device_settings tables, version 3 device_connections,
// version 4 removed_devices, version 5 paused_devices; all versions open
// the same way.
const schemaVersion = 5

const schema = `
CREATE TABLE IF NOT EXISTS devices (
	id          TEXT PRIMARY KEY,
	platform    TEXT NOT NULL DEFAULT '',
	name        TEXT NOT NULL DEFAULT '',
	model       TEXT NOT NULL DEFAULT '',
	os_version  TEXT NOT NULL DEFAULT '',
	backend     TEXT NOT NULL DEFAULT '',
	pairing_ref TEXT NOT NULL DEFAULT '',
	last_seen   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS agents (
	name        TEXT PRIMARY KEY,
	client      TEXT NOT NULL DEFAULT '',
	token_hash  TEXT NOT NULL DEFAULT '',
	scope       TEXT NOT NULL DEFAULT '',
	approved_at INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL DEFAULT 0,
	last_seen   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions (
	id         TEXT PRIMARY KEY,
	agent      TEXT NOT NULL DEFAULT '',
	devices    TEXT NOT NULL DEFAULT '[]',
	started_at INTEGER NOT NULL,
	ended_at   INTEGER NOT NULL DEFAULT 0,
	end_reason TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS tasks (
	id             TEXT PRIMARY KEY,
	session_id     TEXT NOT NULL DEFAULT '',
	device_id      TEXT NOT NULL DEFAULT '',
	device_name    TEXT NOT NULL DEFAULT '',
	agent          TEXT NOT NULL DEFAULT '',
	title          TEXT NOT NULL DEFAULT '',
	state          TEXT NOT NULL DEFAULT '',
	outcome        TEXT NOT NULL DEFAULT '',
	summary        TEXT NOT NULL DEFAULT '',
	started_at     INTEGER NOT NULL,
	ended_at       INTEGER NOT NULL DEFAULT 0,
	step_count     INTEGER NOT NULL DEFAULT 0,
	approval_count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS tasks_started ON tasks(started_at);
CREATE TABLE IF NOT EXISTS steps (
	task_id      TEXT NOT NULL,
	idx          INTEGER NOT NULL,
	tool         TEXT NOT NULL DEFAULT '',
	args         TEXT NOT NULL DEFAULT '{}',
	intent       TEXT NOT NULL DEFAULT '',
	text         TEXT NOT NULL DEFAULT '',
	status       TEXT NOT NULL DEFAULT '',
	started_at   INTEGER NOT NULL,
	elapsed_ms   INTEGER NOT NULL DEFAULT 0,
	before_media TEXT NOT NULL DEFAULT '',
	after_media  TEXT NOT NULL DEFAULT '',
	screen_text  TEXT NOT NULL DEFAULT '',
	target_x     INTEGER NOT NULL DEFAULT 0,
	target_y     INTEGER NOT NULL DEFAULT 0,
	target_w     INTEGER NOT NULL DEFAULT 0,
	target_h     INTEGER NOT NULL DEFAULT 0,
	point_x      INTEGER NOT NULL DEFAULT 0,
	point_y      INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (task_id, idx)
);
CREATE TABLE IF NOT EXISTS approvals (
	id         TEXT PRIMARY KEY,
	task_id    TEXT NOT NULL DEFAULT '',
	step_idx   INTEGER NOT NULL DEFAULT -1,
	kind       TEXT NOT NULL DEFAULT '',
	rule       TEXT NOT NULL DEFAULT '',
	title      TEXT NOT NULL DEFAULT '',
	decision   TEXT NOT NULL DEFAULT '',
	note       TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	decided_at INTEGER NOT NULL DEFAULT 0,
	waited_ms  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS approvals_task ON approvals(task_id);
CREATE TABLE IF NOT EXISTS media (
	id         TEXT PRIMARY KEY,
	kind       TEXT NOT NULL,
	path       TEXT NOT NULL,
	size       INTEGER NOT NULL DEFAULT 0,
	hash       TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS recordings (
	task_id    TEXT PRIMARY KEY,
	path       TEXT NOT NULL,
	started_at INTEGER NOT NULL,
	length_ms  INTEGER NOT NULL DEFAULT 0,
	codec      TEXT NOT NULL DEFAULT '',
	width      INTEGER NOT NULL DEFAULT 0,
	height     INTEGER NOT NULL DEFAULT 0,
	size       INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS device_settings (
	device_id  TEXT PRIMARY KEY,
	background INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS device_connections (
	device_id TEXT PRIMARY KEY,
	way       TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS removed_devices (
	device_id  TEXT PRIMARY KEY,
	removed_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS paused_devices (
	device_id TEXT PRIMARY KEY,
	paused_at INTEGER NOT NULL DEFAULT 0
);
`

// Store is the history database and its media and recordings folders.
type Store struct {
	db       *sql.DB
	dir      string
	mediaDir string
	recDir   string
	mu       sync.RWMutex // held for writing during Wipe and Prune
}

// Open opens or creates the store in dir: dir/pinky.db and dir/media/.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	media := filepath.Join(dir, "media")
	if err := os.MkdirAll(media, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "pinky.db")
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite serializes writers anyway, and this keeps the
	// pragmas on the only connection there is.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("history schema: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &Store{db: db, dir: dir, mediaDir: media, recDir: filepath.Join(dir, "recordings")}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Dir returns the store's directory.
func (s *Store) Dir() string { return s.dir }

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

// NewID returns a random id with the given prefix, such as "t_3f9a1c".
func NewID(prefix string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}

// Device is a phone Pinky has seen.
type Device struct {
	ID, Platform, Name, Model, OSVersion, Backend, PairingRef string
	LastSeen                                                  time.Time
}

// SaveDevice inserts or updates a device.
func (s *Store) SaveDevice(ctx context.Context, d Device) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO devices (id, platform, name, model, os_version, backend, pairing_ref, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET platform=excluded.platform, name=excluded.name, model=excluded.model,
			os_version=excluded.os_version, backend=excluded.backend, pairing_ref=excluded.pairing_ref, last_seen=excluded.last_seen`,
		d.ID, d.Platform, d.Name, d.Model, d.OSVersion, d.Backend, d.PairingRef, ms(d.LastSeen))
	return err
}

// Agent is an agent that connected, and what the person allowed it.
type Agent struct {
	Name, Client, TokenHash string
	// Scope is "everything", "read_only", "watch_only" or "apps:<id>,<id>".
	Scope      string
	ApprovedAt time.Time // zero until the person approves the agent
	CreatedAt  time.Time
	LastSeen   time.Time
}

// Agent returns an agent by name.
func (s *Store) Agent(ctx context.Context, name string) (Agent, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var a Agent
	var approved, created, seen int64
	err := s.db.QueryRowContext(ctx, `SELECT name, client, token_hash, scope, approved_at, created_at, last_seen FROM agents WHERE name = ?`, name).
		Scan(&a.Name, &a.Client, &a.TokenHash, &a.Scope, &approved, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, false, nil
	}
	if err != nil {
		return Agent{}, false, err
	}
	a.ApprovedAt, a.CreatedAt, a.LastSeen = fromMS(approved), fromMS(created), fromMS(seen)
	return a, true, nil
}

// Agents lists every agent.
func (s *Store) Agents(ctx context.Context) ([]Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT name, client, token_hash, scope, approved_at, created_at, last_seen FROM agents ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		var approved, created, seen int64
		if err := rows.Scan(&a.Name, &a.Client, &a.TokenHash, &a.Scope, &approved, &created, &seen); err != nil {
			return nil, err
		}
		a.ApprovedAt, a.CreatedAt, a.LastSeen = fromMS(approved), fromMS(created), fromMS(seen)
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveAgent inserts or updates an agent.
func (s *Store) SaveAgent(ctx context.Context, a Agent) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO agents (name, client, token_hash, scope, approved_at, created_at, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET client=excluded.client, token_hash=excluded.token_hash, scope=excluded.scope,
			approved_at=excluded.approved_at, last_seen=excluded.last_seen`,
		a.Name, a.Client, a.TokenHash, a.Scope, ms(a.ApprovedAt), ms(a.CreatedAt), ms(a.LastSeen))
	return err
}

// Session is one MCP session.
type Session struct {
	ID, Agent string
	Devices   []string // devices the session leased
	Started   time.Time
	Ended     time.Time
	EndReason string
}

// SaveSession inserts or updates a session.
func (s *Store) SaveSession(ctx context.Context, x Session) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	devs, _ := json.Marshal(x.Devices)
	if x.Devices == nil {
		devs = []byte("[]")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id, agent, devices, started_at, ended_at, end_reason)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET devices=excluded.devices, ended_at=excluded.ended_at, end_reason=excluded.end_reason`,
		x.ID, x.Agent, string(devs), ms(x.Started), ms(x.Ended), x.EndReason)
	return err
}

// Task is one agent task on one device.
type Task struct {
	ID, SessionID, DeviceID, DeviceName, Agent, Title string
	State                                             string // gateway.TaskState
	Outcome, Summary                                  string
	Started, Ended                                    time.Time
	StepCount, ApprovalCount                          int
}

// SaveTask inserts or updates a task.
func (s *Store) SaveTask(ctx context.Context, t Task) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO tasks (id, session_id, device_id, device_name, agent, title, state, outcome, summary,
			started_at, ended_at, step_count, approval_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET device_name=excluded.device_name, title=excluded.title, state=excluded.state,
			outcome=excluded.outcome, summary=excluded.summary, ended_at=excluded.ended_at,
			step_count=excluded.step_count, approval_count=excluded.approval_count`,
		t.ID, t.SessionID, t.DeviceID, t.DeviceName, t.Agent, t.Title, t.State, t.Outcome, t.Summary,
		ms(t.Started), ms(t.Ended), t.StepCount, t.ApprovalCount)
	return err
}

// Step is one tool call of a task.
type Step struct {
	TaskID     string
	Index      int
	Tool       string
	Args       string // JSON
	Intent     string
	Text       string // plain language
	Status     string
	Started    time.Time
	Elapsed    time.Duration
	Before     string // media id
	After      string // media id
	ScreenText string // the screen after the step, as the agent read it
	Target     screen.Rect
	Point      screen.Point
}

// SaveStep inserts or updates a step.
func (s *Store) SaveStep(ctx context.Context, st Step) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st.Args == "" {
		st.Args = "{}"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO steps (task_id, idx, tool, args, intent, text, status, started_at, elapsed_ms,
			before_media, after_media, screen_text, target_x, target_y, target_w, target_h, point_x, point_y)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id, idx) DO UPDATE SET tool=excluded.tool, args=excluded.args, intent=excluded.intent,
			text=excluded.text, status=excluded.status, elapsed_ms=excluded.elapsed_ms,
			before_media=CASE WHEN excluded.before_media != '' THEN excluded.before_media ELSE steps.before_media END,
			after_media=CASE WHEN excluded.after_media != '' THEN excluded.after_media ELSE steps.after_media END,
			screen_text=excluded.screen_text, target_x=excluded.target_x, target_y=excluded.target_y,
			target_w=excluded.target_w, target_h=excluded.target_h, point_x=excluded.point_x, point_y=excluded.point_y`,
		st.TaskID, st.Index, st.Tool, st.Args, st.Intent, st.Text, st.Status, ms(st.Started), st.Elapsed.Milliseconds(),
		st.Before, st.After, st.ScreenText, st.Target.X, st.Target.Y, st.Target.W, st.Target.H, st.Point.X, st.Point.Y)
	return err
}

// SetStepMedia sets a step's before or after screenshot, or both, and leaves
// the rest of the step as it is. An empty id leaves that picture unchanged.
func (s *Store) SetStepMedia(ctx context.Context, taskID string, index int, before, after string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err := s.db.ExecContext(ctx, `UPDATE steps SET
			before_media=CASE WHEN ? != '' THEN ? ELSE before_media END,
			after_media=CASE WHEN ? != '' THEN ? ELSE after_media END
		WHERE task_id = ? AND idx = ?`, before, before, after, after, taskID, index)
	return err
}

// Approval is one approval and its answer.
type Approval struct {
	ID        string
	TaskID    string
	StepIndex int // -1 when not tied to a step
	Kind      string
	Rule      string
	Title     string
	Decision  string // empty while open; "timeout" when nobody answered
	Note      string
	Created   time.Time
	Decided   time.Time
	Waited    time.Duration
}

// SaveApproval inserts or updates an approval.
func (s *Store) SaveApproval(ctx context.Context, a Approval) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO approvals (id, task_id, step_idx, kind, rule, title, decision, note, created_at, decided_at, waited_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET step_idx=excluded.step_idx, decision=excluded.decision, note=excluded.note,
			decided_at=excluded.decided_at, waited_ms=excluded.waited_ms`,
		a.ID, a.TaskID, a.StepIndex, a.Kind, a.Rule, a.Title, a.Decision, a.Note, ms(a.Created), ms(a.Decided), a.Waited.Milliseconds())
	return err
}

// Query filters tasks.
type Query struct {
	Search string    // in titles, agents, device names and step text
	Before time.Time // tasks that started before; zero = now
	Limit  int       // default 50
}

// Tasks returns tasks, newest first.
func (s *Store) Tasks(ctx context.Context, q Query) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if q.Limit <= 0 {
		q.Limit = 50
	}
	before := ms(q.Before)
	if before == 0 {
		before = time.Now().Add(time.Hour).UnixMilli()
	}
	where := "started_at < ?"
	args := []any{before}
	if q.Search != "" {
		like := "%" + escapeLike(q.Search) + "%"
		where += ` AND (title LIKE ? ESCAPE '\' OR agent LIKE ? ESCAPE '\' OR device_name LIKE ? ESCAPE '\' OR summary LIKE ? ESCAPE '\'
			OR id IN (SELECT task_id FROM steps WHERE text LIKE ? ESCAPE '\' OR intent LIKE ? ESCAPE '\'))`
		args = append(args, like, like, like, like, like, like)
	}
	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, device_id, device_name, agent, title, state, outcome, summary,
			started_at, ended_at, step_count, approval_count FROM tasks WHERE `+where+` ORDER BY started_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		var t Task
		var started, ended int64
		if err := rows.Scan(&t.ID, &t.SessionID, &t.DeviceID, &t.DeviceName, &t.Agent, &t.Title, &t.State, &t.Outcome, &t.Summary,
			&started, &ended, &t.StepCount, &t.ApprovalCount); err != nil {
			return nil, err
		}
		t.Started, t.Ended = fromMS(started), fromMS(ended)
		out = append(out, t)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// Steps returns a task's steps in order.
func (s *Store) Steps(ctx context.Context, taskID string) ([]Step, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT task_id, idx, tool, args, intent, text, status, started_at, elapsed_ms, before_media,
			after_media, screen_text, target_x, target_y, target_w, target_h, point_x, point_y
		FROM steps WHERE task_id = ? ORDER BY idx`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Step
	for rows.Next() {
		var st Step
		var started, elapsed int64
		if err := rows.Scan(&st.TaskID, &st.Index, &st.Tool, &st.Args, &st.Intent, &st.Text, &st.Status, &started, &elapsed,
			&st.Before, &st.After, &st.ScreenText, &st.Target.X, &st.Target.Y, &st.Target.W, &st.Target.H, &st.Point.X, &st.Point.Y); err != nil {
			return nil, err
		}
		st.Started, st.Elapsed = fromMS(started), time.Duration(elapsed)*time.Millisecond
		out = append(out, st)
	}
	return out, rows.Err()
}

// Approvals returns a task's approvals in order.
func (s *Store) Approvals(ctx context.Context, taskID string) ([]Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, step_idx, kind, rule, title, decision, note, created_at, decided_at, waited_ms
		FROM approvals WHERE task_id = ? ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		var a Approval
		var created, decided, waited int64
		if err := rows.Scan(&a.ID, &a.TaskID, &a.StepIndex, &a.Kind, &a.Rule, &a.Title, &a.Decision, &a.Note, &created, &decided, &waited); err != nil {
			return nil, err
		}
		a.Created, a.Decided, a.Waited = fromMS(created), fromMS(decided), time.Duration(waited)*time.Millisecond
		out = append(out, a)
	}
	return out, rows.Err()
}

// Wipe deletes every session, task, step, approval and media file. Agents
// and devices stay: they are settings, not history.
func (s *Store) Wipe(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, t := range []string{"steps", "approvals", "tasks", "sessions", "media", "recordings"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := os.RemoveAll(s.mediaDir); err != nil {
		return err
	}
	if err := os.MkdirAll(s.mediaDir, 0o700); err != nil {
		return err
	}
	if err := os.RemoveAll(s.recDir); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "VACUUM")
	return err
}

func digest(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}
