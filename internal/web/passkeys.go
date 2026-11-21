package web

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/words"
)

// Passkeys (SPEC.md §5, "Passkeys"), through go-webauthn: signing in
// without a password or code at my.<domain> and roost.<domain>, standing
// in for the code wherever Roostack asks someone to confirm it's them, and
// adding and removing them on Your account → Security.
//
// Each ceremony has two halves. The page's script asks for the options
// (a challenge) as JSON, and the browser asks the device; the device's
// answer then comes back in an ordinary form post (the field
// passkeyField), so what follows is the same redirect or page as after a
// password or code. The challenge is kept in memory for five minutes and
// one use, tied to the browser by a cookie.

const (
	passkeyCookie = "roostack_passkey"
	// passkeyField is the form field with the device's answer, as JSON.
	passkeyField = "passkey"
	// passkeyCeremonyFor is how long a challenge lasts; passkeyConfirmFor
	// how long confirming it's them lasts for adding a passkey.
	passkeyCeremonyFor = 5 * time.Minute
	passkeyConfirmFor  = 5 * time.Minute
	// passkeyFailed is said whenever a passkey doesn't work: the same
	// sentence for one Roostack doesn't know, so the page doesn't say
	// which passkeys exist.
	passkeyFailed = "The passkey didn't work, or you cancelled it. Try again, or sign in with your password."
)

// Kinds of ceremony: signing in with no account named (login), the step
// after a password (second), confirming it's them (confirm), and adding
// a passkey (register).
const (
	ceremonyLogin    = "login"
	ceremonySecond   = "second"
	ceremonyConfirm  = "confirm"
	ceremonyRegister = "register"
)

type passkeyCeremony struct {
	kind    string
	userID  int64 // 0 for login, where the device says who
	session webauthn.SessionData
}

// passkeysWork reports whether passkeys can work for r: at the lab's own
// two addresses, over HTTPS. Browsers refuse them on an IP address, at
// localhost (the SSH tunnel), and over plain HTTP, and the setup pages
// are none of the lab's addresses.
func (s *Server) passkeysWork(r *http.Request) bool {
	if s.Domain == "" || !isHTTPS(r) || fromSetupListener(r) {
		return false
	}
	h := public.HostOnly(r.Host)
	return (s.UIHost != "" && strings.EqualFold(h, s.UIHost)) || (s.SignInHost != "" && strings.EqualFold(h, s.SignInHost))
}

// webAuthn is go-webauthn set up for the lab: passkeys made for its
// domain, accepted at its two addresses only (not at an app's, which the
// browser would allow), no attestation (Roostack doesn't ask which make
// of device made one), and the device always verifying the person, so a
// passkey is both factors.
func (s *Server) webAuthn() (*webauthn.WebAuthn, error) {
	var origins []string
	for _, h := range []string{s.SignInHost, s.UIHost} {
		if h != "" {
			origins = append(origins, "https://"+h)
		}
	}
	return webauthn.New(&webauthn.Config{
		RPID:                  s.Domain,
		RPDisplayName:         cmp.Or(s.labName(), "Roostack"),
		RPOrigins:             origins,
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
	})
}

// passkeyUser is a person as go-webauthn sees them: the random handle
// their devices know them by, and their passkeys for the lab's domain as
// it is now.
type passkeyUser struct {
	u      *store.User
	handle []byte
	creds  []webauthn.Credential
	rows   []store.Passkey // creds[i] is rows[i]
}

func (p *passkeyUser) WebAuthnID() []byte                         { return p.handle }
func (p *passkeyUser) WebAuthnName() string                       { return p.u.Username }
func (p *passkeyUser) WebAuthnDisplayName() string                { return cmp.Or(p.u.Name, p.u.Username) }
func (p *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return p.creds }

func (s *Server) passkeyUser(ctx context.Context, u *store.User) (*passkeyUser, error) {
	handle, err := s.Store.PasskeyHandle(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.Store.UserPasskeys(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	p := &passkeyUser{u: u, handle: handle}
	for _, row := range rows {
		var c webauthn.Credential
		if row.RPID != s.Domain || json.Unmarshal([]byte(row.Credential), &c) != nil {
			continue // made for the domain before: it can't work here
		}
		p.creds = append(p.creds, c)
		p.rows = append(p.rows, row)
	}
	return p, nil
}

// beginCeremony keeps a ceremony's challenge for this browser and sends
// the options to the page.
func (s *Server) beginCeremony(w http.ResponseWriter, r *http.Request, kind string, userID int64, sd *webauthn.SessionData, options any) {
	token, err := authkit.NewToken()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.ceremonies.put(authkit.HashToken(token), &passkeyCeremony{kind: kind, userID: userID, session: *sd}, time.Now().Add(passkeyCeremonyFor))
	http.SetCookie(w, &http.Cookie{Name: passkeyCookie, Value: token, Path: "/", MaxAge: int(passkeyCeremonyFor.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r)})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(options)
}

// takeCeremony is this browser's ceremony of that kind, once.
func (s *Server) takeCeremony(w http.ResponseWriter, r *http.Request, kind string) (*passkeyCeremony, bool) {
	c, err := r.Cookie(passkeyCookie)
	if err != nil {
		return nil, false
	}
	http.SetCookie(w, &http.Cookie{Name: passkeyCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r)})
	cer, ok := s.ceremonies.take(authkit.HashToken(c.Value))
	if !ok || cer.kind != kind {
		return nil, false
	}
	return cer, true
}

// passkeyBeginError answers a page asking for options it can't have.
func passkeyBeginError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// passkeyLoginBegin starts signing in with a passkey with no account
// named: the device offers the lab's passkeys it has. ?mediation=
// conditional is the browser offering them in the username box's
// autofill.
func (s *Server) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if !s.passkeysWork(r) {
		passkeyBeginError(w, http.StatusBadRequest, "Passkeys only work at the lab's address.")
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		s.serverError(w, err)
		return
	}
	mediation := protocol.MediationDefault
	if r.URL.Query().Get("mediation") == "conditional" {
		mediation = protocol.MediationConditional
	}
	opts, sd, err := wa.BeginDiscoverableMediatedLogin(mediation, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.beginCeremony(w, r, ceremonyLogin, 0, sd, opts)
}

// finishPasskeyLogin checks the device's answer to a login ceremony and
// says whose passkey it was. A failure is logged and audited without
// naming an account.
func (s *Server) finishPasskeyLogin(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	cer, ok := s.takeCeremony(w, r, ceremonyLogin)
	if !ok {
		return nil, false
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes([]byte(r.FormValue(passkeyField)))
	if err != nil {
		s.passkeyRefused(r, nil, err)
		return nil, false
	}
	wa, err := s.webAuthn()
	if err != nil {
		s.Log.Error("passkeys", "err", err)
		return nil, false
	}
	var who *passkeyUser
	_, cred, err := wa.ValidatePasskeyLogin(func(_, handle []byte) (webauthn.User, error) {
		u, err := s.Store.UserByPasskeyHandle(r.Context(), handle)
		if err != nil {
			return nil, err
		}
		who, err = s.passkeyUser(r.Context(), u)
		return who, err
	}, cer.session, parsed)
	if err != nil || who == nil {
		s.passkeyRefused(r, nil, err)
		return nil, false
	}
	if !s.passkeyUsed(r, who, cred) {
		return nil, false
	}
	return who.u, true
}

// passkeySecondBegin starts the step after a password with the person's
// own passkeys.
func (s *Server) passkeySecondBegin(w http.ResponseWriter, r *http.Request, u *store.User) {
	s.beginKnown(w, r, u, ceremonySecond)
}

// passkeyConfirmBegin starts confirming it's them, for someone signed in.
func (s *Server) passkeyConfirmBegin(w http.ResponseWriter, r *http.Request, a *acct) {
	s.beginKnown(w, r, a.User, ceremonyConfirm)
}

// beginKnown starts a ceremony for a person Roostack already knows, with
// their passkeys for the lab's domain.
func (s *Server) beginKnown(w http.ResponseWriter, r *http.Request, u *store.User, kind string) {
	if !s.passkeysWork(r) {
		passkeyBeginError(w, http.StatusBadRequest, "Passkeys only work at the lab's address.")
		return
	}
	pu, err := s.passkeyUser(r.Context(), u)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if len(pu.creds) == 0 {
		passkeyBeginError(w, http.StatusBadRequest, "You have no passkey for this lab's address.")
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		s.serverError(w, err)
		return
	}
	opts, sd, err := wa.BeginLogin(pu, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.beginCeremony(w, r, kind, u.ID, sd, opts)
}

// checkPasskeyOf checks the device's answer to a ceremony for u, of that
// kind: the step after a password, or confirming it's them.
func (s *Server) checkPasskeyOf(w http.ResponseWriter, r *http.Request, u *store.User, kind string) bool {
	cer, ok := s.takeCeremony(w, r, kind)
	if !ok || cer.userID != u.ID {
		return false
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes([]byte(r.FormValue(passkeyField)))
	if err != nil {
		s.passkeyRefused(r, u, err)
		return false
	}
	pu, err := s.passkeyUser(r.Context(), u)
	if err != nil {
		s.Log.Error("passkeys", "err", err)
		return false
	}
	wa, err := s.webAuthn()
	if err != nil {
		s.Log.Error("passkeys", "err", err)
		return false
	}
	cred, err := wa.ValidateLogin(pu, cer.session, parsed)
	if err != nil {
		s.passkeyRefused(r, u, err)
		return false
	}
	return s.passkeyUsed(r, pu, cred)
}

// passkeyOffered says whether to offer u's passkey to confirm it's them
// here: they have one, and passkeys work at this address.
func (s *Server) passkeyOffered(r *http.Request, u *store.User) bool {
	return u != nil && u.Passkeys > 0 && s.passkeysWork(r)
}

// hasPasskeyAnswer: the form came with a passkey's answer rather than a
// code or password.
func hasPasskeyAnswer(r *http.Request) bool { return r.FormValue(passkeyField) != "" }

// passkeyUsed records a passkey's use: its counter and when. A counter
// that went backwards means a copy of the passkey may be in use, so it's
// refused and audited.
func (s *Server) passkeyUsed(r *http.Request, pu *passkeyUser, cred *webauthn.Credential) bool {
	for i, c := range pu.creds {
		if !bytes.Equal(c.ID, cred.ID) {
			continue
		}
		row := pu.rows[i]
		if cred.Authenticator.CloneWarning {
			s.Log.Warn("passkey refused: its counter went backwards", "user", pu.u.Username, "passkey", row.Name)
			s.Store.Audit(r.Context(), pu.u, attention.AuditPasskeyCopied, row.Name)
			return false
		}
		b, err := json.Marshal(cred)
		if err == nil {
			err = s.Store.UsePasskey(r.Context(), row.ID, string(b), time.Now())
		}
		if err != nil {
			s.Log.Error("recording a passkey's use", "err", err)
		}
		return true
	}
	return false
}

// passkeyRefused logs a passkey that didn't work, and audits it without
// naming an account unless the person was already known.
func (s *Server) passkeyRefused(r *http.Request, u *store.User, err error) {
	s.Log.Warn("passkey refused", "remote", r.RemoteAddr, "err", err)
	s.Store.Audit(r.Context(), u, "failed passkey", "from "+r.RemoteAddr)
}

// passkeyConfirmed reports whether a confirmed it's them in the last few
// minutes on this browser, for adding a passkey; take uses it up.
func (s *Server) passkeyConfirmed(a *acct, take bool) bool {
	_, ok := s.addConfirmed.lookup(a.Hash, take)
	return ok && a.Hash != ""
}

// passkeyAdd is adding a passkey: confirming it's them first, then the
// device and a name.
func (s *Server) passkeyAdd(w http.ResponseWriter, r *http.Request, a *acct) {
	s.renderPasskeyAdd(w, r, a, http.StatusOK, "")
}

func (s *Server) renderPasskeyAdd(w http.ResponseWriter, r *http.Request, a *acct, status int, msg string) {
	v, err := s.acctSecurityView(r, a)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v.Title, v.Error = "Add a passkey", msg
	v.PasskeyAdd = &acctPasskeyAdd{Confirmed: s.passkeyConfirmed(a, false), Name: deviceName(r.UserAgent())}
	if !v.PasskeysWork {
		v.Error = "Passkeys only work at the lab's address, https://" + s.SignInHost + "."
	}
	s.renderAcct(w, r, a, status, v)
}

// passkeyAddConfirm is confirming it's them before adding a passkey: their
// password and code (the password alone without two-factor), or a passkey
// they already have.
func (s *Server) passkeyAddConfirm(w http.ResponseWriter, r *http.Request, a *acct) {
	u := a.User
	if hasPasskeyAnswer(r) {
		if !s.checkPasskeyOf(w, r, u, ceremonyConfirm) {
			time.Sleep(failedLoginDelay)
			s.renderPasskeyAdd(w, r, a, http.StatusUnauthorized, "The passkey didn't work, or you cancelled it. Try again, or use your password.")
			return
		}
	} else {
		if ok, err := authkit.VerifyPassword(u.PasswordHash, r.FormValue("current")); err != nil || !ok {
			time.Sleep(failedLoginDelay)
			s.renderPasskeyAdd(w, r, a, http.StatusUnauthorized, "Your password is wrong.")
			return
		}
		if u.TOTPSecret != "" {
			res, until, err := s.checkCode(r.Context(), u.ID, r.FormValue("code"))
			if err != nil {
				s.serverError(w, err)
				return
			}
			if res != codeOK {
				time.Sleep(failedLoginDelay)
				s.renderPasskeyAdd(w, r, a, http.StatusUnauthorized, codeMessage(res, until))
				return
			}
		}
	}
	s.addConfirmed.put(a.Hash, struct{}{}, time.Now().Add(passkeyConfirmFor))
	redirect(w, r, "/account/passkeys/add")
}

// passkeyRegisterBegin asks the device to make a passkey, once they've
// confirmed it's them.
func (s *Server) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request, a *acct) {
	if !s.passkeysWork(r) {
		passkeyBeginError(w, http.StatusBadRequest, "Passkeys only work at the lab's address.")
		return
	}
	if !s.passkeyConfirmed(a, false) {
		passkeyBeginError(w, http.StatusForbidden, "Confirm it's you first: reload the page.")
		return
	}
	pu, err := s.passkeyUser(r.Context(), a.User)
	if err != nil {
		s.serverError(w, err)
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		s.serverError(w, err)
		return
	}
	// Every passkey of theirs, the old domain's too, so a device that
	// already has one isn't asked to make another.
	opts, sd, err := wa.BeginRegistration(pu, webauthn.WithExclusions(webauthn.Credentials(pu.creds).CredentialDescriptors()))
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.beginCeremony(w, r, ceremonyRegister, a.User.ID, sd, opts)
}

// passkeyAddSubmit saves the passkey the device made, with its name.
func (s *Server) passkeyAddSubmit(w http.ResponseWriter, r *http.Request, a *acct) {
	ctx := r.Context()
	u := a.User
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = deviceName(r.UserAgent())
	}
	if len(name) > 60 {
		name = name[:60]
	}
	cer, ok := s.takeCeremony(w, r, ceremonyRegister)
	if !ok || cer.userID != u.ID || !s.passkeyConfirmed(a, false) {
		s.renderPasskeyAdd(w, r, a, http.StatusBadRequest, "That took too long. Try again.")
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes([]byte(r.FormValue(passkeyField)))
	if err != nil {
		s.Log.Warn("adding a passkey", "user", u.Username, "err", err)
		s.renderPasskeyAdd(w, r, a, http.StatusBadRequest, "Your device didn't make a passkey, or you cancelled it. Try again.")
		return
	}
	pu, err := s.passkeyUser(ctx, u)
	if err != nil {
		s.serverError(w, err)
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		s.serverError(w, err)
		return
	}
	cred, err := wa.CreateCredential(pu, cer.session, parsed)
	if err != nil {
		s.Log.Warn("adding a passkey", "user", u.Username, "err", err)
		s.renderPasskeyAdd(w, r, a, http.StatusBadRequest, "Roostack couldn't use that passkey: "+err.Error())
		return
	}
	b, err := json.Marshal(cred)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if _, err := s.Store.AddPasskey(ctx, store.Passkey{UserID: u.ID, CredentialID: cred.ID, Credential: string(b), RPID: s.Domain, Name: name}); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.renderPasskeyAdd(w, r, a, http.StatusConflict, "That passkey is already saved.")
			return
		}
		s.serverError(w, err)
		return
	}
	s.passkeyConfirmed(a, true)
	s.Store.Audit(ctx, u, attention.AuditPasskeyAdded, name)
	redirect(w, r, "/account/security?done=passkey-added#passkeys")
}

// passkeyRemove removes one of someone's own passkeys. It needs no
// password: it can only take a way in away. The last way to meet a
// two-factor they need stays until they've added another.
func (s *Server) passkeyRemove(w http.ResponseWriter, r *http.Request, a *acct) {
	ctx := r.Context()
	u := a.User
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if s.twoFactorRequired(u) && u.TOTPSecret == "" && u.Passkeys <= 1 {
		v, err := s.acctSecurityView(r, a)
		if err != nil {
			s.serverError(w, err)
			return
		}
		v.Error = "That's your only way to meet the two-factor you need, so add another passkey or an authenticator app before removing it."
		s.renderAcct(w, r, a, http.StatusConflict, v)
		return
	}
	name, err := s.Store.RemovePasskey(ctx, u.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		redirect(w, r, "/account/security#passkeys")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(ctx, u, attention.AuditPasskeyRemoved, name)
	redirect(w, r, "/account/security?done=passkey-removed#passkeys")
}

// acctPasskeyAdd is the page adding a passkey: Confirmed once they've
// confirmed it's them, and the name suggested for it.
type acctPasskeyAdd struct {
	Confirmed bool
	Name      string
}

// acctPasskey is a row of the Passkeys card.
type acctPasskey struct {
	ID               int64
	Name             string
	Synced, OnlyHere bool
	Added, LastUsed  time.Time
	OtherDomain      string
	Only             bool // their only way to meet a two-factor they need
}

// acctPasskeys are someone's passkeys, for the Passkeys card.
func (s *Server) acctPasskeys(ctx context.Context, u *store.User) ([]acctPasskey, error) {
	rows, err := s.Store.UserPasskeys(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	var out []acctPasskey
	for _, row := range rows {
		p := acctPasskey{ID: row.ID, Name: row.Name, Added: row.CreatedAt, LastUsed: row.LastUsedAt}
		var c webauthn.Credential
		if json.Unmarshal([]byte(row.Credential), &c) == nil {
			p.Synced, p.OnlyHere = c.Flags.BackupEligible, !c.Flags.BackupEligible
		}
		if row.RPID != s.Domain {
			p.OtherDomain = row.RPID
		}
		out = append(out, p)
	}
	if len(out) == 1 && u.TOTPSecret == "" && s.twoFactorRequired(u) {
		out[0].Only = true
	}
	return out, nil
}

// passkeysRemoved says, in an audit entry, how many passkeys went with a
// two-factor reset.
func passkeysRemoved(n int) string {
	if n == 0 {
		return ""
	}
	return " and " + words.Count(int64(n), "passkey", "passkeys") + " removed"
}

// codeMessage is what a code that wasn't accepted is told.
func codeMessage(res codeResult, until time.Time) string {
	switch res {
	case codeLocked:
		return lockedMessage(until)
	case codeUsed:
		return usedCodeMessage
	}
	return "That code didn't work. Check your authenticator app and try again."
}
