package labnet

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/mylab/mylab/internal/agent"
)

// newServer creates a server identity signed by ca.
func newServer(t *testing.T, ca *CA, id string, addr netip.Addr) Identity {
	t.Helper()
	m := Identity{Dir: t.TempDir()}
	csr, err := m.CSR(id)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.Issue(csr, id, addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Save(cert, CertPEM(ca.Cert)); err != nil {
		t.Fatal(err)
	}
	return m
}

// handshake runs a TLS handshake and returns the client's view and the
// server's peer node.
func handshake(t *testing.T, server, client *tls.Config) (string, error) {
	t.Helper()
	a, b := net.Pipe()
	peer := make(chan string, 1)
	go func() {
		s := tls.Server(a, server)
		if err := s.Handshake(); err != nil {
			peer <- "error: " + err.Error()
			a.Close()
			return
		}
		st := s.ConnectionState()
		peer <- PeerNode(&st)
		io.Copy(io.Discard, s)
	}()
	c := tls.Client(b, client)
	err := c.Handshake()
	b.Close()
	return <-peer, err
}

func TestMutualTLS(t *testing.T) {
	ca, err := LoadOrCreateCA(t.TempDir(), "home")
	if err != nil {
		t.Fatal(err)
	}
	ctrl := newServer(t, ca, "n-0000000001", netip.MustParseAddr("100.96.0.1"))
	node := newServer(t, ca, "n-0000000002", netip.MustParseAddr("100.96.0.2"))
	srv, _ := ctrl.ServerTLS()

	cli, _ := node.ClientTLS("n-0000000001")
	if peer, err := handshake(t, srv, cli); err != nil || peer != "n-0000000002" {
		t.Errorf("node → controller: peer %q, err %v", peer, err)
	}
	// Expecting a different server fails.
	cli, _ = node.ClientTLS("n-0000000009")
	if _, err := handshake(t, srv, cli); err == nil {
		t.Error("connected to the wrong server")
	}
	// A certificate from another lab's CA isn't trusted.
	other, _ := LoadOrCreateCA(t.TempDir(), "other")
	stranger := newServer(t, other, "n-0000000003", netip.MustParseAddr("100.96.0.3"))
	cli, _ = stranger.ClientTLS("n-0000000001")
	if peer, err := handshake(t, srv, cli); err == nil && peer != "" {
		t.Errorf("a stranger was accepted as %q", peer)
	}

	// Joining: no certificate yet, the CA checked by fingerprint.
	if peer, err := handshake(t, srv, JoinTLS(ca.Fingerprint(), "n-0000000001")); err != nil || peer != "" {
		t.Errorf("join: peer %q, err %v", peer, err)
	}
	if _, err := handshake(t, srv, JoinTLS(other.Fingerprint(), "n-0000000001")); err == nil {
		t.Error("join must fail when the CA doesn't match the code")
	}
}

func TestCAReload(t *testing.T) {
	dir := t.TempDir()
	a, _ := LoadOrCreateCA(dir, "home")
	b, err := LoadOrCreateCA(dir, "home")
	if err != nil || a.Fingerprint() != b.Fingerprint() {
		t.Errorf("reloaded CA differs: %v", err)
	}
}

func TestJoinCode(t *testing.T) {
	key, _ := agent.NewWireGuardKey()
	ctrlKey, _ := agent.NewWireGuardKey()
	ctrlPub, _ := agent.WireGuardPublicKey(ctrlKey)
	token, hash := NewToken()
	c := JoinCode{Endpoint: "10.0.2.2:51820", ControllerKey: ctrlPub, ControllerID: "n-0000000001",
		ControllerAddr: netip.MustParseAddr("100.96.0.1"), Addr: netip.MustParseAddr("100.96.0.2"), PrefixBits: 24,
		JoinKey: key, CA: "fp", Token: token}
	s := c.String()
	if !strings.HasPrefix(s, "mylab-join-") || len(s) > 400 {
		t.Errorf("code %q (%d chars)", s, len(s))
	}
	got, err := ParseJoinCode("  " + s + "\n")
	if err != nil || got != c || HashToken(got.Token) != hash {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	for _, bad := range []string{"hello", s[:len(s)-10], "mylab-join-!!!"} {
		if _, err := ParseJoinCode(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	c.Endpoint = "evil\nPostUp=x:1"
	if _, err := ParseJoinCode(c.String()); err == nil || errors.Is(err, nil) {
		t.Error("a code with a bad endpoint must be rejected")
	}
}
