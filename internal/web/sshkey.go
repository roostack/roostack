package web

import (
	"cmp"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
)

// The key guide on the Remote access page (SPEC.md §6.10 item 3): until
// Roostack has seen a key login by an account that can use sudo, it shows
// how to make a key, takes the public key, and adds it to an account the
// server has already (agent.SSHKeyRequest), once the Owner has given their
// password and a two-factor code again (stepUp). Making a new account is
// the Owner's to do as root, with the commands the guide shows: the agent
// never makes one.

// sshKeyForm is the guide's form as filled in, never with the password or
// code.
type sshKeyForm struct {
	Key string
	// Existing is the account in the sudo group to add the key to;
	// NewUser names the account in the commands that make one.
	Existing, NewUser string
}

// accountNameRe matches agent's rule for an account's name, to offer the
// Owner's own username when it fits.
var accountNameRe = regexp.MustCompile(`^[a-z][-a-z0-9_]{0,31}$`)

// defaultSSHKeyForm offers the Owner's account if the server has it in the
// sudo group, or the first that is, and names a new account after them in
// the commands.
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

// sshHost is how the guide's ssh command names the server: as the browser
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

// sshKeySubmit checks the key, then that it's the Owner (their password
// and a fresh code, since whoever has the key can use sudo), adds the key
// to the account, and comes back to the guide's last step, which waits for
// a login with it. Every Owner hears of it by email (AuditSSHKeyAdded).
func (s *Server) sshKeySubmit(w http.ResponseWriter, r *http.Request) {
	set := s.server(r)
	if set == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	w.Header().Set("Cache-Control", "no-store")
	key, account := r.FormValue("key"), r.FormValue("existing")
	again := func(status int, msg string) {
		data, err := s.sshData(r, "", "", "")
		if err != nil {
			s.serverError(w, err)
			return
		}
		f, _ := data["KeyForm"].(sshKeyForm)
		f.Key, f.Existing = key, cmp.Or(account, f.Existing)
		data["KeyForm"], data["KeyError"] = f, msg
		w.WriteHeader(status)
		s.render(w, "setup-ssh", data)
	}
	fingerprint, err := agent.SSHKeyFingerprint(key)
	if err != nil {
		again(http.StatusBadRequest, err.Error())
		return
	}
	if !s.stepUp(w, r, "ssh key", again) {
		return
	}
	err = set.AddSSHKey(r.Context(), agent.SSHKeyRequest{Key: key, User: account})
	if msg, ok := usererr.Message(err); ok {
		again(http.StatusBadRequest, msg)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), attention.AuditSSHKeyAdded,
		account+" on "+cmp.Or(m.Name, "the lab's server")+", key "+fingerprint)
	redirect(w, r, m.RemoteAccess()+"?key="+url.QueryEscape(account)+"#key-guide")
}
