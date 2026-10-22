package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AuditSince lists audit rows after id, oldest first (at most 200).
func (s *Store) AuditSince(ctx context.Context, id int64) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, username, action, detail FROM audit_log WHERE id > ? ORDER BY id LIMIT 200`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Username, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// LastAuditID is the newest audit row's id (0 if none).
func (s *Store) LastAuditID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT max(id) FROM audit_log`).Scan(&id)
	return id.Int64, err
}

// Event is one finished piece of work Roostack did on a server: its log,
// kept as history (SPEC.md §6.14).
type Event struct {
	ID                int64
	Started, Finished time.Time
	Kind, Title       string
	Status, Error     string
	Output            string `json:",omitempty"` // the log's lines
}

// MaxEvents is how many events a server keeps, whatever their age.
const MaxEvents = 5000

// AddEvent keeps a finished log, and drops the oldest beyond MaxEvents.
func (s *Store) AddEvent(ctx context.Context, e Event) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO events (started, finished, kind, title, status, error, output) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.Started.Unix(), e.Finished.Unix(), e.Kind, e.Title, e.Status, e.Error, e.Output)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM events WHERE id <= (SELECT max(id) FROM events) - ?`, MaxEvents)
	return err
}

// Events lists events finished before before (zero: now), newest first,
// without their output; kind filters when not "".
func (s *Store) Events(ctx context.Context, before time.Time, kind string, limit int) ([]Event, error) {
	if before.IsZero() {
		before = time.Now().Add(time.Minute)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, started, finished, kind, title, status, error FROM events
		WHERE finished < ? AND (? = '' OR kind = ?) ORDER BY finished DESC, id DESC LIMIT ?`, before.Unix(), kind, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var started, finished int64
		if err := rows.Scan(&e.ID, &started, &finished, &e.Kind, &e.Title, &e.Status, &e.Error); err != nil {
			return nil, err
		}
		e.Started, e.Finished = time.Unix(started, 0), time.Unix(finished, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventByID is one event, with its output.
func (s *Store) EventByID(ctx context.Context, id int64) (Event, error) {
	var e Event
	var started, finished int64
	err := s.db.QueryRowContext(ctx, `SELECT id, started, finished, kind, title, status, error, output FROM events WHERE id = ?`, id).
		Scan(&e.ID, &started, &finished, &e.Kind, &e.Title, &e.Status, &e.Error, &e.Output)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	e.Started, e.Finished = time.Unix(started, 0), time.Unix(finished, 0)
	return e, err
}

// AuditBefore lists audit rows before before (zero: now), newest first.
func (s *Store) AuditBefore(ctx context.Context, before time.Time, limit int) ([]AuditEntry, error) {
	if before.IsZero() {
		before = time.Now().Add(time.Minute)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, username, action, detail FROM audit_log WHERE at < ? ORDER BY at DESC, id DESC LIMIT ?`,
		before.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Username, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// CullHistory deletes events finished before before; it returns how
// many went.
func (s *Store) CullHistory(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE finished < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CullAudit deletes audit rows older than before; it returns how many
// went.
func (s *Store) CullAudit(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit_log WHERE at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// AuditEntry is one audit log row.
type AuditEntry struct {
	ID       int64
	At       time.Time
	Username string
	Action   string
	Detail   string
}

// Audit records who did what (SPEC.md §5). A nil user means the system or
// the command line.
func (s *Store) Audit(ctx context.Context, u *User, action, detail string) error {
	var id any
	name := "system"
	if u != nil {
		id, name = u.ID, u.Username
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (at, user_id, username, action, detail) VALUES (?, ?, ?, ?, ?)`,
		time.Now().Unix(), id, name, action, detail)
	return err
}

// RecentAudit returns the newest entries first.
func (s *Store) RecentAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT at, username, action, detail FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		if err := rows.Scan(&at, &e.Username, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}
