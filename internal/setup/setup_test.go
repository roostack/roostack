package setup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/dns"
	"github.com/mylab/mylab/internal/store"
)

type fakeSecrets map[string][]byte

func (f fakeSecrets) Replace(_ context.Context, name string, v []byte) error {
	f[name] = v
	return nil
}

func newSetup(t *testing.T) (*Setup, fakeSecrets, *int) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lab := config.Default()
	lab.Domain = "lab.example.com" // the placeholder from the example config
	lab.Paths.StateDir = dir
	restarts := 0
	secrets := fakeSecrets{}
	s := &Setup{
		Lab: lab, LabPath: filepath.Join(dir, "lab.yaml"), Store: st, Secrets: secrets,
		Restart: func() { restarts++ },
		// Stand-in for the provider: only the value "good" works.
		CheckDNS: func(_ context.Context, p *dns.Provider, v map[string]string, domain string) (string, error) {
			for _, f := range p.Fields {
				if !f.Optional && v[f.Key] != "good" {
					return "", errors.New("403 Forbidden: invalid credentials")
				}
			}
			return "example.com", nil
		},
	}
	return s, secrets, &restarts
}

func TestDomainStep(t *testing.T) {
	s, secrets, restarts := newSetup(t)
	ctx := context.Background()

	if p, _ := s.Progress(ctx); p.Next() != "/setup/timezone" {
		t.Fatalf("a new lab starts at the time zone step, got %q", p.Next())
	}
	s.Done(ctx, StepTimezone)
	if p, _ := s.Progress(ctx); p.Next() != "/setup/ssh" {
		t.Fatalf("after the time zone comes the SSH step, got %q", p.Next())
	}
	s.Done(ctx, StepSSH)
	if p, _ := s.Progress(ctx); p.Next() != "/setup/domain" {
		t.Fatalf("then the domain step, got %q", p.Next())
	}
	for _, tc := range []struct {
		domain, email, provider string
		values                  map[string]string
		want                    string
	}{
		{"not a domain", "me@example.com", "cloudflare", map[string]string{"api_token": "good"}, "domain like"},
		{"home.example.com", "nope", "cloudflare", map[string]string{"api_token": "good"}, "email address"},
		{"home.example.com", "me@example.com", "", nil, "Choose who manages"},
		{"home.example.com", "me@example.com", "route53", map[string]string{"access_key_id": "good"}, "Enter the secret access key"},
		{"home.example.com", "me@example.com", "cloudflare", map[string]string{"api_token": "bad"}, "Cloudflare said: 403 Forbidden"},
	} {
		err := s.SaveDomain(ctx, tc.domain, tc.email, tc.provider, tc.values, DomainOptions{})
		var ce *CheckError
		if !errors.As(err, &ce) || !strings.Contains(ce.Msg, tc.want) {
			t.Errorf("%+v: err %v", tc, err)
		}
	}
	if *restarts != 0 || len(secrets) != 0 {
		t.Fatal("nothing may be saved or restarted after a failed check")
	}

	// Route 53 with the optional region left empty.
	// Namecheap record management without accepting the risk is refused.
	var ce *CheckError
	if err := s.SaveDomain(ctx, "home.example.com", "me@example.com", "namecheap",
		map[string]string{"user": "good", "api_key": "good"}, DomainOptions{ManageRecords: true}); !errors.As(err, &ce) {
		t.Errorf("risky provider without opt-in: %v", err)
	}

	err := s.SaveDomain(ctx, " Home.Example.com ", "me@example.com", "route53",
		map[string]string{"access_key_id": "good", "secret_access_key": " good ", "region": ""}, DomainOptions{ManageRecords: true})
	if err != nil {
		t.Fatal(err)
	}
	lab, err := config.Load(s.LabPath)
	if err != nil {
		t.Fatal(err)
	}
	if lab.Domain != "home.example.com" || lab.DNS.Provider != "route53" ||
		strings.Join(lab.DNS.Fields, ",") != "access_key_id,secret_access_key" ||
		lab.DNS.Zone != "example.com" || !lab.DNS.ManageRecords || lab.DNS.AllowRiskyRecords {
		t.Errorf("saved lab: %+v", lab)
	}
	if string(secrets[dns.SecretName("secret_access_key")]) != "good" || len(secrets) != 2 || *restarts != 1 {
		t.Errorf("secrets %v, restarts %d", secrets, *restarts)
	}
	if p, _ := s.Progress(ctx); p.Next() != "/setup/backup" {
		t.Errorf("next = %q, want the backup step", p.Next())
	}
}

func TestBackupStep(t *testing.T) {
	bin := os.Getenv("MYLAB_RESTIC_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("restic"); err != nil {
			t.Skip("restic not found; set MYLAB_RESTIC_BIN")
		}
	}
	s, _, restarts := newSetup(t)
	ctx := context.Background()
	for _, k := range []string{keyTimezone, keySSH, keyDomain} {
		s.Store.SetSetting(ctx, k, "done")
	}
	s.NewRepo = func(repo string, env []string) *backup.Repo {
		return &backup.Repo{Bin: bin, URL: repo, PasswordFile: backup.KeyFile(s.Lab.Paths), Env: env,
			CacheDir: filepath.Join(s.Lab.Paths.StateDir, "cache")}
	}

	var ce *CheckError
	if err := s.SaveBackup(ctx, BackupChoice{Kind: "local", Path: "relative/path"}); !errors.As(err, &ce) {
		t.Errorf("relative path: %v", err)
	}
	if err := s.SaveBackup(ctx, BackupChoice{Kind: "s3", Endpoint: "http://insecure", Bucket: "b"}); !errors.As(err, &ce) {
		t.Errorf("non-https S3: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "backups")
	if err := s.SaveBackup(ctx, BackupChoice{Kind: "local", Path: dest}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "config")); err != nil {
		t.Error("restic repository not created:", err)
	}
	key, err := s.RecoveryKey()
	if err != nil || len(key) < 40 {
		t.Errorf("recovery key %q: %v", key, err)
	}
	lab, _ := config.Load(s.LabPath)
	if lab.Backup.Repository != dest {
		t.Errorf("saved repository %q", lab.Backup.Repository)
	}
	if p, _ := s.Progress(ctx); p.Next() != "/setup/recovery-key" || *restarts != 0 {
		t.Errorf("next %q restarts %d: the key must be shown before restarting", p.Next(), *restarts)
	}
	if err := s.ConfirmRecoveryKey(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Progress(ctx); p.Next() != "" || *restarts != 1 {
		t.Errorf("after confirming: next %q restarts %d", p.Next(), *restarts)
	}

	// Choosing a destination again keeps the same key (existing backups
	// stay readable) but asks to confirm it again.
	if err := s.SaveBackup(ctx, BackupChoice{Kind: "local", Path: filepath.Join(t.TempDir(), "b2")}); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.RecoveryKey(); again != key {
		t.Error("the recovery key must not change")
	}
	if p, _ := s.Progress(ctx); p.Next() != "/setup/recovery-key" {
		t.Errorf("next = %q", p.Next())
	}
}

func TestSkipping(t *testing.T) {
	s, _, _ := newSetup(t)
	ctx := context.Background()
	s.Done(ctx, StepTimezone) // the time zone and SSH steps can't be skipped
	s.Store.SetSetting(ctx, keySSH, "done")
	s.DomainLater(ctx)
	s.BackupLater(ctx)
	p, _ := s.Progress(ctx)
	if p.Next() != "" || !p.DomainLater || !p.BackupLater {
		t.Errorf("progress %+v", p)
	}
}

func TestDone(t *testing.T) {
	s, _, _ := newSetup(t)
	ctx := context.Background()
	if err := s.Done(ctx, "nope"); err == nil {
		t.Error("unknown step: want an error")
	}
	s.Done(ctx, StepTimezone)
	if p, _ := s.Progress(ctx); !p.TimezoneDone || p.SSHDone || p.Next() != "/setup/ssh" {
		t.Errorf("after the time zone: %+v", p)
	}
	s.Done(ctx, StepSSH)
	if p, _ := s.Progress(ctx); !p.SSHDone || p.Next() != "/setup/domain" {
		t.Errorf("after SSH: %+v", p)
	}
}
