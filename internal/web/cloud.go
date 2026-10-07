package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/cloud"
	"roostack.cloud/roostack/internal/dns"
)

// CloudLink is the controller's link to Roostack Cloud (cloud.Service;
// SPEC.md §6.6, "Linking a lab to Roostack Cloud").
type CloudLink interface {
	Status() cloud.Status
	Start(ctx context.Context, name string) (string, error)
	Cancel(ctx context.Context)
	Unlink(ctx context.Context) (bool, error)
	DismissForgotten() error
	NameAvailable(ctx context.Context, name string) (cloud.NameAvailability, error)
	// ClaimName makes a roost.ac name the linked lab's address.
	ClaimName(ctx context.Context, name string) error
	Requests() []cloud.Request
}

// cloudUnreachableFor is how long Cloud can't be reached before the home
// page says so (checks are daily; one missed isn't worth a word).
const cloudUnreachableFor = 3 * 24 * time.Hour

// cloudView is what the Roostack Cloud parts show.
type cloudView struct {
	cloud.Status
	// Setup: shown in the wizard's address step (shorter).
	Setup bool
	// Services use the lab's roost.ac address (unlinking takes it away).
	Services []string
	Requests []cloud.Request
}

// Busy: something is under way that the page waits for (a link to be
// confirmed, or a name claimed after linking).
func (v cloudView) Busy() bool {
	return v.Waiting != nil || v.Linked && v.Name != "" && v.Address == "" && v.AddressError == ""
}

func (s *Server) cloudView(r *http.Request, setup bool) cloudView {
	v := cloudView{Status: s.Cloud.Status(), Setup: setup}
	if v.Address != "" {
		if svcs, err := s.Services(r.Context()); err == nil {
			for _, sv := range svcs {
				if len(sv.URLs) > 0 {
					v.Services = append(v.Services, sv.Title)
				}
			}
		}
	}
	if !setup {
		v.Requests = s.Cloud.Requests()
		if len(v.Requests) > 30 {
			v.Requests = v.Requests[:30]
		}
	}
	return v
}

// requireCloud allows Roostack Cloud's pages only while it's in use.
func (s *Server) requireCloud(h http.HandlerFunc) http.Handler {
	return s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		if s.Cloud == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	})
}

func (s *Server) cloudPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Cloud"] = s.cloudView(r, false)
	data["Unlinked"] = r.FormValue("unlinked")
	s.render(w, "settings-cloud", withArea(data, "cloud"))
}

// cloudLive refreshes the link's state while it waits to be confirmed.
func (s *Server) cloudLive(w http.ResponseWriter, r *http.Request) {
	setup := r.FormValue("in") == "setup"
	s.renderLive(w, r, "settings-cloud", "cloud-live", s.cloudView(r, setup))
}

// cloudLinkSubmit starts linking. The form opens in a new window, which
// this page sends on to Cloud's page; the page it came from waits
// (cloudLive).
func (s *Server) cloudLinkSubmit(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	data := map[string]any{"User": currentUser(r)}
	fail := func(msg string) {
		data["Error"] = msg
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "cloud-open", data)
	}
	if name != "" {
		if msg := roostAcNameProblem(name); msg != "" {
			fail(msg)
			return
		}
		a, err := s.Cloud.NameAvailable(r.Context(), name)
		if err != nil {
			fail(cloudMessage(err))
			return
		}
		if !a.Available {
			fail(a.Reason)
			return
		}
	}
	open, err := s.Cloud.Start(r.Context(), name)
	if err != nil {
		fail(cloudMessage(err))
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "cloud", "Started linking the lab to Roostack Cloud")
	data["OpenURL"] = open
	s.render(w, "cloud-open", data)
}

// roostAcSubmit makes a roost.ac name the address of a lab that's already
// linked to Roostack Cloud (SPEC.md §6.6); a lab that isn't links first
// (cloudLinkSubmit, which then does the same).
func (s *Server) roostAcSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Cloud == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	msg := roostAcNameProblem(name)
	if msg == "" && !s.Cloud.Status().Linked {
		msg = "Link the lab to Roostack Cloud first."
	}
	if msg == "" {
		if a, err := s.Cloud.NameAvailable(r.Context(), name); err != nil {
			msg = cloudMessage(err)
		} else if !a.Available {
			msg = a.Reason
		}
	}
	if msg == "" {
		if err := s.Cloud.ClaimName(r.Context(), name); err != nil {
			msg = cloudMessage(err)
		}
	}
	if msg != "" {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "setup-domain", s.formMode(r, "domain", s.addressData(map[string]any{"User": currentUser(r),
			"Providers": dns.Providers(), "Provider": dns.Providers()[0].ID, "Values": map[string]string{}, "Manage": true,
			"CloudName": name, "CloudError": msg})))
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "address set to "+s.Cloud.Status().Address)
	s.restartingOrNext(w, r)
}

// cloudMessage says what went wrong talking to Cloud, for people.
func cloudMessage(err error) string {
	var u *cloud.Unreachable
	var e *cloud.Error
	switch {
	case errors.As(err, &u):
		return "Roostack Cloud can't be reached right now. Check this server's internet connection, or try again later."
	case errors.As(err, &e):
		return e.Message
	}
	return err.Error()
}

// roostAcNameProblem says what's wrong with a roost.ac name ("" if
// nothing).
func roostAcNameProblem(name string) string {
	if !roostAcName.MatchString(name) || strings.Contains(name, "--") {
		return "A name is 3 to 40 lowercase letters, numbers or single hyphens, starting and ending with a letter or number."
	}
	return ""
}

// cloudNameCheck says whether a roost.ac name is free, as it's typed.
func (s *Server) cloudNameCheck(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	v := map[string]any{"Name": name}
	if name == "" {
		v["Empty"] = true
	} else if msg := roostAcNameProblem(name); msg != "" {
		v["Problem"] = msg
	} else if a, err := s.Cloud.NameAvailable(r.Context(), name); err != nil {
		v["Problem"] = cloudMessage(err)
	} else if !a.Available {
		v["Problem"] = a.Reason
	} else {
		v["Free"] = true
	}
	s.renderLive(w, r, "settings-cloud", "cloud-name-check", v)
}

// cloudBack is where a Cloud form returns to: the wizard's step or
// Settings.
func cloudBack(r *http.Request) string {
	if r.FormValue("in") == "setup" {
		return "/setup/domain"
	}
	return "/settings/cloud"
}

func (s *Server) cloudCancelSubmit(w http.ResponseWriter, r *http.Request) {
	s.Cloud.Cancel(r.Context())
	redirect(w, r, cloudBack(r))
}

func (s *Server) cloudUnlinkSubmit(w http.ResponseWriter, r *http.Request) {
	reached, err := s.Cloud.Unlink(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "cloud", "Unlinked the lab from Roostack Cloud")
	redirect(w, r, "/settings/cloud?unlinked="+map[bool]string{true: "told", false: "here"}[reached])
}

func (s *Server) cloudDismissSubmit(w http.ResponseWriter, r *http.Request) {
	if err := s.Cloud.DismissForgotten(); err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/settings/cloud")
}

// cloudSummary is the Settings overview's line for Roostack Cloud.
func cloudSummary(st cloud.Status) (string, string) {
	switch {
	case st.Linked && st.LastError != "" && time.Since(st.LastContact) > cloudUnreachableFor:
		return "Linked, but it can't be reached", "warn"
	case st.Linked:
		return "Linked to " + st.Account, "ok"
	case !st.Forgotten.IsZero():
		return "No longer linked", "warn"
	case st.Waiting != nil:
		return "Waiting for you to confirm in Roostack Cloud", ""
	}
	return "Not linked: Roostack works without it", ""
}
