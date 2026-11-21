package web

import (
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/store"
)

// The SSH key dialog on the Remote access page and the setup step (SPEC.md
// §6.10 item 3): until Roostack has seen a key login by an account that can
// use sudo, it shows how to make a key, then turns the pasted public key
// and the account chosen into the commands to run as root on the server
// (roostackSSHKey in app.js). Roostack never adds a key or makes an account
// itself: roostack-agent has no way to, so that nothing running as the
// roostack user can give itself a way in as root.

// sshKeyForm is what the dialog offers first.
type sshKeyForm struct {
	// Existing is the account in the sudo group to add the key to, or ""
	// for a new one; NewUser names the account in the commands that make
	// one.
	Existing, NewUser string
}

// accountNameRe matches Debian's rule for an account's name (adduser's
// NAME_REGEX), to offer the Owner's own username when it fits.
var accountNameRe = regexp.MustCompile(`^[a-z][-a-z0-9_]{0,31}$`)

// defaultSSHKeyForm offers the Owner's account if the server has it in the
// sudo group, or the first that is, or a new account named after them.
func defaultSSHKeyForm(u *store.User, sudoers []string) sshKeyForm {
	f := sshKeyForm{NewUser: "you"}
	if u != nil && accountNameRe.MatchString(strings.ToLower(u.Username)) {
		f.NewUser = strings.ToLower(u.Username)
	}
	if len(sudoers) > 0 {
		f.Existing = sudoers[0]
	}
	if slices.Contains(sudoers, f.NewUser) {
		f.Existing = f.NewUser
	}
	return f
}

// sshHost is how the dialog's commands name the server: as the browser
// reached this one; another server by its name in the lab.
func sshHost(r *http.Request, m serverRef) string {
	if !m.This {
		return m.Name
	}
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		return h
	}
	return r.Host
}
