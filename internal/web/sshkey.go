package web

import (
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
)

// The key guide on the Remote access page (SPEC.md §6.10 item 3): until
// Roostack has seen a key login by an account that can use sudo, it shows
// how to make a key, takes the public key, and adds it to a new account or
// one the server has already (agent.SSHKeyRequest).

// sshKeyForm is the guide's form as filled in, never with the password.
type sshKeyForm struct {
	Key string
	// Account is "new" or "existing": User is the new account's name,
	// Existing the account in the sudo group to add the key to.
	Account, User, Existing string
}

// accountNameRe matches agent's rule for an account's name, to offer the
// Owner's own username when it fits.
var accountNameRe = regexp.MustCompile(`^[a-z][-a-z0-9_]{0,31}$`)

// defaultSSHKeyForm offers a new account named after the Owner, or their
// account if the server has it in the sudo group already.
func defaultSSHKeyForm(u *store.User, sudoers []string) sshKeyForm {
	f := sshKeyForm{Account: "new"}
	if u != nil && accountNameRe.MatchString(strings.ToLower(u.Username)) {
		f.User = strings.ToLower(u.Username)
	}
	if len(sudoers) > 0 {
		f.Existing = sudoers[0]
	}
	if f.User != "" && slices.Contains(sudoers, f.User) {
		f.Account, f.Existing, f.User = "existing", f.User, ""
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

// sshKeySubmit adds the Owner's public key to an account that can use
// sudo, then comes back to the guide's last step, which waits for a login
// with it.
func (s *Server) sshKeySubmit(w http.ResponseWriter, r *http.Request) {
	set := s.server(r)
	if set == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	f := sshKeyForm{Key: r.FormValue("key"), Account: r.FormValue("account"), User: strings.TrimSpace(r.FormValue("user")),
		Existing: r.FormValue("existing")}
	req := agent.SSHKeyRequest{Key: f.Key, User: f.User, New: true, Password: r.FormValue("password")}
	if f.Account == "existing" {
		req = agent.SSHKeyRequest{Key: f.Key, User: f.Existing}
	}
	var err error
	if req.New && req.Password != r.FormValue("confirm") {
		err = usererr.New("The passwords don't match.")
	} else {
		err = set.AddSSHKey(r.Context(), req)
	}
	if msg, ok := usererr.Message(err); ok {
		data, derr := s.sshData(r, "", "", "")
		if derr != nil {
			s.serverError(w, derr)
			return
		}
		data["KeyForm"], data["KeyError"] = f, msg
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "setup-ssh", data)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	what := "added an SSH key to " + req.User
	if req.New {
		what = "made the account " + req.User + " (in the sudo group) with an SSH key"
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", m.Label()+" SSH: "+what)
	redirect(w, r, m.RemoteAccess()+"?key="+url.QueryEscape(req.User)+"#key-guide")
}
