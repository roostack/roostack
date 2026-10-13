// Package auth is Roostack's roles and usernames (SPEC.md §5). Passwords,
// tokens and two-factor codes are the shared authkit package.
package auth

import (
	"regexp"

	"roostack.cloud/roostack/authkit"
)

// Roles (SPEC.md §5).
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Permissions checked by the web UI.
type Permission int

const (
	// ViewDashboard: see services and their status.
	ViewDashboard Permission = iota
	// ManageServices: install, configure, update, remove services; backups; logs.
	ManageServices
	// ManageHost: host settings, OS updates, restore, recovery key.
	ManageHost
	// ManageUsers: create and remove accounts.
	ManageUsers
)

// Can reports whether a role has a permission.
func Can(role string, p Permission) bool {
	switch role {
	case RoleOwner:
		return true
	case RoleAdmin:
		return p == ViewDashboard || p == ManageServices
	case RoleMember:
		return p == ViewDashboard
	}
	return false
}

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// ValidUsername reports whether a username is acceptable: 1-32 letters,
// digits, dots, dashes or underscores, starting with a letter or digit.
func ValidUsername(name string) bool { return usernameRe.MatchString(name) }

// NewTemporaryPassword returns a random password for a new account or a
// reset (~120 bits), to be changed at the next login.
func NewTemporaryPassword() (string, error) {
	t, err := authkit.NewToken()
	if err != nil {
		return "", err
	}
	return t[:20], nil
}

// ValidRole reports whether role is known.
func ValidRole(role string) bool {
	return role == RoleOwner || role == RoleAdmin || role == RoleMember
}
