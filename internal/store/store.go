// Package store holds Roostack's runtime data in SQLite (SPEC.md §5.1): users,
// sessions, the audit log, settings, backup runs, the lab's servers, backup
// destinations and storage for other labs. Desired state lives in YAML files
// instead.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure Go driver, keeps the binary static
)

// ErrNotFound is returned when a row doesn't exist.
var ErrNotFound = errors.New("not found")

// Store is the runtime database.
type Store struct {
	db *sql.DB
}

// migrations are applied in order; never edit one that has shipped.
var migrations = []string{
	`CREATE TABLE users (
		id            INTEGER PRIMARY KEY,
		username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT NOT NULL,
		role          TEXT NOT NULL,
		totp_secret   TEXT NOT NULL DEFAULT '',
		created_at    INTEGER NOT NULL
	);
	CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL
	);
	CREATE TABLE audit_log (
		id       INTEGER PRIMARY KEY,
		at       INTEGER NOT NULL,
		user_id  INTEGER,
		username TEXT NOT NULL,
		action   TEXT NOT NULL,
		detail   TEXT NOT NULL DEFAULT ''
	);`,
	// The last accepted TOTP time step, so a code can't be reused.
	`ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;`,
	`CREATE TABLE backup_runs (
		id          INTEGER PRIMARY KEY,
		started_at  INTEGER NOT NULL,
		finished_at INTEGER NOT NULL,
		ok          INTEGER NOT NULL,
		snapshot_id TEXT NOT NULL DEFAULT '',
		files_new   INTEGER NOT NULL DEFAULT 0,
		data_added  INTEGER NOT NULL DEFAULT 0,
		error       TEXT NOT NULL DEFAULT ''
	);`,
	// Small runtime flags, e.g. setup wizard progress.
	`CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,
	// Set for passwords printed by the command line: the user chooses
	// their own at the next login.
	`ALTER TABLE users ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE users ADD COLUMN theme TEXT NOT NULL DEFAULT 'auto';`,
	// The colour theme ('' = the default).
	`ALTER TABLE users ADD COLUMN palette TEXT NOT NULL DEFAULT '';`,
	// The theme's accent colour ('' = the theme's default).
	`ALTER TABLE users ADD COLUMN accent TEXT NOT NULL DEFAULT '';`,
	// Other machines in the lab (SPEC.md §10), and join codes not yet used.
	`CREATE TABLE machines (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		addr       TEXT NOT NULL UNIQUE,
		public_key TEXT NOT NULL,
		joined_at  INTEGER NOT NULL
	);
	CREATE TABLE join_codes (
		token_hash TEXT PRIMARY KEY,
		addr       TEXT NOT NULL UNIQUE,
		public_key TEXT NOT NULL,
		expires_at INTEGER NOT NULL
	);`,
	// Where each machine backs up (SPEC.md §10.10). A provider's config is
	// JSON and may hold credentials; the database is readable by Roostack only.
	`CREATE TABLE backup_providers (
		id     INTEGER PRIMARY KEY,
		kind   TEXT NOT NULL,
		name   TEXT NOT NULL,
		config TEXT NOT NULL
	);
	CREATE TABLE backup_targets (
		machine_id  TEXT PRIMARY KEY,
		provider    TEXT NOT NULL,
		repository  TEXT NOT NULL,
		store_host  TEXT NOT NULL DEFAULT '',
		store_hash  TEXT NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL
	);
	CREATE TABLE store_admins (
		machine_id TEXT PRIMARY KEY,
		password   TEXT NOT NULL,
		hash       TEXT NOT NULL
	);`,
	// A provider key made for one machine (e.g. a B2 key limited to its
	// folder), deleted when the machine leaves.
	`ALTER TABLE backup_targets ADD COLUMN scoped_key TEXT NOT NULL DEFAULT '';`,
	// Other labs storing backups here (SPEC.md §10.10), and this lab's
	// storage at other labs.
	`CREATE TABLE storage_guests (
		id          INTEGER PRIMARY KEY,
		name        TEXT NOT NULL,
		quota       INTEGER NOT NULL,
		addr        TEXT NOT NULL UNIQUE,
		public_key  TEXT NOT NULL,
		joined      INTEGER NOT NULL DEFAULT 0,
		token_hash  TEXT NOT NULL,
		expires_at  INTEGER NOT NULL,
		port        INTEGER NOT NULL UNIQUE,
		store_hash  TEXT NOT NULL,
		admin_hash  TEXT NOT NULL,
		created_at  INTEGER NOT NULL
	);`,
	// Machines are called servers everywhere (SPEC.md §10.1).
	`ALTER TABLE machines RENAME TO servers;
	ALTER TABLE backup_targets RENAME COLUMN machine_id TO server_id;
	ALTER TABLE store_admins RENAME COLUMN machine_id TO server_id;
	UPDATE backup_targets SET provider = 'server:' || substr(provider, 9) WHERE provider LIKE 'machine:%';`,
	// The certificate each server may use on the lab API (SPEC.md §10.3):
	// its current serial, and the one it replaced until the new one is used.
	`ALTER TABLE servers ADD COLUMN cert_serial TEXT NOT NULL DEFAULT '';
	ALTER TABLE servers ADD COLUMN prev_serial TEXT NOT NULL DEFAULT '';
	ALTER TABLE servers ADD COLUMN cert_expires INTEGER NOT NULL DEFAULT 0;`,
	// Extra copies of a server's backups, somewhere else (SPEC.md §6.9,
	// 3-2-1), and which destination each backup run went to ('' is the
	// server's main one).
	`CREATE TABLE backup_copies (
		server_id   TEXT NOT NULL,
		provider    TEXT NOT NULL,
		repository  TEXT NOT NULL,
		store_host  TEXT NOT NULL DEFAULT '',
		store_hash  TEXT NOT NULL DEFAULT '',
		scoped_key  TEXT NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL,
		PRIMARY KEY (server_id, provider)
	);
	ALTER TABLE backup_runs ADD COLUMN destination TEXT NOT NULL DEFAULT '';`,
	// A removed guest lab is kept for a while (its data, and its link so it
	// can be told) before it's deleted.
	`ALTER TABLE storage_guests ADD COLUMN removed_at INTEGER NOT NULL DEFAULT 0;`,
	// The space a backup's repository took after each run (0 if unknown).
	`ALTER TABLE backup_runs ADD COLUMN stored INTEGER NOT NULL DEFAULT 0;`,
	// Wrong two-factor codes in a row, and when the last was entered, to
	// limit guesses per account (authkit.TOTPLockedUntil).
	`ALTER TABLE users ADD COLUMN totp_failures INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE users ADD COLUMN totp_failed_at INTEGER NOT NULL DEFAULT 0;`,
	// Where Roostack emails someone (SPEC.md §6.13); empty: no email.
	`ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';`,
	// Notifications (SPEC.md §6.13): the groups someone chose ("" for
	// their role's defaults); what's been emailed about, per person; and
	// the browsers each person has logged in from (a new one is a
	// security email).
	`ALTER TABLE users ADD COLUMN notify TEXT NOT NULL DEFAULT '';
	CREATE TABLE notices (
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		key        TEXT NOT NULL,
		text       TEXT NOT NULL,
		first_seen INTEGER NOT NULL,
		sent_at    INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (user_id, key)
	);
	CREATE TABLE devices (
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		token_hash TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (user_id, token_hash)
	);`,
	// History (SPEC.md §6.14): each finished log of work Roostack did on
	// this server (applies, backups, updates, ...), with its output.
	`CREATE TABLE events (
		id       INTEGER PRIMARY KEY,
		started  INTEGER NOT NULL,
		finished INTEGER NOT NULL,
		kind     TEXT NOT NULL,
		title    TEXT NOT NULL,
		status   TEXT NOT NULL DEFAULT '',
		error    TEXT NOT NULL DEFAULT '',
		output   TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX events_finished ON events (finished);`,
	// Signing out after a while without activity (SPEC.md §5): when each
	// session was last used, and whether it was "keep me signed in".
	`ALTER TABLE sessions ADD COLUMN last_seen INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE sessions ADD COLUMN remember INTEGER NOT NULL DEFAULT 0;`,
	// Sessions from before count as used now, so the update doesn't sign
	// everyone out.
	`UPDATE sessions SET last_seen = CAST(strftime('%s', 'now') AS INTEGER) WHERE last_seen = 0;`,
	// When an emailed notice stopped being seen: "Fixed" waits until it
	// has been gone a while, so a moment's gap isn't news (SPEC.md §6.13).
	`ALTER TABLE notices ADD COLUMN gone_since INTEGER NOT NULL DEFAULT 0;`,
	// Where the controller dials a server that can be reached and it
	// can't (a cloud server joining a lab at home, SPEC.md §10.2): typed
	// with its join code, kept once it has joined.
	`ALTER TABLE join_codes ADD COLUMN dial TEXT NOT NULL DEFAULT '';
	ALTER TABLE servers ADD COLUMN dial TEXT NOT NULL DEFAULT '';`,
	// Main backups that were on a server that left the lab, until each
	// server has a new one (SPEC.md §10.10).
	`CREATE TABLE lost_backups (
		server_id TEXT PRIMARY KEY,
		host_name TEXT NOT NULL,
		lost_at   INTEGER NOT NULL
	);`,
	// Each guest lab's servers have their own user at its store (SPEC.md
	// §10.10, review M21): the bcrypt hash of each one's password.
	`CREATE TABLE storage_guest_users (
		guest_id INTEGER NOT NULL REFERENCES storage_guests(id) ON DELETE CASCADE,
		name     TEXT NOT NULL,
		hash     TEXT NOT NULL,
		PRIMARY KEY (guest_id, name)
	);`,
	// Where a session was signed in (SPEC.md §6.3.2): "" for Roostack's
	// pages, OriginSetup for the setup pages on the home network. Each
	// works only where it was made.
	`ALTER TABLE sessions ADD COLUMN origin TEXT NOT NULL DEFAULT '';`,
}

// OriginSetup marks sessions signed in on the setup pages on the home
// network (SPEC.md §6.3.2).
const OriginSetup = "setup"

// Open opens (creating if needed) and migrates the database.
func Open(path string) (*Store, error) {
	// Create the file ourselves so it is private: it holds password hashes.
	// SQLite gives the -wal and -shm files the same permissions.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := checkFiles(path); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; a single connection avoids "database is
	// locked" errors and is plenty for Roostack.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	return s, nil
}

// checkFiles refuses a database whose file is a symlink, or anything but a
// plain file. SQLite follows a symlink for the database itself, and the
// modernc driver has no way to ask it not to (SQLITE_OPEN_NOFOLLOW); it
// opens the -wal and -shm files with O_NOFOLLOW (docs/ASSUMPTIONS.md S1).
// The folder belongs to the roostack user, who could put a symlink there,
// so the command line never opens the database as root either (SPEC.md
// §6.1). They're checked too, for a clear message.
func checkFiles(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) && p != path {
			continue
		}
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("store: %s isn't a plain file (a symlink?): move it aside", p)
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Backup writes a consistent copy of the database to path, for the backup
// prepare step (SPEC.md §5.1). VACUUM INTO refuses to overwrite, so the copy
// goes to a temporary file that replaces path atomically. It holds password
// hashes, so it's private like the database (SQLite creates it with the
// process's umask).
func (s *Store) Backup(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	os.Remove(tmp)
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// User is a Roostack account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	TOTPSecret   string
	TOTPLastStep int64
	// TOTPFailures counts wrong two-factor codes since the last right one,
	// the last entered at TOTPFailedAt (authkit.TOTPLockedUntil).
	TOTPFailures int
	TOTPFailedAt time.Time
	// MustChangePassword is set for temporary passwords (CLI add/reset).
	MustChangePassword bool
	// Email is where Roostack emails them (SPEC.md §6.13); empty: no email.
	Email string
	// Notify is the notification groups they chose, comma-separated ("" for
	// their role's defaults, "none" for none).
	Notify string
	// Theme is "auto" (follow the device), "light" or "dark".
	Theme string
	// Palette is the colour theme (internal/web themes), "" for the default.
	Palette string
	// Accent is the theme's accent colour, "" for its default.
	Accent    string
	CreatedAt time.Time
}

// SetAccent saves a user's accent colour (the caller checks it exists).
func (s *Store) SetAccent(ctx context.Context, userID int64, accent string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET accent = ? WHERE id = ?`, accent, userID)
	return err
}

// SetPalette saves a user's colour theme (the caller checks it exists).
func (s *Store) SetPalette(ctx context.Context, userID int64, palette string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET palette = ? WHERE id = ?`, palette, userID)
	return err
}

// ErrLastOwner means a change would leave the lab without an Owner.
var ErrLastOwner = errors.New("the lab must keep at least one Owner")

// ListUsers returns every account, by username.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := scanUserRow(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

// SetRole changes a user's role. Demoting the last Owner fails with
// ErrLastOwner; the check is part of the update, so two Owners demoting
// each other at once can't both succeed.
func (s *Store) SetRole(ctx context.Context, userID int64, role string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ? AND (? = 'owner' OR role != 'owner'
		OR (SELECT count(*) FROM users WHERE role = 'owner') > 1)`, role, userID, role)
	if err != nil {
		return err
	}
	return s.lastOwnerCheck(ctx, res, userID)
}

// DeleteUser removes an account and its sessions. Removing the last Owner
// fails with ErrLastOwner.
func (s *Store) DeleteUser(ctx context.Context, userID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ? AND (role != 'owner'
		OR (SELECT count(*) FROM users WHERE role = 'owner') > 1)`, userID)
	if err != nil {
		return err
	}
	return s.lastOwnerCheck(ctx, res, userID)
}

// lastOwnerCheck explains why a guarded update changed nothing.
func (s *Store) lastOwnerCheck(ctx context.Context, res sql.Result, userID int64) error {
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, err := s.UserByID(ctx, userID); err != nil {
		return err // ErrNotFound
	}
	return ErrLastOwner
}

// ResetTOTP turns off a user's two-factor authentication (a lost phone)
// and logs them out everywhere. Owners must set it up again at next login.
func (s *Store) ResetTOTP(ctx context.Context, userID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET totp_secret = '', totp_last_step = 0, totp_failures = 0, totp_failed_at = 0 WHERE id = ?`, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// Themes a user can choose.
var Themes = []string{"auto", "light", "dark"}

// SetTheme saves a user's theme.
func (s *Store) SetTheme(ctx context.Context, userID int64, theme string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET theme = ? WHERE id = ?`, theme, userID)
	return err
}

// SetEmail sets where Roostack emails someone ("" for nowhere).
func (s *Store) SetEmail(ctx context.Context, userID int64, email string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET email = ? WHERE id = ?`, email, userID)
	return err
}

// SetNotify sets someone's notification groups (see User.Notify).
func (s *Store) SetNotify(ctx context.Context, userID int64, groups string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET notify = ? WHERE id = ?`, groups, userID)
	return err
}

// Notice is something someone was (or will be) emailed about.
type Notice struct {
	Key, Text string
	FirstSeen time.Time
	SentAt    time.Time // zero: not emailed yet
	// GoneSince is when it stopped being seen (zero while it's there).
	GoneSince time.Time
}

// Notices lists what's being followed for a person.
func (s *Store) Notices(ctx context.Context, userID int64) (map[string]Notice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, text, first_seen, sent_at, gone_since FROM notices WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Notice{}
	for rows.Next() {
		var n Notice
		var first, sent, gone int64
		if err := rows.Scan(&n.Key, &n.Text, &first, &sent, &gone); err != nil {
			return nil, err
		}
		n.FirstSeen = time.Unix(first, 0)
		if sent != 0 {
			n.SentAt = time.Unix(sent, 0)
		}
		if gone != 0 {
			n.GoneSince = time.Unix(gone, 0)
		}
		out[n.Key] = n
	}
	return out, rows.Err()
}

// SaveNotice records a notice (insert or update).
func (s *Store) SaveNotice(ctx context.Context, userID int64, n Notice) error {
	var sent, gone int64
	if !n.SentAt.IsZero() {
		sent = n.SentAt.Unix()
	}
	if !n.GoneSince.IsZero() {
		gone = n.GoneSince.Unix()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO notices (user_id, key, text, first_seen, sent_at, gone_since) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, key) DO UPDATE SET text = excluded.text, sent_at = excluded.sent_at, gone_since = excluded.gone_since`,
		userID, n.Key, n.Text, n.FirstSeen.Unix(), sent, gone)
	return err
}

// DeleteNotice forgets a notice (it was dealt with).
func (s *Store) DeleteNotice(ctx context.Context, userID int64, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notices WHERE user_id = ? AND key = ?`, userID, key)
	return err
}

// KnownDevice records a browser someone logged in from; it reports
// whether it was new, and whether they had any before.
func (s *Store) KnownDevice(ctx context.Context, userID int64, tokenHash string, now time.Time) (isNew, hadOthers bool, err error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM devices WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return false, false, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO devices (user_id, token_hash, created_at) VALUES (?, ?, ?)`,
		userID, tokenHash, now.Unix())
	if err != nil {
		return false, false, err
	}
	added, _ := res.RowsAffected()
	return added == 1, n > 0, nil
}

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

// CountUsers returns the number of accounts.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// FirstUserCreated is when the first account (the Owner made at first-run
// setup) was created; ok is false while there's none.
func (s *Store) FirstUserCreated(ctx context.Context) (at time.Time, ok bool, err error) {
	var unix sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT min(created_at) FROM users`).Scan(&unix); err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(unix.Int64, 0), unix.Valid, nil
}

// CreateUser adds an account and returns its ID.
func (s *Store) CreateUser(ctx context.Context, u User) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, must_change_password, created_at) VALUES (?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, u.Role, u.MustChangePassword, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CreateFirstUser adds an account only if there are none, atomically, so
// two concurrent first-run setups can't both succeed. It returns false if a
// user already exists.
func (s *Store) CreateFirstUser(ctx context.Context, u User) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, created_at)
		 SELECT ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`,
		u.Username, u.PasswordHash, u.Role, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// UserByUsername looks up an account (case-insensitive).
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ?`, username))
}

// SetPassword replaces a user's password hash. A temporary password
// (e.g. from the CLI reset) must be changed at the next login.
func (s *Store) SetPassword(ctx context.Context, username, hash string, temporary bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, must_change_password = ? WHERE username = ?`,
		hash, temporary, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// Log out everywhere after a reset.
	_, err = s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username = ?)`, username)
	return err
}

const userColumns = `users.id, users.username, users.password_hash, users.role,
	users.totp_secret, users.totp_last_step, users.totp_failures, users.totp_failed_at, users.must_change_password, users.theme, users.palette, users.accent, users.email, users.notify, users.created_at`

// UserByID looks up an account.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// SetTOTP enables two-factor authentication for a user.
func (s *Store) SetTOTP(ctx context.Context, userID int64, secret string, step int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = ?, totp_last_step = ?, totp_failures = 0, totp_failed_at = 0 WHERE id = ?`, secret, step, userID)
	return err
}

// UseTOTPStep records an accepted code's time step, and clears the count
// of wrong codes. It fails if the step isn't newer than the last one, so
// two concurrent logins can't both use the same code.
func (s *Store) UseTOTPStep(ctx context.Context, userID, step int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_last_step = ?, totp_failures = 0, totp_failed_at = 0
		WHERE id = ? AND totp_last_step < ?`, step, userID, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// AddTOTPFailure counts a wrong two-factor code entered at now, and
// returns how many there have been in a row.
func (s *Store) AddTOTPFailure(ctx context.Context, userID int64, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`UPDATE users SET totp_failures = totp_failures + 1, totp_failed_at = ? WHERE id = ?
		RETURNING totp_failures`, now.Unix(), userID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return n, err
}

func (s *Store) scanUser(row *sql.Row) (*User, error) { return scanUserRow(row) }

func scanUserRow(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created, failedAt int64
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &u.TOTPLastStep,
		&u.TOTPFailures, &failedAt, &u.MustChangePassword, &u.Theme, &u.Palette, &u.Accent, &u.Email, &u.Notify, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	if failedAt != 0 {
		u.TOTPFailedAt = time.Unix(failedAt, 0)
	}
	return &u, nil
}

// CreateSession stores a session. Only a hash of the token is stored.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, userID int64, expires time.Time) error {
	return s.CreateSessionRemember(ctx, tokenHash, userID, expires, false)
}

// CreateSessionRemember stores a session; remember: "keep me signed in"
// (no sign-out for inactivity).
func (s *Store) CreateSessionRemember(ctx context.Context, tokenHash string, userID int64, expires time.Time, remember bool) error {
	return s.CreateSessionAt(ctx, "", tokenHash, userID, expires, remember)
}

// CreateSessionAt stores a session signed in at origin ("" or
// OriginSetup), where alone it works.
func (s *Store) CreateSessionAt(ctx context.Context, origin, tokenHash string, userID int64, expires time.Time, remember bool) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, last_seen, remember, origin) VALUES (?, ?, ?, ?, ?, ?)`,
		tokenHash, userID, expires.Unix(), time.Now().Unix(), remember, origin)
	return err
}

// SessionInfo is a session's user, when it was last used, and whether it's
// "keep me signed in", for a session of Roostack's own pages.
func (s *Store) SessionInfo(ctx context.Context, tokenHash string) (u *User, lastSeen time.Time, remember bool, err error) {
	return s.SessionInfoAt(ctx, "", tokenHash)
}

// SessionInfoAt is SessionInfo for a session signed in at origin: one
// from anywhere else isn't found.
func (s *Store) SessionInfoAt(ctx context.Context, origin, tokenHash string) (u *User, lastSeen time.Time, remember bool, err error) {
	var seen int64
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+`, s.last_seen, s.remember
		 FROM sessions s JOIN users ON users.id = s.user_id
		 WHERE s.token_hash = ? AND s.expires_at > ? AND s.origin = ?`, tokenHash, time.Now().Unix(), origin)
	u, err = scanUserRow(scanTail{row, []any{&seen, &remember}})
	return u, time.Unix(seen, 0), remember, err
}

// DeleteSessionsAt signs out every session signed in at origin, e.g. the
// setup pages' once they close.
func (s *Store) DeleteSessionsAt(ctx context.Context, origin string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE origin = ?`, origin)
	return err
}

// scanTail scans a user row with extra columns after the user's.
type scanTail struct {
	row   *sql.Row
	extra []any
}

func (t scanTail) Scan(dest ...any) error { return t.row.Scan(append(dest, t.extra...)...) }

// TouchSession records that a session was used at now.
func (s *Store) TouchSession(ctx context.Context, tokenHash string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen = ? WHERE token_hash = ?`, now.Unix(), tokenHash)
	return err
}

// SessionUser returns the user for a valid, unexpired session.
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+`
		 FROM sessions s JOIN users ON users.id = s.user_id
		 WHERE s.token_hash = ? AND s.expires_at > ? AND s.origin = ''`, tokenHash, time.Now().Unix()))
}

// DeleteSession logs a session out.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteExpiredSessions removes old sessions: past their expiry, or (when
// idle isn't 0) unused for longer than idle, unless kept signed in.
func (s *Store) DeleteExpiredSessions(ctx context.Context, idle time.Duration) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ? OR (? > 0 AND remember = 0 AND last_seen < ?)`,
		now.Unix(), int64(idle.Seconds()), now.Add(-idle).Unix())
	return err
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

// BackupRun is one finished backup attempt.
type BackupRun struct {
	Started, Finished time.Time
	OK                bool
	SnapshotID        string
	FilesNew          int
	DataAdded         uint64
	Error             string
	// Destination is the extra copy it went to, or "" for the main one.
	Destination string
	// Stored is the space the repository took after it (0 if unknown).
	Stored uint64
}

// RecordBackup stores the result of a backup.
func (s *Store) RecordBackup(ctx context.Context, r BackupRun) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO backup_runs (started_at, finished_at, ok, snapshot_id, files_new, data_added, error, destination, stored)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Started.Unix(), r.Finished.Unix(), r.OK, r.SnapshotID, r.FilesNew, r.DataAdded, r.Error, r.Destination, r.Stored)
	return err
}

// LastBackup returns the most recent attempt to the main destination, and
// the most recent success (which may be the same run). Either may be nil.
func (s *Store) LastBackup(ctx context.Context) (last, lastOK *BackupRun, err error) {
	return s.LastBackupTo(ctx, "")
}

// LastBackupTo is LastBackup for one destination ("" is the main one).
func (s *Store) LastBackupTo(ctx context.Context, destination string) (last, lastOK *BackupRun, err error) {
	last, err = s.scanBackup(s.db.QueryRowContext(ctx, backupQuery+` WHERE destination = ? ORDER BY id DESC LIMIT 1`, destination))
	if err != nil {
		return nil, nil, err
	}
	lastOK, err = s.scanBackup(s.db.QueryRowContext(ctx,
		backupQuery+` WHERE ok = 1 AND destination = ? ORDER BY id DESC LIMIT 1`, destination))
	return last, lastOK, err
}

const backupQuery = `SELECT started_at, finished_at, ok, snapshot_id, files_new, data_added, error, destination, stored FROM backup_runs`

func (s *Store) scanBackup(row *sql.Row) (*BackupRun, error) {
	var r BackupRun
	var started, finished int64
	err := row.Scan(&started, &finished, &r.OK, &r.SnapshotID, &r.FilesNew, &r.DataAdded, &r.Error, &r.Destination, &r.Stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Started, r.Finished = time.Unix(started, 0), time.Unix(finished, 0)
	return &r, nil
}

// Setting returns a runtime setting, or "" if unset.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SettingsWithPrefix returns every runtime setting whose key starts with
// prefix, e.g. the DNS records Roostack owns ("dns.owned.").
func (s *Store) SettingsWithPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM settings WHERE substr(key, 1, ?) = ? ORDER BY key`, len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSetting stores a runtime setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Server is another server in the lab (SPEC.md §10).
type Server struct {
	ID, Name string
	Addr     string // on Roostack's network
	// PublicKey is its WireGuard key.
	PublicKey string
	JoinedAt  time.Time
	// CertSerial is the certificate it may use on the lab API; PrevSerial
	// the one that certificate replaced, accepted until the new one is
	// used. An empty CertSerial (joined before serials were kept) accepts
	// any certificate the lab's CA signed for it.
	CertSerial, PrevSerial string
	CertExpires            time.Time
	// Dial is where the controller dials it (host:port), when it can be
	// reached and the controller can't; "" when it dials the controller.
	Dial string
}

// JoinCode is a join code waiting to be used. Only the token's hash is
// kept; PublicKey is the code's one-time WireGuard key.
type JoinCode struct {
	TokenHash, Addr, PublicKey string
	Expires                    time.Time
	// Dial is where the controller dials the new server ("" when the new
	// server dials the controller); the server keeps it once joined.
	Dial string
}

// ErrJoinCode means a join code is unknown, used or expired.
var ErrJoinCode = errors.New("the join code is unknown, already used or expired")

// AddJoinCode records a new join code.
func (s *Store) AddJoinCode(ctx context.Context, c JoinCode) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO join_codes (token_hash, addr, public_key, expires_at, dial) VALUES (?, ?, ?, ?, ?)`,
		c.TokenHash, c.Addr, c.PublicKey, c.Expires.Unix(), c.Dial)
	return err
}

// JoinCodes lists the join codes that haven't expired, dropping the rest.
func (s *Store) JoinCodes(ctx context.Context, now time.Time) ([]JoinCode, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM join_codes WHERE expires_at <= ?`, now.Unix()); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT token_hash, addr, public_key, expires_at, dial FROM join_codes ORDER BY addr`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JoinCode
	for rows.Next() {
		var c JoinCode
		var exp int64
		if err := rows.Scan(&c.TokenHash, &c.Addr, &c.PublicKey, &exp, &c.Dial); err != nil {
			return nil, err
		}
		c.Expires = time.Unix(exp, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Join uses a join code: it adds the server at the code's address and
// deletes the code, in one transaction, so a code works once.
func (s *Store) Join(ctx context.Context, tokenHash string, m Server, now time.Time) (Server, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return m, err
	}
	defer tx.Rollback()
	var exp int64
	err = tx.QueryRowContext(ctx, `SELECT addr, expires_at, dial FROM join_codes WHERE token_hash = ?`, tokenHash).Scan(&m.Addr, &exp, &m.Dial)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && exp <= now.Unix()) {
		return m, ErrJoinCode
	}
	if err != nil {
		return m, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM join_codes WHERE token_hash = ?`, tokenHash); err != nil {
		return m, err
	}
	m.JoinedAt = now
	// Joining again (e.g. after reinstalling) replaces the old entry.
	// Its certificate is recorded once signed (SetServerCert); until then
	// none is accepted, not even one from an earlier join.
	if _, err := tx.ExecContext(ctx, `INSERT INTO servers (id, name, addr, public_key, joined_at, cert_serial, dial)
		VALUES (?, ?, ?, ?, ?, 'pending', ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, addr = excluded.addr, public_key = excluded.public_key,
		joined_at = excluded.joined_at, cert_serial = 'pending', prev_serial = '', cert_expires = 0, dial = excluded.dial`,
		m.ID, m.Name, m.Addr, m.PublicKey, now.Unix(), m.Dial); err != nil {
		return m, err
	}
	return m, tx.Commit()
}

// Servers lists the other servers in the lab.
func (s *Store) Servers(ctx context.Context) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, addr, public_key, joined_at, cert_serial, prev_serial, cert_expires, dial
		FROM servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		var m Server
		var joined, expires int64
		if err := rows.Scan(&m.ID, &m.Name, &m.Addr, &m.PublicKey, &joined, &m.CertSerial, &m.PrevSerial, &expires, &m.Dial); err != nil {
			return nil, err
		}
		m.JoinedAt = time.Unix(joined, 0)
		if expires > 0 {
			m.CertExpires = time.Unix(expires, 0)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetServerCert records the certificate a server may use, and the one it
// replaces (or "").
func (s *Store) SetServerCert(ctx context.Context, id, serial, prev string, expires time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE servers SET cert_serial = ?, prev_serial = ?, cert_expires = ? WHERE id = ?`,
		serial, prev, expires.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveServer forgets a server.
func (s *Store) RemoveServer(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM servers WHERE id = ?`, id)
	return err
}

// RenameServer changes a server's name.
func (s *Store) RenameServer(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE servers SET name = ? WHERE id = ?`, name, id)
	return err
}

// BackupProvider is a place backups can go (SPEC.md §10.10).
type BackupProvider struct {
	ID     int64
	Kind   string
	Name   string
	Config string // JSON, by kind
}

// AddBackupProvider records a provider and returns its ID.
func (s *Store) AddBackupProvider(ctx context.Context, p BackupProvider) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO backup_providers (kind, name, config) VALUES (?, ?, ?)`,
		p.Kind, p.Name, p.Config)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateBackupProvider changes a provider's name and config.
func (s *Store) UpdateBackupProvider(ctx context.Context, p BackupProvider) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backup_providers SET name = ?, config = ? WHERE id = ?`, p.Name, p.Config, p.ID)
	return err
}

// BackupProviders lists the providers.
func (s *Store) BackupProviders(ctx context.Context) ([]BackupProvider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, name, config FROM backup_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupProvider
	for rows.Next() {
		var p BackupProvider
		if err := rows.Scan(&p.ID, &p.Kind, &p.Name, &p.Config); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BackupTarget is where a server backs up.
type BackupTarget struct {
	ServerID string
	// Provider names it: "server:<id>" (a server's disk) or "provider:<id>".
	Provider   string
	Repository string
	// StoreHost and StoreHash: for a server's disk, the storing server
	// and the bcrypt hash of this server's password there.
	StoreHost, StoreHash string
	// ScopedKey is the ID of a provider key made for this server alone.
	ScopedKey string
	Created   time.Time
}

// SetBackupTarget records where a server backs up.
func (s *Store) SetBackupTarget(ctx context.Context, t BackupTarget) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO backup_targets (server_id, provider, repository, store_host, store_hash, scoped_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(server_id) DO UPDATE SET provider = excluded.provider,
		repository = excluded.repository, store_host = excluded.store_host, store_hash = excluded.store_hash,
		scoped_key = excluded.scoped_key, created_at = excluded.created_at`,
		t.ServerID, t.Provider, t.Repository, t.StoreHost, t.StoreHash, t.ScopedKey, t.Created.Unix())
	return err
}

// RemoveBackupTarget forgets where a server backs up (its backups stay).
func (s *Store) RemoveBackupTarget(ctx context.Context, serverID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM backup_targets WHERE server_id = ?`, serverID)
	return err
}

// LostBackup is a server's main backup that was on another server's disk
// when that server left the lab: nothing can reach it now, and the server
// needs a new place for its backups (SPEC.md §10.10).
type LostBackup struct {
	ServerID string
	// HostName is the name of the server it was on.
	HostName string
	Lost     time.Time
}

// SetLostBackup records that a server's main backup was lost.
func (s *Store) SetLostBackup(ctx context.Context, l LostBackup) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO lost_backups (server_id, host_name, lost_at) VALUES (?, ?, ?)
		ON CONFLICT(server_id) DO UPDATE SET host_name = excluded.host_name, lost_at = excluded.lost_at`,
		l.ServerID, l.HostName, l.Lost.Unix())
	return err
}

// RemoveLostBackup forgets a lost main backup (the server has a new one,
// or left the lab too).
func (s *Store) RemoveLostBackup(ctx context.Context, serverID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM lost_backups WHERE server_id = ?`, serverID)
	return err
}

// LostBackups lists the lost main backups.
func (s *Store) LostBackups(ctx context.Context) ([]LostBackup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id, host_name, lost_at FROM lost_backups ORDER BY server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LostBackup
	for rows.Next() {
		var l LostBackup
		var lost int64
		if err := rows.Scan(&l.ServerID, &l.HostName, &lost); err != nil {
			return nil, err
		}
		l.Lost = time.Unix(lost, 0)
		out = append(out, l)
	}
	return out, rows.Err()
}

// BackupTargets lists where each server backs up.
func (s *Store) BackupTargets(ctx context.Context) ([]BackupTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id, provider, repository, store_host, store_hash, scoped_key, created_at
		FROM backup_targets ORDER BY server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupTarget
	for rows.Next() {
		var t BackupTarget
		var created int64
		if err := rows.Scan(&t.ServerID, &t.Provider, &t.Repository, &t.StoreHost, &t.StoreHash, &t.ScopedKey, &created); err != nil {
			return nil, err
		}
		t.Created = time.Unix(created, 0)
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetBackupCopy records an extra copy of a server's backups (one per
// destination: Provider).
func (s *Store) SetBackupCopy(ctx context.Context, t BackupTarget) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO backup_copies (server_id, provider, repository, store_host, store_hash, scoped_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(server_id, provider) DO UPDATE SET repository = excluded.repository,
		store_host = excluded.store_host, store_hash = excluded.store_hash, scoped_key = excluded.scoped_key,
		created_at = excluded.created_at`,
		t.ServerID, t.Provider, t.Repository, t.StoreHost, t.StoreHash, t.ScopedKey, t.Created.Unix())
	return err
}

// BackupCopies lists every server's extra copies.
func (s *Store) BackupCopies(ctx context.Context) ([]BackupTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id, provider, repository, store_host, store_hash, scoped_key, created_at
		FROM backup_copies ORDER BY server_id, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupTarget
	for rows.Next() {
		var t BackupTarget
		var created int64
		if err := rows.Scan(&t.ServerID, &t.Provider, &t.Repository, &t.StoreHost, &t.StoreHash, &t.ScopedKey, &created); err != nil {
			return nil, err
		}
		t.Created = time.Unix(created, 0)
		out = append(out, t)
	}
	return out, rows.Err()
}

// RemoveBackupCopy forgets an extra copy (its backups stay where they are).
func (s *Store) RemoveBackupCopy(ctx context.Context, serverID, provider string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM backup_copies WHERE server_id = ? AND provider = ?`, serverID, provider)
	return err
}

// StoreAdmin returns the controller's admin password (and its hash) for
// a server's backup store, ok false if none yet.
func (s *Store) StoreAdmin(ctx context.Context, serverID string) (password, hash string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT password, hash FROM store_admins WHERE server_id = ?`, serverID).Scan(&password, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return password, hash, err == nil, err
}

// SetStoreAdmin records the controller's admin password for a store.
func (s *Store) SetStoreAdmin(ctx context.Context, serverID, password, hash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO store_admins (server_id, password, hash) VALUES (?, ?, ?)
		ON CONFLICT(server_id) DO UPDATE SET password = excluded.password, hash = excluded.hash`, serverID, password, hash)
	return err
}

// LabID names the lab in backup folders (SPEC.md §10.10): made once, never
// changed.
func (s *Store) LabID(ctx context.Context) (string, error) {
	id, err := s.Setting(ctx, "lab.id")
	if err != nil || id != "" {
		return id, err
	}
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id = "lab-" + hex.EncodeToString(b)
	return id, s.SetSetting(ctx, "lab.id", id)
}

// Guest is another lab storing backups here (SPEC.md §10.10).
type Guest struct {
	ID    int64
	Name  string
	Quota int64 // bytes
	// Addr is the guest's address on the guests' WireGuard link.
	Addr string
	// PublicKey is its WireGuard key (the invite's one-time key until it
	// joins).
	PublicKey string
	Joined    bool
	TokenHash string
	Expires   time.Time // the invite, until joined
	// Port is its store's; its admin access is on Port+1.
	Port int
	// StoreHash is the hash of the store password every server of the
	// guest lab shared, for labs invited before servers had their own
	// (empty since); AdminHash is the guest's admin password's.
	StoreHash, AdminHash string
	// Users are its servers' own store users: name to bcrypt hash.
	Users   map[string]string
	Created time.Time
	// Removed is when the host stopped storing its backups (zero if it
	// still does); its data is deleted after a grace period.
	Removed time.Time
}

// User is the guest's rest-server user name.
func (g Guest) User() string { return fmt.Sprintf("g%d", g.ID) }

const guestColumns = `id, name, quota, addr, public_key, joined, token_hash, expires_at, port, store_hash, admin_hash, created_at, removed_at`

// AddGuest records an invited guest and returns its ID.
func (s *Store) AddGuest(ctx context.Context, g Guest) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO storage_guests (name, quota, addr, public_key, joined, token_hash,
		expires_at, port, store_hash, admin_hash, created_at) VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)`,
		g.Name, g.Quota, g.Addr, g.PublicKey, g.TokenHash, g.Expires.Unix(), g.Port, g.StoreHash, g.AdminHash, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Guests lists the guests, dropping invites that expired unused.
func (s *Store) Guests(ctx context.Context, now time.Time) ([]Guest, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM storage_guests WHERE joined = 0 AND expires_at <= ?`, now.Unix()); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+guestColumns+` FROM storage_guests ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Guest
	for rows.Next() {
		var g Guest
		var joined int
		var exp, created, removed int64
		if err := rows.Scan(&g.ID, &g.Name, &g.Quota, &g.Addr, &g.PublicKey, &joined, &g.TokenHash, &exp, &g.Port,
			&g.StoreHash, &g.AdminHash, &created, &removed); err != nil {
			return nil, err
		}
		g.Joined, g.Expires, g.Created = joined == 1, time.Unix(exp, 0), time.Unix(created, 0)
		if removed > 0 {
			g.Removed = time.Unix(removed, 0)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	users, err := s.db.QueryContext(ctx, `SELECT guest_id, name, hash FROM storage_guest_users`)
	if err != nil {
		return nil, err
	}
	defer users.Close()
	for users.Next() {
		var id int64
		var name, hash string
		if err := users.Scan(&id, &name, &hash); err != nil {
			return nil, err
		}
		for i := range out {
			if out[i].ID == id {
				if out[i].Users == nil {
					out[i].Users = map[string]string{}
				}
				out[i].Users[name] = hash
			}
		}
	}
	return out, users.Err()
}

// SetGuestUser adds or changes one of a guest lab's store users.
func (s *Store) SetGuestUser(ctx context.Context, guestID int64, name, hash string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO storage_guest_users (guest_id, name, hash) VALUES (?, ?, ?)
		ON CONFLICT (guest_id, name) DO UPDATE SET hash = excluded.hash`, guestID, name, hash)
	return err
}

// RemoveGuestUser removes one of a guest lab's store users.
func (s *Store) RemoveGuestUser(ctx context.Context, guestID int64, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM storage_guest_users WHERE guest_id = ? AND name = ?`, guestID, name)
	return err
}

// JoinGuest uses a guest's invite: its own WireGuard key replaces the
// invite's one-time key. Works once.
func (s *Store) JoinGuest(ctx context.Context, tokenHash, publicKey string, now time.Time) (Guest, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE storage_guests SET joined = 1, public_key = ?, token_hash = ''
		WHERE token_hash = ? AND token_hash != '' AND joined = 0 AND expires_at > ?`, publicKey, tokenHash, now.Unix())
	if err != nil {
		return Guest{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Guest{}, ErrJoinCode
	}
	guests, err := s.Guests(ctx, now)
	if err != nil {
		return Guest{}, err
	}
	for _, g := range guests {
		if g.PublicKey == publicKey {
			return g, nil
		}
	}
	return Guest{}, ErrJoinCode
}

// SetGuestQuota changes a guest's quota.
func (s *Store) SetGuestQuota(ctx context.Context, id, quota int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE storage_guests SET quota = ? WHERE id = ?`, quota, id)
	return err
}

// SetGuestRemoved records when the host stopped storing a guest's
// backups.
func (s *Store) SetGuestRemoved(ctx context.Context, id int64, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE storage_guests SET removed_at = ? WHERE id = ? AND removed_at = 0`, at.Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveGuest forgets a guest for good (its data is deleted separately).
func (s *Store) RemoveGuest(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM storage_guests WHERE id = ?`, id)
	return err
}
