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
	if err := s.SetPassword(ctx, "tyris", "new", true); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByUsername(ctx, "tyris"); !u.MustChangePassword {
		t.Error("a temporary password must be changed at the next login")
	}
	if _, err := s.SessionUser(ctx, "tok"); !errors.Is(err, ErrNotFound) {
		t.Errorf("sessions should be cleared after a password reset, got %v", err)
	}
	if err := s.SetPassword(ctx, "nobody", "x", false); !errors.Is(err, ErrNotFound) {
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

func TestBackupRuns(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if last, ok, err := s.LastBackup(ctx); last != nil || ok != nil || err != nil {
		t.Fatalf("empty: %v %v %v", last, ok, err)
	}
	now := time.Now()
	s.RecordBackup(ctx, BackupRun{Started: now, Finished: now, OK: true, SnapshotID: "aaa", FilesNew: 3})
	s.RecordBackup(ctx, BackupRun{Started: now, Finished: now, OK: false, Error: "disk full"})
	last, ok, err := s.LastBackup(ctx)
	if err != nil || last.OK || last.Error != "disk full" || ok.SnapshotID != "aaa" || ok.FilesNew != 3 {
		t.Errorf("last=%+v ok=%+v err=%v", last, ok, err)
	}
}

func TestSettings(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if v, err := s.Setting(ctx, "a"); v != "" || err != nil {
		t.Fatalf("unset: %q %v", v, err)
	}
	s.SetSetting(ctx, "a", "1")
	s.SetSetting(ctx, "a", "2")
	if v, _ := s.Setting(ctx, "a"); v != "2" {
		t.Errorf("got %q", v)
	}
}

func TestUserManagement(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	mum, _ := s.CreateUser(ctx, User{Username: "mum", PasswordHash: "h", Role: "owner"})
	dad, _ := s.CreateUser(ctx, User{Username: "Dad", PasswordHash: "h", Role: "member"})

	users, err := s.ListUsers(ctx)
	if err != nil || len(users) != 2 || users[0].Username != "Dad" || users[1].Theme != "auto" {
		t.Fatalf("users = %+v, %v", users, err)
	}

	// The only Owner can't be demoted or removed.
	if err := s.SetRole(ctx, mum, "admin"); !errors.Is(err, ErrLastOwner) {
		t.Errorf("demote last owner: %v", err)
	}
	if err := s.DeleteUser(ctx, mum); !errors.Is(err, ErrLastOwner) {
		t.Errorf("delete last owner: %v", err)
	}
	// With a second Owner, both are fine.
	if err := s.SetRole(ctx, dad, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRole(ctx, mum, "admin"); err != nil {
		t.Errorf("demote with another owner: %v", err)
	}
	if err := s.SetRole(ctx, dad, "member"); !errors.Is(err, ErrLastOwner) {
		t.Errorf("dad is now the last owner: %v", err)
	}
	if err := s.DeleteUser(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete unknown: %v", err)
	}

	// Resetting 2FA turns it off and logs the user out.
	s.SetTOTP(ctx, mum, "SECRET", 5)
	s.CreateSession(ctx, "mum-session", mum, time.Now().Add(time.Hour))
	if err := s.ResetTOTP(ctx, mum); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, mum); u.TOTPSecret != "" || u.TOTPLastStep != 0 {
		t.Errorf("2FA not reset: %+v", u)
	}
	if _, err := s.SessionUser(ctx, "mum-session"); !errors.Is(err, ErrNotFound) {
		t.Error("sessions should end when 2FA is reset")
	}

	if err := s.DeleteUser(ctx, mum); err != nil {
		t.Fatal(err)
	}
	if users, _ := s.ListUsers(ctx); len(users) != 1 {
		t.Errorf("users after delete: %+v", users)
	}
}

func TestJoinCodes(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	s.AddJoinCode(ctx, JoinCode{TokenHash: "h1", Addr: "100.96.0.2", PublicKey: "k1", Expires: now.Add(time.Hour)})
	s.AddJoinCode(ctx, JoinCode{TokenHash: "old", Addr: "100.96.0.3", PublicKey: "k2", Expires: now.Add(-time.Minute)})
	if codes, err := s.JoinCodes(ctx, now); err != nil || len(codes) != 1 || codes[0].Addr != "100.96.0.2" {
		t.Fatalf("codes = %+v, %v (expired ones dropped)", codes, err)
	}
	if _, err := s.Join(ctx, "nope", Machine{ID: "n-0000000002"}, now); !errors.Is(err, ErrJoinCode) {
		t.Errorf("unknown code: %v", err)
	}
	m, err := s.Join(ctx, "h1", Machine{ID: "n-0000000002", Name: "vps", PublicKey: "own"}, now)
	if err != nil || m.Addr != "100.96.0.2" {
		t.Fatalf("join: %+v %v", m, err)
	}
	if _, err := s.Join(ctx, "h1", Machine{ID: "n-0000000009"}, now); !errors.Is(err, ErrJoinCode) {
		t.Error("a code must work once")
	}
	ms, _ := s.Machines(ctx)
	if len(ms) != 1 || ms[0].Name != "vps" || ms[0].PublicKey != "own" {
		t.Errorf("machines = %+v", ms)
	}
	s.RemoveMachine(ctx, "n-0000000002")
	if ms, _ := s.Machines(ctx); len(ms) != 0 {
		t.Error("not removed")
	}
}

func TestBackupTargets(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	id, err := s.AddBackupProvider(ctx, BackupProvider{Kind: "s3", Name: "B2", Config: `{"bucket":"b"}`})
	if err != nil || id == 0 {
		t.Fatal(id, err)
	}
	if ps, _ := s.BackupProviders(ctx); len(ps) != 1 || ps[0].Name != "B2" {
		t.Errorf("providers = %+v", ps)
	}
	s.SetBackupTarget(ctx, BackupTarget{MachineID: "n-0000000002", Provider: "machine:n-0000000001",
		Repository: "rest:http://100.96.0.1:8000/n-0000000002/lab", StoreHost: "n-0000000001", StoreHash: "$2a$x", Created: time.Now()})
	s.SetBackupTarget(ctx, BackupTarget{MachineID: "n-0000000002", Provider: "provider:1", Repository: "s3:x", Created: time.Now()})
	if ts, _ := s.BackupTargets(ctx); len(ts) != 1 || ts[0].Provider != "provider:1" || ts[0].StoreHost != "" {
		t.Errorf("targets = %+v", ts)
	}
	if _, _, ok, _ := s.StoreAdmin(ctx, "n-0000000001"); ok {
		t.Error("no admin yet")
	}
	s.SetStoreAdmin(ctx, "n-0000000001", "pw", "hash")
	if pw, hash, ok, err := s.StoreAdmin(ctx, "n-0000000001"); !ok || pw != "pw" || hash != "hash" || err != nil {
		t.Errorf("admin = %q %q %v %v", pw, hash, ok, err)
	}
}
