package labnet

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/store"
)

type fakeAgent struct {
	mu   sync.Mutex
	last agent.WireGuard
}

func (f *fakeAgent) ApplyWireGuard(_ context.Context, wg agent.WireGuard) (string, error) {
	if err := wg.Validate(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = wg
	return "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=", nil
}

func (f *fakeAgent) peerKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.last.Peers {
		out = append(out, p.PublicKey+" "+p.AllowedIPs[0])
	}
	return out
}

func TestHubJoin(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ag := &fakeAgent{}
	h := &Hub{StateDir: dir, LabName: "home", Self: nodeid.Identity{ID: "n-0000000001", Name: "nuc"}, Store: st, Agent: ag,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now, SwitchDelay: time.Millisecond}
	ctx := context.Background()
	if h.Enabled() {
		t.Fatal("no network before the first join code")
	}

	code, err := h.NewJoinCode(ctx, "10.0.2.2:51820", "")
	if err != nil {
		t.Fatal(err)
	}
	if !h.Enabled() || ag.last.Address != "100.96.0.1/24" || ag.last.ListenPort != agent.WireGuardPort {
		t.Fatalf("hub interface: %+v", ag.last)
	}
	joinPub, _ := agent.WireGuardPublicKey(code.JoinKey)
	if code.Addr.String() != "100.96.0.2" || !slices.Contains(ag.peerKeys(), joinPub+" 100.96.0.2/32") {
		t.Fatalf("the code's one-time key must be a peer: %v", ag.peerKeys())
	}
	if m, ok, _ := LoadMembership(dir); !ok || m.Role != "controller" {
		t.Errorf("membership = %+v", m)
	}
	// A second code gets the next address.
	if c2, _ := h.NewJoinCode(ctx, "10.0.2.2:51820", ""); c2.Addr.String() != "100.96.0.3" {
		t.Errorf("second code at %s", c2.Addr)
	}

	node := Identity{Dir: t.TempDir()}
	csr, _ := node.CSR("n-0000000002")
	ownKey, _ := agent.NewWireGuardKey()
	ownPub, _ := agent.WireGuardPublicKey(ownKey)
	req := JoinRequest{Token: code.Token, NodeID: "n-0000000002", Name: "vps", PublicKey: ownPub, CSR: csr}
	if _, err := h.Join(ctx, JoinRequest{Token: "wrong", NodeID: "n-0000000002", PublicKey: ownPub, CSR: csr}); !errors.Is(err, store.ErrJoinCode) {
		t.Errorf("wrong token: %v", err)
	}
	resp, err := h.Join(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.Save(resp.Cert, resp.CA); err != nil {
		t.Fatal(err)
	}
	if _, err := node.ClientTLS("n-0000000001"); err != nil {
		t.Error(err)
	}
	if keys := ag.peerKeys(); !slices.Contains(keys, joinPub+" 100.96.0.2/32") {
		t.Error("the one-time key must stay until the reply has gone")
	}
	time.Sleep(50 * time.Millisecond)
	keys := ag.peerKeys()
	if !slices.Contains(keys, ownPub+" 100.96.0.2/32") || slices.Contains(keys, joinPub+" 100.96.0.2/32") {
		t.Errorf("the server's own key must replace the one-time key: %v", keys)
	}
	if _, err := h.Join(ctx, req); err == nil {
		t.Error("a code must work once")
	}
	if _, err := h.Join(ctx, JoinRequest{Token: code.Token, NodeID: "n-0000000001", PublicKey: ownPub, CSR: csr}); err == nil {
		t.Error("a server can't join as the controller")
	}

	h.Rename(ctx, "n-0000000002", "cloud")
	if ms, _ := st.Servers(ctx); len(ms) != 1 || ms[0].Name != "cloud" {
		t.Errorf("rename: %+v", ms)
	}
	if err := h.Remove(ctx, "n-0000000002"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(ag.peerKeys(), ownPub+" 100.96.0.2/32") {
		t.Error("a removed server must lose its WireGuard peer")
	}
}

// TestHubDials: with the controller unreachable, a code says it will dial
// the new server; the hub dials it (with a keepalive) from the code on,
// and keeps dialling it once it has joined.
func TestHubDials(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ag := &fakeAgent{}
	h := &Hub{StateDir: dir, LabName: "home", Self: nodeid.Identity{ID: "n-0000000001", Name: "nuc"}, Store: st, Agent: ag,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now, SwitchDelay: time.Millisecond}
	ctx := context.Background()
	if _, err := h.NewJoinCode(ctx, "", ""); err == nil {
		t.Error("a code with neither address")
	}
	code, err := h.NewJoinCode(ctx, "", "203.0.113.7:51820")
	if err != nil {
		t.Fatal(err)
	}
	if code.Endpoint != "" || !code.Listen {
		t.Fatalf("code: endpoint %q, listen %v", code.Endpoint, code.Listen)
	}
	if _, err := ParseJoinCode(code.String()); err != nil {
		t.Fatalf("parsing a dial-in code: %v", err)
	}
	dialled := func(key string) (agent.Peer, bool) {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		for _, p := range ag.last.Peers {
			if p.PublicKey == key {
				return p, true
			}
		}
		return agent.Peer{}, false
	}
	joinPub, _ := agent.WireGuardPublicKey(code.JoinKey)
	if p, ok := dialled(joinPub); !ok || p.Endpoint != "203.0.113.7:51820" || p.Keepalive != Keepalive {
		t.Fatalf("the code's peer: %+v", p)
	}

	node := Identity{Dir: t.TempDir()}
	csr, _ := node.CSR("n-0000000002")
	ownKey, _ := agent.NewWireGuardKey()
	ownPub, _ := agent.WireGuardPublicKey(ownKey)
	if _, err := h.Join(ctx, JoinRequest{Token: code.Token, NodeID: "n-0000000002", Name: "vps", PublicKey: ownPub, CSR: csr}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if p, ok := dialled(ownPub); !ok || p.Endpoint != "203.0.113.7:51820" || p.Keepalive != Keepalive {
		t.Errorf("the joined server's peer: %+v", p)
	}
	if peers, _ := h.Peers(ctx); peers["n-0000000002"].Dial != "203.0.113.7:51820" {
		t.Errorf("peers: %+v", peers)
	}
}

// TestJoinCodeEither: a code says where the controller is, or that it
// will connect, never both or neither.
func TestJoinCodeEither(t *testing.T) {
	key, _ := agent.NewWireGuardKey()
	pub, _ := agent.WireGuardPublicKey(key)
	base := JoinCode{ControllerKey: pub, ControllerID: "n-0000000001", ControllerAddr: ControllerAddr,
		Addr: ControllerAddr.Next(), PrefixBits: 24, JoinKey: key, CA: "ab", Token: "t"}
	for _, c := range []struct {
		endpoint string
		listen   bool
		ok       bool
	}{{"10.0.2.2:51820", false, true}, {"", true, true}, {"", false, false}, {"10.0.2.2:51820", true, false}} {
		code := base
		code.Endpoint, code.Listen = c.endpoint, c.listen
		if _, err := ParseJoinCode(code.String()); (err == nil) != c.ok {
			t.Errorf("endpoint %q listen %v: %v", c.endpoint, c.listen, err)
		}
	}
}
