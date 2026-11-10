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
// my.<domain> (single sign-on, SPEC.md §5.2). Roostack's own idle limit
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
// a sign-in at my.<domain> (signIn, its hash): it lasts no longer, and
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

// SetSessionDevice records the browser and system a session was signed in
// from ("Firefox on Linux"), for Your account (SPEC.md §5.3).
func (s *Store) SetSessionDevice(ctx context.Context, tokenHash, device string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET device = ? WHERE token_hash = ?`, device, tokenHash)
	return err
}

// Session is one of someone's sessions, for Your account's "Where you're
// signed in" (SPEC.md §5.3).
type Session struct {
	Hash string
	// Origin is "" for Roostack's own pages, OriginSignIn for the sign-in
	// page; SignIn is the sign-in a session of Roostack's own pages came
	// from, if any.
	Origin, SignIn string
	Device         string
	SignedInAt     time.Time
	LastSeen       time.Time
	Expires        time.Time
}

// UserSessions lists someone's sessions that haven't expired, newest
// first, but not the setup pages', which end when those close.
func (s *Store) UserSessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token_hash, origin, COALESCE(sign_in, ''), device, signed_in_at, last_seen, expires_at
		FROM sessions WHERE user_id = ? AND origin != ? AND expires_at > ? ORDER BY signed_in_at DESC, token_hash`,
		userID, OriginSetup, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Session
	for rows.Next() {
		var x Session
		var at, seen, exp int64
		if err := rows.Scan(&x.Hash, &x.Origin, &x.SignIn, &x.Device, &at, &seen, &exp); err != nil {
			return nil, err
		}
		x.SignedInAt, x.LastSeen, x.Expires = time.Unix(at, 0), time.Unix(seen, 0), time.Unix(exp, 0)
		list = append(list, x)
	}
	return list, rows.Err()
}

// DeleteUserSession signs out one of someone's sessions, and the sessions
// of Roostack's own pages made from it; another person's isn't touched.
func (s *Store) DeleteUserSession(ctx context.Context, userID int64, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ? AND user_id = ?`, tokenHash, userID)
	return err
}
