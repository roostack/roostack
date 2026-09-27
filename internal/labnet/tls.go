package labnet

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"slices"
)

// Port is where every server serves the lab API, on its overlay address
// only (reachable over WireGuard).
const Port = 8471

// ServerTLS is the lab API's TLS: this server's certificate, and client
// certificates from the lab's CA. A client without a certificate gets
// through the handshake (it may be joining); handlers check PeerNode.
func (id Identity) ServerTLS() (*tls.Config, error) {
	cert, pool, err := id.load()
	if err != nil {
		return nil, err
	}
	ca, err := readCert(id.caFile())
	if err != nil {
		return nil, err
	}
	// Send the CA too, so a joining server can check it against the
	// fingerprint in its join code.
	cert.Certificate = append(cert.Certificate, ca.Raw)
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}, nil
}

// ClientTLS is for calling another server: it must present a certificate
// from the lab's CA naming peerID.
func (id Identity) ClientTLS(peerID string) (*tls.Config, error) {
	cert, pool, err := id.load()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   peerID,
	}, nil
}

// JoinTLS is for a server that is joining: it has no certificate yet and
// trusts the server only if its chain includes the CA named in the join
// code (by fingerprint).
func JoinTLS(caFingerprint, controllerID string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // checked in VerifyConnection instead
		VerifyConnection: func(cs tls.ConnectionState) error {
			i := slices.IndexFunc(cs.PeerCertificates, func(c *x509.Certificate) bool {
				return Fingerprint(c) == caFingerprint
			})
			if i < 0 || len(cs.PeerCertificates) == 0 {
				return errors.New("the controller's certificate authority doesn't match the join code")
			}
			pool := x509.NewCertPool()
			pool.AddCert(cs.PeerCertificates[i])
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: pool, DNSName: controllerID,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
			return err
		},
	}
}

// PeerNode returns the node ID of a verified client certificate, or "".
func PeerNode(cs *tls.ConnectionState) string {
	if cs == nil || len(cs.VerifiedChains) == 0 {
		return ""
	}
	return cs.VerifiedChains[0][0].Subject.CommonName
}

// CAPEM returns the CA certificate this server trusts.
func (id Identity) CAPEM() ([]byte, error) { return os.ReadFile(id.caFile()) }

func (id Identity) load() (tls.Certificate, *x509.CertPool, error) {
	cert, err := tls.LoadX509KeyPair(id.certFile(), id.keyFile())
	if err != nil {
		return cert, nil, fmt.Errorf("this server's lab certificate: %w", err)
	}
	ca, err := readCert(id.caFile())
	if err != nil {
		return cert, nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return cert, pool, nil
}
