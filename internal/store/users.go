package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// User is a Roostack account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	TOTPSecret   string
	TOTPLastStep int64
	// TOTPFailures counts wrong two-factor codes since the last right one,
	// the last entered at TOTPFailedAt (authkit.TOTPLockedUntil).
	TOTPFailures int
	TOTPFailedAt time.Time
	// MustChangePassword is set for temporary passwords (CLI add/reset).
	MustChangePassword bool
	// Email is where Roostack emails them (SPEC.md §6.13); empty: no email.
	Email string
	// Notify is the notification groups they chose, comma-separated ("" for
	// their role's defaults, "none" for none).
	Notify string
	// Theme is "auto" (follow the device), "light" or "dark".
	Theme string
	// Palette is the colour theme (internal/web themes), "" for the default.
	Palette string
	// Accent is the theme's accent colour, "" for its default.
	Accent string
	// Name is their full name, for apps with single sign-on ("" if not
	// given: apps are told their username).
	Name string
	// SSOID is the ID apps know them by (SPEC.md §5.2), "" until first
	// needed (EnsureSSOID).
	SSOID     string
	CreatedAt time.Time
}

// SetAccent saves a user's accent colour (the caller checks it exists).
func (s *Store) SetAccent(ctx context.Context, userID int64, accent string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET accent = ? WHERE id = ?`, accent, userID)
	return err
}

// SetPalette saves a user's colour theme (the caller checks it exists).
func (s *Store) SetPalette(ctx context.Context, userID int64, palette string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET palette = ? WHERE id = ?`, palette, userID)
	return err
}

// ErrLastOwner means a change would leave the lab without an Owner.
var ErrLastOwner = errors.New("the lab must keep at least one Owner")

// ListUsers returns every account, by username.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

// SetRole changes a user's role. Demoting the last Owner fails with
// ErrLastOwner; the check is part of the update, so two Owners demoting
// each other at once can't both succeed.
func (s *Store) SetRole(ctx context.Context, userID int64, role string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ? AND (? = 'owner' OR role != 'owner'
		OR (SELECT count(*) FROM users WHERE role = 'owner') > 1)`, role, userID, role)
	if err != nil {
		return err
	}
	return s.lastOwnerCheck(ctx, res, userID)
}

// DeleteUser removes an account and its sessions. Removing the last Owner
// fails with ErrLastOwner.
func (s *Store) DeleteUser(ctx context.Context, userID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ? AND (role != 'owner'
		OR (SELECT count(*) FROM users WHERE role = 'owner') > 1)`, userID)
	if err != nil {
		return err
	}
	return s.lastOwnerCheck(ctx, res, userID)
}

// lastOwnerCheck explains why a guarded update changed nothing.
func (s *Store) lastOwnerCheck(ctx context.Context, res sql.Result, userID int64) error {
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, err := s.UserByID(ctx, userID); err != nil {
		return err // ErrNotFound
	}
	return ErrLastOwner
}

// ResetTOTP turns off a user's two-factor authentication (a lost phone)
// and logs them out everywhere. Owners must set it up again at next login.
func (s *Store) ResetTOTP(ctx context.Context, userID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET totp_secret = '', totp_last_step = 0, totp_failures = 0, totp_failed_at = 0 WHERE id = ?`, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// Themes a user can choose.
var Themes = []string{"auto", "light", "dark"}

// SetTheme saves a user's theme.
func (s *Store) SetTheme(ctx context.Context, userID int64, theme string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET theme = ? WHERE id = ?`, theme, userID)
	return err
}

// SetEmail sets where Roostack emails someone ("" for nowhere).
func (s *Store) SetEmail(ctx context.Context, userID int64, email string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET email = ? WHERE id = ?`, email, userID)
	return err
}

// SetNotify sets someone's notification groups (see User.Notify).
func (s *Store) SetNotify(ctx context.Context, userID int64, groups string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET notify = ? WHERE id = ?`, groups, userID)
	return err
}

// Notice is something someone was (or will be) emailed about.
type Notice struct {
	Key, Text string
	FirstSeen time.Time
	SentAt    time.Time // zero: not emailed yet
	// GoneSince is when it stopped being seen (zero while it's there).
	GoneSince time.Time
}

// Notices lists what's being followed for a person.
func (s *Store) Notices(ctx context.Context, userID int64) (map[string]Notice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, text, first_seen, sent_at, gone_since FROM notices WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Notice{}
	for rows.Next() {
		var n Notice
		var first, sent, gone int64
		if err := rows.Scan(&n.Key, &n.Text, &first, &sent, &gone); err != nil {
			return nil, err
		}
		n.FirstSeen = time.Unix(first, 0)
		if sent != 0 {
			n.SentAt = time.Unix(sent, 0)
		}
		if gone != 0 {
			n.GoneSince = time.Unix(gone, 0)
		}
		out[n.Key] = n
	}
	return out, rows.Err()
}

// SaveNotice records a notice (insert or update).
func (s *Store) SaveNotice(ctx context.Context, userID int64, n Notice) error {
	var sent, gone int64
	if !n.SentAt.IsZero() {
		sent = n.SentAt.Unix()
	}
	if !n.GoneSince.IsZero() {
		gone = n.GoneSince.Unix()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO notices (user_id, key, text, first_seen, sent_at, gone_since) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, key) DO UPDATE SET text = excluded.text, sent_at = excluded.sent_at, gone_since = excluded.gone_since`,
		userID, n.Key, n.Text, n.FirstSeen.Unix(), sent, gone)
	return err
}

// DeleteNotice forgets a notice (it was dealt with).
func (s *Store) DeleteNotice(ctx context.Context, userID int64, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notices WHERE user_id = ? AND key = ?`, userID, key)
	return err
}

// KnownDevice records a browser someone logged in from; it reports
// whether it was new, and whether they had any before.
func (s *Store) KnownDevice(ctx context.Context, userID int64, tokenHash string, now time.Time) (isNew, hadOthers bool, err error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM devices WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return false, false, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO devices (user_id, token_hash, created_at) VALUES (?, ?, ?)`,
		userID, tokenHash, now.Unix())
	if err != nil {
		return false, false, err
	}
	added, _ := res.RowsAffected()
	return added == 1, n > 0, nil
}

// CountUsers returns the number of accounts.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// FirstUserCreated is when the first account (the Owner made at first-run
// setup) was created; ok is false while there's none.
func (s *Store) FirstUserCreated(ctx context.Context) (at time.Time, ok bool, err error) {
	var unix sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT min(created_at) FROM users`).Scan(&unix); err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(unix.Int64, 0), unix.Valid, nil
}

// CreateUser adds an account and returns its ID.
func (s *Store) CreateUser(ctx context.Context, u User) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, must_change_password, email, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, u.Role, u.MustChangePassword, u.Email, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CreateFirstUser adds an account only if there are none, atomically, so
// two concurrent first-run setups can't both succeed. It returns false if a
// user already exists.
func (s *Store) CreateFirstUser(ctx context.Context, u User) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, email, created_at)
		 SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`,
		u.Username, u.PasswordHash, u.Role, u.Email, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// UserByUsername looks up an account (case-insensitive).
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ?`, username))
}

// SetPassword replaces a user's password hash. A temporary password
// (e.g. from the CLI reset) must be changed at the next login.
func (s *Store) SetPassword(ctx context.Context, username, hash string, temporary bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, must_change_password = ? WHERE username = ?`,
		hash, temporary, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// Log out everywhere after a reset.
	_, err = s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE username = ?)`, username)
	return err
}

const userColumns = `users.id, users.username, users.password_hash, users.role,
	users.totp_secret, users.totp_last_step, users.totp_failures, users.totp_failed_at, users.must_change_password, users.theme, users.palette, users.accent, users.email, users.notify, users.name, users.sso_id, users.created_at`

// UserByID looks up an account.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// SetTOTP enables two-factor authentication for a user.
func (s *Store) SetTOTP(ctx context.Context, userID int64, secret string, step int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = ?, totp_last_step = ?, totp_failures = 0, totp_failed_at = 0 WHERE id = ?`, secret, step, userID)
	return err
}

// UseTOTPStep records an accepted code's time step, and clears the count
// of wrong codes. It fails if the step isn't newer than the last one, so
// two concurrent logins can't both use the same code.
func (s *Store) UseTOTPStep(ctx context.Context, userID, step int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_last_step = ?, totp_failures = 0, totp_failed_at = 0
		WHERE id = ? AND totp_last_step < ?`, step, userID, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// AddTOTPFailure counts a wrong two-factor code entered at now, and
// returns how many there have been in a row.
func (s *Store) AddTOTPFailure(ctx context.Context, userID int64, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`UPDATE users SET totp_failures = totp_failures + 1, totp_failed_at = ? WHERE id = ?
		RETURNING totp_failures`, now.Unix(), userID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return n, err
}

// scanUser reads userColumns; a missing row is ErrNotFound.
func scanUser(row scanner) (*User, error) {
	var u User
	var created, failedAt int64
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TOTPSecret, &u.TOTPLastStep,
		&u.TOTPFailures, &failedAt, &u.MustChangePassword, &u.Theme, &u.Palette, &u.Accent, &u.Email, &u.Notify, &u.Name, &u.SSOID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	if failedAt != 0 {
		u.TOTPFailedAt = time.Unix(failedAt, 0)
	}
	return &u, nil
}
