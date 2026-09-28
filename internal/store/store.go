// Package store holds Roostack's runtime data in SQLite (SPEC.md §5.1): users,
// sessions and the audit log. Desired state lives in YAML files instead.
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
}

// Open opens (creating if needed) and migrates the database.
func Open(path string) (*Store, error) {
	// Create the file ourselves so it is private: it holds password hashes.
	// SQLite gives the -wal and -shm files the same permissions.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrExist) {
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
	// MustChangePassword is set for temporary passwords (CLI add/reset).
	MustChangePassword bool
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
	res, err := s.db.ExecContext(ctx, `UPDATE users SET totp_secret = '', totp_last_step = 0 WHERE id = ?`, userID)
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

// CountUsers returns the number of accounts.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
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
	users.totp_secret, users.totp_last_step, users.must_change_password, users.theme, users.palette, users.accent, users.created_at`

// UserByID looks up an account.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// SetTOTP enables two-factor authentication for a user.
func (s *Store) SetTOTP(ctx context.Context, userID int64, secret string, step int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = ?, totp_last_step = ? WHERE id = ?`, secret, step, userID)
	return err
}

// UseTOTPStep records an accepted code's time step. It fails if the step
// isn't newer than the last one, so two concurrent logins can't both use
// the same code.
func (s *Store) UseTOTPStep(ctx context.Context, userID, step int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, userID, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) scanUser(row *sql.Row) (*User, error) { return scanUserRow(row) }

func scanUserRow(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created int64
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &u.TOTPLastStep,
		&u.MustChangePassword, &u.Theme, &u.Palette, &u.Accent, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	return &u, nil
}

// CreateSession stores a session. Only a hash of the token is stored.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, userID int64, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		tokenHash, userID, expires.Unix())
	return err
}

// SessionUser returns the user for a valid, unexpired session.
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+`
		 FROM sessions s JOIN users ON users.id = s.user_id
		 WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, time.Now().Unix()))
}

// DeleteSession logs a session out.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteExpiredSessions removes old sessions.
func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix())
	return err
}

// AuditEntry is one audit log row.
type AuditEntry struct {
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
}

// JoinCode is a join code waiting to be used. Only the token's hash is
// kept; PublicKey is the code's one-time WireGuard key.
type JoinCode struct {
	TokenHash, Addr, PublicKey string
	Expires                    time.Time
}

// ErrJoinCode means a join code is unknown, used or expired.
var ErrJoinCode = errors.New("the join code is unknown, already used or expired")

// AddJoinCode records a new join code.
func (s *Store) AddJoinCode(ctx context.Context, c JoinCode) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO join_codes (token_hash, addr, public_key, expires_at) VALUES (?, ?, ?, ?)`,
		c.TokenHash, c.Addr, c.PublicKey, c.Expires.Unix())
	return err
}

// JoinCodes lists the join codes that haven't expired, dropping the rest.
func (s *Store) JoinCodes(ctx context.Context, now time.Time) ([]JoinCode, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM join_codes WHERE expires_at <= ?`, now.Unix()); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT token_hash, addr, public_key, expires_at FROM join_codes ORDER BY addr`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JoinCode
	for rows.Next() {
		var c JoinCode
		var exp int64
		if err := rows.Scan(&c.TokenHash, &c.Addr, &c.PublicKey, &exp); err != nil {
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
	err = tx.QueryRowContext(ctx, `SELECT addr, expires_at FROM join_codes WHERE token_hash = ?`, tokenHash).Scan(&m.Addr, &exp)
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO servers (id, name, addr, public_key, joined_at, cert_serial)
		VALUES (?, ?, ?, ?, ?, 'pending')
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, addr = excluded.addr, public_key = excluded.public_key,
		joined_at = excluded.joined_at, cert_serial = 'pending', prev_serial = '', cert_expires = 0`,
		m.ID, m.Name, m.Addr, m.PublicKey, now.Unix()); err != nil {
		return m, err
	}
	return m, tx.Commit()
}

// Servers lists the other servers in the lab.
func (s *Store) Servers(ctx context.Context) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, addr, public_key, joined_at, cert_serial, prev_serial, cert_expires
		FROM servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		var m Server
		var joined, expires int64
		if err := rows.Scan(&m.ID, &m.Name, &m.Addr, &m.PublicKey, &joined, &m.CertSerial, &m.PrevSerial, &expires); err != nil {
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
	Port                 int
	StoreHash, AdminHash string
	Created              time.Time
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
	return out, rows.Err()
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
