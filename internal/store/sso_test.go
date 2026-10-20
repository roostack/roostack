package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Groups, a service's access, and who can use it.
func TestServiceAccess(t *testing.T) {
	s, ctx := open(t), context.Background()
	mum, _ := s.CreateUser(ctx, User{Username: "mum", PasswordHash: "x", Role: "member"})
	jo, _ := s.CreateUser(ctx, User{Username: "jo", PasswordHash: "x", Role: "member"})
	family, err := s.CreateGroup(ctx, "Family")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGroup(ctx, "family"); !errors.Is(err, ErrGroupExists) {
		t.Errorf("a second Family: %v", err)
	}
	if err := s.SetGroupMembers(ctx, family, []int64{mum}); err != nil {
		t.Fatal(err)
	}

	// Everyone, until chosen.
	for _, u := range []int64{mum, jo} {
		if ok, err := s.CanUse(ctx, "mealie", u); !ok || err != nil {
			t.Errorf("user %d before a choice: %v, %v", u, ok, err)
		}
	}
	if err := s.SetServiceAccess(ctx, "mealie", Access{Chosen: true, Groups: []int64{family}}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.CanUse(ctx, "mealie", mum); !ok {
		t.Error("mum is in Family")
	}
	if ok, _ := s.CanUse(ctx, "mealie", jo); ok {
		t.Error("jo isn't")
	}
	// Chosen with nobody: nobody.
	if err := s.SetServiceAccess(ctx, "immich", Access{Chosen: true}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.CanUse(ctx, "immich", mum); ok {
		t.Error("nobody chosen for immich")
	}
	// A person chosen by name.
	if err := s.SetServiceAccess(ctx, "immich", Access{Chosen: true, Users: []int64{jo}}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.CanUse(ctx, "immich", jo); !ok {
		t.Error("jo was chosen for immich")
	}
	// Removing the group takes it out of the service's access.
	if err := s.DeleteGroup(ctx, family); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.ServiceAccess(ctx, "mealie"); !a.Chosen || len(a.Groups) != 0 {
		t.Errorf("after removing Family: %+v", a)
	}
	if ok, _ := s.CanUse(ctx, "mealie", mum); ok {
		t.Error("mum's group is gone")
	}
}

// A person's ID for apps is made once and kept.
func TestSSOID(t *testing.T) {
	s, ctx := open(t), context.Background()
	id, _ := s.CreateUser(ctx, User{Username: "mum", PasswordHash: "x", Role: "member"})
	a, err := s.EnsureSSOID(ctx, id)
	if err != nil || len(a) != 32 {
		t.Fatalf("%q, %v", a, err)
	}
	b, _ := s.EnsureSSOID(ctx, id)
	u, _ := s.UserByID(ctx, id)
	if a != b || u.SSOID != a {
		t.Errorf("%q then %q, user has %q", a, b, u.SSOID)
	}
	if _, err := s.EnsureSSOID(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such person: %v", err)
	}
}

// Tokens work while their sign-in does, and go with it.
func TestSSOTokens(t *testing.T) {
	s, ctx := open(t), context.Background()
	id, _ := s.CreateUser(ctx, User{Username: "mum", PasswordHash: "x", Role: "member"})
	if err := s.CreateSessionAt(ctx, OriginSignIn, "sess", id, time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSessionAt(ctx, "", "ui", id, time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	tok := SSOToken{Hash: "h1", Kind: TokenRefresh, Service: "mealie", UserID: id, Session: "sess", Expires: time.Now().Add(time.Hour)}
	if err := s.AddSSOToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	got, u, err := s.LookupSSOToken(ctx, "h1", TokenRefresh)
	if err != nil || got.Service != "mealie" || u.Username != "mum" {
		t.Fatalf("%+v %v %v", got, u, err)
	}
	if _, _, err := s.LookupSSOToken(ctx, "h1", TokenAccess); !errors.Is(err, ErrNotFound) {
		t.Errorf("another kind: %v", err)
	}
	// A token of Roostack's own pages' session isn't one.
	s.AddSSOToken(ctx, SSOToken{Hash: "h2", Kind: TokenHost, Service: "pdf", UserID: id, Session: "ui", Expires: time.Now().Add(time.Hour)})
	if _, _, err := s.LookupSSOToken(ctx, "h2", TokenHost); !errors.Is(err, ErrNotFound) {
		t.Errorf("a token tied to Roostack's own session: %v", err)
	}
	// Used once.
	if ok, _ := s.DeleteSSOToken(ctx, "h1"); !ok {
		t.Error("first delete")
	}
	if ok, _ := s.DeleteSSOToken(ctx, "h1"); ok {
		t.Error("second delete")
	}
	// Signing out ends the sign-in's tokens.
	s.AddSSOToken(ctx, SSOToken{Hash: "h3", Kind: TokenHost, Service: "pdf", UserID: id, Session: "sess", Expires: time.Now().Add(time.Hour)})
	if err := s.DeleteSession(ctx, "sess"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LookupSSOToken(ctx, "h3", TokenHost); !errors.Is(err, ErrNotFound) {
		t.Errorf("after signing out: %v", err)
	}
	// The idle limit doesn't end a sign-in for services.
	s.CreateSessionAt(ctx, OriginSignIn, "old", id, time.Now().Add(time.Hour), false)
	s.db.Exec(`UPDATE sessions SET last_seen = ?`, time.Now().Add(-2*time.Hour).Unix())
	if err := s.DeleteExpiredSessions(ctx, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.SignInSession(ctx, "old"); err != nil {
		t.Errorf("sign-in after the idle limit: %v", err)
	}
	if _, _, _, err := s.SessionInfo(ctx, "ui"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Roostack's own idle session: %v", err)
	}
}
