package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"
)

// Single sign-on's runtime data (SPEC.md §5.2): people's names and the ID
// apps know them by, groups, who may sign in to each service, and the
// tokens the sign-in page hands out.

// SetName saves a person's full name ("" for none).
func (s *Store) SetName(ctx context.Context, userID int64, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET name = ? WHERE id = ?`, name, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// EnsureSSOID returns the ID apps know a person by, making it the first
// time: random, so it's never given to anyone else, and fixed, so a
// renamed person keeps their accounts in apps.
func (s *Store) EnsureSSOID(ctx context.Context, userID int64) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET sso_id = ? WHERE id = ? AND sso_id = ''`,
		hex.EncodeToString(buf), userID); err != nil {
		return "", err
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT sso_id FROM users WHERE id = ?`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// Group is a named group of people (SPEC.md §5.2).
type Group struct {
	ID      int64
	Name    string
	Members []int64 // user IDs
}

// ErrGroupExists means another group has that name.
var ErrGroupExists = errors.New("a group with that name exists")

// Groups lists every group with its members, by name.
func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id, g.name, m.user_id FROM user_groups g
		LEFT JOIN user_group_members m ON m.group_id = g.id ORDER BY g.name COLLATE NOCASE, g.id, m.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []Group
	for rows.Next() {
		var id int64
		var name string
		var member sql.NullInt64
		if err := rows.Scan(&id, &name, &member); err != nil {
			return nil, err
		}
		if len(groups) == 0 || groups[len(groups)-1].ID != id {
			groups = append(groups, Group{ID: id, Name: name})
		}
		if member.Valid {
			g := &groups[len(groups)-1]
			g.Members = append(g.Members, member.Int64)
		}
	}
	return groups, rows.Err()
}

// CreateGroup adds a group; ErrGroupExists if the name is taken.
func (s *Store) CreateGroup(ctx context.Context, name string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO user_groups (name, created_at) VALUES (?, ?)`, name, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, ErrGroupExists
		}
		return 0, err
	}
	return res.LastInsertId()
}

// RenameGroup changes a group's name; ErrGroupExists if it's taken.
func (s *Store) RenameGroup(ctx context.Context, id int64, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE user_groups SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrGroupExists
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteGroup removes a group (and it from every service's access).
func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM user_groups WHERE id = ?`, id)
	return err
}

// SetGroupMembers replaces a group's members.
func (s *Store) SetGroupMembers(ctx context.Context, id int64, users []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_members WHERE group_id = ?`, id); err != nil {
		return err
	}
	for _, u := range users {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_group_members (group_id, user_id) VALUES (?, ?)`, id, u); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetUserGroups replaces the groups a person is in.
func (s *Store) SetUserGroups(ctx context.Context, userID int64, groups []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_group_members WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, g := range groups {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO user_group_members (group_id, user_id) VALUES (?, ?)`, g, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Access is who may sign in to a service: everyone in the lab unless
// Chosen, then the Groups and Users chosen (SPEC.md §5.2).
type Access struct {
	Chosen bool
	Groups []int64
	Users  []int64
}

// ServiceAccess is who may sign in to a service.
func (s *Store) ServiceAccess(ctx context.Context, service string) (Access, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, ref FROM service_access WHERE service = ? ORDER BY kind, ref`, service)
	if err != nil {
		return Access{}, err
	}
	defer rows.Close()
	var a Access
	for rows.Next() {
		var kind string
		var ref int64
		if err := rows.Scan(&kind, &ref); err != nil {
			return Access{}, err
		}
		switch kind {
		case "chosen":
			a.Chosen = true
		case "group":
			a.Groups = append(a.Groups, ref)
		case "user":
			a.Users = append(a.Users, ref)
		}
	}
	return a, rows.Err()
}

// SetServiceAccess replaces who may sign in to a service. Everyone (not
// Chosen) keeps no rows.
func (s *Store) SetServiceAccess(ctx context.Context, service string, a Access) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM service_access WHERE service = ?`, service); err != nil {
		return err
	}
	if a.Chosen {
		add := func(kind string, ref int64) error {
			_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO service_access (service, kind, ref) VALUES (?, ?, ?)`, service, kind, ref)
			return err
		}
		if err := add("chosen", 0); err != nil {
			return err
		}
		for _, g := range a.Groups {
			if err := add("group", g); err != nil {
				return err
			}
		}
		for _, u := range a.Users {
			if err := add("user", u); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// DeleteServiceAccess forgets a removed service's access.
func (s *Store) DeleteServiceAccess(ctx context.Context, service string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM service_access WHERE service = ?`, service)
	return err
}

// CanUse reports whether a person may sign in to a service: everyone may,
// unless its access is chosen, then those chosen and the members of the
// groups chosen.
func (s *Store) CanUse(ctx context.Context, service string, userID int64) (bool, error) {
	a, err := s.ServiceAccess(ctx, service)
	if err != nil || !a.Chosen {
		return err == nil, err
	}
	if slices.Contains(a.Users, userID) {
		return true, nil
	}
	if len(a.Groups) == 0 {
		return false, nil
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM user_group_members m
		JOIN service_access a ON a.service = ? AND a.kind = 'group' AND a.ref = m.group_id
		WHERE m.user_id = ?`, service, userID).Scan(&n)
	return n > 0, err
}

// SSOToken is something the sign-in page handed out (SPEC.md §5.2): only
// its hash is kept.
type SSOToken struct {
	Hash string
	// Kind is TokenAccess, TokenRefresh or TokenHost.
	Kind string
	// Service is the app (its client ID) or walled service it's for.
	Service string
	UserID  int64
	// Session is the sign-in it came from (the session's token hash):
	// signing out ends it.
	Session string
	Scope   string
	Expires time.Time
}

// Kinds of SSOToken.
const (
	// TokenAccess lets an app ask who someone is (userinfo).
	TokenAccess = "access"
	// TokenRefresh gets an app new tokens without signing in again.
	TokenRefresh = "refresh"
	// TokenHost is a walled service's cookie, on its address only.
	TokenHost = "host"
)

// AddSSOToken stores a token.
func (s *Store) AddSSOToken(ctx context.Context, t SSOToken) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sso_tokens (token_hash, kind, service, user_id, session_hash, scope, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, t.Hash, t.Kind, t.Service, t.UserID, t.Session, t.Scope, t.Expires.Unix(), time.Now().Unix())
	return err
}

// LookupSSOToken finds an unexpired token of a kind whose sign-in is still
// valid, with its person; ErrNotFound otherwise.
func (s *Store) LookupSSOToken(ctx context.Context, hash, kind string) (*SSOToken, *User, error) {
	var t SSOToken
	var expires int64
	now := time.Now().Unix()
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+`, t.token_hash, t.kind, t.service, t.user_id, t.session_hash, t.scope, t.expires_at
		FROM sso_tokens t JOIN users ON users.id = t.user_id JOIN sessions s ON s.token_hash = t.session_hash
		WHERE t.token_hash = ? AND t.kind = ? AND t.expires_at > ? AND s.expires_at > ? AND s.origin = ?`,
		hash, kind, now, now, OriginSignIn)
	u, err := scanUserRow(scanTail{row, []any{&t.Hash, &t.Kind, &t.Service, &t.UserID, &t.Session, &t.Scope, &expires}})
	if err != nil {
		return nil, nil, err
	}
	t.Expires = time.Unix(expires, 0)
	return &t, u, nil
}

// DeleteSSOToken removes a token (a refresh token once used); it reports
// whether there was one, so a token is used only once even when two
// requests race.
func (s *Store) DeleteSSOToken(ctx context.Context, hash string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sso_tokens WHERE token_hash = ?`, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SignIn is a sign-in on the sign-in page for services.
type SignIn struct {
	User    *User
	Expires time.Time
	// Remember: "keep me signed in".
	Remember bool
	// At is when the person last typed their password for it (OIDC's
	// auth_time).
	At time.Time
}

// SignInSession is a sign-in, for the sign-in page's own cookie.
func (s *Store) SignInSession(ctx context.Context, tokenHash string) (*SignIn, error) {
	var ends, at int64
	var remember bool
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+`, s.expires_at, s.remember, s.signed_in_at
		FROM sessions s JOIN users ON users.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND s.origin = ?`, tokenHash, time.Now().Unix(), OriginSignIn)
	u, err := scanUserRow(scanTail{row, []any{&ends, &remember, &at}})
	if err != nil {
		return nil, err
	}
	return &SignIn{User: u, Expires: time.Unix(ends, 0), Remember: remember, At: time.Unix(at, 0)}, nil
}

// SignedInAgain records that someone typed their password again for a
// sign-in (an app asked for a fresh one), keeping the sign-in and what it
// handed out.
func (s *Store) SignedInAgain(ctx context.Context, tokenHash string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET signed_in_at = ? WHERE token_hash = ? AND origin = ?`,
		at.Unix(), tokenHash, OriginSignIn)
	return err
}

// UserByEmail finds the one person with an email address
// (case-insensitive); ErrNotFound if none or more than one has it.
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE email = ? COLLATE NOCASE`, email).Scan(&n); err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, ErrNotFound
	}
	return s.scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ? COLLATE NOCASE`, email))
}
