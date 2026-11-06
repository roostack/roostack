package store

import (
	"context"
	"time"
)

// RemovedService is a service removed with its data kept (SPEC.md §8.1,
// "Data left by removed services"): which package it was, on which
// server, and when and by whom it was removed. The data itself is the
// server's to list (node.LeftServices); this only names it.
type RemovedService struct {
	Server, Name            string
	Package, Title, Version string
	At                      time.Time
	By                      string
}

// RecordRemoved notes a service's removal, replacing an earlier one of
// the same service on the same server.
func (s *Store) RecordRemoved(ctx context.Context, r RemovedService) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO removed_services (server, name, package, title, version, removed_at, removed_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (server, name) DO UPDATE SET package = excluded.package, title = excluded.title, version = excluded.version,
			removed_at = excluded.removed_at, removed_by = excluded.removed_by`,
		r.Server, r.Name, r.Package, r.Title, r.Version, r.At.Unix(), r.By)
	return err
}

// RemovedServices lists every removal noted, newest first.
func (s *Store) RemovedServices(ctx context.Context) ([]RemovedService, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server, name, package, title, version, removed_at, removed_by
		FROM removed_services ORDER BY removed_at DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RemovedService
	for rows.Next() {
		var r RemovedService
		var at int64
		if err := rows.Scan(&r.Server, &r.Name, &r.Package, &r.Title, &r.Version, &at, &r.By); err != nil {
			return nil, err
		}
		r.At = time.Unix(at, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ForgetRemoved forgets a removal once its data is deleted.
func (s *Store) ForgetRemoved(ctx context.Context, server, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM removed_services WHERE server = ? AND name = ?`, server, name)
	return err
}
