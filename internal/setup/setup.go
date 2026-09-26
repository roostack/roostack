package setup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/store"
)

// Wizard progress, stored as runtime settings.
const (
	keyDomain   = "setup.domain"   // "done" or "later"
	keyBackup   = "setup.backup"   // "done" or "later"
	keyRecovery = "setup.recovery" // "confirmed" once the key was saved
)

// Secrets replaces a Podman secret's value.
type Secrets interface {
	Replace(ctx context.Context, name string, value []byte) error
}

// Setup saves what the wizard collects.
type Setup struct {
	Lab     config.Lab
	LabPath string
	Store   *store.Store
	Secrets Secrets
	HTTP    *http.Client
	// NewRepo returns a restic repository for a destination (to create it).
	NewRepo func(repository string, env []string) *backup.Repo
	// Restart restarts MyLab so every part picks up the new settings.
	Restart func()
}

// Progress is how far the wizard got.
type Progress struct {
	DomainDone    bool
	DomainLater   bool
	BackupDone    bool
	BackupLater   bool
	RecoverySaved bool
}

// Next returns the wizard page still to do, or "" when finished.
func (p Progress) Next() string {
	switch {
	case !p.DomainDone && !p.DomainLater:
		return "/setup/domain"
	case !p.BackupDone && !p.BackupLater:
		return "/setup/backup"
	case p.BackupDone && !p.RecoverySaved:
		return "/setup/recovery-key"
	}
	return ""
}

// Progress reads the wizard state.
func (s *Setup) Progress(ctx context.Context) (Progress, error) {
	var p Progress
	d, err := s.Store.Setting(ctx, keyDomain)
	if err != nil {
		return p, err
	}
	b, err := s.Store.Setting(ctx, keyBackup)
	if err != nil {
		return p, err
	}
	r, err := s.Store.Setting(ctx, keyRecovery)
	if err != nil {
		return p, err
	}
	p.DomainDone, p.DomainLater = d == "done", d == "later"
	p.BackupDone, p.BackupLater = b == "done", b == "later"
	p.RecoverySaved = r == "confirmed"
	return p, nil
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// SaveDomain checks and saves the lab's domain and Cloudflare token, then
// restarts MyLab so Caddy and every service use it.
func (s *Setup) SaveDomain(ctx context.Context, domain, email, token string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	email = strings.TrimSpace(email)
	if !domainRe.MatchString(domain) {
		return &CheckError{"Enter a domain like lab.example.com."}
	}
	if _, err := mail.ParseAddress(email); err != nil || strings.ContainsAny(email, " <>") {
		return &CheckError{"Enter an email address. Let's Encrypt uses it to warn about certificate problems."}
	}
	if _, err := CheckCloudflare(ctx, s.HTTP, token, domain); err != nil {
		return err
	}
	if err := s.Secrets.Replace(ctx, generate.CaddyDNSSecret, []byte(strings.TrimSpace(token))); err != nil {
		return err
	}
	lab := s.Lab
	lab.Domain, lab.Email = domain, email
	lab.DNS = config.DNS{Provider: "cloudflare"}
	if err := lab.Save(s.LabPath); err != nil {
		return err
	}
	if err := s.Store.SetSetting(ctx, keyDomain, "done"); err != nil {
		return err
	}
	s.Restart()
	return nil
}

// DomainLater skips the domain step for now. Services can't get HTTPS
// certificates until it's done, so the dashboard keeps reminding.
func (s *Setup) DomainLater(ctx context.Context) error {
	return s.Store.SetSetting(ctx, keyDomain, "later")
}

// BackupChoice is a backup destination from the wizard.
type BackupChoice struct {
	Kind string // "local" or "s3"
	// Local: a folder, e.g. on a second disk.
	Path string
	// S3-compatible storage (Backblaze B2, Wasabi, AWS, ...).
	Endpoint, Bucket, Prefix, AccessKey, SecretKey string
}

func (c BackupChoice) repository() (string, []string, error) {
	switch c.Kind {
	case "local":
		p := filepath.Clean(strings.TrimSpace(c.Path))
		if !filepath.IsAbs(p) {
			return "", nil, &CheckError{"Enter a full folder path, starting with /."}
		}
		return p, nil, nil
	case "s3":
		endpoint := strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
		bucket := strings.Trim(strings.TrimSpace(c.Bucket), "/")
		if !strings.HasPrefix(endpoint, "https://") || bucket == "" {
			return "", nil, &CheckError{"Enter the storage endpoint (starting with https://) and a bucket name."}
		}
		if strings.TrimSpace(c.AccessKey) == "" || strings.TrimSpace(c.SecretKey) == "" {
			return "", nil, &CheckError{"Enter the access key and secret key."}
		}
		repo := "s3:" + endpoint + "/" + bucket
		if prefix := strings.Trim(strings.TrimSpace(c.Prefix), "/"); prefix != "" {
			repo += "/" + prefix
		}
		env := []string{"AWS_ACCESS_KEY_ID=" + strings.TrimSpace(c.AccessKey), "AWS_SECRET_ACCESS_KEY=" + strings.TrimSpace(c.SecretKey)}
		return repo, env, nil
	}
	return "", nil, &CheckError{"Choose where to keep backups."}
}

// SaveBackup creates the recovery key (if needed) and the backup
// repository, and saves the destination. The recovery key is then shown
// once (RecoveryKey) before MyLab restarts.
func (s *Setup) SaveBackup(ctx context.Context, c BackupChoice) error {
	repoURL, env, err := c.repository()
	if err != nil {
		return err
	}
	keyFile := backup.KeyFile(s.Lab.Paths)
	if _, err := os.Stat(keyFile); errors.Is(err, os.ErrNotExist) {
		key, err := auth.NewToken()
		if err != nil {
			return err
		}
		if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	ictx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := s.NewRepo(repoURL, env).Init(ictx); err != nil {
		return &CheckError{fmt.Sprintf("Couldn't create the backup there: %s", firstLine(err.Error()))}
	}

	lab := s.Lab
	lab.Backup = config.Backup{Repository: repoURL, Env: env}
	if err := lab.Save(s.LabPath); err != nil {
		return err
	}
	if err := s.Store.SetSetting(ctx, keyBackup, "done"); err != nil {
		return err
	}
	// A new destination means the key must be confirmed again.
	return s.Store.SetSetting(ctx, keyRecovery, "")
}

// AdoptExisting marks the backup step done if backups were already set up
// outside the wizard (e.g. with `mylab backup init`, which showed the key).
func (s *Setup) AdoptExisting(ctx context.Context) error {
	if s.Lab.Backup.Repository == "" {
		return nil
	}
	if _, err := os.Stat(backup.KeyFile(s.Lab.Paths)); err != nil {
		return nil
	}
	if v, err := s.Store.Setting(ctx, keyBackup); err != nil || v != "" {
		return err
	}
	if err := s.Store.SetSetting(ctx, keyBackup, "done"); err != nil {
		return err
	}
	return s.Store.SetSetting(ctx, keyRecovery, "confirmed")
}

// BackupLater skips backups for now (the dashboard warns until set up).
func (s *Setup) BackupLater(ctx context.Context) error {
	return s.Store.SetSetting(ctx, keyBackup, "later")
}

// RecoveryKey returns the backup recovery key to show the Owner.
func (s *Setup) RecoveryKey() (string, error) {
	data, err := os.ReadFile(backup.KeyFile(s.Lab.Paths))
	return strings.TrimSpace(string(data)), err
}

// ConfirmRecoveryKey records that the Owner saved the key, then restarts
// MyLab so nightly backups start with the new destination.
func (s *Setup) ConfirmRecoveryKey(ctx context.Context) error {
	if err := s.Store.SetSetting(ctx, keyRecovery, "confirmed"); err != nil {
		return err
	}
	s.Restart()
	return nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}
