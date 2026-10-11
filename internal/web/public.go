package web

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/public"
)

// Public access (SPEC.md §6.11): the Owner makes a service's address
// public from its page, after a warning (and the package's reasons for
// anything it marks unsafe). The page then shows a short checklist:
// protection (CrowdSec), the DNS record, the router's port forwarding (or
// on a lab on the internet the provider's firewall, SPEC.md §6.12), and a
// check from outside, with what to look at if it fails.

// PublicAccess is making services public, in internal/manager.
type PublicAccess interface {
	Access(name string) ([]manager.RouteAccess, error)
	SetPublic(ctx context.Context, name string, req manager.PublicRequest) error
	PublicHosts() []string
	SignalSharing() (asked, share bool)
	SetSignalSharing(share bool) error
	// The lab's way in for visitors (SPEC.md §10.5): servers that can be
	// its edge, the one it has ("", false for port forwarding at home),
	// and changing it.
	EdgeServers(ctx context.Context) []manager.EdgeServer
	Edge(ctx context.Context) (manager.EdgeServer, bool)
	SetEdge(ctx context.Context, id string) error
	// Roostack Cloud's relay as the way in instead.
	RelayOn() bool
	SetRelay(ctx context.Context, on bool) error
	// Who the relay lets in by country.
	Countries() config.Countries
	SetCountries(ctx context.Context, c config.Countries) error
}

// publicCheckKey is where a successful outside check is recorded.
func publicCheckKey(host string) string { return "public.checked." + host }

// PublicCheck is the last successful check of an address from outside.
type PublicCheck struct {
	At   time.Time
	From string
}

// RecordPublicCheck saves a successful outside check (public.Checks.Record).
func RecordPublicCheck(ctx context.Context, s interface {
	SetSetting(context.Context, string, string) error
}, host string, at time.Time, from string) error {
	return s.SetSetting(ctx, publicCheckKey(host), at.UTC().Format(time.RFC3339)+" "+from)
}

func (s *Server) lastCheck(ctx context.Context, host string) *PublicCheck {
	v, err := s.Store.Setting(ctx, publicCheckKey(host))
	if err != nil || v == "" {
		return nil
	}
	at, from, _ := strings.Cut(v, " ")
	t, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return nil
	}
	return &PublicCheck{At: t, From: from}
}

// routeAccess finds the route the request is about.
func (s *Server) routeAccess(r *http.Request) (*manager.RouteAccess, error) {
	routes, err := s.Manager.Access(r.PathValue("name"))
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(routes, func(ra manager.RouteAccess) bool { return ra.Route == r.PathValue("route") })
	if i < 0 {
		return nil, manager.ErrNotFound
	}
	return &routes[i], nil
}

func (s *Server) publicPage(w http.ResponseWriter, r *http.Request) {
	if s.noAddress(w, r) {
		return
	}
	s.renderPublic(w, r, "")
}

// noAddress sends a request about public access back to the service while
// the lab has no address: there's nothing to make public (SPEC.md §6.6).
func (s *Server) noAddress(w http.ResponseWriter, r *http.Request) bool {
	if s.Domain != "" {
		return false
	}
	redirect(w, r, "/services/"+url.PathEscape(r.PathValue("name")))
	return true
}

// renderPublic shows an address's public access page, with a form error.
func (s *Server) renderPublic(w http.ResponseWriter, r *http.Request, formErr string) {
	ra, err := s.routeAccess(r)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	p, err := s.Manager.ServicePackage(r.PathValue("name"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	asked, share := s.Manager.SignalSharing()
	data := map[string]any{"User": currentUser(r), "Package": p, "Name": r.PathValue("name"), "Route": ra,
		"SharingAsked": asked, "Share": share, "Error": formErr}
	s.addPublicUI(data)
	if !s.onInternet() {
		if e, ok := s.Manager.Edge(r.Context()); ok {
			data["Edge"] = e
		}
	}
	if ra.Public {
		data["Protected"] = s.Protected != nil && s.Protected(r.Context())
		data["Check"] = s.lastCheck(r.Context(), ra.Host)
		if s.DNS != nil {
			st := s.DNS.Status()
			data["LANIP"], data["PublicIP"] = st.IP, st.PublicIP
			for _, rec := range st.Public {
				if rec.Host == ra.Host {
					data["Record"] = rec
				}
			}
		}
		if s.Checks != nil {
			if token := s.Checks.Current(ra.Host); token != "" {
				link := "https://" + ra.Host + public.CheckPath + token
				data["CheckLink"] = link
				if qr, err := qrSVG(link); err == nil {
					data["CheckQR"] = qr
				}
			}
		}
	}
	if formErr != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, "service-public", data)
}

func (s *Server) publicSubmit(w http.ResponseWriter, r *http.Request) {
	if s.noAddress(w, r) {
		return
	}
	ra, err := s.routeAccess(r)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	name, u := r.PathValue("name"), currentUser(r)
	makePublic := r.FormValue("public") == "on"
	if makePublic {
		// Asked the first time something goes public (SPEC.md §6.11).
		if share := r.FormValue("share"); share != "" {
			if err := s.Manager.SetSignalSharing(share == "yes"); err != nil {
				s.serverError(w, err)
				return
			}
			s.Store.Audit(r.Context(), u, "public access", "CrowdSec sharing: "+share)
		} else if asked, _ := s.Manager.SignalSharing(); !asked {
			s.renderPublic(w, r, "Choose whether CrowdSec shares attack reports.")
			return
		}
	}
	req := manager.PublicRequest{Route: ra.Route, Public: makePublic,
		AcceptUnsafe: r.FormValue("accept_unsafe") == "on", OpenPaths: r.Form["open"]}
	if err := s.Manager.SetPublic(r.Context(), name, req); err != nil {
		if msg, ok := userMessage(err); ok {
			s.renderPublic(w, r, msg)
			return
		}
		s.serverError(w, err)
		return
	}
	detail := ra.Host + " is private again"
	if makePublic {
		detail = ra.Host + " made public"
		if ra.Unsafe != "" {
			detail += ", though the package says it isn't safe"
		}
		if len(req.OpenPaths) > 0 {
			detail += "; opened " + strings.Join(req.OpenPaths, ", ")
		}
	}
	s.Store.Audit(r.Context(), u, "public access", detail)
	if s.DNS != nil {
		s.DNS.Refresh()
	}
	redirect(w, r, "/services/"+name+"/public/"+ra.Route)
}

// publicCheckNew makes a check link for the address.
func (s *Server) publicCheckNew(w http.ResponseWriter, r *http.Request) {
	ra, err := s.routeAccess(r)
	if err != nil || !ra.Public || s.Checks == nil {
		http.NotFound(w, r)
		return
	}
	s.Checks.New(ra.Host)
	redirect(w, r, "/services/"+r.PathValue("name")+"/public/"+ra.Route+"#check")
}

// publicCheckVisit answers a check link, opened on a phone. Caddy passes
// only this path on a public address to Roostack (caddy.CheckPattern);
// the visitor's address is in X-Forwarded-For, which Caddy sets itself
// (it trusts no proxies in front of it). The page is plain text-like HTML,
// since the address's other paths (styles too) go to the service.
func (s *Server) publicCheckVisit(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	result := public.VisitUnknown
	if s.Checks != nil && fromLoopback(r) {
		fwd := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		result, err = s.Checks.Visit(r.PathValue("token"), host, strings.TrimSpace(fwd[len(fwd)-1]))
		if err != nil {
			s.Log.Error("recording an outside check", "err", err)
		}
	}
	msg := map[int]string{
		public.VisitUnknown: "This check link has expired or was already used. Make a new one on the service's page in Roostack.",
		public.VisitFromHome: "This came from your home network, so it doesn't show whether the internet can reach " + host +
			". Turn Wi-Fi off, so your phone uses mobile data, and open the link again.",
		public.VisitOutside: "It works: " + host + " is reachable from the internet. You can close this page; Roostack has ticked the check.",
	}[result]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if result == public.VisitUnknown {
		w.WriteHeader(http.StatusNotFound)
	}
	fmt.Fprintf(w, "<!doctype html><meta name=viewport content=\"width=device-width\"><title>Roostack check</title><p>%s</p>\n",
		html.EscapeString(msg))
}

// addPublicUI says where the lab is, and whether Roostack's own pages are
// public (on a lab on the internet, SPEC.md §6.3.2) and at which address.
func (s *Server) addPublicUI(data map[string]any) {
	c := s.network()
	internet := c.Saved == config.NetworkInternet
	data["Internet"] = internet
	data["PublicUI"] = internet && !c.UITunnelOnly && s.Domain != ""
	data["UIHost"] = s.uiHost()
}

// fromLoopback says whether a request came from this server itself (its
// Caddy), which is the only way a check visit reaches the UI.
func fromLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// publicSettingsPage is Settings → Public access: what's public, whether
// CrowdSec is running, and the sharing choice.
func (s *Server) publicSettingsPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	asked, share := s.Manager.SignalSharing()
	data["SharingAsked"], data["Share"] = asked, share
	hosts := s.Manager.PublicHosts()
	data["Hosts"] = hosts
	// How visitors come in: port forwarding at home, or through a cloud
	// server in the lab (SPEC.md §10.5). A lab on the internet is reached
	// directly.
	if !s.onInternet() {
		data["EdgeServers"] = s.Manager.EdgeServers(r.Context())
		if e, ok := s.Manager.Edge(r.Context()); ok {
			data["Edge"] = e
		}
		data["CanRelay"], data["Relay"] = s.canRelay(), s.Manager.RelayOn()
		data["RelayError"] = r.FormValue("relay_error")
		if s.Manager.RelayOn() {
			data["VisitorsPeriod"] = r.FormValue("period")
			c := s.Manager.Countries()
			data["Countries"], data["CountryList"] = c, countryList()
			data["CountriesOpen"] = c.Open(time.Now())
			data["CountriesChosen"] = countryNamesOf(append(slices.Clone(c.Allow), c.Block...))
			data["CountriesError"] = r.FormValue("countries_error")
		}
	}
	if len(hosts) > 0 {
		data["Protected"] = s.Protected != nil && s.Protected(r.Context())
		if s.DNS != nil {
			st := s.DNS.Status()
			data["Records"], data["PublicIP"] = st.Public, st.PublicIP
		}
	}
	data["Saved"] = r.FormValue("saved")
	s.render(w, "settings-public", withArea(data, "public"))
}

// canRelay: Roostack Cloud's relay can be the way in: the lab is linked,
// and its address is its roost.ac name (custom domains come later).
func (s *Server) canRelay() bool {
	if s.Cloud == nil {
		return false
	}
	st := s.Cloud.Status()
	return st.Linked && st.Address != "" && st.Address == s.Domain
}

// publicEdgeSubmit sets how visitors reach the lab's public services:
// port forwarding (""), a cloud server in the lab (its ID), or Roostack
// Cloud's relay ("cloud").
func (s *Server) publicEdgeSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("edge")
	if id == "cloud" {
		if err := s.Manager.SetRelay(r.Context(), true); err != nil {
			redirect(w, r, "/settings/public?relay_error="+url.QueryEscape(cloudMessage(err))+"#way-in")
			return
		}
		s.Store.Audit(r.Context(), currentUser(r), "public access", "visitors come in through Roostack Cloud's relay")
		redirect(w, r, "/settings/public?saved=edge#way-in")
		return
	}
	if s.Manager.RelayOn() {
		if err := s.Manager.SetRelay(r.Context(), false); err != nil {
			s.serverError(w, err)
			return
		}
	}
	if err := s.Manager.SetEdge(r.Context(), id); err != nil {
		if msg, ok := userMessage(err); ok {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		s.serverError(w, err)
		return
	}
	what := "port forwarding at home"
	if id != "" {
		what = "through server " + id
	}
	s.Store.Audit(r.Context(), currentUser(r), "public access", "visitors come in "+what)
	redirect(w, r, "/settings/public?saved=edge#way-in")
}

// publicCountriesSubmit sets who the relay lets in by country: anyone,
// only visitors from some countries, or everyone but those from some. A
// time everyone is let in until (travel) is kept while there's a limit.
func (s *Server) publicCountriesSubmit(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	var codes []string
	for _, c := range r.Form["countries"] {
		if _, ok := countryNames[c]; ok && !slices.Contains(codes, c) {
			codes = append(codes, c)
		}
	}
	slices.Sort(codes)
	c := config.Countries{OpenUntil: s.Manager.Countries().OpenUntil}
	switch mode := r.FormValue("mode"); {
	case mode == "anyone":
	case (mode == "allow" || mode == "block") && len(codes) == 0:
		s.countriesFailed(w, r, "Choose at least one country, or let in anyone.")
		return
	case mode == "allow":
		c.Allow = codes
	case mode == "block":
		c.Block = codes
	default:
		http.Error(w, "Choose who can visit.", http.StatusBadRequest)
		return
	}
	if err := s.Manager.SetCountries(r.Context(), c); err != nil {
		s.countriesFailed(w, r, cloudMessage(err))
		return
	}
	what := "anyone, from any country"
	if len(c.Allow) > 0 {
		what = "only visitors from " + strings.Join(c.Allow, ", ")
	} else if len(c.Block) > 0 {
		what = "everyone except visitors from " + strings.Join(c.Block, ", ")
	}
	s.Store.Audit(r.Context(), currentUser(r), "public access", "Roostack Cloud's relay lets in "+what)
	redirect(w, r, "/settings/public?saved=countries#countries")
}

// openDays are how long everyone can be let in for, while travelling.
var openDays = []int{1, 7, 14, 30}

// publicCountriesOpen lets everyone in for a while ("open", with days),
// for travel, or ends that now ("close").
func (s *Server) publicCountriesOpen(w http.ResponseWriter, r *http.Request) {
	c := s.Manager.Countries()
	if !c.Limited() {
		redirect(w, r, "/settings/public#countries")
		return
	}
	what := "limits visitors by country again"
	if r.FormValue("action") == "open" {
		days, _ := strconv.Atoi(r.FormValue("days"))
		if !slices.Contains(openDays, days) {
			http.Error(w, "Choose how long for.", http.StatusBadRequest)
			return
		}
		c.OpenUntil = time.Now().AddDate(0, 0, days)
		what = "lets everyone in until " + c.OpenUntil.Format("2 January 2006 15:04")
	} else {
		c.OpenUntil = time.Time{}
	}
	if err := s.Manager.SetCountries(r.Context(), c); err != nil {
		s.countriesFailed(w, r, cloudMessage(err))
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "public access", "Roostack Cloud's relay "+what)
	redirect(w, r, "/settings/public?saved=countries#countries")
}

func (s *Server) countriesFailed(w http.ResponseWriter, r *http.Request, msg string) {
	redirect(w, r, "/settings/public?countries_error="+url.QueryEscape(msg)+"#countries")
}

func (s *Server) publicSharingSubmit(w http.ResponseWriter, r *http.Request) {
	share := r.FormValue("share")
	if share != "yes" && share != "no" {
		http.Error(w, "Choose whether to share.", http.StatusBadRequest)
		return
	}
	if err := s.Manager.SetSignalSharing(share == "yes"); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "public access", "CrowdSec sharing: "+share)
	redirect(w, r, "/settings/public")
}

func init() {
	templateFuncs["publicState"] = func(state string) string { return publicState[state] }
}

// publicState is a record's state for people.
var publicState = map[string]string{
	dnsrecords.PublicOK:       "Points at your home",
	dnsrecords.PublicPending:  "Set; waiting for DNS to catch up",
	dnsrecords.PublicWaiting:  "Waiting for protection to start",
	dnsrecords.PublicManual:   "Add it at your DNS provider",
	dnsrecords.PublicConflict: "Another record is in the way",
	dnsrecords.PublicError:    "Couldn't be set",
}
