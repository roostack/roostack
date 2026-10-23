package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/cloud"
	"roostack.cloud/roostack/internal/dns"
)

// CloudLink is the controller's link to Roostack Cloud (cloud.Service;
// SPEC.md §6.6, "Linking a lab to Roostack Cloud").
type CloudLink interface {
	Status() cloud.Status
	Start(ctx context.Context, name, returnURL string) (string, error)
	Cancel(ctx context.Context)
	Unlink(ctx context.Context) (bool, error)
	DismissForgotten() error
	NameAvailable(ctx context.Context, name string) (cloudapi.NameAvailability, error)
	// ClaimName makes a roost.ac name the linked lab's address.
	ClaimName(ctx context.Context, name string) error
	Requests() []cloud.Request
	// NameSuffix is the suffix Cloud's names have: roost.ac, or a test
	// Cloud's own.
	NameSuffix(ctx context.Context) string
	// RelayStats are the lab's statistics through the relay (cloudapi).
	RelayStats(ctx context.Context, days int, byDay bool) (cloudapi.RelayStats, error)
	// RelayName is the lab's relay name and its own domain's proof.
	RelayName() (string, *cloudapi.RelayDomain)
	// RelayStanding is when the account turned the relay off at Cloud,
	// and why Cloud suspended it, as Cloud last said; TurnRelayOn turns
	// it on again (the Owner's choice: the lab's updates leave it off).
	RelayStanding() (turnedOff time.Time, suspended string)
	TurnRelayOn(ctx context.Context) error
	// RelayPaused is when the account paused public access at the relay's
	// edges from Cloud's website (zero: it isn't); ResumeRelay lifts it
	// (the Owner's choice, SPEC.md §6.16).
	RelayPaused() time.Time
	ResumeRelay(ctx context.Context) error
}

// cloudSuffix is the suffix the lab's Cloud gives names, for the pages.
func (s *Server) cloudSuffix(ctx context.Context) string {
	if s.Cloud == nil {
		return cloudapi.DefaultNameSuffix
	}
	return s.Cloud.NameSuffix(ctx)
}

// cloudView is what the Roostack Cloud parts show.
type cloudView struct {
	cloud.Status
	// Setup: shown in the wizard's address step (shorter).
	Setup bool
	// Services use the lab's roost.ac address (unlinking takes it away).
	Services []string
	// Relayed: public visitors come in through Cloud's relay (unlinking
	// turns it off).
	Relayed  bool
	Requests []cloud.Request
	// Suffix is the one Cloud's names have (cloudSuffix).
	Suffix string
}

// Busy: something is under way that the page waits for (a link to be
// confirmed, or a name claimed after linking).
func (v cloudView) Busy() bool {
	return v.Waiting != nil || v.Linked && v.Name != "" && v.Address == "" && v.AddressError == ""
}

func (s *Server) cloudView(r *http.Request, setup bool) cloudView {
	v := cloudView{Status: s.Cloud.Status(), Setup: setup, Suffix: s.cloudSuffix(r.Context())}
	if v.Address != "" {
		if svcs, err := s.Services(r.Context()); err == nil {
			for _, sv := range svcs {
				if len(sv.URLs) > 0 {
					v.Services = append(v.Services, sv.Title)
				}
			}
		}
	}
	v.Relayed = v.Linked && s.Manager.RelayOn()
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
	data := s.pageData(r)
	data["Cloud"] = s.cloudView(r, false)
	data["Unlinked"] = r.FormValue("unlinked")
	s.render(w, "settings-cloud", withArea(data, "cloud"))
}

// cloudLive refreshes the link's state while it waits to be confirmed.
func (s *Server) cloudLive(w http.ResponseWriter, r *http.Request) {
	setup := r.FormValue("in") == "setup"
	s.renderLive(w, r, "settings-cloud", "cloud-live", s.cloudView(r, setup))
}

// cloudLinkSubmit starts linking, in the same tab (SPEC.md §6.6): this
// page goes on to Cloud's, which sends the browser back to the lab's page
// for the link (cloudLinkedPage) when it's confirmed or cancelled.
func (s *Server) cloudLinkSubmit(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	setup := r.FormValue("in") == "setup"
	data := map[string]any{"User": currentUser(r), "Setup": setup, "Wizard": setup && s.wizardOpen(r), "CloudName": name}
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
		// A held name may be the Owner's own (its lab unlinked): only Cloud,
		// once linked, knows, so linking goes ahead and the claim decides.
		if !a.Available && !a.Held {
			fail(a.Reason)
			return
		}
	}
	// Back to the address the browser uses for the lab (an SSH tunnel's, or
	// roost.<domain>): the form's Origin, which sameOrigin has checked.
	back := r.Header.Get("Origin") + "/settings/cloud/linked"
	if setup {
		back = r.Header.Get("Origin") + "/setup/address/linked"
	}
	open, err := s.Cloud.Start(r.Context(), name, back)
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
	if msg == "" && !s.ownCloudName(name) {
		if a, err := s.Cloud.NameAvailable(r.Context(), name); err != nil {
			msg = cloudMessage(err)
		} else if !a.Available && !a.Held {
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
		return "Roostack Cloud can't be reached right now. Check this server's internet connection or try again later."
	case errors.As(err, &e):
		return e.Message
	}
	return err.Error()
}

// roostAcNameProblem says what's wrong with a roost.ac name ("" if
// nothing).
func roostAcNameProblem(name string) string {
	if !roostAcName.MatchString(name) || strings.Contains(name, "--") {
		return "A name is 3 to 40 lowercase letters, numbers, or single hyphens, starting and ending with a letter or number."
	}
	return ""
}

// cloudNameCheck says whether a roost.ac name is free, as it's typed.
func (s *Server) cloudNameCheck(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.FormValue("name")))
	v := map[string]any{"Name": name, "Suffix": s.cloudSuffix(r.Context())}
	if name == "" {
		v["Empty"] = true
	} else if msg := roostAcNameProblem(name); msg != "" {
		v["Problem"] = msg
	} else if s.ownCloudName(name) {
		v["Own"] = true
	} else if a, err := s.Cloud.NameAvailable(r.Context(), name); err != nil {
		v["Problem"] = cloudMessage(err)
	} else if a.Held {
		v["Held"] = true
	} else if !a.Available {
		v["Problem"] = a.Reason
	} else {
		v["Free"] = true
	}
	s.renderLive(w, r, "settings-cloud", "cloud-name-check", v)
}

// ownCloudName: name is the linked lab's own roost.ac name (still claimed,
// though it may not be the address now). Asking whether it's free is
// anonymous, so Cloud says it's taken; the claim, made as the lab, gives it
// back (found on VM3, 2026-10-14, going back from an own domain).
func (s *Server) ownCloudName(name string) bool {
	st := s.Cloud.Status()
	return st.Linked && st.Name != "" && st.Name == name
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
	s.Store.Audit(r.Context(), currentUser(r), "cloud", "Cancelled linking the lab to Roostack Cloud")
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
	s.Store.Audit(r.Context(), currentUser(r), "cloud", "Dismissed the note that Roostack Cloud no longer knows the lab")
	redirect(w, r, "/settings/cloud")
}

// cloudSummary is the Settings overview's line for Roostack Cloud.
func cloudSummary(st cloud.Status) (string, string) {
	switch {
	case st.Linked && st.Refused != "":
		return "Linked, but Roostack Cloud is refusing its requests", "warn"
	case st.Linked && st.LastError != "" && time.Since(st.LastContact) > attention.CloudUnreachableFor:
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

// cloudStage is how far a link (and its name) has got, for the page the
// browser comes back to from Cloud.
type cloudStage struct {
	cloudView
	// Done: linked, and the name asked for (if any) is the lab's address;
	// Failed: it didn't happen (StartError or AddressError say why).
	Done, Failed bool
	// Asked is the roost.ac name asked for ("" for none), while waiting or
	// once linked.
	Asked string
}

func (s *Server) cloudStage(r *http.Request, setup bool) cloudStage {
	st := cloudStage{cloudView: s.cloudView(r, setup), Asked: s.Cloud.Status().Name}
	if st.Waiting != nil {
		st.Asked = st.Waiting.Name
	}
	switch {
	case st.Linked && st.AddressError != "":
		st.Failed = true
	case st.Linked && (st.Name == "" || st.Address != ""):
		st.Done = true
	case !st.Linked && st.Waiting == nil:
		st.Failed = true
	}
	return st
}

// cloudNext is where the browser goes once the link is done (or isn't):
// in the wizard, its next step (with the address said at the top) or
// back to the address step to say what went wrong; otherwise Settings.
func (s *Server) cloudNext(r *http.Request, setup bool, st cloudStage) string {
	if !setup {
		return "/settings/cloud"
	}
	if st.Failed {
		return "/setup/domain"
	}
	next := s.wizardNext(r)
	if st.Address != "" {
		next += "?address=1"
	}
	return next
}

// cloudLinkedPage is where Cloud sends the browser back to: each stage as
// it happens, and on by itself once it's done (SPEC.md §6.6). The lab
// asks Cloud in the background, so it finishes whether or not the
// browser comes back here.
func (s *Server) cloudLinkedPage(w http.ResponseWriter, r *http.Request) {
	setup := strings.HasPrefix(r.URL.Path, "/setup/")
	st := s.cloudStage(r, setup)
	if st.Done || st.Failed {
		redirect(w, r, s.cloudNext(r, setup, st))
		return
	}
	data := s.pageData(r)
	data["Stage"], data["Setup"], data["Wizard"], data["Suffix"] = st, setup, setup && s.wizardOpen(r), s.cloudSuffix(r.Context())
	s.render(w, "cloud-linked", data)
}

// cloudProgress refreshes the stages, and moves the page on when done.
func (s *Server) cloudProgress(w http.ResponseWriter, r *http.Request) {
	setup := r.FormValue("in") == "setup"
	st := s.cloudStage(r, setup)
	if st.Done || st.Failed {
		w.Header().Set("HX-Redirect", s.cloudNext(r, setup, st))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.renderLive(w, r, "cloud-linked", "cloud-progress", map[string]any{"Stage": st, "Setup": setup, "Suffix": s.cloudSuffix(r.Context())})
}
