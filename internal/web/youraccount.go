package web

import (
	"cmp"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/sso"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/words"
)

// Your account (SPEC.md §5.3): each person's own pages, in the lab's look
// and without scripts. The same handlers serve them twice: at my.<domain>,
// on the sign-in page's listener with its session, and at /account on
// Roostack's own address with Roostack's session, for a lab without an
// address and for the steps Roostack asks for before anything else (a
// temporary password, two-factor, the Owner's email address), which can
// come before there's a sign-in page. Members are sent here from
// Roostack's pages.

// acct is who's on Your account's pages, and where.
type acct struct {
	User *store.User
	// Hash is this browser's session (its token hash): the sign-in at
	// my.<domain>, or Roostack's own session.
	Hash string
	// SignIn: at my.<domain>; otherwise at Roostack's own address.
	SignIn   bool
	Remember bool
	// Expires is when the sign-in ends (at my.<domain>).
	Expires time.Time
}

type acctHandler func(w http.ResponseWriter, r *http.Request, a *acct)

// acctPagesServices is the Services tab at Roostack's own address, whose
// home page is Roostack's; at my.<domain> it's the home page.
const acctPagesServices = "/account/services"

// home is the Services tab's path where a is.
func (a *acct) home() string {
	if a.SignIn {
		return "/"
	}
	return acctPagesServices
}

// acctRoutes adds Your account's pages to mux, with who's signed in from
// with.
func (s *Server) acctRoutes(mux *http.ServeMux, with func(acctHandler) http.Handler) {
	mux.Handle("GET /account", with(s.acctAccount))
	mux.Handle("POST /account", with(s.acctDetailsSubmit))
	mux.Handle("POST /account/mode", with(s.acctModeSubmit))
	mux.Handle("GET /account/security", with(s.acctSecurity))
	mux.Handle("POST /account/password", with(s.acctPasswordSubmit))
	mux.Handle("GET /account/two-factor", with(s.acctTwoFactor))
	mux.Handle("POST /account/two-factor", with(s.acctTwoFactorSubmit))
	mux.Handle("POST /account/sign-out", with(s.acctSignOutSubmit))
	mux.Handle("POST /account/sign-out-others", with(s.acctSignOutOthersSubmit))
	mux.Handle("GET /account/notifications", with(s.acctNotifications))
	mux.Handle("POST /account/notifications", with(s.acctNotificationsSubmit))
	mux.Handle("GET /account/devices", with(s.acctDevicesPage))
	mux.Handle("GET /account/devices/add", with(s.acctDeviceAdd))
	mux.Handle("POST /account/devices", with(s.acctDeviceSubmit))
	mux.Handle("GET /account/devices/{id}", with(s.acctDeviceChange))
	mux.Handle("POST /account/devices/{id}", with(s.acctDeviceChangeSubmit))
	mux.Handle("POST /account/devices/{id}/remove", with(s.acctDeviceRemove))
}

// signInAcct is Your account at my.<domain>: someone not signed in there
// signs in first, and comes back to the page they asked for.
func (s *Server) signInAcct(h acctHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.signInSession(r)
		if !ok {
			next := "/"
			if r.Method == http.MethodGet {
				next = r.URL.RequestURI()
			}
			http.Redirect(w, r, "/sign-in?"+url.Values{"next": {next}}.Encode(), http.StatusSeeOther)
			return
		}
		h(w, r, &acct{User: sess.User, Hash: sess.Hash, SignIn: true, Remember: sess.Remember, Expires: sess.Expires})
	})
}

// pagesAcct is Your account at Roostack's own address. Once Caddy serves
// my.<domain>, a page opened here goes there, unless Roostack is asking
// for a step first (acctFirst), which Roostack's own session does here.
func (s *Server) pagesAcct(h acctHandler) http.Handler {
	return s.requireLogin(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if r.Method == http.MethodGet && !s.acctFirst(u) && s.pagesSignIn(r) {
			to := r.URL.RequestURI()
			if r.URL.Path == acctPagesServices {
				to = "/"
			}
			http.Redirect(w, r, "https://"+s.SignInHost+to, http.StatusSeeOther)
			return
		}
		a := &acct{User: u, Remember: sessionOf(r).Remember}
		if c, err := r.Cookie(sessionCookie); err == nil {
			a.Hash = authkit.HashToken(c.Value)
		}
		h(w, r, a)
	})
}

// acctHome is Your account's Services tab, for someone on Roostack's own
// pages: at my.<domain> once Caddy serves it, here until then.
func (s *Server) acctHome(r *http.Request) string {
	if s.pagesSignIn(r) {
		return "https://" + s.SignInHost + "/"
	}
	return acctPagesServices
}

// acctFirst reports whether Roostack asks u for a step before anything
// else (middleware.go's require): a new password, two-factor, or the
// Owner's email address.
func (s *Server) acctFirst(u *store.User) bool {
	return u.MustChangePassword || ownerNeedsEmail(u) || (u.TOTPSecret == "" && s.twoFactorRequired(u))
}

// acctPagesRoutes adds Your account to Roostack's own address, with its
// Services tab and the lab's logo for its header.
func (s *Server) acctPagesRoutes(mux *http.ServeMux) {
	s.acctRoutes(mux, s.pagesAcct)
	mux.Handle("GET "+acctPagesServices, s.pagesAcct(s.acctServices))
	mux.HandleFunc("GET /brand/app/{name}", s.lookApp)
}

// parseAcctPages are Your account's templates: the lab's look, no
// sidebar, no scripts.
func parseAcctPages() (*template.Template, error) {
	return template.New("youraccount.html").Funcs(templateFuncs()).ParseFS(templateFS, "templates/youraccount.html", "templates/components.html")
}

// acctView is what Your account's pages show.
type acctView struct {
	// Tab is the page's tab, and Title its heading.
	Tab, Title string
	Tabs       []acctTab
	LabName    string
	Look       signInLook
	// LookBase is where the lab's logo is: /look at my.<domain>, /brand at
	// Roostack's own address; Favicon its browser tab's icon.
	LookBase, Favicon string
	User              *store.User
	// SignOut is where the header's Sign out posts.
	SignOut string
	// Owner names who to ask for what only the Owner changes.
	Owner string
	// Error and Done say how a form went; Action is where a form posts,
	// keeping the page to go on to after a step Roostack asked for.
	Error, Done, Action string

	// Services.
	Apps []signInApp
	// RunLab is Roostack's pages, for Owners and Admins; Photos the photo
	// migration's page, when it applies (SPEC.md §8.5).
	RunLab, Photos string
	Until          time.Time
	SignedInHref   string

	// Account.
	Name, Email string
	Groups      []string
	NeedEmail   bool
	Mode        string

	// Security.
	Sessions []acctSession
	// TwoFactorRequired: Roostack asks u for two-factor (Owners, and Admins
	// while Roostack's pages are public).
	TwoFactorRequired bool
	// Temporary: they're using a temporary password.
	Temporary bool

	// Two-factor: a new key being set up; Change: replacing one that's on.
	TwoFactor *acctTwoFactor

	// VPN devices (vpn.go).
	Devices *acctDevices

	// Notifications.
	NotifyGroups []attention.NotifyGroup
	Chosen       map[string]bool
	MailReady    bool
	CanSetMail   bool
}

type acctTab struct{ ID, Title, Href string }

type acctTwoFactor struct {
	Secret, SecretFmt string
	URL               template.URL
	QR                template.HTML
	Change            bool
}

// acctSession is a row of "Where you're signed in": a sign-in, with the
// sessions of Roostack's own pages made from it, or a session of Roostack's
// pages signed in on its own login page (PagesOnly).
type acctSession struct {
	Hash, Device       string
	SignedIn, LastUsed time.Time
	This, PagesOnly    bool
}

// renderAcct shows one of Your account's pages.
func (s *Server) renderAcct(w http.ResponseWriter, r *http.Request, a *acct, status int, v acctView) {
	v.User = a.User
	v.LabName = s.labName()
	v.Look = s.signInLook(r.Context())
	v.Owner = s.ownerName(r.Context())
	v.Mode = a.User.Theme
	v.LookBase, v.Favicon, v.SignOut = "/look", "/favicon.ico", sso.PathSignOut
	if !a.SignIn {
		v.LookBase, v.Favicon, v.SignOut = "/brand", "/brand/icon", "/logout"
	}
	v.Tabs = []acctTab{{"services", "Services", a.home()}, {"account", "Account", "/account"},
		{"security", "Security", "/account/security"}, {"notifications", "Notifications", "/account/notifications"}}
	if s.vpnOn() {
		// Once the Owner has turned the lab's VPN on (SPEC.md §10.11).
		v.Tabs = append(v.Tabs, acctTab{"devices", "VPN devices", "/account/devices"})
	}
	var buf strings.Builder
	if err := s.acctPages.Execute(&buf, v); err != nil {
		s.Log.Error("render Your account", "err", err)
		http.Error(w, "Something went wrong showing this page. Details are in the Roostack log.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write([]byte(buf.String()))
}

// acctServices is the Services tab: the services someone can use, Roostack's
// pages for Owners and Admins, and the photo migration when it applies.
func (s *Server) acctServices(w http.ResponseWriter, r *http.Request, a *acct) {
	v := acctView{Tab: "services", Title: "Your services", Apps: s.usableApps(r, a), SignedInHref: "/account/security#signed-in"}
	if a.SignIn {
		v.Until = a.Expires
	}
	if auth.Can(a.User.Role, auth.ManageServices) {
		v.RunLab = "/"
		if a.SignIn {
			v.RunLab = "https://" + s.UIHost + "/"
		}
	}
	if len(s.migratable(r.Context(), a.User)) > 0 {
		v.Photos = "/immich-photos"
		if a.SignIn {
			v.Photos = "https://" + s.UIHost + "/immich-photos"
		}
	}
	s.renderAcct(w, r, a, http.StatusOK, v)
}

// usableApps are the services a's person can open: those with single
// sign-on that they may use, each opening where the app starts signing in
// through the lab (so a click signs them in), and those that keep their
// own sign-in, which anyone at home can reach.
func (s *Server) usableApps(r *http.Request, a *acct) []signInApp {
	services, err := s.Services(r.Context())
	if err != nil {
		s.Log.Error("listing services for Your account", "err", err)
		return nil
	}
	clients := map[string]sso.Client{}
	if s.SSO != nil {
		for _, c := range s.SSO.SSOClients() {
			clients[c.ID] = c
		}
	}
	s.withIcons(services, false)
	logos := "/look/app/"
	if !a.SignIn {
		logos = "/brand/app/"
	}
	var apps []signInApp
	for _, sv := range services {
		if len(sv.URLs) == 0 || sv.NoAddress || sv.Install != nil {
			continue
		}
		app := signInApp{Title: sv.Title, URL: sv.URLs[0], Host: strings.TrimPrefix(sv.URLs[0], "https://"), DrawnIcon: sv.DrawnIcon,
			Off: sv.Status == StatusOff}
		if c, ok := clients[sv.Name]; ok {
			if ok, err := s.Store.CanUse(r.Context(), c.ID, a.User.ID); err != nil || !ok {
				continue
			}
			app.URL, app.Logo = cmp.Or(c.StartURL, app.URL), logos+c.ID
		} else {
			app.OwnSignIn = true
		}
		apps = append(apps, app)
		delete(clients, sv.Name)
	}
	// An app with single sign-on that isn't listed (on another server's
	// list not yet in): at the address its sign-in comes back to.
	for _, c := range clients {
		if c.AdminsOnly {
			continue // Roostack's own, not a service
		}
		host := c.Host
		if host == "" && len(c.RedirectURIs) > 0 {
			if u, err := url.Parse(c.RedirectURIs[0]); err == nil {
				host = u.Host
			}
		}
		if ok, err := s.Store.CanUse(r.Context(), c.ID, a.User.ID); host == "" || err != nil || !ok {
			continue
		}
		apps = append(apps, signInApp{Title: c.Title, URL: cmp.Or(c.StartURL, "https://"+host+"/"), Host: host, Logo: logos + c.ID})
	}
	slices.SortStableFunc(apps, func(x, y signInApp) int { return strings.Compare(strings.ToLower(x.Title), strings.ToLower(y.Title)) })
	return apps
}

// acctAccount is the Account tab: name and email address, which people
// change themselves; username, role, and groups, which the Owner changes;
// light or dark.
func (s *Server) acctAccount(w http.ResponseWriter, r *http.Request, a *acct) {
	v := s.acctAccountView(r, a)
	if r.URL.Query().Get("saved") != "" {
		v.Done = "Saved."
	}
	s.renderAcct(w, r, a, http.StatusOK, v)
}

func (s *Server) acctAccountView(r *http.Request, a *acct) acctView {
	v := acctView{Tab: "account", Title: "Your account", Name: a.User.Name, Email: a.User.Email,
		NeedEmail: ownerNeedsEmail(a.User), Action: withQuery("/account", nextQuery(r))}
	if groups, err := s.Store.Groups(r.Context()); err == nil {
		for _, g := range groups {
			if slices.Contains(g.Members, a.User.ID) {
				v.Groups = append(v.Groups, g.Name)
			}
		}
	}
	return v
}

// maxNameLength is the longest name someone may give, in characters.
const maxNameLength = 100

// acctDetailsSubmit saves someone's name and email address.
func (s *Server) acctDetailsSubmit(w http.ResponseWriter, r *http.Request, a *acct) {
	u := a.User
	name := strings.Join(strings.Fields(r.FormValue("name")), " ")
	email := strings.TrimSpace(r.FormValue("email"))
	msg := ""
	switch {
	case utf8.RuneCountInString(name) > maxNameLength:
		msg = "Choose a name of up to 100 characters."
	case email == "" && u.Role == auth.RoleOwner:
		msg = "As the Owner, you need an email address so that Roostack can tell you when the lab needs you."
	case email != "":
		msg = emailProblem(email)
	}
	if msg != "" {
		v := s.acctAccountView(r, a)
		v.Name, v.Email, v.Error = name, email, msg
		s.renderAcct(w, r, a, http.StatusBadRequest, v)
		return
	}
	if name != u.Name {
		if err := s.Store.SetName(r.Context(), u.ID, name); err != nil {
			s.serverError(w, err)
			return
		}
		s.Store.Audit(r.Context(), u, "account", "name changed to "+cmp.Or(name, "none"))
	}
	if email != u.Email {
		if err := s.Store.SetEmail(r.Context(), u.ID, email); err != nil {
			s.serverError(w, err)
			return
		}
		// Audited: a changed address also redirects later security emails.
		s.Store.Audit(r.Context(), u, "account", "notification email changed to "+cmp.Or(email, "none"))
	}
	if ownerNeedsEmail(u) { // asked for it before anything else: carry on
		redirect(w, r, nextOf(r))
		return
	}
	redirect(w, r, "/account?saved=details")
}

// acctModeSubmit saves light or dark, for these pages and Roostack's.
func (s *Server) acctModeSubmit(w http.ResponseWriter, r *http.Request, a *acct) {
	mode := r.FormValue("mode")
	if !slices.Contains(store.Themes, mode) {
		http.Error(w, "Choose light, dark, or like this device.", http.StatusBadRequest)
		return
	}
	if err := s.Store.SetTheme(r.Context(), a.User.ID, mode); err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/account?saved=mode#appearance")
}

// acctSecurity is the Security tab: password, two-factor, and where
// someone's signed in.
func (s *Server) acctSecurity(w http.ResponseWriter, r *http.Request, a *acct) {
	v, err := s.acctSecurityView(r, a)
	if err != nil {
		s.serverError(w, err)
		return
	}
	switch r.URL.Query().Get("done") {
	case "two-factor":
		v.Done = "Two-factor authentication is on."
	case "two-factor-changed":
		v.Done = "Your new authenticator app is the one Roostack will ask for from now on."
	case "signed-out":
		v.Done = "Signed out on that device."
	case "signed-out-others":
		v.Done = "Signed out everywhere else."
	}
	s.renderAcct(w, r, a, http.StatusOK, v)
}

func (s *Server) acctSecurityView(r *http.Request, a *acct) (acctView, error) {
	v := acctView{Tab: "security", Title: "Security", TwoFactorRequired: s.twoFactorRequired(a.User),
		Temporary: a.User.MustChangePassword, Action: withQuery("/account/password", nextQuery(r))}
	var err error
	v.Sessions, err = s.acctSessions(r, a)
	return v, err
}

// acctSessions are the rows of "Where you're signed in": each sign-in,
// last used when it or a session of Roostack's pages made from it was,
// and each session signed in on Roostack's own login page.
func (s *Server) acctSessions(r *http.Request, a *acct) ([]acctSession, error) {
	list, err := s.Store.UserSessions(r.Context(), a.User.ID)
	if err != nil {
		return nil, err
	}
	this := acctThis(list, a.Hash)
	byHash := map[string]int{}
	var rows []acctSession
	for _, x := range list {
		if x.SignIn != "" {
			continue
		}
		byHash[x.Hash] = len(rows)
		rows = append(rows, acctSession{Hash: x.Hash, Device: x.Device, SignedIn: x.SignedInAt, LastUsed: x.LastSeen,
			This: x.Hash == this, PagesOnly: x.Origin == ""})
	}
	for _, x := range list {
		if i, ok := byHash[x.SignIn]; ok && x.LastSeen.After(rows[i].LastUsed) {
			rows[i].LastUsed = x.LastSeen
		}
	}
	return rows, nil
}

// acctThis is the row of "Where you're signed in" that's this browser: its
// session, or the sign-in it came from.
func acctThis(list []store.Session, hash string) string {
	for _, x := range list {
		if x.Hash == hash && x.SignIn != "" {
			return x.SignIn
		}
	}
	return hash
}

// acctPasswordSubmit changes someone's own password. Every session of theirs
// ends, so other devices sign in again; this one gets a new session.
func (s *Server) acctPasswordSubmit(w http.ResponseWriter, r *http.Request, a *acct) {
	u := a.User
	fail := func(msg string) {
		v, err := s.acctSecurityView(r, a)
		if err != nil {
			s.serverError(w, err)
			return
		}
		v.Error = msg
		s.renderAcct(w, r, a, http.StatusBadRequest, v)
	}
	ok, err := authkit.VerifyPassword(u.PasswordHash, r.FormValue("current"))
	if errors.Is(err, authkit.ErrBusy) {
		fail(busyMessage)
		return
	}
	if err != nil || !ok {
		time.Sleep(failedLoginDelay)
		fail("Your current password is wrong.")
		return
	}
	pw := r.FormValue("password")
	if pw != r.FormValue("confirm") {
		fail("The new passwords don't match.")
		return
	}
	if err := authkit.CheckPasswordPolicy(pw); err != nil {
		fail(words.Capitalise(err.Error()) + ".")
		return
	}
	hash, err := authkit.HashPassword(pw)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.Store.SetPassword(r.Context(), u.Username, hash, false); err != nil {
		s.serverError(w, err)
		return
	}
	if a.SignIn {
		err = s.issueSignIn(w, r, u, a.Remember)
	} else {
		err = s.startSession(w, r, u, a.Remember)
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "password changed", "")
	if u.MustChangePassword { // done with the temporary password: carry on
		redirect(w, r, nextOf(r))
		return
	}
	v, err := s.acctSecurityView(r, a)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v.Done = "Password changed. Other devices you were signed in on will need to sign in again."
	s.renderAcct(w, r, a, http.StatusOK, v)
}

// acctTwoFactor turns on two-factor, or with it on, moves it to a new
// authenticator app. The Owner turns it on in the setup wizard while that
// lasts (twoFactorInWizard).
func (s *Server) acctTwoFactor(w http.ResponseWriter, r *http.Request, a *acct) {
	if !a.SignIn && a.User.TOTPSecret == "" && s.twoFactorInWizard(r, a.User) {
		redirect(w, r, firstThen(wizardTwoFactor, r))
		return
	}
	secret, err := authkit.NewTOTPSecret()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderAcctTwoFactor(w, r, a, http.StatusOK, secret, "")
}

func (s *Server) renderAcctTwoFactor(w http.ResponseWriter, r *http.Request, a *acct, status int, secret, msg string) {
	// Group the key in fours so it's easier to type into an app.
	var groups []string
	for i := 0; i < len(secret); i += 4 {
		groups = append(groups, secret[i:min(i+4, len(secret))])
	}
	link := authkit.TOTPURL(cmp.Or(s.labName(), "Roostack"), a.User.Username, secret)
	code, err := qrSVG(link)
	if err != nil {
		s.Log.Error("drawing the 2FA QR code", "err", err) // the typed key still works
	}
	v := acctView{Tab: "security", Title: "Two-factor authentication", TwoFactorRequired: s.twoFactorRequired(a.User), Error: msg,
		Action: withQuery("/account/two-factor", nextQuery(r)),
		TwoFactor: &acctTwoFactor{Secret: secret, SecretFmt: strings.Join(groups, " "), URL: template.URL(link), QR: code,
			Change: a.User.TOTPSecret != ""}}
	if v.TwoFactor.Change {
		v.Title = "Use a new authenticator app"
	}
	w.Header().Set("Cache-Control", "no-store") // the key being set up
	s.renderAcct(w, r, a, status, v)
}

// acctTwoFactorSubmit checks a code from the new key and saves it. Moving
// two-factor to a new app asks for the password too, so that a browser left
// signed in can't take it over.
func (s *Server) acctTwoFactorSubmit(w http.ResponseWriter, r *http.Request, a *acct) {
	u := a.User
	// The new secret round-trips through the form. Tampering with it only
	// affects the user's own account, and the code must match it.
	secret := strings.TrimSpace(r.FormValue("secret"))
	change := u.TOTPSecret != ""
	if change {
		if ok, err := authkit.VerifyPassword(u.PasswordHash, r.FormValue("current")); errors.Is(err, authkit.ErrBusy) {
			s.renderAcctTwoFactor(w, r, a, http.StatusServiceUnavailable, secret, busyMessage)
			return
		} else if err != nil || !ok {
			time.Sleep(failedLoginDelay)
			s.renderAcctTwoFactor(w, r, a, http.StatusBadRequest, secret, "Your password is wrong.")
			return
		}
	}
	step, ok := authkit.VerifyTOTP(secret, r.FormValue("code"), time.Now(), 0)
	if !ok {
		s.renderAcctTwoFactor(w, r, a, http.StatusBadRequest, secret,
			"That code didn't work. Make sure you added the key above, then enter the current code.")
		return
	}
	if err := s.Store.SetTOTP(r.Context(), u.ID, secret, step); err != nil {
		s.serverError(w, err)
		return
	}
	if change {
		s.Store.Audit(r.Context(), u, attention.AuditTwoFactorChanged, "")
		redirect(w, r, "/account/security?done=two-factor-changed")
		return
	}
	s.Store.Audit(r.Context(), u, "2fa enabled", "")
	if next := nextOf(r); next != "/" {
		redirect(w, r, next)
		return
	}
	redirect(w, r, "/account/security?done=two-factor")
}

// acctSignOutSubmit signs out one of someone's other sessions, and the
// sessions of Roostack's pages made from it.
func (s *Server) acctSignOutSubmit(w http.ResponseWriter, r *http.Request, a *acct) {
	list, err := s.Store.UserSessions(r.Context(), a.User.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	hash, this := r.FormValue("session"), acctThis(list, a.Hash)
	i := slices.IndexFunc(list, func(x store.Session) bool { return x.Hash == hash && x.SignIn == "" })
	if i < 0 || hash == this {
		// Gone already, or this browser, which signs out with the header's
		// button.
		redirect(w, r, "/account/security#signed-in")
		return
	}
	if err := s.Store.DeleteUserSession(r.Context(), a.User.ID, hash); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), a.User, "signed out", "a device from Your account: "+cmp.Or(list[i].Device, "an unknown browser"))
	redirect(w, r, "/account/security?done=signed-out#signed-in")
}

// acctSignOutOthersSubmit signs out every session of someone's but this
// browser's.
func (s *Server) acctSignOutOthersSubmit(w http.ResponseWriter, r *http.Request, a *acct) {
	list, err := s.Store.UserSessions(r.Context(), a.User.ID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	this, n := acctThis(list, a.Hash), 0
	for _, x := range list {
		if x.Hash == this || x.SignIn == this || x.Hash == a.Hash {
			continue
		}
		if err := s.Store.DeleteUserSession(r.Context(), a.User.ID, x.Hash); err != nil {
			s.serverError(w, err)
			return
		}
		n++
	}
	if n > 0 {
		s.Store.Audit(r.Context(), a.User, "signed out", "everywhere else, from Your account")
	}
	redirect(w, r, "/account/security?done=signed-out-others#signed-in")
}

// acctNotifications is the Notifications tab: which emails someone gets,
// from the groups that apply to their role (SPEC.md §6.13).
func (s *Server) acctNotifications(w http.ResponseWriter, r *http.Request, a *acct) {
	v := s.acctNotificationsView(a)
	if r.URL.Query().Get("saved") != "" {
		v.Done = "Saved."
	}
	s.renderAcct(w, r, a, http.StatusOK, v)
}

func (s *Server) acctNotificationsView(a *acct) acctView {
	ready := false
	if s.Mail != nil {
		m, _ := s.Mail.Settings()
		ready = m.Configured()
	}
	return acctView{Tab: "notifications", Title: "Notifications", Email: a.User.Email, NotifyGroups: attention.GroupsFor(a.User.Role),
		Chosen: attention.UserGroups(a.User), MailReady: ready, CanSetMail: auth.Can(a.User.Role, auth.ManageHost)}
}

// acctNotificationsSubmit saves which groups of notifications someone gets.
func (s *Server) acctNotificationsSubmit(w http.ResponseWriter, r *http.Request, a *acct) {
	r.ParseForm()
	var chosen []string
	for _, g := range attention.GroupsFor(a.User.Role) {
		if slices.Contains(r.Form["notify"], g.ID) {
			chosen = append(chosen, g.ID)
		}
	}
	if err := s.Store.SetNotify(r.Context(), a.User.ID, cmp.Or(strings.Join(chosen, ","), "none")); err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/account/notifications?saved=1")
}
