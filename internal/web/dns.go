package web

import (
	"errors"
	"net/http"

	"github.com/mylab/mylab/internal/dnsrecords"
)

// dnsManage turns DNS record management on or off.
func (s *Server) dnsManage(w http.ResponseWriter, r *http.Request) {
	if s.DNS == nil {
		http.NotFound(w, r)
		return
	}
	on := r.FormValue("manage") == "on"
	err := s.DNS.SetManaging(on, r.FormValue("accept_risk") == "on")
	if errors.Is(err, dnsrecords.ErrNeedsRisk) {
		http.Error(w, "Tick the box confirming you understand the risk.", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	what := "stopped managing DNS records"
	if on {
		what = "started managing DNS records"
	}
	s.Store.Audit(r.Context(), currentUser(r), "dns", what)
	redirect(w, r, "/settings/home-network")
}

func (s *Server) dnsTakeOverPage(w http.ResponseWriter, r *http.Request) {
	if s.DNS == nil {
		http.NotFound(w, r)
		return
	}
	st := s.DNS.Status()
	if st.State != dnsrecords.StateConflict {
		redirect(w, r, "/")
		return
	}
	s.render(w, "dns-takeover", map[string]any{"User": currentUser(r), "DNS": st})
}

// dnsTakeOver lets MyLab manage (and overwrite) a record it didn't create,
// after an explicit confirmation.
func (s *Server) dnsTakeOver(w http.ResponseWriter, r *http.Request) {
	if s.DNS == nil {
		http.NotFound(w, r)
		return
	}
	st := s.DNS.Status()
	if r.FormValue("confirm") != "on" {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "dns-takeover", map[string]any{"User": currentUser(r), "DNS": st,
			"Error": "Tick the box to confirm."})
		return
	}
	if err := s.DNS.TakeOver(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "dns", "took over "+st.Record+" (was "+st.Current+")")
	redirect(w, r, "/settings/home-network")
}
