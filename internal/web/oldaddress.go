package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
)

// oldWarnFor is how long before the lab's old address stops that the
// home page's notice becomes a warning.
const oldWarnFor = 7 * 24 * time.Hour

// oldAddress is the lab's previous address while it still works, for
// pages (SPEC.md §6.6).
type oldAddress struct {
	config.OldAddress
	// Domain is the lab's current address.
	Now string
}

// oldAddress is nil when there's none.
func (s *Server) oldAddress() *oldAddress {
	if s.OldAddress == nil || s.Domain == "" || !time.Now().Before(s.OldAddress.Until) {
		return nil
	}
	return &oldAddress{OldAddress: *s.OldAddress, Now: s.Domain}
}

// URL is u (an address at the lab's current address) at the old one, or
// "" if it isn't one of the lab's addresses.
func (o *oldAddress) URL(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	sub, ok := strings.CutSuffix(p.Hostname(), "."+o.Now)
	if !ok {
		return ""
	}
	p.Host = sub + "." + o.Domain
	return p.String()
}

// Soon: it stops within oldWarnFor.
func (o *oldAddress) Soon() bool { return time.Until(o.Until) < oldWarnFor }

// oldAddressRow is one address that changed: a service's, or Roostack's.
type oldAddressRow struct {
	Title, Old, New string
}

// oldAddressRows lists every address that changed, Roostack's first.
func (s *Server) oldAddressRows(r *http.Request, o *oldAddress) []oldAddressRow {
	ui := "https://" + caddy.AdminSubdomain + "." + o.Now
	rows := []oldAddressRow{{Title: "Roostack", Old: o.URL(ui), New: ui}}
	if svcs, err := s.Services(r.Context()); err == nil {
		for _, sv := range svcs {
			for _, u := range sv.URLs {
				if old := o.URL(u); old != "" {
					rows = append(rows, oldAddressRow{Title: sv.Title, Old: old, New: u})
				}
			}
		}
	}
	return rows
}

// oldAddressEnd stops the old address now.
func (s *Server) oldAddressEnd(w http.ResponseWriter, r *http.Request) {
	o := s.oldAddress()
	if o == nil {
		redirect(w, r, "/settings/domain")
		return
	}
	if err := s.Setup.EndOldAddress(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "the old address "+o.Domain+" stopped early")
	redirect(w, r, "/restarting")
}
