package labnet

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/store"
)

func TestGuestHub(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ag := &fakeAgent{}
	h := &GuestHub{LabName: func() string { return "Mum's lab" }, Store: st, Agent: ag, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now,
		Usage: func(context.Context, store.Guest) (int64, error) { return 3 << 30, nil }}
	ctx := context.Background()
	if h.Enabled(ctx) {
		t.Fatal("not offered yet")
	}
	inv, err := h.NewInvite(ctx, "Sam's lab", 50<<30, "203.0.113.7:51821")
	if err != nil {
		t.Fatal(err)
	}
	if !h.Enabled(ctx) || ag.last.Interface != agent.GuestsInterface || ag.last.ListenPort != agent.GuestsPort {
		t.Fatalf("guests interface: %+v", ag.last)
	}
	if !agent.GuestRange.Contains(inv.HostAddr) || !strings.HasSuffix(ag.last.Address, "/24") {
		t.Errorf("host addr %s, interface %s", inv.HostAddr, ag.last.Address)
	}
	parsed, err := ParseStorageInvite("  " + inv.String())
	if err != nil || parsed != inv || inv.Port != FirstGuestPort || inv.Quota != 50<<30 {
		t.Fatalf("invite = %+v, %v", parsed, err)
	}
	joinPub, _ := agent.WireGuardPublicKey(inv.JoinKey)
	if !slices.Contains(ag.peerKeys(), joinPub+" "+inv.Addr.String()+"/32") {
		t.Errorf("the invite's key must be a peer: %v", ag.peerKeys())
	}
	// A second guest gets the next address and ports.
	inv2, _ := h.NewInvite(ctx, "Sam's lab", 10<<30, "203.0.113.7:51821")
	if inv2.Port != FirstGuestPort+2 || inv2.Addr == inv.Addr {
		t.Errorf("second invite %s:%d", inv2.Addr, inv2.Port)
	}

	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	ownKey, _ := agent.NewWireGuardKey()
	ownPub, _ := agent.WireGuardPublicKey(ownKey)
	join := func(token string) int {
		body := strings.NewReader(`{"token":"` + token + `","public_key":"` + ownPub + `"}`)
		resp, err := http.Post(srv.URL+"/v1/guest/join", "application/json", body)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := join("wrong"); code != http.StatusForbidden {
		t.Errorf("wrong token: %d", code)
	}
	if code := join(inv.Token); code != http.StatusOK {
		t.Fatalf("join: %d", code)
	}
	if code := join(inv.Token); code != http.StatusForbidden {
		t.Error("an invite works once")
	}
	usage := func(user, pw string) (int, GuestUsage) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/guest/usage", nil)
		req.SetBasicAuth(user, pw)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var u GuestUsage
		json.NewDecoder(resp.Body).Decode(&u)
		return resp.StatusCode, u
	}
	if code, u := usage(inv.User, inv.AdminPassword); code != http.StatusOK || u.Used != 3<<30 || u.Quota != 50<<30 {
		t.Errorf("usage: %d %+v", code, u)
	}
	if _, u := usage(inv.User, inv.AdminPassword); u.Host != "Mum's lab" || !u.DeleteOn.IsZero() {
		t.Errorf("the host's name, and no removal: %+v", u)
	}
	if inv.StorePassword != "" {
		t.Error("new invites have no shared store password")
	}

	// The guest's controller gives each of its servers a store user.
	changed := 0
	h.Changed = func() { changed++ }
	call := func(method, name, body, user, pw string) int {
		req, _ := http.NewRequest(method, srv.URL+"/v1/guest/users/"+name, strings.NewReader(body))
		req.SetBasicAuth(user, pw)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	_, hash, _ := NewPassword()
	body := `{"hash":"` + hash + `"}`
	if code := call(http.MethodPut, "n-0000000002", body, inv.User, inv.AdminPassword); code != http.StatusOK || changed != 1 {
		t.Fatalf("set a server's user: %d, changed %d", code, changed)
	}
	users := func() map[string]string {
		guests, _ := st.Guests(ctx, time.Now())
		for _, g := range guests {
			if g.User() == inv.User {
				return g.Users
			}
		}
		return nil
	}
	if users()["n-0000000002"] != hash {
		t.Errorf("users = %v", users())
	}
	for _, c := range []struct {
		name, body, user, pw string
		want                 int
	}{
		{"n-3", body, inv.User, "wrong", http.StatusForbidden},
		{"n-3", body, inv2.User, inv2.AdminPassword, http.StatusForbidden}, // not joined
		{"n-3", `{"hash":"plain"}`, inv.User, inv.AdminPassword, http.StatusBadRequest},
		{"n-3", `{"hash":"` + hash + `\nx:` + hash + `"}`, inv.User, inv.AdminPassword, http.StatusBadRequest},
		{"a%3Ab", body, inv.User, inv.AdminPassword, http.StatusBadRequest},
		{inv.User, body, inv.User, inv.AdminPassword, http.StatusBadRequest}, // the lab's own name
	} {
		if code := call(http.MethodPut, c.name, c.body, c.user, c.pw); code != c.want {
			t.Errorf("PUT %s %s as %s: %d, want %d", c.name, c.body, c.user, code, c.want)
		}
	}
	if len(users()) != 1 {
		t.Errorf("refused users were added: %v", users())
	}
	if code := call(http.MethodDelete, "n-0000000002", "", inv.User, inv.AdminPassword); code != http.StatusOK || len(users()) != 0 {
		t.Errorf("remove a server's user: %d %v", code, users())
	}
	call(http.MethodPut, "n-0000000002", body, inv.User, inv.AdminPassword)
	// Removed: the guest still hears, with the date its backups go.
	guests, _ := st.Guests(ctx, time.Now())
	removedAt := time.Now().Add(-time.Hour)
	for _, g := range guests {
		if g.User() == inv.User {
			st.SetGuestRemoved(ctx, g.ID, removedAt)
		}
	}
	if code, u := usage(inv.User, inv.AdminPassword); code != http.StatusOK || !u.DeleteOn.Equal(removedAt.Add(GuestGrace).Truncate(time.Second)) {
		t.Errorf("a removed guest: %d %+v", code, u)
	}
	if code := call(http.MethodPut, "n-0000000003", body, inv.User, inv.AdminPassword); code != http.StatusForbidden {
		t.Errorf("a removed guest adds users: %d", code)
	}
	// Its users go with it.
	for _, g := range guests {
		if g.User() == inv.User {
			st.RemoveGuest(ctx, g.ID)
		}
	}
	var left int
	for _, g := range must(st.Guests(ctx, time.Now())) {
		left += len(g.Users)
	}
	if left != 0 {
		t.Error("a deleted guest's users stay")
	}
	if code, _ := usage(inv.User, "wrong"); code != http.StatusForbidden {
		t.Error("usage needs the admin password")
	}
	if code, _ := usage(inv2.User, inv2.AdminPassword); code != http.StatusForbidden {
		t.Error("a guest that hasn't joined gets nothing")
	}
	for _, bad := range []string{"hello", inv.String()[:40], strings.Replace(inv.String(), "roostack-storage-", "roostack-join-", 1)} {
		if _, err := ParseStorageInvite(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

// statusAgent applies links and reports handshakes as told.
type statusAgent struct{ handshake int64 }

func (a *statusAgent) ApplyWireGuard(context.Context, agent.WireGuard) (string, error) {
	return "", nil
}
func (a *statusAgent) WireGuardStatus(context.Context, string) (agent.WireGuardStatus, error) {
	return agent.WireGuardStatus{Handshakes: map[string]int64{"hostkey=": a.handshake}}, nil
}

// Joining gives up early when the other lab's UDP port can't be reached
// (no handshake), and says which it is when the link is up but nothing
// answers.
func TestJoinStorageUnreachable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("127.0.0.2 is on Linux's loopback only: elsewhere connecting to it waits rather than being refused")
	}
	handshakeTimeout, joinTimeout = 100*time.Millisecond, 3*time.Second
	t.Cleanup(func() { handshakeTimeout, joinTimeout = 20*time.Second, 45*time.Second })
	key, _ := agent.NewWireGuardKey()
	// Nothing listens on 127.0.0.2's guest API port.
	inv := StorageInvite{HostName: "Mum's lab", Endpoint: "203.0.113.7:51821", HostKey: "hostkey=",
		HostAddr: netip.MustParseAddr("127.0.0.2"), JoinKey: key, Token: "t"}
	start := time.Now()
	err := JoinStorage(context.Background(), inv, "roostack-st1", &statusAgent{})
	if err == nil || !strings.Contains(err.Error(), "Mum's lab didn't answer at 203.0.113.7:51821") ||
		!strings.Contains(err.Error(), "Cloud Firewall") || strings.Contains(err.Error(), "127.0.0.2") {
		t.Errorf("no handshake: %v", err)
	}
	if time.Since(start) >= joinTimeout {
		t.Errorf("it should give up once there's no handshake, not wait %v", time.Since(start))
	}
	err = JoinStorage(context.Background(), inv, "roostack-st1", &statusAgent{handshake: 1790000000})
	if err == nil || !strings.Contains(err.Error(), "the link to Mum's lab at 203.0.113.7:51821 works") {
		t.Errorf("handshake but no answer: %v", err)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
