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

// schemaVersion is the version schema gives a new database: the 37 steps
// that built it before the first release, squashed into one (review S14,
// 2026-10-22). A database at 37 carries on untouched; an older one is from
// a development version and can't be opened.
const schemaVersion = 37

// schema is a new database, one statement at a time.
var schema = []string{
	// Roostack accounts (SPEC.md §5).
	//   - totp_last_step is the last accepted two-factor time step, so a code
	//     can't be reused; totp_failures counts wrong codes in a row, the last
	//     entered at totp_failed_at, to limit guesses per account
	//     (authkit.TOTPLockedUntil).
	//   - must_change_password is set for passwords printed by the command
	//     line: the user chooses their own at the next login.
	//   - theme is the light or dark mode, palette the colour theme ('' = the
	//     default), accent the theme's accent colour ('' = the theme's default).
	//   - email is where Roostack emails someone (SPEC.md §6.13; '' for no
	//     email), and notify the notification groups they chose ('' for their
	//     role's defaults).
	//   - name is their full name, and sso_id the ID apps know them by, for
	//     single sign-on (SPEC.md §5.2): made when first needed, never reused,
	//     unlike users.id, which SQLite may give a new account after the newest
	//     is removed.
	`CREATE TABLE users (
		id                   INTEGER PRIMARY KEY,
		username             TEXT NOT NULL UNIQUE COLLATE NOCASE,
		password_hash        TEXT NOT NULL,
		role                 TEXT NOT NULL,
		totp_secret          TEXT NOT NULL DEFAULT '',
		created_at           INTEGER NOT NULL,
		totp_last_step       INTEGER NOT NULL DEFAULT 0,
		must_change_password INTEGER NOT NULL DEFAULT 0,
		theme                TEXT NOT NULL DEFAULT 'auto',
		palette              TEXT NOT NULL DEFAULT '',
		accent               TEXT NOT NULL DEFAULT '',
		totp_failures        INTEGER NOT NULL DEFAULT 0,
		totp_failed_at       INTEGER NOT NULL DEFAULT 0,
		email                TEXT NOT NULL DEFAULT '',
		notify               TEXT NOT NULL DEFAULT '',
		name                 TEXT NOT NULL DEFAULT '',
		sso_id               TEXT NOT NULL DEFAULT ''
	)`,
	// Signed-in browsers: only a hash of each token is kept.
	//   - last_seen and remember ("keep me signed in") sign people out after a
	//     while without activity (SPEC.md §5).
	//   - origin is where it was signed in: '' for Roostack's pages,
	//     OriginSetup for the setup pages on the home network (SPEC.md §6.3.2),
	//     OriginSignIn for login.<domain>. Each works only where it was made.
	//   - signed_in_at is when someone last typed their password for it (OIDC's
	//     auth_time, SPEC.md §5.2): apps can ask for a recent sign-in.
	//   - sign_in is the sign-in at login.<domain> a session of Roostack's own
	//     pages came from (SPEC.md §5.2): it ends with it.
	`CREATE TABLE sessions (
		token_hash   TEXT PRIMARY KEY,
		user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at   INTEGER NOT NULL,
		last_seen    INTEGER NOT NULL DEFAULT 0,
		remember     INTEGER NOT NULL DEFAULT 0,
		origin       TEXT NOT NULL DEFAULT '',
		signed_in_at INTEGER NOT NULL DEFAULT 0,
		sign_in      TEXT REFERENCES sessions(token_hash) ON DELETE CASCADE
	)`,
	`CREATE INDEX sessions_sign_in ON sessions (sign_in)`,
	// Who did what (SPEC.md §5).
	`CREATE TABLE audit_log (
		id       INTEGER PRIMARY KEY,
		at       INTEGER NOT NULL,
		user_id  INTEGER,
		username TEXT NOT NULL,
		action   TEXT NOT NULL,
		detail   TEXT NOT NULL DEFAULT ''
	)`,
	// Small runtime flags, e.g. setup wizard progress.
	`CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
	// Notifications (SPEC.md §6.13): what's been emailed about, per person,
	// and the browsers each person has logged in from (a new one is a
	// security email). A notice's gone_since is when it stopped being seen:
	// "Fixed" waits until it has been gone a while, so a moment's gap isn't
	// news.
	`CREATE TABLE notices (
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		key        TEXT NOT NULL,
		text       TEXT NOT NULL,
		first_seen INTEGER NOT NULL,
		sent_at    INTEGER NOT NULL DEFAULT 0,
		gone_since INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (user_id, key)
	)`,
	`CREATE TABLE devices (
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		token_hash TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (user_id, token_hash)
	)`,
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
	)`,
	`CREATE INDEX events_finished ON events (finished)`,
	// Other servers in the lab (SPEC.md §10), and join codes not yet used.
	//   - cert_serial is the certificate a server may use on the lab API
	//     (SPEC.md §10.3), and prev_serial the one it replaced, until the new
	//     one is used.
	//   - dial is where the controller dials a server that can be reached
	//     when it can't (a cloud server joining a lab at home, SPEC.md
	//     §10.2): typed with its join code, kept once it has joined.
	`CREATE TABLE servers (
		id           TEXT PRIMARY KEY,
		name         TEXT NOT NULL,
		addr         TEXT NOT NULL UNIQUE,
		public_key   TEXT NOT NULL,
		joined_at    INTEGER NOT NULL,
		cert_serial  TEXT NOT NULL DEFAULT '',
		prev_serial  TEXT NOT NULL DEFAULT '',
		cert_expires INTEGER NOT NULL DEFAULT 0,
		dial         TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE join_codes (
		token_hash TEXT PRIMARY KEY,
		addr       TEXT NOT NULL UNIQUE,
		public_key TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		dial       TEXT NOT NULL DEFAULT ''
	)`,
	// Each finished backup attempt (SPEC.md §6.9).
	//   - repository is the one it went to: results follow a repository when
	//     the main backup and a copy swap.
	//   - stored is the space the repository took after it (0 if unknown).
	//   - services are the services a main backup has (comma-separated; NULL:
	//     from before they were kept), so a service installed since its
	//     server's last backup isn't shown as in it.
	//   - destination ('' for the server's main backup, else a copy's name) is
	//     kept only to give older runs their repository once, from lab.yaml
	//     (AdoptBackupRuns).
	`CREATE TABLE backup_runs (
		id          INTEGER PRIMARY KEY,
		started_at  INTEGER NOT NULL,
		finished_at INTEGER NOT NULL,
		ok          INTEGER NOT NULL,
		snapshot_id TEXT NOT NULL DEFAULT '',
		files_new   INTEGER NOT NULL DEFAULT 0,
		data_added  INTEGER NOT NULL DEFAULT 0,
		error       TEXT NOT NULL DEFAULT '',
		destination TEXT NOT NULL DEFAULT '',
		stored      INTEGER NOT NULL DEFAULT 0,
		services    TEXT,
		repository  TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX backup_runs_repository ON backup_runs (repository, id)`,
	// Where each server backs up (SPEC.md §10.10). A provider's config is
	// JSON and may hold credentials; the database is readable by Roostack
	// only. A target's scoped_key is a provider key made for one server
	// (e.g. a B2 key limited to its folder), deleted when the server leaves.
	`CREATE TABLE backup_providers (
		id     INTEGER PRIMARY KEY,
		kind   TEXT NOT NULL,
		name   TEXT NOT NULL,
		config TEXT NOT NULL
	)`,
	`CREATE TABLE backup_targets (
		server_id  TEXT PRIMARY KEY,
		provider   TEXT NOT NULL,
		repository TEXT NOT NULL,
		store_host TEXT NOT NULL DEFAULT '',
		store_hash TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		scoped_key TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE TABLE store_admins (
		server_id TEXT PRIMARY KEY,
		password  TEXT NOT NULL,
		hash      TEXT NOT NULL
	)`,
	// Extra copies of a server's backups, somewhere else (SPEC.md §6.9,
	// 3-2-1).
	`CREATE TABLE backup_copies (
		server_id  TEXT NOT NULL,
		provider   TEXT NOT NULL,
		repository TEXT NOT NULL,
		store_host TEXT NOT NULL DEFAULT '',
		store_hash TEXT NOT NULL DEFAULT '',
		scoped_key TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		PRIMARY KEY (server_id, provider)
	)`,
	// Main backups that were on a server that left the lab, until each
	// server has a new one (SPEC.md §10.10).
	`CREATE TABLE lost_backups (
		server_id TEXT PRIMARY KEY,
		host_name TEXT NOT NULL,
		lost_at   INTEGER NOT NULL
	)`,
	// Other labs storing backups here (SPEC.md §10.10). A removed guest lab
	// is kept for a while (its data, and its link so it can be told) before
	// it's deleted. Each guest lab's servers have their own user at its store
	// (review M21): the bcrypt hash of each one's password.
	`CREATE TABLE storage_guests (
		id         INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		quota      INTEGER NOT NULL,
		addr       TEXT NOT NULL UNIQUE,
		public_key TEXT NOT NULL,
		joined     INTEGER NOT NULL DEFAULT 0,
		token_hash TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		port       INTEGER NOT NULL UNIQUE,
		store_hash TEXT NOT NULL,
		admin_hash TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		removed_at INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE storage_guest_users (
		guest_id INTEGER NOT NULL REFERENCES storage_guests(id) ON DELETE CASCADE,
		name     TEXT NOT NULL,
		hash     TEXT NOT NULL,
		PRIMARY KEY (guest_id, name)
	)`,
	// Media libraries, per server (SPEC.md §8.1, "Media libraries"): a folder
	// on a drive (its filesystem's UUID, and the folder on it) or on the
	// server, optionally read as its owner (uid and gid; 0: not).
	//   - A library can be a network share (SMB or NFS): its server, share or
	//     export, and SMB username. Never its password, which only the server
	//     holding it keeps (root's).
	//   - photos_for is set for a library kept for migrating photos into one
	//     Immich (SPEC.md §8.5, "Additional locations"): its service's name; no
	//     service reads it. Empty for the libraries services read.
	`CREATE TABLE media_libraries (
		server_id    TEXT NOT NULL,
		name         TEXT NOT NULL,
		title        TEXT NOT NULL,
		drive        TEXT NOT NULL DEFAULT '',
		drive_name   TEXT NOT NULL DEFAULT '',
		sub          TEXT NOT NULL DEFAULT '',
		folder       TEXT NOT NULL DEFAULT '',
		owner_uid    INTEGER NOT NULL DEFAULT 0,
		owner_gid    INTEGER NOT NULL DEFAULT 0,
		owner_name   TEXT NOT NULL DEFAULT '',
		created_at   INTEGER NOT NULL,
		share_type   TEXT NOT NULL DEFAULT '',
		share_server TEXT NOT NULL DEFAULT '',
		share_path   TEXT NOT NULL DEFAULT '',
		share_user   TEXT NOT NULL DEFAULT '',
		photos_for   TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (server_id, name)
	)`,
	// Monitoring (SPEC.md §6.15): every check's results for two days, each
	// hour's summary for as long as the lab keeps history, the times a check
	// was down, and the checks people added.
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
	)`,
	`CREATE INDEX monitor_results_key ON monitor_results (key, at)`,
	`CREATE TABLE monitor_hours (
		key        TEXT NOT NULL,
		hour       INTEGER NOT NULL,
		checks     INTEGER NOT NULL,
		ups        INTEGER NOT NULL,
		paused     INTEGER NOT NULL,
		millis_sum INTEGER NOT NULL,
		millis_max INTEGER NOT NULL,
		PRIMARY KEY (key, hour)
	)`,
	`CREATE TABLE monitor_outages (
		key     TEXT NOT NULL,
		started INTEGER NOT NULL,
		ended   INTEGER NOT NULL DEFAULT 0,
		error   TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX monitor_outages_key ON monitor_outages (key, started)`,
	`CREATE TABLE monitor_checks (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		name        TEXT NOT NULL,
		address     TEXT NOT NULL,
		skip_verify INTEGER NOT NULL DEFAULT 0,
		alert       INTEGER NOT NULL DEFAULT 1,
		created_at  INTEGER NOT NULL
	)`,
	// Single sign-on (SPEC.md §5.2): groups of people; who may sign in to
	// each service (no rows: everyone; kind 'chosen' marks a choice, then
	// 'group' and 'user' rows are the groups and people chosen); and what the
	// sign-in page hands out, each tied to the sign-in it came from: apps'
	// access and refresh tokens, and walled services' cookies.
	`CREATE TABLE user_groups (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT NOT NULL UNIQUE COLLATE NOCASE,
		created_at INTEGER NOT NULL
	)`,
	`CREATE TABLE user_group_members (
		group_id INTEGER NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
		user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		PRIMARY KEY (group_id, user_id)
	)`,
	`CREATE TABLE service_access (
		service TEXT NOT NULL,
		kind    TEXT NOT NULL,
		ref     INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (service, kind, ref)
	)`,
	`CREATE TRIGGER service_access_user AFTER DELETE ON users BEGIN
		DELETE FROM service_access WHERE kind = 'user' AND ref = OLD.id;
	END`,
	`CREATE TRIGGER service_access_group AFTER DELETE ON user_groups BEGIN
		DELETE FROM service_access WHERE kind = 'group' AND ref = OLD.id;
	END`,
	`CREATE TABLE sso_tokens (
		token_hash   TEXT PRIMARY KEY,
		kind         TEXT NOT NULL,
		service      TEXT NOT NULL,
		user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		session_hash TEXT NOT NULL REFERENCES sessions(token_hash) ON DELETE CASCADE,
		scope        TEXT NOT NULL DEFAULT '',
		expires_at   INTEGER NOT NULL,
		created_at   INTEGER NOT NULL
	)`,
	`CREATE INDEX sso_tokens_session ON sso_tokens (session_hash)`,
}

// migrations change the schema after schemaVersion, in order: migrations[i]
// takes a database to version schemaVersion+i+1. Never edit one that has
// shipped.
var migrations = []string{}

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
		var fe *FormError
		if errors.As(err, &fe) {
			return nil, err // a sentence for people, as it is
		}
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

// FormError is a problem a person can fix, with a sentence saying how
// (CLAUDE.md's convention; review D3 will make it one type with the
// others).
type FormError struct{ Msg string }

func (e *FormError) Error() string { return e.Msg }

// migrate creates a new database's schema, or brings one at schemaVersion
// or later up to date. Each step is a transaction with its version.
func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > 0 && version < schemaVersion {
		return &FormError{fmt.Sprintf("Roostack's database is from a development version of Roostack before the first release "+
			"(database version %d), which this version can't open. This server needs a fresh install of Roostack.", version)}
	}
	if version == 0 {
		if err := s.step(schema, schemaVersion); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
		version = schemaVersion
	}
	for i := version - schemaVersion; i < len(migrations); i++ {
		if err := s.step([]string{migrations[i]}, schemaVersion+i+1); err != nil {
			return fmt.Errorf("migration %d: %w", schemaVersion+i+1, err)
		}
	}
	return nil
}

// step runs statements and sets the database's version, in one
// transaction.
func (s *Store) step(statements []string, version int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range statements {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return err
	}
	return tx.Commit()
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
