package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// OriginSetup marks sessions signed in on the setup pages on the home
// network (SPEC.md §6.3.2).
const OriginSetup = "setup"

// OriginSignIn marks sessions signed in on the sign-in page for services,
// login.<domain> (single sign-on, SPEC.md §5.2). Roostack's own idle limit
// doesn't apply to them: they last until they expire.
const OriginSignIn = "sign-in"

// CreateSessionRemember stores a session (only a hash of its token); remember: "keep me signed in"
// (no sign-out for inactivity).
func (s *Store) CreateSessionRemember(ctx context.Context, tokenHash string, userID int64, expires time.Time, remember bool) error {
	return s.CreateSessionAt(ctx, "", tokenHash, userID, expires, remember)
}

// CreateSessionAt stores a session signed in at origin ("" or
// OriginSetup), where alone it works.
func (s *Store) CreateSessionAt(ctx context.Context, origin, tokenHash string, userID int64, expires time.Time, remember bool) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, last_seen, remember, origin, signed_in_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tokenHash, userID, expires.Unix(), now, remember, origin, now)
	return err
}

// CreateLinkedSession stores a session of Roostack's own pages made from
// a sign-in at login.<domain> (signIn, its hash): it lasts no longer, and
// ends when the sign-in does (SPEC.md §5.2).
func (s *Store) CreateLinkedSession(ctx context.Context, tokenHash string, userID int64, signIn string, expires time.Time, remember bool) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, last_seen, remember, origin, signed_in_at, sign_in)
		 VALUES (?, ?, ?, ?, ?, '', ?, ?)`,
		tokenHash, userID, expires.Unix(), now, remember, now, signIn)
	return err
}

// SessionSignIn is the sign-in a session of Roostack's own pages came from
// (its hash), or "" for one signed in on Roostack's own login page.
func (s *Store) SessionSignIn(ctx context.Context, tokenHash string) (string, error) {
	var signIn sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT sign_in FROM sessions WHERE token_hash = ? AND origin = ''`, tokenHash).Scan(&signIn)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return signIn.String, err
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
	u, err = scanUser(scanTail{row, []any{&seen, &remember}})
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

// DeleteSession logs a session out, and the sessions of Roostack's own
// pages made from it if it's a sign-in.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteExpiredSessions removes old sessions: past their expiry, or (when
// idle isn't 0) unused for longer than idle, unless kept signed in or
// signed in on the sign-in page for services (OriginSignIn), whose own
// expiry is their limit. Their tokens go with them.
func (s *Store) DeleteExpiredSessions(ctx context.Context, idle time.Duration) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?
		OR (? > 0 AND remember = 0 AND origin != ? AND last_seen < ?)`,
		now.Unix(), int64(idle.Seconds()), OriginSignIn, now.Add(-idle).Unix())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM sso_tokens WHERE expires_at <= ?`, now.Unix())
	return err
}
