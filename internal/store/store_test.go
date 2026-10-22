package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "roostack.db"))
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

	if err := s.CreateSessionRemember(ctx, "tok", u.ID, time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if got, _, _, err := s.SessionInfo(ctx, "tok"); err != nil || got.ID != u.ID {
		t.Errorf("session user: %+v %v", got, err)
	}
	if err := s.CreateSessionRemember(ctx, "old", u.ID, time.Now().Add(-time.Minute), false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.SessionInfo(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session should not be valid, got %v", err)
	}

	// A session works only where it was signed in, and the setup pages'
	// go together (SPEC.md §6.3.2).
	if err := s.CreateSessionAt(ctx, OriginSetup, "setup-tok", u.ID, time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.SessionInfo(ctx, "setup-tok"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the setup pages' session on Roostack's own pages: %v", err)
	}
	if _, _, _, err := s.SessionInfoAt(ctx, OriginSetup, "tok"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Roostack's own session on the setup pages: %v", err)
	}
	if got, _, _, err := s.SessionInfoAt(ctx, OriginSetup, "setup-tok"); err != nil || got.ID != u.ID {
		t.Errorf("the setup pages' session there: %v", err)
	}
	if err := s.DeleteSessionsAt(ctx, OriginSetup); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.SessionInfoAt(ctx, OriginSetup, "setup-tok"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the setup pages' session after they closed: %v", err)
	}
	if _, _, _, err := s.SessionInfo(ctx, "tok"); err != nil {
		t.Errorf("Roostack's own session after the setup pages closed: %v", err)
	}
	if at, ok, err := s.FirstUserCreated(ctx); err != nil || !ok || time.Since(at) > time.Minute {
		t.Errorf("first account made %v (%v, %v)", at, ok, err)
	}

	// A password reset logs the user out everywhere.
	if err := s.SetPassword(ctx, "tyris", "new", true); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByUsername(ctx, "tyris"); !u.MustChangePassword {
		t.Error("a temporary password must be changed at the next login")
	}
	if _, _, _, err := s.SessionInfo(ctx, "tok"); !errors.Is(err, ErrNotFound) {
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
	if fi, err := os.Stat(backup); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the copy holds password hashes, so must be private: %v %v", fi.Mode(), err)
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
	path := filepath.Join(t.TempDir(), "roostack.db")
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

// A symlink where the database or its -wal or -shm file goes is refused,
// and what it points to is left alone (SPEC.md §6.1).
func TestOpenRefusesSymlinks(t *testing.T) {
	for _, name := range []string{"roostack.db", "roostack.db-wal", "roostack.db-shm"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "roostack.db")
			if name != "roostack.db" {
				s, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				s.Close()
			}
			target := filepath.Join(dir, "elsewhere")
			if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			if s, err := Open(path); err == nil {
				s.Close()
				t.Fatal("opened with a symlink in place")
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the symlink's target was created: %v", err)
			}
		})
	}
}

func TestReopenKeepsSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roostack.db")
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
	if last, ok, err := s.LastBackup(ctx, "/mnt/a"); last != nil || ok != nil || err != nil {
		t.Fatalf("empty: %v %v %v", last, ok, err)
	}
	now := time.Now()
	s.RecordBackup(ctx, BackupRun{Started: now, Finished: now, OK: true, SnapshotID: "aaa", FilesNew: 3, Repository: "/mnt/a"})
	s.RecordBackup(ctx, BackupRun{Started: now, Finished: now, OK: false, Error: "disk full", Repository: "/mnt/a"})
	// Another repository's runs are its own.
	s.RecordBackup(ctx, BackupRun{Started: now, Finished: now, OK: true, SnapshotID: "bbb", Repository: "s3:https://s3.example/b"})
	last, ok, err := s.LastBackup(ctx, "/mnt/a")
	if err != nil || last.OK || last.Error != "disk full" || ok.SnapshotID != "aaa" || ok.FilesNew != 3 || ok.Services != nil ||
		ok.Repository != "/mnt/a" {
		t.Errorf("last=%+v ok=%+v err=%v", last, ok, err)
	}
	if last, _, _ := s.LastBackup(ctx, "s3:https://s3.example/b"); last == nil || last.SnapshotID != "bbb" {
		t.Errorf("the other repository: %+v", last)
	}
	if last, ok, _ := s.LastBackup(ctx, ""); last != nil || ok != nil {
		t.Error("no repository has no runs")
	}
	// The services a run backed up: none is not unknown.
	for _, want := range [][]string{{}, {"immich", "forgejo"}} {
		s.RecordBackup(ctx, BackupRun{Started: now, Finished: now, OK: true, Services: want, Repository: "/mnt/a"})
		if _, ok, _ := s.LastBackup(ctx, "/mnt/a"); ok.Services == nil || !slices.Equal(ok.Services, want) {
			t.Errorf("services %q: got %#v", want, ok.Services)
		}
	}
}

// TestAdoptBackupRuns: runs recorded by role before repositories were
// (destination "" the main backup, else a copy's name) get the repository
// that role has in lab.yaml, once, so nothing shown before is lost.
func TestAdoptBackupRuns(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	for _, r := range []struct {
		destination, snapshot string
		ok                    bool
	}{{"", "main1", true}, {"", "", false}, {"provider:1", "copy1", true}, {"provider:9", "gone", true}} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO backup_runs (started_at, finished_at, ok, snapshot_id, destination)
			VALUES (?, ?, ?, ?, ?)`, at.Unix(), at.Unix(), r.ok, r.snapshot, r.destination); err != nil {
			t.Fatal(err)
		}
	}
	repos := map[string]string{"": "/mnt/usb/roostack", "provider:1": "s3:https://s3.example/b"}
	if err := s.AdoptBackupRuns(ctx, repos); err != nil {
		t.Fatal(err)
	}
	last, ok, _ := s.LastBackup(ctx, "/mnt/usb/roostack")
	if last == nil || last.OK || ok == nil || ok.SnapshotID != "main1" {
		t.Errorf("main backup: last=%+v ok=%+v", last, ok)
	}
	if _, ok, _ := s.LastBackup(ctx, "s3:https://s3.example/b"); ok == nil || ok.SnapshotID != "copy1" {
		t.Errorf("copy: %+v", ok)
	}
	// Once only: a later start (after a swap, say) changes nothing.
	s.db.ExecContext(ctx, `INSERT INTO backup_runs (started_at, finished_at, ok, snapshot_id, destination) VALUES (?, ?, 1, 'late', '')`,
		at.Unix(), at.Unix())
	if err := s.AdoptBackupRuns(ctx, map[string]string{"": "s3:https://s3.example/b"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.LastBackup(ctx, "s3:https://s3.example/b"); ok == nil || ok.SnapshotID != "copy1" {
		t.Errorf("adopted twice: %+v", ok)
	}
	var orphans int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM backup_runs WHERE repository = ''`).Scan(&orphans)
	if orphans != 2 { // the stopped copy's run, and the one recorded after
		t.Errorf("runs without a repository: %d, want 2", orphans)
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
	s.CreateSessionRemember(ctx, "mum-session", mum, time.Now().Add(time.Hour), false)
	if err := s.ResetTOTP(ctx, mum); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, mum); u.TOTPSecret != "" || u.TOTPLastStep != 0 {
		t.Errorf("2FA not reset: %+v", u)
	}
	if _, _, _, err := s.SessionInfo(ctx, "mum-session"); !errors.Is(err, ErrNotFound) {
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
	if _, err := s.Join(ctx, "nope", Server{ID: "n-0000000002"}, now); !errors.Is(err, ErrJoinCode) {
		t.Errorf("unknown code: %v", err)
	}
	m, err := s.Join(ctx, "h1", Server{ID: "n-0000000002", Name: "vps", PublicKey: "own"}, now)
	if err != nil || m.Addr != "100.96.0.2" {
		t.Fatalf("join: %+v %v", m, err)
	}
	if _, err := s.Join(ctx, "h1", Server{ID: "n-0000000009"}, now); !errors.Is(err, ErrJoinCode) {
		t.Error("a code must work once")
	}
	ms, _ := s.Servers(ctx)
	if len(ms) != 1 || ms[0].Name != "vps" || ms[0].PublicKey != "own" {
		t.Errorf("servers = %+v", ms)
	}
	s.RemoveServer(ctx, "n-0000000002")
	if ms, _ := s.Servers(ctx); len(ms) != 0 {
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
	s.SetBackupTarget(ctx, BackupTarget{ServerID: "n-0000000002", Provider: "server:n-0000000001",
		Repository: "rest:http://100.96.0.1:8000/n-0000000002/lab", StoreHost: "n-0000000001", StoreHash: "$2a$x", Created: time.Now()})
	s.SetBackupTarget(ctx, BackupTarget{ServerID: "n-0000000002", Provider: "provider:1", Repository: "s3:x", Created: time.Now()})
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

func TestGuests(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	id, err := s.AddGuest(ctx, Guest{Name: "Sam's lab", Quota: 50 << 30, Addr: "100.101.23.2", PublicKey: "onetime",
		TokenHash: "h", Expires: now.Add(time.Hour), Port: 8100, StoreHash: "sh", AdminHash: "ah"})
	if err != nil {
		t.Fatal(err)
	}
	s.AddGuest(ctx, Guest{Name: "expired", Addr: "100.101.23.3", PublicKey: "x", TokenHash: "old", Expires: now.Add(-time.Minute), Port: 8102})
	gs, _ := s.Guests(ctx, now)
	if len(gs) != 1 || gs[0].User() != fmt.Sprintf("g%d", id) || gs[0].Joined {
		t.Fatalf("guests = %+v (expired invites dropped)", gs)
	}
	if _, err := s.JoinGuest(ctx, "wrong", "own", now); !errors.Is(err, ErrJoinCode) {
		t.Errorf("wrong token: %v", err)
	}
	g, err := s.JoinGuest(ctx, "h", "own", now)
	if err != nil || !g.Joined || g.PublicKey != "own" {
		t.Fatalf("join: %+v %v", g, err)
	}
	if _, err := s.JoinGuest(ctx, "h", "again", now); !errors.Is(err, ErrJoinCode) {
		t.Error("an invite works once")
	}
	// A joined guest stays, even past the invite's expiry.
	if gs, _ := s.Guests(ctx, now.Add(2*time.Hour)); len(gs) != 1 {
		t.Error("joined guests don't expire")
	}
	s.SetGuestQuota(ctx, id, 10<<30)
	s.RemoveGuest(ctx, id)
	if gs, _ := s.Guests(ctx, now); len(gs) != 0 {
		t.Error("not removed")
	}
}

// The hourly clean-up deletes expired sessions and, unless kept signed in,
// those unused for longer than the idle limit (0: no limit).
func TestDeleteIdleSessions(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	id, _ := s.CreateUser(ctx, User{Username: "sam", PasswordHash: "x", Role: "admin"})
	later := time.Now().Add(time.Hour)
	s.CreateSessionRemember(ctx, "idle", id, later, false)
	s.CreateSessionRemember(ctx, "kept", id, later, true)
	s.CreateSessionRemember(ctx, "recent", id, later, false)
	s.TouchSession(ctx, "idle", time.Now().Add(-2*time.Hour))
	s.TouchSession(ctx, "kept", time.Now().Add(-2*time.Hour))
	alive := func(h string) bool { _, _, _, err := s.SessionInfo(ctx, h); return err == nil }

	s.DeleteExpiredSessions(ctx, 0)
	if !alive("idle") {
		t.Fatal("no idle limit: kept")
	}
	s.DeleteExpiredSessions(ctx, 30*time.Minute)
	if alive("idle") || !alive("kept") || !alive("recent") {
		t.Errorf("idle %v, kept %v, recent %v", alive("idle"), alive("kept"), alive("recent"))
	}
}
