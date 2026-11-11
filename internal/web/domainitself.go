package web

import (
	"bytes"
	"cmp"
	"context"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/usererr"
)

// The domain itself (SPEC.md §5.3): what someone gets at the bare domain
// (smith.family), the Owner's choice on Settings → Your lab's address.
// Caddy serves it (a redirect, or the simple page as plain files Roostack
// writes here); the DNS records manager points it at the lab.

// DomainItselfChooser keeps the Owner's choice (the manager: lab.yaml,
// then an apply).
type DomainItselfChooser interface {
	DomainItself() config.DomainItself
	SetDomainItself(d config.DomainItself) error
}

// domainItselfView is the card on Settings → Your lab's address.
type domainItselfView struct {
	Domain string
	// Shows is what it shows now (config.Lab.Shows), and Choice the form's
	// fields.
	Shows  string
	Choice config.DomainItself
	// Services are the services it can go to or link to: those with an
	// address.
	Services []ServiceView
	RoostAc  bool
	Internet bool
	Error    string
	Saved    bool
	// Record is what DNS says about the domain itself: someone else's
	// record in the way, or one to add by hand ("" for nothing to say).
	Conflict, Manual string
}

// domainLab is the lab's address with the domain itself as chosen, for
// config.Lab's rules.
func (s *Server) domainLab(d config.DomainItself) config.Lab {
	return config.Lab{Domain: s.Domain, DNS: config.DNS{Provider: s.DNSProvider}, DomainItself: d}
}

// domainItselfView makes the card's view (nil without an address or the
// manager).
func (s *Server) domainItselfView(r *http.Request) *domainItselfView {
	if s.DomainItself == nil || s.Domain == "" {
		return nil
	}
	d := s.DomainItself.DomainItself()
	lab := s.domainLab(d)
	v := &domainItselfView{Domain: s.Domain, Shows: lab.Shows(), Choice: d, RoostAc: lab.RoostAc(), Internet: s.onInternet(),
		Saved: r.FormValue("saved") == "itself", Services: s.domainServices(r.Context())}
	if s.DNS != nil && lab.ServesDomain() {
		st := s.DNS.Status()
		switch {
		case st.State == dnsrecords.StateConflict && st.Record == s.Domain:
			v.Conflict = st.Current
		case st.State == dnsrecords.StateManual && st.IP != "" && !lab.DomainPublic():
			v.Manual = s.Domain + "  A  " + st.IP
		}
	}
	return v
}

// domainServices are the services with an address, by title.
func (s *Server) domainServices(ctx context.Context) []ServiceView {
	if s.Services == nil {
		return nil
	}
	all, _ := s.Services(ctx)
	var out []ServiceView
	for _, v := range all {
		if len(v.URLs) > 0 {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b ServiceView) int { return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)) })
	return out
}

// domainItselfSubmit saves the Owner's choice.
func (s *Server) domainItselfSubmit(w http.ResponseWriter, r *http.Request) {
	if s.DomainItself == nil || s.Domain == "" {
		http.NotFound(w, r)
		return
	}
	services := s.domainServices(r.Context())
	has := func(name string) bool {
		return slices.ContainsFunc(services, func(v ServiceView) bool { return v.Name == name })
	}
	d := config.DomainItself{Show: r.FormValue("show"), Public: r.FormValue("public") == "anyone"}
	// The simple page's words are kept whatever is chosen, so that they're
	// there again if the Owner goes back to it.
	d.Page.Heading = strings.Join(strings.Fields(r.FormValue("heading")), " ")
	d.Page.Text = strings.TrimSpace(strings.ReplaceAll(r.FormValue("text"), "\r\n", "\n"))
	for _, name := range r.Form["link"] {
		if has(name) && !slices.Contains(d.Page.Services, name) {
			d.Page.Services = append(d.Page.Services, name)
		}
	}
	var problem string
	switch {
	case utf8.RuneCountInString(d.Page.Heading) > config.PageHeadingMax:
		problem = "The page's heading can be at most 100 characters."
	case utf8.RuneCountInString(d.Page.Text) > config.PageTextMax:
		problem = "The page's text can be at most 2,000 characters."
	}
	switch d.Show {
	case config.ShowNothing, config.ShowAccount:
	case config.ShowService:
		d.Service = r.FormValue("service")
		if !has(d.Service) {
			problem = "Choose the service it will go to."
		}
	case config.ShowAddress:
		d.Address = strings.TrimSpace(r.FormValue("address"))
		if u, err := url.Parse(d.Address); err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" ||
			strings.ContainsAny(d.Address, " \t\n\"{}") {
			problem = "Enter a web address that starts with https://."
		} else if strings.EqualFold(u.Hostname(), s.Domain) {
			problem = "That's " + s.Domain + " itself: choose another address."
		}
	case config.ShowPage:
	default:
		problem = "Choose what " + s.Domain + " will show."
	}
	if problem == "" {
		if err := s.DomainItself.SetDomainItself(d); err != nil {
			msg, ok := usererr.Message(err)
			if !ok {
				s.serverError(w, err)
				return
			}
			problem = msg
		}
	}
	if problem != "" {
		v := s.domainItselfView(r)
		v.Choice, v.Shows, v.Error = d, cmp.Or(d.Show, v.Shows), problem
		data := s.domainAreaData(r)
		data["DomainItself"] = v
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "settings-domain", withArea(data, "domain"))
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "settings", "what "+s.Domain+" shows: "+d.Show)
	if d.Show == config.ShowPage {
		s.WriteDomainPage(r.Context())
	}
	if s.DNS != nil {
		s.DNS.Refresh()
	}
	redirect(w, r, "/settings/domain?saved=itself#domain-itself")
}

var domainPageTemplate = sync.OnceValues(func() (*template.Template, error) {
	return template.New("domain-page.html").Funcs(templateFuncs()).ParseFS(templateFS, "templates/domain-page.html")
})

// domainLink is a link on the simple page.
type domainLink struct{ Title, URL string }

// WriteDomainPage writes the simple page while it's chosen, as plain
// files with the status page's stylesheets and the lab's logo.
func (s *Server) WriteDomainPage(ctx context.Context) {
	if s.DomainPageDir == "" || s.DomainItself == nil {
		return
	}
	d := s.DomainItself.DomainItself()
	if s.domainLab(d).Shows() != config.ShowPage {
		return
	}
	if err := s.writeDomainPage(ctx, d.Page); err != nil {
		s.Log.Error("writing the page for "+s.Domain, "err", err)
	}
}

func (s *Server) writeDomainPage(ctx context.Context, p config.SimplePage) error {
	var links []domainLink
	services := s.domainServices(ctx)
	for _, name := range p.Services {
		if i := slices.IndexFunc(services, func(v ServiceView) bool { return v.Name == name }); i >= 0 {
			links = append(links, domainLink{Title: services[i].Title, URL: services[i].URLs[0]})
		}
	}
	look := s.signInLook(ctx)
	data := map[string]any{"LabName": s.labName(), "Heading": p.Heading, "Paragraphs": paragraphs(p.Text), "Links": links,
		"Look": look, "Theme": statusTheme(look.Palette)}
	for _, part := range []string{"mark", "wide", "dark"} {
		if look.Has(part) {
			data["Logo"+part] = statusLogo(part, look.Files[part])
		}
	}
	t, err := domainPageTemplate()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return err
	}
	return writePageFiles(s.DomainPageDir, buf.Bytes(), look, s.lookPath)
}

// paragraphs splits text at blank lines.
func paragraphs(text string) []string {
	var out []string
	for _, p := range strings.Split(text, "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
