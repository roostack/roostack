package labnet

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/store"
)

// copyIdentity keeps a server's current certificate, as a removed or
// not-yet-renewed server would still have it.
func copyIdentity(t *testing.T, id Identity) Identity {
	t.Helper()
	dst := Identity{Dir: t.TempDir()}
	for _, f := range []string{"node.key", "node.crt", "ca.crt"} {
		data, err := os.ReadFile(filepath.Join(id.Dir, f))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dst.Dir, f), data, 0o600)
	}
	return dst
}

func TestCertRevocationAndRenewal(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := &Hub{StateDir: dir, LabName: "home", Self: nodeid.Identity{ID: "n-0000000001", Name: "nuc"}, Store: st, Agent: &fakeAgent{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now, SwitchDelay: time.Millisecond}
	ctx := context.Background()
	code, err := h.NewJoinCode(ctx, "10.0.2.2:51820", "")
	if err != nil {
		t.Fatal(err)
	}
	node := Identity{Dir: t.TempDir()}
	csr, _ := node.CSR("n-0000000002")
	key, _ := agent.NewWireGuardKey()
	pub, _ := agent.WireGuardPublicKey(key)
	resp, err := h.Join(ctx, JoinRequest{Token: code.Token, NodeID: "n-0000000002", Name: "vps", PublicKey: pub, CSR: csr})
	if err != nil {
		t.Fatal(err)
	}
	node.Save(resp.Cert, resp.CA)

	// The controller's lab API.
	mux := http.NewServeMux()
	mux.Handle("GET /v1/ping", h.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, Caller(r))
	})))
	mux.Handle("POST /v1/renew", h.Require(h.RenewHandler()))
	srv := httptest.NewUnstartedServer(mux)
	if srv.TLS, err = (Identity{Dir: IdentityDir(dir)}).ServerTLS(); err != nil {
		t.Fatal(err)
	}
	srv.StartTLS()
	defer srv.Close()
	call := func(id Identity) int {
		t.Helper()
		conf, err := id.ClientTLS("n-0000000001")
		if err != nil {
			t.Fatal(err)
		}
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: conf, DisableKeepAlives: true}}
		r, err := c.Get(srv.URL + "/v1/ping")
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if code := call(node); code != http.StatusOK {
		t.Fatalf("a joined server: %d", code)
	}

	// Fresh certificates aren't renewed; ones near expiry are.
	c, _ := node.Cert()
	if due, _ := node.NeedsRenewal(time.Now()); due || c.NotAfter.Sub(c.NotBefore) > certValidity+time.Hour {
		t.Errorf("a new certificate is valid for a year and not due: %v", c.NotAfter)
	}
	old := copyIdentity(t, node)
	renew := RenewFromController(srv.URL, "n-0000000001", node)
	if err := RenewIfDue(ctx, node, "n-0000000002", time.Now(), renew); err != nil {
		t.Fatal(err)
	}
	if c2, _ := node.Cert(); Serial(c2) != Serial(c) {
		t.Fatal("renewed too early")
	}
	if err := RenewIfDue(ctx, node, "n-0000000002", c.NotAfter.Add(-RenewBefore+time.Hour), renew); err != nil {
		t.Fatal(err)
	}
	c2, _ := node.Cert()
	if Serial(c2) == Serial(c) {
		t.Fatal("not renewed")
	}
	// The old certificate works until the new one is used (the reply
	// might have been lost), then stops.
	if code := call(old); code != http.StatusOK {
		t.Errorf("the replaced certificate before the new one is used: %d", code)
	}
	if code := call(node); code != http.StatusOK {
		t.Errorf("the renewed certificate: %d", code)
	}
	if code := call(old); code != http.StatusForbidden {
		t.Errorf("the replaced certificate after the new one is used: %d", code)
	}
	// A removed server's certificate is refused, though the CA signed it.
	h.Remove(ctx, "n-0000000002")
	if code := call(node); code != http.StatusForbidden {
		t.Errorf("a removed server: %d", code)
	}
}

func TestLegacyCertsRenewAtOnce(t *testing.T) {
	ca, err := LoadOrCreateCA(t.TempDir(), "home")
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{Dir: t.TempDir()}
	csr, _ := id.CSR("n-0000000002")
	// Issued for five years, as before renewal existed.
	cert, _, _ := ca.issue(csr, "n-0000000002", ControllerAddr, 5*365*24*time.Hour)
	id.Save(cert, CertPEM(ca.Cert))
	if due, _ := id.NeedsRenewal(time.Now()); !due {
		t.Error("a certificate issued for longer than a year should renew")
	}
}
