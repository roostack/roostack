package cloud

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
)

// PollEvery is how often the lab asks about a link being made (a
// variable, so tests can make it short).
var PollEvery = 3 * time.Second

// How often the lab checks in once linked (SPEC.md §6.6).
const (
	checkEvery = 24 * time.Hour
	// retryEvery is how soon a check that couldn't reach Cloud is tried
	// again.
	retryEvery = time.Hour
	// keepRequests is how many requests the log keeps.
	keepRequests = 200
)

// Dir is where the controller keeps its link to Cloud: in Roostack's
// state, so it's backed up with it.
func Dir(stateDir string) string { return filepath.Join(stateDir, "cloud") }

// Service is the controller's link to Roostack Cloud.
type Service struct {
	// WireGuard makes the relay's interface (nil: no relay).
	WireGuard WireGuardAgent
	// LookupTXT reads a name's TXT records for checking a claimed name
	// (tests replace it; nil asks the zone's own name servers).
	LookupTXT func(ctx context.Context, name string) ([]string, error)
	// suffix is Cloud's name suffix as it last said, and when (NameSuffix).
	suffix   string
	suffixAt time.Time
	dir      string
	cfg      config.Cloud
	version  string
	labName  func() string
	log      *slog.Logger
	client   *client
	now      func() time.Time

	mu      sync.Mutex
	st      linkState
	cert    *tls.Certificate
	pending *pending
	// startErr is why the last attempt to start linking failed;
	// addrErr why the roost.ac name asked for couldn't be set up.
	startErr, addrErr string

	// UseName makes a claimed roost.ac name the lab's address (it saves
	// it and restarts Roostack); called when a link asked for a name.
	UseName func(ctx context.Context, claim cloudapi.NameClaim) error
	// RecoveryKey reads the lab's recovery key, for its recovery identity
	// (recovery.go); "" or nil while there's none.
	RecoveryKey func() (string, error)
	// Dropped is called when the lab loses its roost.ac address with the
	// link (unlinked, here or in Cloud): the lab goes back to having no
	// address.
	Dropped func(ctx context.Context)
}

// linkState is what's kept in link.json.
type linkState struct {
	Linked bool `json:"linked"`
	cloudapi.LabInfo
	// Name is the roost.ac name asked for when linking; Address the full
	// name once claimed, while it's the lab's address.
	Name        string    `json:"name,omitempty"`
	Address     string    `json:"address,omitempty"`
	Since       time.Time `json:"since,omitzero"`
	LastContact time.Time `json:"last_contact,omitzero"`
	// LastTry and LastError: the last check, when it didn't reach Cloud.
	LastTry   time.Time `json:"last_try,omitzero"`
	LastError string    `json:"last_error,omitempty"`
	// Relay is Roostack Cloud's relay for the lab, when it's on.
	Relay *relayState `json:"relay,omitempty"`
	// Forgotten is when Cloud stopped accepting the lab's certificate,
	// and the lab forgot the link (shown until it's linked again or the
	// Owner dismisses it).
	Forgotten time.Time `json:"forgotten,omitzero"`
	// ForgottenAccount is the account it was linked to.
	ForgottenAccount string `json:"forgotten_account,omitempty"`
	// RecoveryIdentity is the public key Cloud was last told (recovery.go).
	RecoveryIdentity []byte `json:"recovery_identity,omitempty"`
}

// pending is a link being made: only in memory (a restart forgets it;
// it expires in minutes anyway).
type pending struct {
	key     *ecdsa.PrivateKey
	name    string
	started cloudapi.LinkStarted
	cancel  context.CancelFunc
	err     string
}

// New loads the controller's link from stateDir. It makes no request.
func New(stateDir string, cfg config.Cloud, version string, labName func() string, log *slog.Logger) (*Service, error) {
	s := &Service{dir: Dir(stateDir), cfg: cfg, version: version, labName: labName, log: log, now: time.Now}
	c, err := newClient(cfg, s.currentCert)
	if err != nil {
		return nil, err
	}
	c.log = s.record
	s.client = c
	data, err := os.ReadFile(s.file("link.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &s.st); err != nil {
			return nil, fmt.Errorf("%s: %w", s.file("link.json"), err)
		}
	}
	if s.st.Linked {
		cert, err := tls.LoadX509KeyPair(s.file("lab.crt"), s.file("lab.key"))
		if err != nil {
			// Without its certificate the lab can't prove who it is: it
			// has to be linked again.
			log.Error("Roostack Cloud: the lab's certificate can't be read; it needs linking again", "err", err)
			s.forget()
		} else {
			s.cert = &cert
		}
	}
	return s, nil
}

func (s *Service) file(name string) string { return filepath.Join(s.dir, name) }

func (s *Service) currentCert() *tls.Certificate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cert
}

// Status is the link as the UI shows it.
type Status struct {
	Linked bool
	cloudapi.LabInfo
	Name, Address        string
	AddressError         string
	Since, LastContact   time.Time
	LastTry              time.Time
	LastError            string
	Forgotten            time.Time
	ForgottenAccount     string
	CertFingerprint      string
	CertIssued, CertEnds time.Time
	// Waiting is a link being made; StartError why the last one didn't
	// start.
	Waiting    *Waiting
	StartError string
	// Dir is where the link is kept; URL is Cloud's address.
	Dir, URL string
}

// Waiting is a link waiting to be confirmed in Cloud.
type Waiting struct {
	Code, OpenURL, Name string
	Expires             time.Time
	// Error is why asking about it failed, if it did.
	Error string
}

// Status reports the link.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Linked: s.st.Linked, LabInfo: s.st.LabInfo, Name: s.st.Name, Address: s.st.Address,
		AddressError: s.addrErr, Since: s.st.Since,
		LastContact: s.st.LastContact, LastTry: s.st.LastTry, LastError: s.st.LastError,
		Forgotten: s.st.Forgotten, ForgottenAccount: s.st.ForgottenAccount,
		StartError: s.startErr, Dir: s.dir, URL: s.cfg.Address()}
	if s.cert != nil && s.cert.Leaf != nil {
		sum := sha256.Sum256(s.cert.Leaf.Raw)
		st.CertFingerprint = strings.ToUpper(hex.EncodeToString(sum[:8]))
		st.CertIssued, st.CertEnds = s.cert.Leaf.NotBefore, s.cert.Leaf.NotAfter
	}
	if p := s.pending; p != nil {
		st.Waiting = &Waiting{Code: p.started.Code, OpenURL: p.started.OpenURL, Name: p.name,
			Expires: p.started.Expires, Error: p.err}
	}
	return st
}

// NameSuffix is the suffix Cloud's names have (roost.ac, or a test
// Cloud's own), asked once a day and kept; roost.ac until Cloud has said.
// It never waits more than a few seconds, for the pages that show it.
func (s *Service) NameSuffix(ctx context.Context) string {
	s.mu.Lock()
	suffix, at := s.suffix, s.suffixAt
	s.mu.Unlock()
	if suffix != "" && time.Since(at) < 24*time.Hour {
		return suffix
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var info cloudapi.Info
	if err := s.client.do(ctx, false, http.MethodGet, cloudapi.PathInfo, nil, &info); err != nil || info.NameSuffix == "" {
		return cmp.Or(suffix, cloudapi.DefaultNameSuffix)
	}
	s.mu.Lock()
	s.suffix, s.suffixAt = info.NameSuffix, time.Now()
	s.mu.Unlock()
	return info.NameSuffix
}

// NameAvailable asks Cloud whether a roost.ac name can be had.
func (s *Service) NameAvailable(ctx context.Context, name string) (cloudapi.NameAvailability, error) {
	var a cloudapi.NameAvailability
	err := s.client.do(ctx, false, http.MethodGet, cloudapi.PathNames+url.PathEscape(name), nil, &a)
	return a, err
}

// Start begins linking the lab, asking for name ("" for none): it
// returns the page to open in Cloud, which sends the browser back to
// returnURL ("" for none) when it's done. The lab then asks Cloud until
// the Owner confirms there, the code expires, or Cancel, whether or not
// the browser comes back.
func (s *Service) Start(ctx context.Context, name, returnURL string) (string, error) {
	s.mu.Lock()
	if s.st.Linked {
		s.mu.Unlock()
		return "", errors.New("this lab is already linked to Roostack Cloud")
	}
	s.mu.Unlock()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: s.labName()}}, key)
	if err != nil {
		return "", err
	}
	var started cloudapi.LinkStarted
	err = s.client.do(ctx, false, http.MethodPost, cloudapi.PathLinks,
		cloudapi.LinkRequest{LabName: s.labName(), Name: name, Version: s.version, CSR: csr, ReturnURL: returnURL}, &started)
	if err == nil {
		err = checkOpenURL(started.OpenURL)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.startErr = err.Error()
		return "", err
	}
	if s.pending != nil {
		s.pending.cancel()
	}
	pctx, cancel := context.WithDeadline(context.Background(), started.Expires)
	p := &pending{key: key, name: name, started: started, cancel: cancel}
	s.pending, s.startErr = p, ""
	go s.poll(pctx, p)
	return started.OpenURL, nil
}

// checkOpenURL accepts only a web page to open: https, or http on this
// machine (a test Cloud).
func checkOpenURL(raw string) error {
	u, err := url.Parse(raw)
	if err == nil && u.Host != "" && (u.Scheme == "https" ||
		u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return nil
	}
	return fmt.Errorf("Roostack Cloud gave an unexpected page to open (%q)", raw)
}

// poll asks Cloud about p until it's linked, expired or cancelled.
func (s *Service) poll(ctx context.Context, p *pending) {
	t := time.NewTicker(PollEvery)
	defer t.Stop()
	defer p.cancel()
	for {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			if s.pending == p {
				s.pending = nil
				if !s.st.Linked && ctx.Err() == context.DeadlineExceeded {
					s.startErr = "The code expired before it was confirmed in Roostack Cloud. Try again."
				}
			}
			s.mu.Unlock()
			return
		case <-t.C:
		}
		var ls cloudapi.LinkStatus
		err := s.client.do(ctx, false, http.MethodPost, cloudapi.PathLinkStatus, cloudapi.LinkSecret{Secret: p.started.Secret}, &ls)
		s.mu.Lock()
		if s.pending != p {
			s.mu.Unlock()
			return
		}
		p.err = ""
		if err != nil {
			p.err = err.Error()
		}
		s.mu.Unlock()
		if err != nil {
			continue
		}
		switch ls.State {
		case cloudapi.StateLinked:
			if err := s.linked(p, ls); err != nil {
				s.log.Error("Roostack Cloud: linking", "err", err)
				s.mu.Lock()
				s.pending, s.startErr = nil, "Linking didn't finish: "+err.Error()
				s.mu.Unlock()
			}
			return
		case cloudapi.StateExpired:
			s.mu.Lock()
			if s.pending == p {
				s.pending, s.startErr = nil, "Linking was cancelled, or the code expired, in Roostack Cloud. Try again."
			}
			s.mu.Unlock()
			return
		}
	}
}

// linked keeps the certificate Cloud issued for p's key.
func (s *Service) linked(p *pending, ls cloudapi.LinkStatus) error {
	cert, err := certFor(p.key, ls.Certificate)
	if err != nil {
		return err
	}
	keyPEM, err := keyPEM(p.key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != p {
		return errors.New("cancelled")
	}
	if err := s.saveCert(keyPEM, []byte(ls.Certificate)); err != nil {
		return err
	}
	now := s.now()
	s.cert, s.pending = cert, nil
	s.client.renewed()
	s.st = linkState{Linked: true, LabInfo: ls.LabInfo, Name: p.name, Since: now, LastContact: now}
	s.log.Info("Roostack Cloud: lab linked", "account", ls.Account, "lab", ls.LabID)
	if err := s.save(); err != nil {
		return err
	}
	go func() {
		if err := s.RegisterIdentity(context.Background()); err != nil {
			s.log.Warn("Roostack Cloud: registering the recovery identity (tried again at the next check)", "err", err)
		}
	}()
	if p.name != "" {
		go func() {
			if err := s.ClaimName(context.Background(), p.name); err != nil {
				s.log.Error("Roostack Cloud: setting up the lab's roost.ac address", "name", p.name, "err", err)
			}
		}()
	}
	return nil
}

// ClaimName claims a roost.ac name for the linked lab and makes it the
// lab's address (UseName), which restarts Roostack. Why it failed is
// also in Status.AddressError.
func (s *Service) ClaimName(ctx context.Context, name string) error {
	var claim cloudapi.NameClaim
	err := s.client.do(ctx, true, http.MethodPost, cloudapi.PathLabName, cloudapi.NameRequest{Name: name}, &claim)
	if err == nil && (claim.Name == "" || len(claim.Credentials) == 0) {
		err = errors.New("Roostack Cloud's answer had no name or credentials")
	}
	if err == nil && s.UseName == nil {
		err = errors.New("nothing here can use a roost.ac name")
	}
	if err == nil {
		// Before anything uses it: its credentials do what certificates
		// need (check.go).
		err = s.checkClaim(ctx, claim)
	}
	if err == nil {
		s.mu.Lock()
		s.st.Name, s.st.Address = name, claim.Name
		err = s.save()
		s.mu.Unlock()
	}
	if err == nil {
		err = s.UseName(ctx, claim)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addrErr = ""
	if err != nil {
		s.addrErr = err.Error()
		var e *Error
		if errors.As(err, &e) {
			s.addrErr = e.Message
		}
		if s.st.Address == claim.Name {
			s.st.Address = ""
			s.save()
		}
	}
	return err
}

// certFor checks a certificate (PEM) Cloud issued is for key and current.
func certFor(key *ecdsa.PrivateKey, certPEM string) (*tls.Certificate, error) {
	b, _ := pem.Decode([]byte(certPEM))
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, errors.New("Roostack Cloud sent no certificate")
	}
	leaf, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("Roostack Cloud's certificate is for another key")
	}
	if time.Now().After(leaf.NotAfter) {
		return nil, errors.New("Roostack Cloud's certificate has already expired")
	}
	return &tls.Certificate{Certificate: [][]byte{b.Bytes}, PrivateKey: key, Leaf: leaf}, nil
}

func keyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// Cancel stops waiting for a link to be confirmed.
func (s *Service) Cancel(ctx context.Context) {
	s.mu.Lock()
	p := s.pending
	s.pending, s.startErr = nil, ""
	s.mu.Unlock()
	if p == nil {
		return
	}
	p.cancel()
	// Tell Cloud, so the code stops working there too; it expires anyway.
	if err := s.client.do(ctx, false, http.MethodPost, cloudapi.PathLinkCancel, cloudapi.LinkSecret{Secret: p.started.Secret}, nil); err != nil {
		s.log.Warn("Roostack Cloud: cancelling a link", "err", err)
	}
}

// Unlink removes the link: it tells Cloud, then deletes the lab's key.
// If Cloud can't be reached (or refuses), the link is removed here
// anyway, and reached is false.
func (s *Service) Unlink(ctx context.Context) (reached bool, err error) {
	s.mu.Lock()
	linked := s.st.Linked
	s.mu.Unlock()
	if !linked {
		return true, nil
	}
	// Whatever Cloud answers, the link goes here: the account's page can
	// remove the lab there too.
	err = s.client.do(ctx, true, http.MethodDelete, cloudapi.PathLab, nil, nil)
	reached = err == nil || errors.Is(err, ErrUnlinked)
	if !reached {
		s.log.Warn("Roostack Cloud: telling Cloud the lab is unlinked", "err", err)
	}
	s.mu.Lock()
	address := s.st.Address
	s.forget()
	s.st = linkState{}
	s.log.Info("Roostack Cloud: lab unlinked", "cloud_told", reached)
	err = s.save()
	s.mu.Unlock()
	if address != "" && s.Dropped != nil {
		s.Dropped(ctx)
	}
	return reached, err
}

// DismissForgotten hides the note that Cloud stopped knowing the lab.
func (s *Service) DismissForgotten() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Forgotten, s.st.ForgottenAccount = time.Time{}, ""
	return s.save()
}

// forget deletes the lab's key and certificate (s.mu held).
func (s *Service) forget() {
	for _, f := range []string{"lab.key", "lab.crt"} {
		if err := os.Remove(s.file(f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.log.Error("Roostack Cloud: removing the lab's certificate", "file", f, "err", err)
		}
	}
	s.cert = nil
	s.st.Linked = false
	// Not linked, not relayed: the relay's interface goes too.
	if s.st.Relay != nil && s.WireGuard != nil {
		go func() {
			if err := s.WireGuard.RemoveWireGuard(context.Background(), agent.RelayInterface); err != nil {
				s.log.Error("Roostack Cloud: removing the relay's network", "err", err)
			}
		}()
	}
	s.st.Relay = nil
	s.client.renewed()
}

// Check checks in with Cloud: it refreshes what Cloud says about the
// lab, renews the certificate when due, and registers the recovery
// identity if it changed. If Cloud no longer accepts the certificate, the
// lab relinks with its recovery key, and forgets the link only if that's
// refused (SPEC.md §6.6, §6.9).
func (s *Service) Check(ctx context.Context) error {
	s.mu.Lock()
	linked := s.st.Linked
	s.mu.Unlock()
	if !linked {
		return nil
	}
	var info cloudapi.LabInfo
	err := s.client.do(ctx, true, http.MethodGet, cloudapi.PathLab, nil, &info)
	if errors.Is(err, ErrUnlinked) {
		if rerr := s.relink(ctx); rerr == nil {
			err = s.client.do(ctx, true, http.MethodGet, cloudapi.PathLab, nil, &info)
		} else {
			s.log.Warn("Roostack Cloud: relinking with the recovery key", "err", rerr)
			if retryLater(rerr) {
				err = rerr // tried again at the next check; the link is kept
			}
		}
	}
	if err == nil {
		err = s.renewIfDue(ctx)
	}
	if err == nil {
		if rerr := s.RegisterIdentity(ctx); rerr != nil {
			// Not the check's failure: it's tried again at the next one.
			s.log.Warn("Roostack Cloud: registering the recovery identity", "err", rerr)
		}
	}
	s.mu.Lock()
	now := s.now()
	dropped := false
	switch {
	case errors.Is(err, ErrUnlinked):
		s.log.Warn("Roostack Cloud no longer accepts this lab's certificate; forgetting the link")
		account := s.st.Account
		dropped = s.st.Address != ""
		s.forget()
		s.st = linkState{Forgotten: now, ForgottenAccount: account}
	case err != nil:
		s.st.LastTry, s.st.LastError = now, err.Error()
	default:
		s.st.LabInfo = info
		s.st.LastContact, s.st.LastTry, s.st.LastError = now, time.Time{}, ""
	}
	serr := s.save()
	s.mu.Unlock()
	if dropped && s.Dropped != nil {
		s.Dropped(ctx)
	}
	if serr != nil {
		return serr
	}
	return err
}

// renewIfDue renews the certificate, with a new key, once two thirds of
// its life has passed.
func (s *Service) renewIfDue(ctx context.Context) error {
	s.mu.Lock()
	cert := s.cert
	s.mu.Unlock()
	if cert == nil || cert.Leaf == nil {
		return nil
	}
	life := cert.Leaf.NotAfter.Sub(cert.Leaf.NotBefore)
	if s.now().Before(cert.Leaf.NotBefore.Add(life * 2 / 3)) {
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: cert.Leaf.Subject}, key)
	if err != nil {
		return err
	}
	var rr cloudapi.RenewResponse
	if err := s.client.do(ctx, true, http.MethodPost, cloudapi.PathLabRenew, cloudapi.RenewRequest{CSR: csr}, &rr); err != nil {
		return err
	}
	next, err := certFor(key, rr.Certificate)
	if err != nil {
		return err
	}
	kp, err := keyPEM(key)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.saveCert(kp, []byte(rr.Certificate)); err != nil {
		return err
	}
	s.cert = next
	s.client.renewed()
	s.log.Info("Roostack Cloud: the lab's certificate was renewed", "until", next.Leaf.NotAfter)
	return nil
}

// saveCert writes the key (0600) and certificate, each replaced in one
// step (s.mu held).
func (s *Service) saveCert(keyPEM, certPEM []byte) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	if err := writeFile(s.file("lab.key"), keyPEM, 0o600); err != nil {
		return err
	}
	return writeFile(s.file("lab.crt"), certPEM, 0o644)
}

// save writes link.json (s.mu held).
func (s *Service) save() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(s.file("link.json"), append(data, '\n'), 0o600)
}

func writeFile(file string, data []byte, mode os.FileMode) error {
	tmp := file + ".new"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// Run checks in when Roostack starts, then once a day (hourly while
// Cloud can't be reached), until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for first := true; ; first = false {
		s.mu.Lock()
		st := s.st
		s.mu.Unlock()
		due := st.Linked && (first || s.now().Sub(st.LastContact) >= checkEvery &&
			(st.LastTry.IsZero() || s.now().Sub(st.LastTry) >= retryEvery))
		if due {
			if err := s.Check(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("Roostack Cloud: checking in", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Request is one request the lab made to Cloud.
type Request struct {
	At     time.Time
	Method string
	Path   string
	// Status is the HTTP status; 0 if Cloud wasn't reached (Error says why).
	Status int
	Error  string
	// Count is how many times in a row it was made with the same answer
	// (asking about a link every few seconds is one line), up to At.
	Count int `json:",omitempty"`
}

// record appends a request to requests.log, keeping the latest few.
func (s *Service) record(method, path string, status int, err error) {
	r := Request{At: s.now().UTC(), Method: method, Path: path, Status: status}
	if err != nil {
		r.Error = err.Error()
	}
	line, _ := json.Marshal(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return
	}
	old, _ := os.ReadFile(s.file("requests.log"))
	lines := bytes.Split(bytes.TrimSpace(old), []byte("\n"))
	if len(old) == 0 {
		lines = nil
	}
	// The same request with the same answer as the last one adds to it.
	var last Request
	if n := len(lines); n > 0 && json.Unmarshal(lines[n-1], &last) == nil &&
		last.Method == r.Method && last.Path == r.Path && last.Status == r.Status && last.Error == r.Error {
		r.Count = max(last.Count, 1) + 1
		line, _ = json.Marshal(r)
		lines = lines[:n-1]
	}
	lines = append(lines, line)
	if len(lines) > keepRequests {
		lines = lines[len(lines)-keepRequests:]
	}
	if err := writeFile(s.file("requests.log"), append(bytes.Join(lines, []byte("\n")), '\n'), 0o600); err != nil {
		s.log.Warn("Roostack Cloud: recording a request", "err", err)
	}
}

// Requests are the lab's recent requests to Cloud, newest first.
func (s *Service) Requests() []Request {
	s.mu.Lock()
	data, _ := os.ReadFile(s.file("requests.log"))
	s.mu.Unlock()
	var out []Request
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var r Request
		if json.Unmarshal(line, &r) == nil && !r.At.IsZero() {
			out = append([]Request{r}, out...)
		}
	}
	return out
}
