package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"
)

// Passkey is one of a person's passkeys (SPEC.md §5, "Passkeys").
type Passkey struct {
	ID     int64
	UserID int64
	// CredentialID is what the device calls it; Credential go-webauthn's
	// record of it as JSON (internal/passkey).
	CredentialID []byte
	Credential   string
	// RPID is the lab's domain when it was made: it only works there.
	RPID       string
	Name       string
	CreatedAt  time.Time
	LastUsedAt time.Time
}

const passkeyColumns = `id, user_id, credential_id, credential, rp_id, name, created_at, last_used_at`

func scanPasskey(row scanner) (*Passkey, error) {
	var p Passkey
	var created, used int64
	err := row.Scan(&p.ID, &p.UserID, &p.CredentialID, &p.Credential, &p.RPID, &p.Name, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt = time.Unix(created, 0)
	if used != 0 {
		p.LastUsedAt = time.Unix(used, 0)
	}
	return &p, nil
}

// UserPasskeys are a person's passkeys, oldest first.
func (s *Store) UserPasskeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+passkeyColumns+` FROM passkeys WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// CountPasskeys is how many passkeys people have made for a domain.
func (s *Store) CountPasskeys(ctx context.Context, rpID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM passkeys WHERE rp_id = ?`, rpID).Scan(&n)
	return n, err
}

// PasskeyByCredentialID finds a passkey by what the device calls it.
func (s *Store) PasskeyByCredentialID(ctx context.Context, id []byte) (*Passkey, error) {
	return scanPasskey(s.db.QueryRowContext(ctx, `SELECT `+passkeyColumns+` FROM passkeys WHERE credential_id = ?`, id))
}

// AddPasskey saves a new passkey; one whose credential ID is already
// someone's is refused.
func (s *Store) AddPasskey(ctx context.Context, p Passkey) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO passkeys (user_id, credential_id, credential, rp_id, name, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, p.UserID, p.CredentialID, p.Credential, p.RPID, p.Name, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UsePasskey records a sign-in with a passkey: its updated record (the
// counter) and when.
func (s *Store) UsePasskey(ctx context.Context, id int64, credential string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE passkeys SET credential = ?, last_used_at = ? WHERE id = ?`, credential, at.Unix(), id)
	return err
}

// RemovePasskey removes one of a person's passkeys, and returns its name.
func (s *Store) RemovePasskey(ctx context.Context, userID, id int64) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `DELETE FROM passkeys WHERE id = ? AND user_id = ? RETURNING name`, id, userID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}

// PasskeyHandle is the random ID a person's devices know them by, made
// with their first passkey: never their username or number, which a
// device would show to whatever asks.
func (s *Store) PasskeyHandle(ctx context.Context, userID int64) ([]byte, error) {
	var h []byte
	if err := s.db.QueryRowContext(ctx, `SELECT passkey_handle FROM users WHERE id = ?`, userID).Scan(&h); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(h) > 0 {
		return h, nil
	}
	h = make([]byte, 32)
	rand.Read(h)
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET passkey_handle = ? WHERE id = ? AND passkey_handle IS NULL`, h, userID); err != nil {
		return nil, err
	}
	// Another request may have made one first: theirs stands.
	err := s.db.QueryRowContext(ctx, `SELECT passkey_handle FROM users WHERE id = ?`, userID).Scan(&h)
	return h, err
}

// UserByPasskeyHandle finds the person a device's passkey belongs to.
func (s *Store) UserByPasskeyHandle(ctx context.Context, handle []byte) (*User, error) {
	if len(handle) == 0 {
		return nil, ErrNotFound
	}
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE passkey_handle = ?`, handle))
}
