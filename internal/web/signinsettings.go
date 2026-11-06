package web

import (
	"net/http"
)

// Settings → Single sign-on (SPEC.md §5.2, "Single sign-on's own page";
// docs/mockups/lab-notes): the whole picture in one place, which was
// spread over People and each service's page until 2026-11-06. Which
// services use it and who can use each, how many people can sign in,
// whether the sign-in page is working, and its two choices. People keeps
// a card of it (ssoSummary).

func (s *Server) signInRoutes(mux *http.ServeMux) {
	mux.Handle("GET /settings/sign-in", s.requireOwner(s.signInSettingsPage))
	mux.Handle("POST /settings/sign-in", s.requireOwner(s.signInSettingsSubmit))
}

// ssoServiceRow is a service whose package takes single sign-on.
type ssoServiceRow struct {
	Name, Title, App, Logo string
	// On, Progress (ssoCard.Progress) and Who (ssoCard.Summary).
	On       bool
	Progress string
	Who      string
}

// ssoSummary is single sign-on at a glance, for its page and People.
type ssoSummary struct {
	Services []ssoServiceRow
	// On counts the services using it; Others are the services that
	// keep their own accounts.
	On     int
	Others []string
	// People can sign in; NoEmail of them have no email address, which
	// the apps need to find their account.
	People, NoEmail int
	// Host is the sign-in page's address ("" without a lab address), and
	// Served whether Caddy serves it.
	Host   string
	Served bool
	// TwoFactorEveryone: everyone needs two-factor there; Remember: it
	// keeps people signed in for 30 days unless they untick it.
	TwoFactorEveryone, Remember bool
}

// OnTitles lists the services using it, in words.
func (v ssoSummary) OnTitles() []string {
	var out []string
	for _, r := range v.Services {
		if r.On {
			out = append(out, r.Title)
		}
	}
	return out
}

// ssoSummaryFor works out the summary, or nil without a sign-in page.
func (s *Server) ssoSummaryFor(r *http.Request) *ssoSummary {
	if s.SignIn == nil || s.SSO == nil {
		return nil
	}
	ctx := r.Context()
	v := &ssoSummary{Host: s.signInAddress(), Remember: s.signInRememberDefault(ctx)}
	if tf, _ := s.Store.Setting(ctx, settingSignInTwoFactor); tf == "everyone" {
		v.TwoFactorEveryone = true
	}
	v.Served = v.Host != "" && len(s.SSO.SSOClients()) > 0 && s.signInServed(ctx)
	if users, err := s.Store.ListUsers(ctx); err == nil {
		v.People = len(users)
		for _, u := range users {
			if u.Email == "" {
				v.NoEmail++
			}
		}
	}
	services, err := s.servicesFor(r)
	if err != nil {
		return v
	}
	for _, sv := range services {
		p, err := s.Manager.ServicePackage(sv.Name)
		if err != nil {
			continue
		}
		c := s.ssoCardFor(r, sv.Name, p)
		if c == nil {
			v.Others = append(v.Others, sv.Title)
			continue
		}
		row := ssoServiceRow{Name: sv.Name, Title: sv.Title, App: p.Title, Logo: sv.IconURL, On: c.On, Progress: c.Progress(), Who: c.Summary()}
		if row.App == row.Title {
			row.App = ""
		}
		if row.On {
			v.On++
		}
		v.Services = append(v.Services, row)
	}
	return v
}

func (s *Server) signInSettingsPage(w http.ResponseWriter, r *http.Request) {
	sum := s.ssoSummaryFor(r)
	if sum == nil {
		http.NotFound(w, r)
		return
	}
	data := s.pageData(r)
	data["SSO"] = sum
	data["Saved"] = r.URL.Query().Get("saved") == "1"
	s.render(w, "settings-sign-in", withArea(data, "sign-in"))
}

// signInSettingsSubmit saves the sign-in page's two choices.
func (s *Server) signInSettingsSubmit(w http.ResponseWriter, r *http.Request) {
	if s.SignIn == nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()
	r.Form.Set("sso", "1")
	what := s.saveSignInChoices(r)
	s.Store.Audit(r.Context(), currentUser(r), "settings", "single sign-on"+what)
	redirect(w, r, "/settings/sign-in?saved=1#signing-in")
}

// signInAddress is the sign-in page's address, or "" while the lab has
// none (SignInHost is then "login." alone).
func (s *Server) signInAddress() string {
	if s.Domain == "" {
		return ""
	}
	return s.SignInHost
}
