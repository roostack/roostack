package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// BackupRun is one finished backup attempt.
type BackupRun struct {
	Started, Finished time.Time
	OK                bool
	SnapshotID        string
	FilesNew          int
	DataAdded         uint64
	Error             string
	// Repository is the restic repository it went to: what a backup's
	// results are found by, whether it's the main backup or a copy at the
	// time (they swap).
	Repository string
	// Stored is the space the repository took after it (0 if unknown).
	Stored uint64
	// Services are the services it backed up (nil: unknown, from before
	// they were kept, or a copy's run before 2026-10-19).
	Services []string
}

// RecordBackup stores the result of a backup.
func (s *Store) RecordBackup(ctx context.Context, r BackupRun) error {
	var services *string
	if r.Services != nil {
		joined := strings.Join(r.Services, ",")
		services = &joined
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO backup_runs (started_at, finished_at, ok, snapshot_id, files_new, data_added, error, repository, stored, services)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Started.Unix(), r.Finished.Unix(), r.OK, r.SnapshotID, r.FilesNew, r.DataAdded, r.Error, r.Repository, r.Stored, services)
	return err
}

// LastBackup returns the most recent attempt to a repository, and the
// most recent success (which may be the same run). Either may be nil.
func (s *Store) LastBackup(ctx context.Context, repository string) (last, lastOK *BackupRun, err error) {
	if repository == "" {
		return nil, nil, nil
	}
	last, err = s.scanBackup(s.db.QueryRowContext(ctx, backupQuery+` WHERE repository = ? ORDER BY id DESC LIMIT 1`, repository))
	if err != nil {
		return nil, nil, err
	}
	lastOK, err = s.scanBackup(s.db.QueryRowContext(ctx,
		backupQuery+` WHERE ok = 1 AND repository = ? ORDER BY id DESC LIMIT 1`, repository))
	return last, lastOK, err
}

const backupQuery = `SELECT started_at, finished_at, ok, snapshot_id, files_new, data_added, error, repository, stored, services FROM backup_runs`

// AdoptBackupRuns gives runs recorded before repositories were (by role:
// destination "" for the main backup, else a copy's name) the repository
// that role has in lab.yaml now: repositories maps each destination to
// it. It does that once, the first time it's called (Roostack's first
// start since runs have repositories); runs of a role lab.yaml no longer
// has (a copy since stopped) keep none and aren't shown, as before.
func (s *Store) AdoptBackupRuns(ctx context.Context, repositories map[string]string) error {
	const done = "backup.runs.by-repository"
	if v, err := s.Setting(ctx, done); err != nil || v != "" {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for destination, repository := range repositories {
		if repository == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE backup_runs SET repository = ? WHERE repository = '' AND destination = ?`,
			repository, destination); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, '1')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, done); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) scanBackup(row *sql.Row) (*BackupRun, error) {
	var r BackupRun
	var started, finished int64
	var services sql.NullString
	err := row.Scan(&started, &finished, &r.OK, &r.SnapshotID, &r.FilesNew, &r.DataAdded, &r.Error, &r.Repository, &r.Stored, &services)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Started, r.Finished = time.Unix(started, 0), time.Unix(finished, 0)
	if services.Valid {
		r.Services = []string{}
		if services.String != "" {
			r.Services = strings.Split(services.String, ",")
		}
	}
	return &r, nil
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
	return queryAll(ctx, s.db, scanProvider, `SELECT id, kind, name, config FROM backup_providers ORDER BY name`)
}

func scanProvider(row scanner) (BackupProvider, error) {
	var p BackupProvider
	err := row.Scan(&p.ID, &p.Kind, &p.Name, &p.Config)
	return p, err
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
	return queryAll(ctx, s.db, scanLostBackup, `SELECT server_id, host_name, lost_at FROM lost_backups ORDER BY server_id`)
}

func scanLostBackup(row scanner) (LostBackup, error) {
	var l LostBackup
	var lost int64
	if err := row.Scan(&l.ServerID, &l.HostName, &lost); err != nil {
		return l, err
	}
	l.Lost = time.Unix(lost, 0)
	return l, nil
}

// BackupTargets lists where each server backs up.
func (s *Store) BackupTargets(ctx context.Context) ([]BackupTarget, error) {
	return queryAll(ctx, s.db, scanTarget, `SELECT `+targetColumns+` FROM backup_targets ORDER BY server_id`)
}

// targetColumns are backup_targets' and backup_copies', for scanTarget.
const targetColumns = `server_id, provider, repository, store_host, store_hash, scoped_key, created_at`

func scanTarget(row scanner) (BackupTarget, error) {
	var t BackupTarget
	var created int64
	if err := row.Scan(&t.ServerID, &t.Provider, &t.Repository, &t.StoreHost, &t.StoreHash, &t.ScopedKey, &created); err != nil {
		return t, err
	}
	t.Created = time.Unix(created, 0)
	return t, nil
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
	return queryAll(ctx, s.db, scanTarget, `SELECT `+targetColumns+` FROM backup_copies ORDER BY server_id, created_at`)
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
