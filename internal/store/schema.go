// Package store holds Roostack's runtime data in SQLite (SPEC.md §5.1): users,
// sessions, the audit log, settings, backup runs, the lab's servers, backup
// destinations and storage for other labs. Desired state lives in YAML files
// instead.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure Go driver, keeps the binary static
)

// ErrNotFound is returned when a row doesn't exist.
var ErrNotFound = errors.New("not found")

// Store is the runtime database.
type Store struct {
	db *sql.DB
}

// scanner is a *sql.Row or *sql.Rows.
type scanner interface{ Scan(...any) error }

// queryAll runs a query and reads each of its rows with scan: the loop
// every list would otherwise write out.
func queryAll[T any](ctx context.Context, db *sql.DB, scan func(scanner) (T, error), query string, args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
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
	// The services each main backup has (comma-separated; NULL: from
	// before they were kept), so a service installed since its server's
	// last backup isn't shown as in it.
	`ALTER TABLE backup_runs ADD COLUMN services TEXT;`,
	// The repository each backup run went to (SPEC.md §6.9): results
	// follow a destination when the main backup and a copy swap. Older
	// runs are given theirs once, from lab.yaml (AdoptBackupRuns);
	// destination is kept only for that.
	`ALTER TABLE backup_runs ADD COLUMN repository TEXT NOT NULL DEFAULT '';
	CREATE INDEX backup_runs_repository ON backup_runs (repository, id);`,
	// Media libraries, per server (SPEC.md §8.1, "Media libraries"): a
	// folder on a drive (its filesystem's UUID, and the folder on it) or
	// on the server, optionally read as its owner (uid and gid; 0: not).
	`CREATE TABLE media_libraries (
		server_id   TEXT NOT NULL,
		name        TEXT NOT NULL,
		title       TEXT NOT NULL,
		drive       TEXT NOT NULL DEFAULT '',
		drive_name  TEXT NOT NULL DEFAULT '',
		sub         TEXT NOT NULL DEFAULT '',
		folder      TEXT NOT NULL DEFAULT '',
		owner_uid   INTEGER NOT NULL DEFAULT 0,
		owner_gid   INTEGER NOT NULL DEFAULT 0,
		owner_name  TEXT NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL,
		PRIMARY KEY (server_id, name)
	);`,
	// A media library can be a network share (SMB or NFS): its server,
	// share or export, and SMB username. Never its password, which only
	// the server holding it keeps (root's).
	`ALTER TABLE media_libraries ADD COLUMN share_type TEXT NOT NULL DEFAULT '';
	ALTER TABLE media_libraries ADD COLUMN share_server TEXT NOT NULL DEFAULT '';
	ALTER TABLE media_libraries ADD COLUMN share_path TEXT NOT NULL DEFAULT '';
	ALTER TABLE media_libraries ADD COLUMN share_user TEXT NOT NULL DEFAULT '';`,
	// Monitoring (SPEC.md §6.15): every check's results for two days, each
	// hour's summary for as long as the lab keeps history, the times a
	// check was down, and the checks people added.
	`CREATE TABLE monitor_results (
		key          TEXT NOT NULL,
		at           INTEGER NOT NULL,
		up           INTEGER NOT NULL,
		paused       INTEGER NOT NULL,
		status       INTEGER NOT NULL DEFAULT 0,
		millis       INTEGER NOT NULL DEFAULT 0,
		error        TEXT NOT NULL DEFAULT '',
		cert_issued  INTEGER NOT NULL DEFAULT 0,
		cert_expires INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX monitor_results_key ON monitor_results (key, at);
	CREATE TABLE monitor_hours (
		key        TEXT NOT NULL,
		hour       INTEGER NOT NULL,
		checks     INTEGER NOT NULL,
		ups        INTEGER NOT NULL,
		paused     INTEGER NOT NULL,
		millis_sum INTEGER NOT NULL,
		millis_max INTEGER NOT NULL,
		PRIMARY KEY (key, hour)
	);
	CREATE TABLE monitor_outages (
		key     TEXT NOT NULL,
		started INTEGER NOT NULL,
		ended   INTEGER NOT NULL DEFAULT 0,
		error   TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX monitor_outages_key ON monitor_outages (key, started);
	CREATE TABLE monitor_checks (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT NOT NULL,
		address     TEXT NOT NULL,
		skip_verify INTEGER NOT NULL DEFAULT 0,
		alert       INTEGER NOT NULL DEFAULT 1,
		created_at  INTEGER NOT NULL
	);`,
	// Single sign-on (SPEC.md §5.2): each person's full name, and the ID
	// apps know them by (made when first needed, never reused, unlike
	// users.id, which SQLite may give a new account after the newest is
	// removed); groups of people; who may sign in to each service (no
	// rows: everyone; kind 'chosen' marks a choice, then 'group' and
	// 'user' rows are the groups and people chosen); and what the sign-in
	// page hands out, each tied to the sign-in it came from: apps' access
	// and refresh tokens, and walled services' cookies.
	`ALTER TABLE users ADD COLUMN name TEXT NOT NULL DEFAULT '';
	ALTER TABLE users ADD COLUMN sso_id TEXT NOT NULL DEFAULT '';
	CREATE TABLE user_groups (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
		created_at INTEGER NOT NULL
	);
	CREATE TABLE user_group_members (
		group_id INTEGER NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
		user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		PRIMARY KEY (group_id, user_id)
	);
	CREATE TABLE service_access (
		service TEXT NOT NULL,
		kind    TEXT NOT NULL,
		ref     INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (service, kind, ref)
	);
	CREATE TRIGGER service_access_user AFTER DELETE ON users BEGIN
		DELETE FROM service_access WHERE kind = 'user' AND ref = OLD.id;
	END;
	CREATE TRIGGER service_access_group AFTER DELETE ON user_groups BEGIN
		DELETE FROM service_access WHERE kind = 'group' AND ref = OLD.id;
	END;
	CREATE TABLE sso_tokens (
		token_hash   TEXT PRIMARY KEY,
		kind         TEXT NOT NULL,
		service      TEXT NOT NULL,
		user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		session_hash TEXT NOT NULL REFERENCES sessions(token_hash) ON DELETE CASCADE,
		scope        TEXT NOT NULL DEFAULT '',
		expires_at   INTEGER NOT NULL,
		created_at   INTEGER NOT NULL
	);
	CREATE INDEX sso_tokens_session ON sso_tokens (session_hash);`,
	// When someone last typed their password for a session (OIDC's
	// auth_time, SPEC.md §5.2): apps can ask for a recent sign-in.
	`ALTER TABLE sessions ADD COLUMN signed_in_at INTEGER NOT NULL DEFAULT 0;`,
	// The sign-in a session of Roostack's own pages came from, when
	// someone signed in at login.<domain> (SPEC.md §5.2): it ends with it.
	`ALTER TABLE sessions ADD COLUMN sign_in TEXT REFERENCES sessions(token_hash) ON DELETE CASCADE;
	CREATE INDEX sessions_sign_in ON sessions (sign_in);`,
	// A media library kept for migrating photos into one Immich (SPEC.md
	// §8.5, "Additional locations"): its service's name; no service reads
	// it. Empty for the libraries services read.
	`ALTER TABLE media_libraries ADD COLUMN photos_for TEXT NOT NULL DEFAULT '';`,
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
