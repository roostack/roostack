// Package cloudtest is a fake of Roostack Cloud's API for labs, for tests
// and for trying linking on a dev VM (cmd/roostack-fakecloud). It has
// none of Cloud's logic: one pretend account, names taken from a list,
// and a page with a Confirm button (SPEC.md §6.6). It's replaced by the
// real endpoints as soon as they exist.
package cloudtest

import (
	"crypto/ecdsa"
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
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
)

// Fake is a pretend Roostack Cloud.
type Fake struct {
	// Account is how the one account is shown to labs.
	Account string
	// Taken are roost.ac names that aren't available.
	Taken map[string]bool
	// CertLife is how long lab certificates last.
	CertLife time.Duration
	// PageURL, if set, is the address people open the confirm page at
	// (otherwise the address the lab reached).
	PageURL string

	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey

	mu    sync.Mutex
	links map[string]*link // by secret
	labs  map[string]*lab  // by lab ID
	// Requests counts requests by "METHOD path".
	Requests map[string]int
}

type link struct {
	code, name, labName string
	csr                 *x509.CertificateRequest
	expires             time.Time
	state               string
	labID               string
	cert                string
}

type lab struct {
	name    string
	serials map[string]bool // certificates it may use
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
	return &Fake{Account: "o••••@example.com", Taken: map[string]bool{"taken": true}, CertLife: 90 * 24 * time.Hour,
		caCert: ca, caKey: key, links: map[string]*link{}, labs: map[string]*lab{}, Requests: map[string]int{}}
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
		if l.code == code && l.state == "waiting" && time.Now().Before(l.expires) {
			return f.confirm(l)
		}
	}
	return fmt.Errorf("no link waiting with code %s", code)
}

func (f *Fake) confirm(l *link) error {
	l.labID = "lab-" + hex.EncodeToString(serial().Bytes()[:4])
	f.labs[l.labID] = &lab{name: l.labName, serials: map[string]bool{}}
	cert, err := f.issue(l.labID, l.csr)
	if err != nil {
		return err
	}
	l.state, l.cert = "linked", cert
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

// Codes are the codes of links waiting to be confirmed.
func (f *Fake) Codes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, l := range f.links {
		if l.state == "waiting" {
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
	delete(f.labs, labID)
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
	case r.Method == http.MethodPost && r.URL.Path == "/v1/links":
		f.startLink(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/links/status":
		f.linkStatus(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/links/cancel":
		f.cancelLink(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/names/"):
		name := strings.TrimPrefix(r.URL.Path, "/v1/names/")
		if f.Taken[name] {
			reply(w, map[string]any{"available": false, "reason": name + ".roost.ac is taken."})
		} else {
			reply(w, map[string]any{"available": true})
		}
	case r.URL.Path == "/v1/lab" || r.URL.Path == "/v1/lab/renew":
		f.labAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/link/"):
		f.page(w, r)
	default:
		fail(w, http.StatusNotFound, "not found")
	}
}

func (f *Fake) startLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LabName string `json:"lab_name"`
		Name    string `json:"name"`
		Version string `json:"version"`
		CSR     []byte `json:"csr"`
	}
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
	l := &link{code: code, name: req.Name, labName: req.LabName, csr: csr, expires: time.Now().Add(10 * time.Minute), state: "waiting"}
	f.mu.Lock()
	f.links[secret] = l
	f.mu.Unlock()
	page := f.PageURL
	if page == "" {
		page = "https://" + r.Host
	}
	reply(w, map[string]any{"code": code, "open_url": page + "/link/" + code, "secret": secret, "expires": l.expires})
}

func (f *Fake) secretLink(r *http.Request) *link {
	var req struct {
		Secret string `json:"secret"`
	}
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
	case l.state == "waiting" && time.Now().After(l.expires):
		reply(w, map[string]any{"state": "expired"})
	case l.state == "linked":
		reply(w, map[string]any{"state": "linked", "certificate": l.cert, "lab_id": l.labID, "account": f.Account})
	default:
		reply(w, map[string]any{"state": l.state})
	}
}

func (f *Fake) cancelLink(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l := f.secretLink(r); l != nil && l.state == "waiting" {
		l.state = "expired"
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
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/lab":
		reply(w, map[string]any{"lab_id": id, "account": f.Account})
	case r.Method == http.MethodDelete && r.URL.Path == "/v1/lab":
		delete(f.labs, id)
		reply(w, map[string]any{})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/lab/renew":
		var req struct {
			CSR []byte `json:"csr"`
		}
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
		reply(w, map[string]any{"certificate": cert})
	default:
		fail(w, http.StatusNotFound, "not found")
	}
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
	if l != nil && (l.state == "waiting" && time.Now().Before(l.expires) || l.state == "linked") {
		data["Lab"], data["Name"] = l.labName, l.name
		if r.Method == http.MethodPost && l.state == "waiting" {
			if err := f.confirm(l); err != nil {
				f.mu.Unlock()
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		data["Done"] = l.state == "linked"
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
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
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
