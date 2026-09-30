package labnet

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	h := &GuestHub{LabName: "Mum's lab", Store: st, Agent: ag, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now,
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
	if code, _ := usage(inv.User, inv.StorePassword); code != http.StatusForbidden {
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
