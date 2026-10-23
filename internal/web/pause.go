package web

import (
	"cmp"
	"net/http"
	"slices"

	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
)

// Pauses is pausing public access (SPEC.md §6.16), in internal/manager.
type Pauses interface {
	PublicPauses() (manager.PublicPauses, error)
	PausePublic(by string, r manager.PauseRequest) error
	LiftPause(r manager.PauseRequest) error
}

// builtWithChoice is something the lab's public services are built with,
// on the pause page: which services, and whether it's paused already.
type builtWithChoice struct {
	Name     string
	Services []string
	Paused   bool
}

// builtWithChoices are what the public services are built with, in the
// order first met (a service's own names first, then its databases').
func builtWithChoices(pp manager.PublicPauses) []builtWithChoice {
	var out []builtWithChoice
	for _, s := range pp.Services {
		for _, b := range s.BuiltWith {
			i := slices.IndexFunc(out, func(c builtWithChoice) bool { return c.Name == b })
			if i < 0 {
				_, paused := pp.BuiltWith[b]
				out = append(out, builtWithChoice{Name: b, Paused: paused})
				i = len(out) - 1
			}
			out[i].Services = append(out[i].Services, s.Title)
		}
	}
	return out
}

// pauseData adds what the services lists and Home show about pausing
// public access: CanPause while anything is public, and Paused, the names
// of the services that are.
func (s *Server) pauseData(data map[string]any) {
	pp, err := s.Manager.PublicPauses()
	if err != nil {
		s.Log.Error("listing public access", "err", err)
		return
	}
	paused := map[string]bool{}
	for _, ps := range pp.PausedServices() {
		paused[ps.Name] = true
	}
	data["CanPause"], data["Paused"] = len(pp.Services) > 0, paused
}

// pausePage is Pause public access: what to pause, and what will happen.
// Owners and Admins can pause, since it only ever reduces what's reachable.
func (s *Server) pausePage(w http.ResponseWriter, r *http.Request) {
	s.renderPause(w, r, "")
}

func (s *Server) renderPause(w http.ResponseWriter, r *http.Request, msg string) {
	pp, err := s.Manager.PublicPauses()
	if err != nil {
		s.serverError(w, err)
		return
	}
	data := s.pageData(r)
	data["Pauses"], data["BuiltWith"], data["Error"] = pp, builtWithChoices(pp), msg
	data["Scope"] = r.FormValue("scope")
	data["Chosen"] = r.Form["service"]
	s.render(w, "pause", data)
}

// pauseRequest reads what a pause or lift form asks for: scope "all",
// "built" (built_with values) or "some" (service values); a lift's form
// may name several at once.
func pauseRequest(r *http.Request) manager.PauseRequest {
	var req manager.PauseRequest
	switch r.FormValue("scope") {
	case "all":
		req.All = true
	case "built":
		req.BuiltWith = r.Form["built_with"]
	case "some":
		req.Services = r.Form["service"]
	default:
		req.All = r.FormValue("all") == "yes"
		req.BuiltWith, req.Services = r.Form["built_with"], r.Form["service"]
	}
	return req
}

// pauseSubmit pauses what the form asks for, records who did, and goes
// back to where it was asked (Home by default).
func (s *Server) pauseSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	req := pauseRequest(r)
	u := currentUser(r)
	if err := s.Manager.PausePublic(u.Username, req); err != nil {
		if msg, ok := usererr.Message(err); ok {
			s.renderPause(w, r, msg)
			return
		}
		if err == manager.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, attention.AuditPublicPaused, s.pauseDetail(req))
	redirect(w, r, localNext(r.FormValue("next")))
}

// liftSubmit opens public access again for what the form names: the
// Owner's alone, since exposing something to the internet is the Owner's
// call everywhere else (SPEC.md §6.11).
func (s *Server) liftSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	req := pauseRequest(r)
	if err := s.Manager.LiftPause(req); err != nil {
		if err == manager.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		if msg, ok := usererr.Message(err); ok {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), attention.AuditPublicOpened, s.pauseDetail(req))
	redirect(w, r, localNext(r.FormValue("next")))
}

// pauseDetail says what a pause or lift was about, for the audit log and
// the Owners' email: "everything", "services built with php", "Lychee".
func (s *Server) pauseDetail(req manager.PauseRequest) string {
	var parts []string
	if req.All {
		parts = append(parts, "everything")
	}
	if len(req.BuiltWith) > 0 {
		parts = append(parts, "services built with "+words.And(req.BuiltWith))
	}
	titles := map[string]string{}
	if pp, err := s.Manager.PublicPauses(); err == nil {
		for _, ps := range pp.Services {
			titles[ps.Name] = ps.Title
		}
	}
	for _, name := range req.Services {
		parts = append(parts, cmp.Or(titles[name], name))
	}
	return words.And(parts)
}
