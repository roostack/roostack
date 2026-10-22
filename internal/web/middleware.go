package web

import (
	"errors"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/store"
)

// What every request goes through on its way to its handler: the
// session it belongs to, the checks of who may see a page, and the
// browser's protections (SPEC.md §5, §6.10).

type userKey struct{}

// currentUser returns the logged-in user, or nil.
func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey{}).(*store.User)
	return u
}

func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			hash := authkit.HashToken(c.Value)
			// Only a session signed in here: the setup pages' and
			// Roostack's own don't work on each other (SPEC.md §6.3.2).
			u, lastSeen, remember, err := s.Store.SessionInfoAt(r.Context(), sessionOrigin(r), hash)
			switch {
			case err == nil:
				r = s.checkSession(r, hash, u, lastSeen, remember)
			case !errors.Is(err, store.ErrNotFound):
				s.serverError(w, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireLogin checks the user is logged in.
func (s *Server) requireLogin(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.needsSetup() {
			redirect(w, r, "/setup")
			return
		}
		if currentUser(r) == nil {
			if s.pagesSignIn(r) {
				s.toSignInPage(w, r)
				return
			}
			q := url.Values{}
			if sessionOf(r).Expired {
				q.Set("expired", "1")
			}
			if next := pageOf(r); next != "/" {
				q.Set("next", next)
			}
			redirect(w, r, withQuery("/login", q))
			return
		}
		h(w, r)
	})
}

// require checks the user is logged in and has a permission. Those who
// must use two-factor authentication set it up before doing anything else
// (SPEC.md §5).
func (s *Server) require(p auth.Permission, h http.HandlerFunc) http.Handler {
	return s.requireLogin(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u.TOTPSecret == "" && s.twoFactorRequired(u) {
			redirect(w, r, firstThen(s.twoFactorPath(r, u), r))
			return
		}
		if u.MustChangePassword {
			redirect(w, r, firstThen("/account", r))
			return
		}
		if ownerNeedsEmail(u) {
			redirect(w, r, firstThen("/account/notifications", r))
			return
		}
		// Owners finish the setup wizard next.
		if u.Role == auth.RoleOwner {
			if p, err := s.Setup.Progress(r.Context()); err == nil && p.Next() != "" {
				redirect(w, r, p.Next())
				return
			}
		}
		if !auth.Can(u.Role, p) {
			http.Error(w, "You don't have permission to do that.", http.StatusForbidden)
			return
		}
		h(w, r)
	})
}

// twoFactorRequired says whether someone must use two-factor
// authentication (SPEC.md §5): Owners always, and Admins while Roostack's
// own pages are public (a lab on the internet whose UI isn't kept to an
// SSH tunnel), since they can reveal secrets and change services.
func (s *Server) twoFactorRequired(u *store.User) bool {
	switch u.Role {
	case auth.RoleOwner:
		return true
	case auth.RoleAdmin:
		c := s.network()
		return c.Saved == config.NetworkInternet && !c.UITunnelOnly
	}
	return false
}

// pageOf is the page r asked for, to come back to after logging in or a
// step Roostack asks for first (a new password, two-factor): its path and
// query, or for an htmx request the page it came from. "/" for anything
// else (a form sent, another site).
func pageOf(r *http.Request) string {
	if r.Method != http.MethodGet {
		return "/"
	}
	if r.Header.Get("HX-Request") == "true" {
		u, err := url.Parse(r.Header.Get("HX-Current-URL"))
		if err != nil || u.Host != r.Host {
			return "/"
		}
		return localNext(u.RequestURI())
	}
	return localNext(r.URL.RequestURI())
}

// localNext is where to go after logging in: a page of Roostack's own
// (a path on this address, never another site's, nor logging in again or
// out), or "/".
func localNext(next string) string {
	u, err := url.Parse(next)
	if err != nil || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") ||
		strings.ContainsAny(next, "\\\r\n") || u.Host != "" || u.Path == "/login" || strings.HasPrefix(u.Path, "/login/") ||
		strings.HasPrefix(u.Path, "/logout") {
		return "/"
	}
	return next
}

// nextOf is the page to go on to from r's ?next=, or "/".
func nextOf(r *http.Request) string { return localNext(r.URL.Query().Get("next")) }

// firstThen is path, coming back to r's page afterwards (?next=).
func firstThen(path string, r *http.Request) string {
	next := pageOf(r)
	if u, _ := url.Parse(next); next == "/" || u.Path == path {
		return path
	}
	return withQuery(path, url.Values{"next": {next}})
}

// withQuery is path with q, if any.
func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// sameOrigin rejects cross-site form posts (CSRF). Browsers always send an
// Origin header on POST; requests without one are rejected.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			o, err := url.Parse(r.Header.Get("Origin"))
			if err != nil || o.Host == "" || o.Host != r.Host {
				http.Error(w, "Cross-site request rejected.", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets browser protections on every response.
//
// Alpine.js's standard build needs 'unsafe-eval'; switching to its CSP
// build would remove that (docs/ASSUMPTIONS.md K1).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-eval'; "+
			"style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// recoverPanics turns a crash in one request into a logged error and an
// error page, instead of the browser getting an empty response.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.Log.Error("request crashed", "method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				http.Error(w, "Something went wrong. Details are in the Roostack log.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requireWizard allows the setup wizard to the Owner.
func (s *Server) requireWizard(h http.HandlerFunc) http.Handler {
	return s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		h(w, r)
	})
}

// requireOwner allows host settings (SPEC.md §5) to the Owner, once they
// have 2FA.
func (s *Server) requireOwner(h http.HandlerFunc) http.Handler {
	return s.requireLogin(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if !auth.Can(u.Role, auth.ManageHost) {
			http.Error(w, "Only the Owner can change these settings.", http.StatusForbidden)
			return
		}
		if u.TOTPSecret == "" {
			redirect(w, r, firstThen(s.twoFactorPath(r, u), r))
			return
		}
		if u.MustChangePassword {
			redirect(w, r, firstThen("/account", r))
			return
		}
		if ownerNeedsEmail(u) {
			redirect(w, r, firstThen("/account/notifications", r))
			return
		}
		h(w, r)
	})
}
