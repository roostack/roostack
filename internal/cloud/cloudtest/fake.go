// Package cloudtest is a fake of Roostack Cloud's API for labs, for
// Roostack's own tests (a dev VM links to a development Cloud, from
// Cloud's repository, instead). It has
// none of Cloud's logic: one pretend account, names taken from a list,
// and a page with a Confirm button (SPEC.md §6.6). It's replaced by the
// real endpoints as soon as they exist.
package cloudtest

import (
	"bytes"
	"cmp"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html/template"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/config"
)

// Fake is a pretend Roostack Cloud.
type Fake struct {
	// Account is how the one account is shown to labs.
	Account string
	// Taken are roost.ac names that aren't available; Held, ones kept
	// for an account (any lab may claim them in the fake).
	Taken, Held map[string]bool
	// CertLife is how long lab certificates last.
	CertLife time.Duration
	// PageURL, if set, is the address people open the confirm page at
	// (otherwise the address the lab reached).
	PageURL string
	// NameSuffix follows claimed names: roost.test, so nothing asks for
	// real roost.ac certificates or records.
	NameSuffix string
	// ChallengeURL, if set, is where the acme-dns and records APIs are
	// reached (Caddy and Roostack don't trust the fake's HTTPS
	// certificate), otherwise the address the lab reached.
	ChallengeURL string
	// NoRelay refuses the relay, as Cloud does for an account without the
	// pilot grant. Relayed is each relayed lab's request, by lab ID.
	NoRelay bool
	Relayed map[string]cloudapi.RelayRequest
	// Suspended answers every lab request 423 Locked, as Cloud did
	// before 2026-10-16 while the account was suspended.
	Suspended bool
	// SuspendedFor is why the account is suspended, as Cloud says now:
	// the lab may still check in (told why) and report; its other
	// requests are answered 423 Locked.
	SuspendedFor string
	// NameStates are what the lab is told about its names (waiting,
	// refused, suspended): when it checks in, with the relay's answers,
	// and with a claim (that name's).
	NameStates []cloudapi.NameStatus
	// RelayOff is when the account turned the relay off at Cloud (zero:
	// it's on), until the lab asks to turn it on; RelaySuspended why
	// it's suspended.
	RelayOff       time.Time
	RelaySuspended string
	// Reports are each lab's last report, by lab ID; NoReports answers
	// them 404, as a Cloud from before them does.
	Reports   map[string]cloudapi.LabReport
	NoReports bool
	// Confirmed are the reported addresses' states other than waiting
	// (confirmed, declined), by address; the first 10 reported are kept,
	// the rest not, as Cloud does (LabInfo.Recipients). AskedAt is when
	// each kept one was asked to confirm (zero: held back).
	Confirmed map[string]string
	AskedAt   time.Time
	// Bans are each relayed lab's CrowdSec bans as last sent.
	Bans map[string][]string
	// RelayStats is what the relay's statistics answer, and StatsQuery
	// the query it was last asked with.
	RelayStats cloudapi.RelayStats
	StatsQuery string
	// DomainVerified says a relayed lab's own domain is proven (its TXT
	// record found).
	DomainVerified bool
	// StorageQuota is Cloud backup's quota (0: not open), StorageUsed
	// what's stored, and StorageAsked the key requests.
	StorageQuota, StorageUsed int64
	StorageAsked              []cloudapi.StorageRequest
	// Identities are the labs' recovery identities (public keys), by lab
	// ID, unlinked labs' included; RestoreRepositories what a proven
	// restore is told is there, by lab ID; and Proven each proof's
	// purpose, in order.
	Identities          map[string][]byte
	RestoreRepositories map[string][]string
	Proven              []string
	// RestoreStatus, if set, is how every restore request is answered
	// (429: rate limited).
	RestoreStatus int
	challenges    map[string][]byte

	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey

	mu    sync.Mutex
	links map[string]*link // by secret
	labs  map[string]*lab  // by lab ID
	// unlinked are the labs unlinked, by lab ID: their backups can still
	// be restored.
	unlinked map[string]*lab
	// names are claimed names, by lab ID.
	names map[string]*claimed
	// Records are the claimed names' records ("fqdn TYPE" → value), and
	// TXT the challenge records written through acme-dns (by subdomain).
	Records map[string]string
	TXT     map[string][]string
	// Requests counts requests by "METHOD path".
	Requests map[string]int
}

type link struct {
	code, name, labName string
	returnURL           string
	csr                 *x509.CertificateRequest
	expires             time.Time
	state               string
	labID               string
	cert                string
}

// claimed is a lab's roost.ac name and its acme-dns account.
type claimed struct {
	fqdn, user, pass, sub string
}

type lab struct {
	name    string
	serials map[string]bool // certificates it may use
	// linked and unlinked are when (unlinked: zero while it's linked).
	linked, unlinked time.Time
}

// New makes a fake Cloud with its own certificate authority.
func New() *Fake {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "Fake Roostack Cloud"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	ca, _ := x509.ParseCertificate(der)
	return &Fake{Account: "o••••@example.com", Taken: map[string]bool{"taken": true}, Held: map[string]bool{"held": true}, CertLife: 90 * 24 * time.Hour,
		caCert: ca, caKey: key, links: map[string]*link{}, labs: map[string]*lab{}, Requests: map[string]int{},
		NameSuffix: "roost.test", names: map[string]*claimed{}, Records: map[string]string{}, TXT: map[string][]string{}}
}

// CAPEM is the fake's certificate authority, which signs its HTTPS
// certificate (config.Cloud.CAFile).
func (f *Fake) CAPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.caCert.Raw})
}

// TLSConfig serves HTTPS for hosts (names or IP addresses), asking for
// lab certificates without requiring one.
func (f *Fake) TLSConfig(hosts ...string) *tls.Config {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: hosts[0]},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, &key.PublicKey, f.caKey)
	if err != nil {
		panic(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(f.caCert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: pool}
}

// Confirm confirms a link by its code, as the Owner would in Cloud.
func (f *Fake) Confirm(code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.links {
		if l.code == code && l.state == cloudapi.StateWaiting && time.Now().Before(l.expires) {
			return f.confirm(l)
		}
	}
	return fmt.Errorf("no link waiting with code %s", code)
}

// Decline cancels a link by its code, as the Owner would on Cloud's
// page: the lab hears it has expired.
func (f *Fake) Decline(code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.links {
		if l.code == code && l.state == cloudapi.StateWaiting {
			l.state = cloudapi.StateExpired
		}
	}
}

func (f *Fake) confirm(l *link) error {
	l.labID = "lab-" + hex.EncodeToString(serial().Bytes()[:4])
	f.labs[l.labID] = &lab{name: l.labName, serials: map[string]bool{}, linked: time.Now()}
	cert, err := f.issue(l.labID, l.csr)
	if err != nil {
		return err
	}
	l.state, l.cert = cloudapi.StateLinked, cert
	return nil
}

// Asked is what the lab waiting with code sent: its name and the
// roost.ac name it asked for.
func (f *Fake) Asked(code string) (labName, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.links {
		if l.code == code {
			return l.labName, l.name
		}
	}
	return "", ""
}

// Return is where the link with code sends the browser back to.
func (f *Fake) Return(code string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.links {
		if l.code == code {
			return l.returnURL
		}
	}
	return ""
}

// Codes are the codes of links waiting to be confirmed.
func (f *Fake) Codes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, l := range f.links {
		if l.state == cloudapi.StateWaiting {
			out = append(out, l.code)
		}
	}
	return out
}

// Unlink removes a lab, as its account's page would: its certificates
// stop working.
func (f *Fake) Unlink(labID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if lb := f.labs[labID]; lb != nil {
		lb.unlinked = time.Now()
		if f.unlinked == nil {
			f.unlinked = map[string]*lab{}
		}
		f.unlinked[labID] = lb
	}
	delete(f.labs, labID)
}

// Revoke stops a lab's certificates working, the lab still linked (as
// when one expired while the lab was off).
func (f *Fake) Revoke(labID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if lb := f.labs[labID]; lb != nil {
		lb.serials = map[string]bool{}
	}
}

// Labs are the linked labs' IDs.
func (f *Fake) Labs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id := range f.labs {
		out = append(out, id)
	}
	return out
}

// issue signs a lab certificate (f.mu held).
func (f *Fake) issue(labID string, csr *x509.CertificateRequest) (string, error) {
	sn := serial()
	tmpl := &x509.Certificate{SerialNumber: sn, Subject: pkix.Name{CommonName: labID},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(f.CertLife),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, csr.PublicKey, f.caKey)
	if err != nil {
		return "", err
	}
	f.labs[labID].serials[sn.Text(16)] = true
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}

// ServeHTTP answers the lab's API and the confirm page.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.Requests[r.Method+" "+r.URL.Path]++
	f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && r.URL.Path == cloudapi.PathLinks:
		f.startLink(w, r)
	case r.Method == http.MethodPost && r.URL.Path == cloudapi.PathLinkStatus:
		f.linkStatus(w, r)
	case r.Method == http.MethodPost && r.URL.Path == cloudapi.PathLinkCancel:
		f.cancelLink(w, r)
	case r.Method == http.MethodGet && r.URL.Path == cloudapi.PathInfo:
		reply(w, cloudapi.Info{NameSuffix: f.NameSuffix})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, cloudapi.PathNames):
		name := strings.TrimPrefix(r.URL.Path, cloudapi.PathNames)
		if f.Held[name] {
			reply(w, cloudapi.NameAvailability{Held: true, Reason: name + ".roost.ac is kept for the account that had it."})
		} else if f.Taken[name] {
			reply(w, cloudapi.NameAvailability{Available: false, Reason: name + ".roost.ac is taken."})
		} else {
			reply(w, cloudapi.NameAvailability{Available: true})
		}
	case r.URL.Path == cloudapi.PathLab || r.URL.Path == cloudapi.PathLabRenew || r.URL.Path == cloudapi.PathLabName ||
		r.URL.Path == cloudapi.PathLabRelay || r.URL.Path == cloudapi.PathLabRelayBans || r.URL.Path == cloudapi.PathLabRelayStats ||
		r.URL.Path == cloudapi.PathLabStorageKeys || r.URL.Path == cloudapi.PathLabStorage || r.URL.Path == cloudapi.PathLabRecoveryIdentity ||
		r.URL.Path == cloudapi.PathLabReport && !f.NoReports:
		f.labAPI(w, r)
	case r.Method == http.MethodPost && (r.URL.Path == cloudapi.PathRestoreChallenge || r.URL.Path == cloudapi.PathRestoreAccess ||
		r.URL.Path == cloudapi.PathRestoreRelink):
		f.restore(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/update":
		f.acmeUpdate(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/records/"):
		f.records(w, r)
	case strings.HasPrefix(r.URL.Path, "/link/"):
		f.page(w, r)
	default:
		fail(w, http.StatusNotFound, "not found")
	}
}

func (f *Fake) startLink(w http.ResponseWriter, r *http.Request) {
	var req cloudapi.LinkRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		fail(w, http.StatusBadRequest, "bad request")
		return
	}
	csr, err := x509.ParseCertificateRequest(req.CSR)
	if err != nil || csr.CheckSignature() != nil {
		fail(w, http.StatusBadRequest, "bad certificate request")
		return
	}
	if f.Taken[req.Name] {
		fail(w, http.StatusConflict, req.Name+".roost.ac is taken.")
		return
	}
	code := strings.ToUpper(hex.EncodeToString(serial().Bytes()[:3]))
	code = code[:3] + "-" + code[3:]
	secret := hex.EncodeToString(serial().Bytes())
	l := &link{code: code, name: req.Name, labName: req.LabName, returnURL: req.ReturnURL, csr: csr, expires: time.Now().Add(10 * time.Minute), state: cloudapi.StateWaiting}
	f.mu.Lock()
	f.links[secret] = l
	f.mu.Unlock()
	page := f.PageURL
	if page == "" {
		page = "https://" + r.Host
	}
	reply(w, cloudapi.LinkStarted{Code: code, OpenURL: page + "/link/" + code, Secret: secret, Expires: l.expires})
}

func (f *Fake) secretLink(r *http.Request) *link {
	var req cloudapi.LinkSecret
	json.NewDecoder(r.Body).Decode(&req)
	return f.links[req.Secret]
}

func (f *Fake) linkStatus(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.secretLink(r)
	switch {
	case l == nil:
		fail(w, http.StatusNotFound, "no such link")
	case l.state == cloudapi.StateWaiting && time.Now().After(l.expires):
		reply(w, cloudapi.LinkStatus{State: cloudapi.StateExpired})
	case l.state == cloudapi.StateLinked:
		reply(w, cloudapi.LinkStatus{State: cloudapi.StateLinked, Certificate: l.cert,
			LabInfo: cloudapi.LabInfo{LabID: l.labID, Account: f.Account}})
	default:
		reply(w, cloudapi.LinkStatus{State: l.state})
	}
}

func (f *Fake) cancelLink(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l := f.secretLink(r); l != nil && l.state == cloudapi.StateWaiting {
		l.state = cloudapi.StateExpired
	}
	reply(w, map[string]any{})
}

// labAPI is the lab's own endpoints: its client certificate says which
// lab it is.
func (f *Fake) labAPI(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		fail(w, http.StatusUnauthorized, "a lab certificate is needed")
		return
	}
	c := r.TLS.PeerCertificates[0]
	id := c.Subject.CommonName
	lb := f.labs[id]
	if lb == nil || !lb.serials[c.SerialNumber.Text(16)] {
		fail(w, http.StatusUnauthorized, "this lab isn't linked")
		return
	}
	if f.Suspended {
		fail(w, http.StatusLocked, "This account is suspended.")
		return
	}
	if f.SuspendedFor != "" && r.URL.Path != cloudapi.PathLab && r.URL.Path != cloudapi.PathLabReport && r.URL.Path != cloudapi.PathLabRenew {
		fail(w, http.StatusLocked, "This lab's Roostack Cloud account is suspended: "+f.SuspendedFor+". Its Owner can see why at roost.ac.")
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == cloudapi.PathLab:
		reply(w, cloudapi.LabInfo{LabID: id, Account: f.Account, Suspended: f.SuspendedFor, Names: f.NameStates,
			Recipients: f.recipients(id)})
	case r.Method == http.MethodPut && r.URL.Path == cloudapi.PathLabReport:
		var req cloudapi.LabReport
		json.NewDecoder(r.Body).Decode(&req)
		if f.Reports == nil {
			f.Reports = map[string]cloudapi.LabReport{}
		}
		f.Reports[id] = req
		reply(w, map[string]any{})
	case r.Method == http.MethodPut && r.URL.Path == cloudapi.PathLabRecoveryIdentity:
		var req cloudapi.RecoveryIdentity
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.PublicKey) != ed25519.PublicKeySize {
			fail(w, http.StatusBadRequest, "not a public key")
			return
		}
		if !ed25519.Verify(req.PublicKey, cloudapi.RestoreMessage(cloudapi.ProveRegister, id), req.Signature) {
			fail(w, http.StatusBadRequest, "The registration isn't signed with the recovery identity's key.")
			return
		}
		if f.Identities == nil {
			f.Identities = map[string][]byte{}
		}
		f.Identities[id] = req.PublicKey
		reply(w, map[string]any{})
	case r.Method == http.MethodPut && r.URL.Path == cloudapi.PathLabRelay:
		var req cloudapi.RelayRequest
		json.NewDecoder(r.Body).Decode(&req)
		if f.NoRelay {
			fail(w, http.StatusBadRequest, "Roostack Cloud's relay isn't open for this account yet.")
			return
		}
		if f.Relayed == nil {
			f.Relayed = map[string]cloudapi.RelayRequest{}
		}
		f.Relayed[id] = req
		if req.TurnOn {
			f.RelayOff = time.Time{}
		}
		reply(w, f.relay(req))
	case r.Method == http.MethodGet && r.URL.Path == cloudapi.PathLabRelay:
		req, ok := f.Relayed[id]
		if !ok {
			fail(w, http.StatusNotFound, "This lab isn't relayed.")
			return
		}
		reply(w, f.relay(req))
	case r.Method == http.MethodDelete && r.URL.Path == cloudapi.PathLabRelay:
		delete(f.Relayed, id)
		delete(f.Bans, id)
		reply(w, map[string]any{})
	case r.Method == http.MethodPost && r.URL.Path == cloudapi.PathLabStorageKeys:
		var req cloudapi.StorageRequest
		json.NewDecoder(r.Body).Decode(&req)
		if f.StorageQuota <= 0 {
			fail(w, http.StatusBadRequest, "Roostack Cloud backup isn't open for this account yet.")
			return
		}
		f.StorageAsked = append(f.StorageAsked, req)
		prefix := "labs/" + id + "/"
		if req.Server != "" {
			prefix += req.Server + "/"
		}
		reply(w, cloudapi.StorageKeys{Repository: "s3:https://s3.test/backups/" + strings.TrimSuffix(prefix, "/"), Prefix: prefix,
			AccessKeyID: "TEMP", SecretAccessKey: "s", SessionToken: "tok", Region: "test", Expires: time.Now().Add(12 * time.Hour)})
	case r.Method == http.MethodGet && r.URL.Path == cloudapi.PathLabStorage:
		reply(w, cloudapi.StorageUsage{QuotaBytes: f.StorageQuota, UsedBytes: f.StorageUsed, Servers: []cloudapi.ServerStorage{}})
	case r.Method == http.MethodGet && r.URL.Path == cloudapi.PathLabRelayStats:
		f.StatsQuery = r.URL.RawQuery
		reply(w, f.RelayStats)
	case r.Method == http.MethodPut && r.URL.Path == cloudapi.PathLabRelayBans:
		if _, ok := f.Relayed[id]; !ok {
			fail(w, http.StatusNotFound, "This lab isn't relayed.")
			return
		}
		var req cloudapi.RelayBans
		json.NewDecoder(r.Body).Decode(&req)
		if f.Bans == nil {
			f.Bans = map[string][]string{}
		}
		f.Bans[id] = req.Bans
		reply(w, map[string]any{})
	case r.Method == http.MethodDelete && r.URL.Path == cloudapi.PathLab:
		delete(f.labs, id)
		delete(f.names, id)
		reply(w, map[string]any{})
	case r.Method == http.MethodPost && r.URL.Path == cloudapi.PathLabName:
		var req cloudapi.NameRequest
		json.NewDecoder(r.Body).Decode(&req)
		// A lab's own name isn't taken for it (as Cloud's claim).
		if cur := f.names[id]; f.Taken[req.Name] && (cur == nil || cur.fqdn != req.Name+"."+f.NameSuffix) {
			fail(w, http.StatusConflict, req.Name+".roost.ac is taken.")
			return
		}
		c := &claimed{fqdn: req.Name + "." + f.NameSuffix, user: hex.EncodeToString(serial().Bytes()[:8]),
			pass: hex.EncodeToString(serial().Bytes()), sub: hex.EncodeToString(serial().Bytes()[:8])}
		f.names[id] = c
		base := f.ChallengeURL
		if base == "" {
			base = "https://" + r.Host
		}
		claim := cloudapi.NameClaim{Name: c.fqdn, Credentials: map[string]string{cloudapi.CredServerURL: base,
			cloudapi.CredUsername: c.user, cloudapi.CredPassword: c.pass, cloudapi.CredSubdomain: c.sub,
			cloudapi.CredRecordsURL: base + "/v1/records"}}
		for _, n := range f.NameStates {
			if n.Host == c.fqdn {
				claim.Status = &n
			}
		}
		reply(w, claim)
	case r.Method == http.MethodPost && r.URL.Path == cloudapi.PathLabRenew:
		var req cloudapi.RenewRequest
		json.NewDecoder(r.Body).Decode(&req)
		csr, err := x509.ParseCertificateRequest(req.CSR)
		if err != nil || csr.CheckSignature() != nil {
			fail(w, http.StatusBadRequest, "bad certificate request")
			return
		}
		cert, err := f.issue(id, csr)
		if err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		reply(w, cloudapi.RenewResponse{Certificate: cert})
	default:
		fail(w, http.StatusNotFound, "not found")
	}
}

// account finds the claimed name an acme-dns account belongs to (f.mu
// held).
func (f *Fake) account(r *http.Request) *claimed {
	for _, c := range f.names {
		if c.user == r.Header.Get(cloudapi.HeaderUser) && c.pass == r.Header.Get(cloudapi.HeaderKey) {
			return c
		}
	}
	return nil
}

// acmeUpdate is acme-dns's /update: it sets the account's challenge TXT
// record (acme-dns keeps the latest two).
func (f *Fake) acmeUpdate(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.account(r)
	var req cloudapi.ACMEUpdate
	json.NewDecoder(r.Body).Decode(&req)
	if c == nil || req.Subdomain != c.sub {
		fail(w, http.StatusUnauthorized, "forbidden")
		return
	}
	t := append(f.TXT[c.sub], req.TXT)
	if len(t) > 2 {
		t = t[len(t)-2:]
	}
	f.TXT[c.sub] = t
	reply(w, map[string]string{"txt": req.TXT})
}

// records is Cloud's records API for a claimed name: /v1/records/<name
// relative to it, "@" for itself>/<type>. Only A records inside the name.
func (f *Fake) records(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.account(r)
	name, typ, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/records/"), "/")
	if c == nil || !ok {
		fail(w, http.StatusUnauthorized, "forbidden")
		return
	}
	if typ != "A" {
		fail(w, http.StatusBadRequest, "only A records")
		return
	}
	fqdn := c.fqdn
	if name != "@" {
		fqdn = name + "." + c.fqdn
	}
	key := fqdn + " " + typ
	switch r.Method {
	case http.MethodGet:
		if v, ok := f.Records[key]; ok {
			reply(w, cloudapi.RecordValues{Values: []string{v}})
		} else {
			reply(w, cloudapi.RecordValues{Values: []string{}})
		}
	case http.MethodPut:
		var req cloudapi.RecordValue
		json.NewDecoder(r.Body).Decode(&req)
		if net.ParseIP(req.Value) == nil {
			fail(w, http.StatusBadRequest, "not an address")
			return
		}
		f.Records[key] = req.Value
		reply(w, map[string]any{})
	default:
		fail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// Name is the name lab claimed, or "".
func (f *Fake) Name(labID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.names[labID]; c != nil {
		return c.fqdn
	}
	return ""
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html><meta charset="utf-8">
<meta name="viewport" content="width=device-width"><title>Fake Roostack Cloud</title>
<body style="font-family:sans-serif;max-width:36rem;margin:2rem auto;padding:0 1rem">
<p><b>Fake Roostack Cloud</b> (for development)</p>
{{if .Done}}<h1>Linked</h1><p>{{.Lab}} is linked to {{.Account}}. You can close this window.</p>
{{else if .Lab}}<h1>Link a lab?</h1>
<p><b>{{.Lab}}</b> asks to be linked to {{.Account}}{{if .Name}}, with the name <b>{{.Name}}.roost.ac</b>{{end}}.</p>
<p>Check the lab shows this code: <b style="font-family:monospace;font-size:1.4em">{{.Code}}</b></p>
<form method="post"><button>Confirm</button></form>
{{else}}<h1>No such code</h1><p>It expired, or was already used.</p>{{end}}`))

func (f *Fake) page(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimPrefix(r.URL.Path, "/link/")
	f.mu.Lock()
	var l *link
	for _, x := range f.links {
		if x.code == code {
			l = x
		}
	}
	data := map[string]any{"Code": code, "Account": f.Account}
	if l != nil && (l.state == cloudapi.StateWaiting && time.Now().Before(l.expires) || l.state == cloudapi.StateLinked) {
		data["Lab"], data["Name"] = l.labName, l.name
		if r.Method == http.MethodPost && l.state == cloudapi.StateWaiting {
			if err := f.confirm(l); err != nil {
				f.mu.Unlock()
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		data["Done"] = l.state == cloudapi.StateLinked
	}
	f.mu.Unlock()
	pageTmpl.Execute(w, data)
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(cloudapi.ErrorBody{Error: msg})
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if n.BitLen() < 64 {
		n.SetBit(n, 100, 1)
	}
	return n
}

// Start runs a fake Cloud over HTTPS for a test, and returns the lab
// settings that reach it.
func Start(t testing.TB) (*Fake, config.Cloud) {
	t.Helper()
	f := New()
	srv := httptest.NewUnstartedServer(f)
	srv.TLS = f.TLSConfig("127.0.0.1")
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, f.CAPEM(), 0o644); err != nil {
		t.Fatal(err)
	}
	return f, config.Cloud{URL: srv.URL, CAFile: ca}
}

// relay is the fake's answer to a relay request: one edge, a relay name,
// and the own domain's proof (verified as DomainVerified says).
func (f *Fake) relay(req cloudapi.RelayRequest) cloudapi.Relay {
	r := cloudapi.Relay{Addr: "100.81.1.1", Prefix: 16, Name: "r-test.relay.roost.test", Edges: []cloudapi.RelayEdge{{Name: "syd-1",
		Endpoint: "198.51.100.10:51830", PublicKey: "ZWRnZWVkZ2VlZGdlZWRnZWVkZ2VlZGdlZWRnZWVkZ2U=", Addr: "100.81.0.1",
		PublicIPv4: "198.51.100.10"}}, TurnedOff: f.RelayOff, Suspended: f.RelaySuspended, Names: f.NameStates}
	if req.Domain != "" {
		r.Domain = &cloudapi.RelayDomain{Domain: req.Domain, ProofName: "_roostack." + req.Domain, ProofValue: "roostack-proof=test",
			Verified: f.DomainVerified}
		if !f.DomainVerified {
			r.Domain.Problem = "There's no TXT record at _roostack." + req.Domain + " yet."
		}
	}
	return r
}

// restore answers the recovery identity's endpoints: a challenge for a
// registered key, then keys to read the lab's backups or a new
// certificate, for a signed challenge (once).
func (f *Fake) restore(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RestoreStatus != 0 {
		fail(w, f.RestoreStatus, "Too many requests from this address. Try again in a minute.")
		return
	}
	// labsOf lists the labs with the identity, as Cloud does: the linked
	// one first, then those unlinked, the latest first.
	labsOf := func(pub []byte) []string {
		var out []string
		for id, k := range f.Identities {
			if bytes.Equal(k, pub) && (f.labs[id] != nil || f.unlinked[id] != nil) {
				out = append(out, id)
			}
		}
		slices.SortFunc(out, func(a, b string) int {
			la, lb := f.labs[a], f.labs[b]
			switch {
			case la != nil && lb == nil:
				return -1
			case la == nil && lb != nil:
				return 1
			case la == nil:
				return f.unlinked[b].unlinked.Compare(f.unlinked[a].unlinked)
			}
			return strings.Compare(a, b)
		})
		return out
	}
	if r.URL.Path == cloudapi.PathRestoreChallenge {
		var req cloudapi.RestoreChallengeRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(labsOf(req.PublicKey)) == 0 {
			fail(w, http.StatusNotFound, "No lab in Roostack Cloud has this recovery key.")
			return
		}
		b := make([]byte, 24)
		rand.Read(b)
		ch := hex.EncodeToString(b)
		if f.challenges == nil {
			f.challenges = map[string][]byte{}
		}
		f.challenges[ch] = req.PublicKey
		reply(w, cloudapi.RestoreChallenge{Challenge: ch, Expires: time.Now().Add(5 * time.Minute)})
		return
	}
	var req cloudapi.RelinkRequest
	json.NewDecoder(r.Body).Decode(&req)
	purpose := cloudapi.ProveAccess
	if r.URL.Path == cloudapi.PathRestoreRelink {
		purpose = cloudapi.ProveRelink
	}
	pub, ok := f.challenges[req.Challenge]
	delete(f.challenges, req.Challenge)
	if !ok || !bytes.Equal(pub, req.PublicKey) || len(pub) != ed25519.PublicKeySize ||
		!ed25519.Verify(pub, cloudapi.RestoreMessage(purpose, req.Challenge), req.Signature) {
		fail(w, http.StatusUnauthorized, "That doesn't prove the recovery key.")
		return
	}
	ids := labsOf(pub)
	f.Proven = append(f.Proven, purpose)
	if purpose == cloudapi.ProveAccess {
		var acc cloudapi.RestoreAccess
		for _, id := range ids {
			lb := cmp.Or(f.labs[id], f.unlinked[id])
			acc.Labs = append(acc.Labs, cloudapi.RestoreLab{LabID: id, LabName: lb.name, LinkedAt: lb.linked, UnlinkedAt: lb.unlinked,
				Repositories: f.RestoreRepositories[id],
				Keys: cloudapi.StorageKeys{AccessKeyID: "fake-restore-" + id, SecretAccessKey: "fake", SessionToken: "fake", Region: "test",
					Expires: time.Now().Add(12 * time.Hour)}})
		}
		reply(w, acc)
		return
	}
	id := ""
	if len(ids) > 0 {
		id = ids[0]
	}
	if f.labs[id] == nil {
		fail(w, http.StatusBadRequest, "This lab was unlinked from Roostack Cloud: link it again.")
		return
	}
	csr, err := x509.ParseCertificateRequest(req.CSR)
	if err != nil || csr.CheckSignature() != nil {
		fail(w, http.StatusBadRequest, "bad certificate request")
		return
	}
	cert, err := f.issue(id, csr)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	reply(w, cloudapi.Relinked{LabInfo: cloudapi.LabInfo{LabID: id, Account: f.Account}, Certificate: cert})
}

// recipients is how the addresses of the lab's last report stand (f.mu
// held): none until it reports.
func (f *Fake) recipients(id string) []cloudapi.RecipientStatus {
	var out []cloudapi.RecipientStatus
	for i, e := range f.Reports[id].Emails {
		r := cloudapi.RecipientStatus{Email: e, State: cloudapi.RecipientWaiting, Asked: f.AskedAt}
		if st, ok := f.Confirmed[e]; ok {
			r.State = st
		}
		if i >= 10 {
			r = cloudapi.RecipientStatus{Email: e, State: cloudapi.RecipientNotKept}
		}
		out = append(out, r)
	}
	return out
}
