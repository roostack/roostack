package web

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/labnet"
)

// The address another server (joining the lab) or another lab (storage
// for others) reaches this one at over WireGuard (SPEC.md §10.2, §10.10).
// It's written into the join code or invite, and the right one depends on
// where the other side is, so it's chosen each time from what Roostack
// can tell (this server's home-network addresses, a name from the domain,
// the public address), suggesting the last one used; "another address"
// covers a VPN, a router forwarding another port, another dynamic DNS
// name or a test setup.

// LinkWantedKey remembers that link.<domain> was chosen: from then on
// its DNS record is kept up to date (dnsrecords/link.go).
const LinkWantedKey = "dns.link"

// addressOption is one address to choose.
type addressOption struct {
	Value, Title, Hint string
	// Link: it's link.<domain>; its record's state shows under it.
	Link bool
}

// addressChoice is the address-choice template's data.
type addressChoice struct {
	Legend   string
	Options  []addressOption
	Selected string // an option's Value, or "other"
	Other    string // the typed address, when Selected is "other"
	Port     int
	Link     *dnsrecords.PublicRecord
	// Dial offers the opposite way round, for a join code: this server
	// can't be reached, so it dials the new server at DialAddr (SPEC.md
	// §10.2). Selected is then "dial".
	Dial     bool
	DialAddr string
}

// addressChoices lists the addresses to offer on port, suggesting saved
// (the last one used). lan: offer this server's home-network addresses
// (a new server at home; not for another lab). here: the server they
// reach is this one (else only names and addresses for the whole home).
func (s *Server) addressChoices(r *http.Request, legend string, port int, lan, here bool, saved string) addressChoice {
	c := addressChoice{Legend: legend, Port: port}
	p := strconv.Itoa(port)
	internet := s.onInternet()
	if lan && here {
		for _, a := range labnet.HomeAddresses() {
			title, hint := "This server's home-network address", "For a server at home, on the same network ("+a.Iface+")."
			if internet {
				title, hint = "This server's private network address", "For a server on the same private network at your provider ("+a.Iface+")."
			}
			c.Options = append(c.Options, addressOption{Value: net.JoinHostPort(a.IP, p), Title: title, Hint: hint})
		}
	}
	var st dnsrecords.Status
	if s.DNS != nil {
		st = s.DNS.Status()
	}
	domain := s.Domain != "" && s.Setup != nil && s.domainDone(r)
	if domain && (here || !internet) {
		hint := "For a server or lab anywhere. Roostack keeps this name pointing at your home's public address, which can change; " +
			"the port has to be forwarded on your router."
		if internet {
			hint = "For a server or lab anywhere: your domain already points at this server."
		}
		c.Options = append(c.Options, addressOption{Value: net.JoinHostPort(caddy.LinkSubdomain+"."+s.Domain, p),
			Title: "A name from your domain", Hint: hint, Link: true})
		c.Link = st.Link
	}
	public := st.PublicIP
	if internet {
		public = st.IP
	}
	if ip := net.ParseIP(public); ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && (here || !internet) {
		hint := "Your home's public address now. It can change, and the port has to be forwarded on your router."
		if internet {
			hint = "This server's public address."
		}
		c.Options = append(c.Options, addressOption{Value: net.JoinHostPort(public, p), Title: "The public address", Hint: hint})
	}
	// The suggestion: the last one used, else home-network for a server at
	// home, else the name, else the public address.
	switch {
	case saved != "" && c.has(saved):
		c.Selected = saved
	case saved != "":
		c.Selected, c.Other = "other", saved
	case len(c.Options) == 0:
		c.Selected = "other"
	default:
		c.Selected = c.Options[0].Value
		for _, o := range c.Options {
			if o.Link && (internet || !lan) {
				c.Selected = o.Value
			}
		}
	}
	return c
}

func (c addressChoice) has(v string) bool {
	for _, o := range c.Options {
		if o.Value == v {
			return true
		}
	}
	return false
}

// addressFromForm is the chosen address: an option, or the typed one.
func addressFromForm(r *http.Request) string {
	if v := r.FormValue("address"); v != "other" {
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(r.FormValue("address_other"))
}

// useAddress remembers that link.<domain> is used, the first time it's
// chosen, so Roostack starts keeping its DNS record up to date.
func (s *Server) useAddress(ctx context.Context, endpoint string) {
	host, _, _ := net.SplitHostPort(endpoint)
	if s.Domain == "" || host != caddy.LinkSubdomain+"."+s.Domain {
		return
	}
	if v, _ := s.Store.Setting(ctx, LinkWantedKey); v == "on" {
		return
	}
	s.Store.SetSetting(ctx, LinkWantedKey, "on")
	if s.DNS != nil {
		s.DNS.Refresh()
	}
}
