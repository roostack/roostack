package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "mylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUsersAndSessions(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	ok, err := s.CreateFirstUser(ctx, User{Username: "Tyris", PasswordHash: "h", Role: "owner"})
	if err != nil || !ok {
		t.Fatalf("first user: %v %v", ok, err)
	}
	if ok, _ := s.CreateFirstUser(ctx, User{Username: "eve", PasswordHash: "h", Role: "owner"}); ok {
		t.Error("a second 'first user' must be refused")
	}

	u, err := s.UserByUsername(ctx, "tyris") // case-insensitive
	if err != nil || u.Role != "owner" {
		t.Fatalf("lookup: %+v %v", u, err)
	}

	if err := s.CreateSession(ctx, "tok", u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SessionUser(ctx, "tok"); err != nil || got.ID != u.ID {
		t.Errorf("session user: %+v %v", got, err)
	}
	if err := s.CreateSession(ctx, "old", u.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session should not be valid, got %v", err)
	}

	// A password reset logs the user out everywhere.
	if err := s.SetPassword(ctx, "tyris", "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, "tok"); !errors.Is(err, ErrNotFound) {
		t.Errorf("sessions should be cleared after a password reset, got %v", err)
	}
	if err := s.SetPassword(ctx, "nobody", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("reset for unknown user: %v", err)
	}
}

func TestAuditAndBackup(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	s.Audit(ctx, nil, "install", "vaultwarden")
	s.Audit(ctx, &User{ID: 1, Username: "tyris"}, "remove", "vaultwarden")
	entries, err := s.RecentAudit(ctx, 10)
	if err != nil || len(entries) != 2 || entries[0].Username != "tyris" || entries[1].Username != "system" {
		t.Errorf("audit: %+v %v", entries, err)
	}

	backup := filepath.Join(t.TempDir(), "db-dump", "copy.db")
	for i := 0; i < 2; i++ { // missing directory, then overwriting
		if err := s.Backup(ctx, backup); err != nil {
			t.Fatal(err)
		}
	}
	copy, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if got, _ := copy.RecentAudit(ctx, 10); len(got) != 2 {
		t.Errorf("backup copy has %d audit entries, want 2", len(got))
	}
}

func TestTOTPSteps(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	id, _ := s.CreateUser(ctx, User{Username: "tyris", PasswordHash: "h", Role: "owner"})
	if err := s.SetTOTP(ctx, id, "SECRET", 100); err != nil {
		t.Fatal(err)
	}
	u, _ := s.UserByID(ctx, id)
	if u.TOTPSecret != "SECRET" || u.TOTPLastStep != 100 {
		t.Errorf("got %+v", u)
	}
	if ok, _ := s.UseTOTPStep(ctx, id, 100); ok {
		t.Error("the same step must not be usable twice")
	}
	if ok, _ := s.UseTOTPStep(ctx, id, 101); !ok {
		t.Error("a newer step should be accepted")
	}
}

func TestFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mylab.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.CountUsers(context.Background())
	for _, f := range []string{path, path + "-wal"} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(f), mode)
		}
	}
}

func TestReopenKeepsSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mylab.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.CreateUser(context.Background(), User{Username: "a", PasswordHash: "h", Role: "owner"})
	s.Close()
	s, err = Open(path) // migrations must not re-run
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, _ := s.CountUsers(context.Background()); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}
