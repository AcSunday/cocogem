// Package store owns SQLite transactions, persistence and replay notification.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	w "github.com/AcSunday/cocogem/internal/workspace"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// ErrNotFound is returned when a requested object does not exist.
var ErrNotFound = errors.New("store: not found")

// Store serializes local transactions; notifications never carry authoritative data.
type Store struct {
	db      *sql.DB
	mu      sync.Mutex
	changed chan struct{}
}

// Tx exposes operations that must commit together.
type Tx struct {
	ctx context.Context
	tx  *sql.Tx
}

// Open creates or migrates a private SQLite database.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("database must not be a symlink")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db, changed: make(chan struct{})}, nil
}

// Close releases the database after the scheduler has stopped.
func (s *Store) Close() error { return s.db.Close() }

// Changed returns a notification before a snapshot read, avoiding lost wakeups.
func (s *Store) Changed() <-chan struct{} { s.mu.Lock(); defer s.mu.Unlock(); return s.changed }

// Read obtains a consistent snapshot. Callbacks must not perform network I/O.
func (s *Store) Read(ctx context.Context, fn func(*Tx) error) error {
	return s.transaction(ctx, false, fn)
}

// Write atomically persists state and its events, then wakes subscribers.
func (s *Store) Write(ctx context.Context, fn func(*Tx) error) error {
	return s.transaction(ctx, true, fn)
}

func (s *Store) transaction(ctx context.Context, write bool, fn func(*Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(&Tx{ctx: ctx, tx: tx}); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if write {
		close(s.changed)
		s.changed = make(chan struct{})
	}
	return nil
}

func get[T any](t *Tx, q string, args ...any) (T, error) {
	var value T
	var body string
	err := t.tx.QueryRowContext(t.ctx, q, args...).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return value, ErrNotFound
	}
	if err != nil {
		return value, err
	}
	err = json.Unmarshal([]byte(body), &value)
	return value, err
}
func list[T any](t *Tx, q string, args ...any) ([]T, error) {
	values := []T{}
	rows, err := t.tx.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b string
		var v T
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(b), &v); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}
func (t *Tx) exec(q string, args ...any) error {
	_, err := t.tx.ExecContext(t.ctx, q, args...)
	return err
}
func body(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Workspaces lists workspaces in creation order.
func (t *Tx) Workspaces() ([]w.Workspace, error) {
	return list[w.Workspace](t, "SELECT body FROM workspaces ORDER BY rowid")
}

// Workspace reads one workspace.
func (t *Tx) Workspace(id string) (w.Workspace, error) {
	return get[w.Workspace](t, "SELECT body FROM workspaces WHERE id=?", id)
}

// SaveWorkspace creates or replaces a workspace in the current transaction.
func (t *Tx) SaveWorkspace(v w.Workspace) error {
	return t.exec("INSERT INTO workspaces(id,revision,body) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,body=excluded.body", v.ID, v.Revision, body(v))
}

// Agents lists the workspace's stable member identities.
func (t *Tx) Agents(workspaceID string) ([]w.Agent, error) {
	return list[w.Agent](t, "SELECT body FROM agent_profiles WHERE workspace_id=? ORDER BY rowid", workspaceID)
}

// Agent reads a member.
func (t *Tx) Agent(id string) (w.Agent, error) {
	return get[w.Agent](t, "SELECT body FROM agent_profiles WHERE id=?", id)
}

// SaveAgent stores a validated member; handle uniqueness is also enforced by SQL.
func (t *Tx) SaveAgent(v w.Agent) error {
	return t.exec("INSERT INTO agent_profiles(id,workspace_id,handle,revision,body) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET handle=excluded.handle,revision=excluded.revision,body=excluded.body", v.ID, v.WorkspaceID, v.Handle, v.Revision, body(v))
}

// Threads lists conversations by recent activity.
func (t *Tx) Threads(workspaceID string) ([]w.Thread, error) {
	return list[w.Thread](t, "SELECT body FROM threads WHERE workspace_id=? ORDER BY updated_at DESC,id DESC", workspaceID)
}

// Thread reads a conversation.
func (t *Tx) Thread(id string) (w.Thread, error) {
	return get[w.Thread](t, "SELECT body FROM threads WHERE id=?", id)
}

// SaveThread persists configuration and activity order.
func (t *Tx) SaveThread(v w.Thread) error {
	return t.exec("INSERT INTO threads(id,workspace_id,updated_at,revision,body) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at,revision=excluded.revision,body=excluded.body", v.ID, v.WorkspaceID, v.UpdatedAt, v.Revision, body(v))
}

// Run reads a run snapshot.
func (t *Tx) Run(id string) (w.Run, error) {
	return get[w.Run](t, "SELECT body FROM runs WHERE id=?", id)
}

// Runs lists a conversation's runs in creation order.
func (t *Tx) Runs(threadID string) ([]w.Run, error) {
	return list[w.Run](t, "SELECT body FROM runs WHERE thread_id=? ORDER BY created_at,id", threadID)
}

// Active returns queued or executing runs in FIFO order.
func (t *Tx) Active() ([]w.Run, error) {
	return list[w.Run](t, "SELECT body FROM runs WHERE status IN ('queued','running','canceling') ORDER BY created_at,id")
}

// Idempotent reads the original run and normalized request digest.
func (t *Tx) Idempotent(threadID, key string) (w.Run, string, error) {
	var r w.Run
	var b, hash string
	err := t.tx.QueryRowContext(t.ctx, "SELECT body,request_hash FROM runs WHERE thread_id=? AND idempotency_key=?", threadID, key).Scan(&b, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return r, "", ErrNotFound
	}
	if err != nil {
		return r, "", err
	}
	err = json.Unmarshal([]byte(b), &r)
	return r, hash, err
}

// InsertRun relies on unique constraints for idempotency and active-run exclusion.
func (t *Tx) InsertRun(v w.Run, key, hash string) error {
	return t.exec("INSERT INTO runs(id,workspace_id,thread_id,status,idempotency_key,request_hash,created_at,body) VALUES(?,?,?,?,?,?,?,?)", v.ID, v.WorkspaceID, v.ThreadID, v.Status, key, hash, v.CreatedAt, body(v))
}

// SaveRun refuses to rewrite a previously committed terminal state.
func (t *Tx) SaveRun(v w.Run) error {
	old, err := t.Run(v.ID)
	if err != nil {
		return err
	}
	if w.Terminal(old.Status) {
		return w.ErrConflict
	}
	return t.exec("UPDATE runs SET status=?,body=? WHERE id=?", v.Status, body(v), v.ID)
}

// Steps reads a run's deterministic plan.
func (t *Tx) Steps(runID string) ([]w.Step, error) {
	return list[w.Step](t, "SELECT body FROM steps WHERE run_id=? ORDER BY position", runID)
}

// SaveStep persists a planned step or its state without changing its position.
func (t *Tx) SaveStep(v w.Step) error {
	return t.exec("INSERT INTO steps(id,run_id,status,position,body) VALUES(?,?,?,(SELECT COUNT(*) FROM steps WHERE run_id=?),?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,body=excluded.body", v.ID, v.RunID, v.Status, v.RunID, body(v))
}

// Messages returns stable timeline order, including unfinished bubbles.
func (t *Tx) Messages(threadID string) ([]w.Message, error) {
	return list[w.Message](t, "SELECT body FROM messages WHERE thread_id=? ORDER BY position", threadID)
}

// SaveMessage updates one bubble rather than appending streaming duplicates.
func (t *Tx) SaveMessage(v w.Message) error {
	return t.exec("INSERT INTO messages(id,thread_id,run_id,position,body) VALUES(?,?,?,(SELECT COALESCE(MAX(position),0)+1 FROM messages),?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", v.ID, v.ThreadID, v.RunID, body(v))
}

// AppendEvent allocates a per-run sequence inside the state transaction.
func (t *Tx) AppendEvent(runID, stepID, agentID, kind string, data any) error {
	var seq int64
	if err := t.tx.QueryRowContext(t.ctx, "SELECT COALESCE(MAX(seq),0)+1 FROM events WHERE run_id=?", runID).Scan(&seq); err != nil {
		return err
	}
	e := w.Event{SchemaVersion: 1, RunID: runID, Seq: seq, Type: kind, OccurredAt: w.Now(), StepID: stepID, AgentID: agentID, Data: json.RawMessage(body(data))}
	return t.exec("INSERT INTO events(run_id,seq,type,body) VALUES(?,?,?,?)", runID, seq, kind, body(e))
}

// Events returns bounded replay batches after an exclusive sequence cursor.
func (t *Tx) Events(runID string, after int64) ([]w.Event, error) {
	return list[w.Event](t, "SELECT body FROM events WHERE run_id=? AND seq>? ORDER BY seq LIMIT 500", runID, after)
}

// Cursor reads the latest committed event sequence.
func (t *Tx) Cursor(runID string) (int64, error) {
	var v int64
	err := t.tx.QueryRowContext(t.ctx, "SELECT COALESCE(MAX(seq),0) FROM events WHERE run_id=?", runID).Scan(&v)
	return v, err
}

// SaveSource creates an immutable evidence capture.
func (t *Tx) SaveSource(v w.Source) error {
	return t.exec("INSERT INTO sources(id,workspace_id,thread_id,run_id,body) VALUES(?,?,?,?,?)", v.ID, v.WorkspaceID, v.ThreadID, v.RunID, body(v))
}

// Source reads captured evidence, never the current file or web page.
func (t *Tx) Source(id string) (w.Source, error) {
	return get[w.Source](t, "SELECT body FROM sources WHERE id=?", id)
}

// Sources returns the evidence available in one conversation.
func (t *Tx) Sources(threadID string) ([]w.Source, error) {
	return list[w.Source](t, "SELECT body FROM sources WHERE thread_id=? ORDER BY rowid", threadID)
}

// Artifacts returns all immutable versions for a conversation.
func (t *Tx) Artifacts(threadID string) ([]w.Artifact, error) {
	return list[w.Artifact](t, "SELECT body FROM artifacts WHERE thread_id=? ORDER BY rowid", threadID)
}

// Artifact reads an exact artifact version.
func (t *Tx) Artifact(id string, version int) (w.Artifact, error) {
	return get[w.Artifact](t, "SELECT body FROM artifacts WHERE id=? AND version=?", id, version)
}

// SaveArtifact creates a version or updates its separate feedback metadata.
func (t *Tx) SaveArtifact(v w.Artifact) error {
	return t.exec("INSERT INTO artifacts(id,version,thread_id,run_id,revision,body) VALUES(?,?,?,?,?,?) ON CONFLICT(id,version) DO UPDATE SET revision=excluded.revision,body=excluded.body", v.ID, v.Version, v.ThreadID, v.RunID, v.Revision, body(v))
}
