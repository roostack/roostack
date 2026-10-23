package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

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

// SettingGetter and SettingSetter are the settings table: the Store, or
// what stands in for it where a package keeps to an interface (node.KV,
// osupdate.KV, a test's map).
type (
	SettingGetter interface {
		Setting(ctx context.Context, key string) (string, error)
	}
	SettingSetter interface {
		SetSetting(ctx context.Context, key, value string) error
	}
)

// GetJSON reads a setting that holds JSON as a T: T's zero value if the
// setting isn't set. If the JSON doesn't fit a T, the error comes with
// whatever could be read.
func GetJSON[T any](ctx context.Context, kv SettingGetter, key string) (T, error) {
	var v T
	s, err := kv.Setting(ctx, key)
	if err != nil || s == "" {
		return v, err
	}
	err = json.Unmarshal([]byte(s), &v)
	return v, err
}

// PutJSON writes v to a setting as JSON.
func PutJSON(ctx context.Context, kv SettingSetter, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return kv.SetSetting(ctx, key, string(data))
}
