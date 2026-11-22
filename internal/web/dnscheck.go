package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"roostack.cloud/roostack/internal/labdns"
)

// The check on Your account's Services tab (SPEC.md §6.5.3): whether the
// device in use looks names up with Roostack's own DNS server. A name
// under the domain can't tell, since the public wildcard answers every
// name too, so the page loads a picture from a name nobody has asked for
// before (check-<token>.<domain>), and Roostack then asks the DNS server
// whether it was asked for that name. dnscheck.js shows the answer;
// without scripts, Check this device asks on a page of its own.

// dnsCheck is the check on the page.
type dnsCheck struct {
	// Image is the picture's address, whose name the browser looks up;
	// Result is where the answer is.
	Image, Result string
	// Addr is the lab's DNS server's address, for setting a device up.
	Addr string
	// State is the answer, when this page is it: "yes" or "no".
	State string
}

var checkToken = regexp.MustCompile(`^[0-9a-f]{12}$`)

// dnsCheckFor is a new check, nil while Roostack's own DNS server isn't
// the lab's (or the lab's names are its test address, which browsers
// answer themselves).
func (s *Server) dnsCheckFor(r *http.Request) *dnsCheck {
	if s.Manager == nil || s.Domain == "" || strings.HasSuffix(s.Domain, ".localhost") || !s.Manager.LabDNSInUse(r.Context()) {
		return nil
	}
	var b [6]byte
	rand.Read(b[:])
	token := hex.EncodeToString(b[:])
	return &dnsCheck{Image: "https://" + labdns.CheckName(token, s.Domain) + "/dns-check.svg",
		Result: "/account/dns-check?t=" + token, Addr: s.Manager.Resolver(r.Context()).Addr}
}

// allowCheckImage lets the page load c's picture (the CSP's img-src).
func allowCheckImage(w http.ResponseWriter, c *dnsCheck) {
	if c == nil {
		return
	}
	host := strings.TrimSuffix(c.Image, "/dns-check.svg")
	h := w.Header()
	csp := h.Get("Content-Security-Policy")
	if strings.Contains(csp, "img-src ") {
		csp = strings.Replace(csp, "img-src ", "img-src "+host+" ", 1)
	} else {
		csp += "; img-src 'self' " + host
	}
	h.Set("Content-Security-Policy", csp)
}

// acctDNSCheck answers a check: as JSON for dnscheck.js, else as the
// Services tab with the answer.
func (s *Server) acctDNSCheck(w http.ResponseWriter, r *http.Request, a *acct) {
	token := r.URL.Query().Get("t")
	if !checkToken.MatchString(token) {
		http.Error(w, "That check isn't one Roostack made.", http.StatusBadRequest)
		return
	}
	seen, err := s.Manager.LabDNSCheck(r.Context(), token)
	if err != nil {
		s.Log.Warn("asking the lab's DNS server for a check", "err", err)
	}
	if r.URL.Query().Get("json") != "" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]any{"seen": seen, "known": err == nil})
		return
	}
	v := s.acctServicesView(r, a)
	if v.DNSCheck != nil {
		v.DNSCheck.State = "no"
		if seen {
			v.DNSCheck.State = "yes"
		}
	}
	allowCheckImage(w, v.DNSCheck)
	s.renderAcct(w, r, a, http.StatusOK, v)
}
