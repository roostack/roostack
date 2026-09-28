// Package labnet is the network between a lab's servers (SPEC.md §10.2):
// the controller's certificate authority, each server's certificate,
// join codes, and the mutual TLS every controller ↔ node request uses on
// top of WireGuard.
package labnet

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"time"
)

// Validity periods. Servers renew their certificates when RenewBefore is
// left (renew.go); the CA isn't renewed yet.
const (
	caValidity   = 20 * 365 * 24 * time.Hour
	certValidity = 365 * 24 * time.Hour
	// RenewBefore is how long before expiry a certificate is renewed.
	RenewBefore = 120 * 24 * time.Hour
)

// CA is the controller's certificate authority. Its key never leaves the
// controller.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// CADir is where the controller keeps its CA.
func CADir(stateDir string) string { return filepath.Join(stateDir, "labca") }

// LoadOrCreateCA reads the CA in dir, creating it on first use.
func LoadOrCreateCA(dir, labName string) (*CA, error) {
	certFile, keyFile := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if _, err := os.Stat(certFile); err == nil {
		cert, err := readCert(certFile)
		if err != nil {
			return nil, err
		}
		key, err := readKey(keyFile)
		if err != nil {
			return nil, err
		}
		return &CA{Cert: cert, Key: key}, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Roostack " + labName + " servers"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := writeKey(keyFile, key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certFile, certPEM(der), 0o644); err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key}, nil
}

// Fingerprint identifies the CA in join codes (SHA-256 of its certificate).
func (ca *CA) Fingerprint() string { return Fingerprint(ca.Cert) }

// Fingerprint is a certificate's SHA-256, base64url.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Issue signs a server's certificate: its node ID as the name and its
// overlay address, usable as both TLS server and client.
func (ca *CA) Issue(csrDER []byte, nodeID string, addr netip.Addr) ([]byte, *x509.Certificate, error) {
	return ca.issue(csrDER, nodeID, addr, certValidity)
}

func (ca *CA) issue(csrDER []byte, nodeID string, addr netip.Addr, validity time.Duration) ([]byte, *x509.Certificate, error) {
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: nodeID},
		DNSNames:     []string{nodeID},
		IPAddresses:  []net.IP{addr.AsSlice()},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, csr.PublicKey, ca.Key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return certPEM(der), cert, nil
}

// Serial is how the controller records a certificate.
func Serial(c *x509.Certificate) string { return c.SerialNumber.Text(16) }

// Identity is this server's key and certificate on the lab network, with
// the CA that signed it. Every server, the controller included, has one.
type Identity struct {
	Dir string
}

// IdentityDir is where a server keeps its network identity.
func IdentityDir(stateDir string) string { return filepath.Join(stateDir, "labnet") }

func (id Identity) keyFile() string  { return filepath.Join(id.Dir, "node.key") }
func (id Identity) certFile() string { return filepath.Join(id.Dir, "node.crt") }
func (id Identity) caFile() string   { return filepath.Join(id.Dir, "ca.crt") }

// CSR creates this server's key (if needed) and a signing request.
func (id Identity) CSR(nodeID string) ([]byte, error) {
	key, err := readKey(id.keyFile())
	if errors.Is(err, os.ErrNotExist) {
		if key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(id.Dir, 0o700); err != nil {
			return nil, err
		}
		err = writeKey(id.keyFile(), key)
	}
	if err != nil {
		return nil, err
	}
	return x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: nodeID}}, key)
}

// Save stores the signed certificate and the CA.
func (id Identity) Save(certPEMBytes, caPEM []byte) error {
	if err := writeFile(id.certFile(), certPEMBytes); err != nil {
		return err
	}
	return writeFile(id.caFile(), caPEM)
}

// Cert is this server's current certificate.
func (id Identity) Cert() (*x509.Certificate, error) { return readCert(id.certFile()) }

// NeedsRenewal reports whether the certificate should be renewed: it
// expires within RenewBefore, or was issued for longer than certificates
// are now (before renewal existed).
func (id Identity) NeedsRenewal(now time.Time) (bool, error) {
	c, err := id.Cert()
	if err != nil {
		return false, err
	}
	return c.NotAfter.Sub(now) < RenewBefore || c.NotAfter.Sub(c.NotBefore) > certValidity+24*time.Hour, nil
}

// writeFile replaces a file in one step, so a reader never sees half of it.
func writeFile(file string, data []byte) error {
	tmp := file + ".new"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// Exists reports whether the server has a signed certificate.
func (id Identity) Exists() bool {
	_, err := os.Stat(id.certFile())
	return err == nil
}

// CertPEM encodes a certificate.
func CertPEM(c *x509.Certificate) []byte { return certPEM(c.Raw) }

func certPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func readCert(file string) (*x509.Certificate, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(data)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s: not a PEM certificate", file)
	}
	return x509.ParseCertificate(b.Bytes)
}

func readKey(file string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(data)
	if b == nil {
		return nil, fmt.Errorf("%s: not a PEM key", file)
	}
	return x509.ParseECPrivateKey(b.Bytes)
}

func writeKey(file string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}
