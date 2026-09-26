package web

import (
	"errors"
	"net/http"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/dns"
	"github.com/mylab/mylab/internal/setup"
)

// requireWizard allows the setup wizard to the Owner.
func (s *Server) requireWizard(h http.HandlerFunc) http.Handler {
	return s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		if s.Setup == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	})
}

// requireOwner allows host settings (SPEC.md §5) to the Owner, once they
// have 2FA.
func (s *Server) requireOwner(h http.HandlerFunc) http.Handler {
	return s.requireLogin(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if !auth.Can(u.Role, auth.ManageHost) {
			http.Error(w, "Only the Owner can change these settings.", http.StatusForbidden)
			return
		}
		if u.TOTPSecret == "" {
			redirect(w, r, "/account/2fa")
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
	provider := s.DNSProvider
	if dns.Get(provider) == nil {
		provider = dns.Providers()[0].ID
	}
	s.render(w, "setup-domain", map[string]any{"User": currentUser(r), "Domain": s.Domain,
		"Providers": dns.Providers(), "Provider": provider, "Values": map[string]string{}, "Manage": true})
}

func (s *Server) domainSubmit(w http.ResponseWriter, r *http.Request) {
	domain, email, provider := r.FormValue("domain"), r.FormValue("email"), r.FormValue("provider")
	// Fields are named dns.<provider>.<key>; only the chosen provider's count.
	values := map[string]string{}
	kept := map[string]string{} // re-shown on error: never the sensitive ones
	if p := dns.Get(provider); p != nil {
		for _, f := range p.Fields {
			name := "dns." + p.ID + "." + f.Key
			values[f.Key] = r.FormValue(name)
			if !f.Sensitive {
				kept[name] = values[f.Key]
			}
		}
	}
	opts := setup.DomainOptions{ManageRecords: r.FormValue("manage_records") == "on", AcceptRisk: r.FormValue("accept_risk") == "on"}
	err := s.Setup.SaveDomain(r.Context(), domain, email, provider, values, opts)
	if s.wizardError(w, r, err, "setup-domain", map[string]any{"Domain": domain, "Email": email,
		"Providers": dns.Providers(), "Provider": provider, "Values": kept, "Manage": opts.ManageRecords, "AcceptRisk": opts.AcceptRisk}) {
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "domain set to "+domain)
	redirect(w, r, "/restarting")
}

func (s *Server) domainLater(w http.ResponseWriter, r *http.Request) {
	if err := s.Setup.DomainLater(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/")
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
	redirect(w, r, "/setup/recovery-key")
}

func (s *Server) backupLater(w http.ResponseWriter, r *http.Request) {
	if err := s.Setup.BackupLater(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/")
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
	redirect(w, r, "/restarting")
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
