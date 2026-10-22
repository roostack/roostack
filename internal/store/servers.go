package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

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
