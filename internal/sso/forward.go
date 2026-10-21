package sso

import (
	"net/http"
	"net/url"
	"strings"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/store"
)

// Forward auth (SPEC.md §5.2, "Walled apps"): Caddy asks Check about each
// request to a walled service. Someone without the service's cookie is
// sent to the sign-in host (PathStart), which, once they're signed in and
// allowed, hands them back to the service's address (PathCallback) with a
// one-time code; that sets a cookie on the service's address only. Caddy
// removes the cookie before the request reaches the service, so a broken
// or malicious app never sees it, and one service's cookie is no use at
// another. Considered: one cookie for the lab's whole domain, as most
// walls do (every app would receive it, and a compromised one could act
// as that person in every other).

// Headers a walled service is told who someone is by. Caddy removes any a
// visitor sends, on every route.
var Headers = []string{"Remote-User", "Remote-Email", "Remote-Name", "Remote-Groups"}

// wallClient is the walled service a check or callback is for: by its ID,
// and only at its own address.
func (p *Provider) wallClient(id, host string) (Client, bool) {
	c, ok := p.Client(id)
	if !ok || c.Host == "" || !strings.EqualFold(c.Host, host) {
		return Client{}, false
	}
	return c, true
}

// Check is forward auth's check (PathCheck?service=<id>): 200 with the
// person's headers when the request carries the service's cookie for
// someone allowed in; otherwise the way to sign in.
func (p *Provider) Check(w http.ResponseWriter, r *http.Request) {
	host := r.Header.Get("X-Forwarded-Host")
	c, ok := p.wallClient(r.URL.Query().Get("service"), host)
	if !ok {
		// Not a walled service at this address (or not Caddy asking).
		http.Error(w, "Not a walled service.", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie(HostCookie); err == nil {
		t, u, err := p.Store.LookupSSOToken(r.Context(), authkit.HashToken(cookie.Value), store.TokenHost)
		if err == nil && t.Service == c.ID {
			if ok, err := p.Store.CanUse(r.Context(), c.ID, u.ID); err == nil && ok {
				claims, err := p.claims(r.Context(), c, u, allScopes)
				if err != nil {
					p.serverError(w, err)
					return
				}
				w.Header().Set("Remote-User", u.Username)
				if email, _ := claims["email"].(string); email != "" {
					w.Header().Set("Remote-Email", email)
				}
				w.Header().Set("Remote-Name", headerSafe(claims["name"].(string)))
				w.Header().Set("Remote-Groups", strings.Join(claims["groups"].([]string), ","))
				w.WriteHeader(http.StatusOK)
				return
			}
		}
	}
	// Caddy passes this answer to the visitor. A page load goes to sign
	// in; anything else (an app's own requests) is just refused.
	method := r.Header.Get("X-Forwarded-Method")
	if method != "" && method != http.MethodGet && method != http.MethodHead {
		http.Error(w, "Sign in first.", http.StatusUnauthorized)
		return
	}
	back := "https://" + c.Host + r.Header.Get("X-Forwarded-Uri")
	http.Redirect(w, r, p.Issuer()+PathStart+"?"+url.Values{"service": {c.ID}, "rd": {back}}.Encode(), http.StatusFound)
}

// headerSafe keeps a value to one line of printable characters.
func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// StartRequest is a walled service's visitor arriving on the sign-in host
// (PathStart), checked.
type StartRequest struct {
	Client Client
	// Back is where on the service to return to: its address only.
	Back string
}

// ParseStart checks PathStart's query: the service, and where to return,
// which must be on the service's own address (no open redirect).
func (p *Provider) ParseStart(q url.Values) (*StartRequest, bool) {
	c, ok := p.Client(q.Get("service"))
	if !ok || c.Host == "" {
		return nil, false
	}
	back := "https://" + c.Host + "/"
	if u, err := url.Parse(q.Get("rd")); err == nil && u.Scheme == "https" && strings.EqualFold(u.Host, c.Host) && u.User == nil {
		back = u.String()
	}
	return &StartRequest{Client: c, Back: back}, true
}

// Handoff returns where to send someone signed in and allowed: the
// service's own address, with a one-time code that sets its cookie.
func (p *Provider) Handoff(req *StartRequest, s Session) (string, error) {
	code, err := authkit.NewToken()
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.handoffs == nil {
		p.handoffs = map[string]*handoff{}
	}
	now := p.now()
	for k, h := range p.handoffs {
		if now.After(h.expires) {
			delete(p.handoffs, k)
		}
	}
	p.handoffs[authkit.HashToken(code)] = &handoff{client: req.Client.ID, rd: req.Back, userID: s.User.ID, session: s.Hash,
		sessionEnds: s.Expires, remember: s.Remember, expires: now.Add(CodeLifetime)}
	return "https://" + req.Client.Host + PathCallback + "?" + url.Values{"code": {code}}.Encode(), nil
}

// Callback is PathCallback on a walled service's address: it swaps the
// hand-off's code for the service's cookie, then returns to the page the
// visitor wanted.
func (p *Provider) Callback(w http.ResponseWriter, r *http.Request) {
	key := authkit.HashToken(r.URL.Query().Get("code"))
	p.mu.Lock()
	h := p.handoffs[key]
	delete(p.handoffs, key)
	p.mu.Unlock()
	if h == nil || p.now().After(h.expires) {
		http.Error(w, "That sign-in link has expired. Open the service again.", http.StatusBadRequest)
		return
	}
	c, ok := p.wallClient(h.client, r.Host)
	if !ok {
		http.Error(w, "That sign-in link is for another address.", http.StatusBadRequest)
		return
	}
	tok, err := p.newToken(r.Context(), store.TokenHost, c.ID, h.userID, h.session, "", h.sessionEnds)
	if err != nil {
		p.serverError(w, err)
		return
	}
	// Host-only (no Domain): sent to this service's address alone.
	ck := &http.Cookie{Name: HostCookie, Value: tok, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
	if h.remember {
		ck.Expires = h.sessionEnds
	}
	http.SetCookie(w, ck)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, h.rd, http.StatusFound)
}
