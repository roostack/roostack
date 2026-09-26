package web

import (
	"errors"
	"net/http"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/setup"
)

// requireWizard allows the setup wizard to the Owner (host settings,
// SPEC.md §5) once they have 2FA.
func (s *Server) requireWizard(h http.HandlerFunc) http.Handler {
	return s.requireLogin(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if s.Setup == nil {
			http.NotFound(w, r)
			return
		}
		if !auth.Can(u.Role, auth.ManageHost) {
			http.Error(w, "Only the Owner can change these settings.", http.StatusForbidden)
			return
		}
		if u.TOTPSecret == "" {
			http.Redirect(w, r, "/account/2fa", http.StatusSeeOther)
			return
		}
		h(w, r)
	})
}

// healthz reports this process's boot ID. With ?since=<old boot ID> from the
// restarting page, it tells htmx to move on once a new process answers.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if since := r.URL.Query().Get("since"); since != "" && since != s.bootID {
		w.Header().Set("HX-Redirect", "/")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte("ok"))
}

func (s *Server) restartingPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "restarting", map[string]any{"User": currentUser(r), "BootID": s.bootID})
}

func (s *Server) domainPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "setup-domain", map[string]any{"User": currentUser(r), "Domain": s.Domain})
}

func (s *Server) domainSubmit(w http.ResponseWriter, r *http.Request) {
	domain, email := r.FormValue("domain"), r.FormValue("email")
	err := s.Setup.SaveDomain(r.Context(), domain, email, r.FormValue("token"))
	if s.wizardError(w, r, err, "setup-domain", map[string]any{"Domain": domain, "Email": email}) {
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "domain set to "+domain)
	http.Redirect(w, r, "/restarting", http.StatusSeeOther)
}

func (s *Server) domainLater(w http.ResponseWriter, r *http.Request) {
	if err := s.Setup.DomainLater(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) backupPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "setup-backup", map[string]any{"User": currentUser(r), "Kind": "local"})
}

func (s *Server) backupSubmit(w http.ResponseWriter, r *http.Request) {
	c := setup.BackupChoice{
		Kind: r.FormValue("kind"), Path: r.FormValue("path"),
		Endpoint: r.FormValue("endpoint"), Bucket: r.FormValue("bucket"), Prefix: r.FormValue("prefix"),
		AccessKey: r.FormValue("access_key"), SecretKey: r.FormValue("secret_key"),
	}
	err := s.Setup.SaveBackup(r.Context(), c)
	// Keep what was typed, except the secret key.
	if s.wizardError(w, r, err, "setup-backup", map[string]any{"Kind": c.Kind, "Path": c.Path,
		"Endpoint": c.Endpoint, "Bucket": c.Bucket, "Prefix": c.Prefix, "AccessKey": c.AccessKey}) {
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "backup destination set")
	http.Redirect(w, r, "/setup/recovery-key", http.StatusSeeOther)
}

func (s *Server) backupLater(w http.ResponseWriter, r *http.Request) {
	if err := s.Setup.BackupLater(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) recoveryPage(w http.ResponseWriter, r *http.Request) {
	key, err := s.Setup.RecoveryKey()
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "recovery-key", map[string]any{"User": currentUser(r), "Key": key})
}

func (s *Server) recoveryConfirm(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("saved") != "on" {
		key, err := s.Setup.RecoveryKey()
		if err != nil {
			s.serverError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "recovery-key", map[string]any{"User": currentUser(r), "Key": key,
			"Error": "Tick the box once you've saved the key."})
		return
	}
	if err := s.Setup.ConfirmRecoveryKey(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "recovery key confirmed")
	http.Redirect(w, r, "/restarting", http.StatusSeeOther)
}

// wizardError re-renders a wizard page for a user-fixable error. It returns
// true if it handled err.
func (s *Server) wizardError(w http.ResponseWriter, r *http.Request, err error, page string, data map[string]any) bool {
	if err == nil {
		return false
	}
	var ce *setup.CheckError
	if !errors.As(err, &ce) {
		s.serverError(w, err)
		return true
	}
	data["User"] = currentUser(r)
	data["Error"] = ce.Msg
	w.WriteHeader(http.StatusBadRequest)
	s.render(w, page, data)
	return true
}
