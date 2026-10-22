package web

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/words"
)

// hostname is this server's name as the system knows it: the suggested
// name in the lab.
func hostname() string {
	name, _ := os.Hostname()
	return name
}

// First run (SPEC.md §5, §10.2): the setup code is checked first; then the
// person says what this server is for: a new lab (create the Owner), a
// lab they already have (join it with a code from its Settings →
// Servers), or a lab to restore from its backups. Nothing is chosen for
// them: each is hard to undo.

// setupCodeCookie remembers, for 30 minutes, that this browser entered the
// right setup code; the code is checked again when setup finishes.
const (
	setupCodeCookie = "roostack_setup"
	setupPassFor    = 30 * time.Minute
)

// setupPassed reports whether this browser entered the setup code lately,
// or the form carries it.
func (s *Server) setupPassed(r *http.Request) bool {
	s.setupMu.Lock()
	token := s.setupToken
	s.setupMu.Unlock()
	if token == "" {
		return false
	}
	if authkit.TokensEqual(strings.TrimSpace(r.PostFormValue("token")), token) {
		return true
	}
	c, err := r.Cookie(setupCodeCookie)
	if err != nil {
		return false
	}
	_, ok := s.setupPasses.get(originOf(r) + authkit.HashToken(c.Value))
	return ok
}

// setupCodeSubmit checks the setup code, the first step.
func (s *Server) setupCodeSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.needsSetup() {
		redirect(w, r, "/")
		return
	}
	s.setupMu.Lock()
	ok := s.setupToken != "" && authkit.TokensEqual(strings.TrimSpace(r.PostFormValue("token")), s.setupToken)
	s.setupMu.Unlock()
	if !ok {
		time.Sleep(failedLoginDelay)
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "setup", map[string]any{"Error": "That setup code isn't right. Copy it again from what the installer showed, or from " + s.SetupTokenFile + " on the server."})
		return
	}
	token, err := authkit.NewToken()
	if err != nil {
		s.serverError(w, err)
		return
	}
	// Kept with where it was entered: it works only there (SPEC.md §6.3.2).
	s.setupPasses.put(originOf(r)+authkit.HashToken(token), struct{}{}, time.Now().Add(setupPassFor))
	http.SetCookie(w, &http.Cookie{Name: setupCodeCookie, Value: token, Path: "/setup", MaxAge: int(setupPassFor.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r)})
	redirect(w, r, "/setup")
}

// setupData is the first-run page's data at its second step.
func (s *Server) setupData(extra map[string]any) map[string]any {
	data := map[string]any{"Passed": true, "CanRestore": s.LabRestore != nil, "CanJoin": s.JoinLab != nil,
		"ServerName": hostname()}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

// setupJoinSubmit adds this server to a lab the person already has: the
// code from that lab's Settings → Servers, and this server's name. On
// success Roostack restarts as part of the lab, and the page says where
// to manage it.
func (s *Server) setupJoinSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.needsSetup() {
		redirect(w, r, "/")
		return
	}
	if s.JoinLab == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	code := strings.TrimSpace(r.FormValue("code"))
	fail := func(status int, msg string) {
		w.WriteHeader(status)
		s.render(w, "setup", s.setupData(map[string]any{"Choice": "join", "Error": msg, "Code": code, "ServerName": name}))
	}
	if !s.setupPassed(r) {
		redirect(w, r, "/setup")
		return
	}
	c, err := labnet.ParseJoinCode(code)
	switch {
	case err != nil:
		fail(http.StatusBadRequest, words.Capitalise(err.Error())+". Copy it again from your lab's Settings → Servers → Add a server.")
		return
	case name == "" || len([]rune(name)) > 40 || strings.ContainsFunc(name, unicode.IsControl):
		fail(http.StatusBadRequest, "Give this server a name of up to 40 characters, such as nas or vps.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	addr, err := s.JoinLab(ctx, c, name)
	if err != nil {
		fail(http.StatusBadGateway, words.Capitalise(err.Error())+". Nothing was changed here: fix it and try again, or make a new code if this one has expired.")
		return
	}
	s.render(w, "setup-joined", map[string]any{"Lab": c.LabName, "UI": c.UI, "Endpoint": c.Endpoint, "Addr": addr, "Name": name})
}
