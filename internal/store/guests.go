package store

import (
	"context"
	"fmt"
	"time"
)

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
	out, err := queryAll(ctx, s.db, scanGuest, `SELECT `+guestColumns+` FROM storage_guests ORDER BY name`)
	if err != nil {
		return nil, err
	}
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

func scanGuest(row scanner) (Guest, error) {
	var g Guest
	var joined int
	var exp, created, removed int64
	if err := row.Scan(&g.ID, &g.Name, &g.Quota, &g.Addr, &g.PublicKey, &joined, &g.TokenHash, &exp, &g.Port,
		&g.StoreHash, &g.AdminHash, &created, &removed); err != nil {
		return g, err
	}
	g.Joined, g.Expires, g.Created = joined == 1, time.Unix(exp, 0), time.Unix(created, 0)
	if removed > 0 {
		g.Removed = time.Unix(removed, 0)
	}
	return g, nil
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
