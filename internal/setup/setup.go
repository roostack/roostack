package setup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
)

// Wizard progress, stored as runtime settings.
const (
	keyTimezone = "setup.timezone" // "done" once the Owner confirmed it
	keySSH      = "setup.ssh"      // "done" once the Owner chose SSH access
	keyDomain   = "setup.domain"   // "done" or "later"
	keyBackup   = "setup.backup"   // "done" or "later"
	keyRecovery = "setup.recovery" // "confirmed" once the key was saved
)

// Secrets handles Podman secrets: the DNS provider's credentials.
type Secrets interface {
	Replace(ctx context.Context, name string, value []byte) error
	Read(ctx context.Context, name string) ([]byte, error)
	Remove(ctx context.Context, name string) error
	List(ctx context.Context) (map[string]bool, error)
}

// Setup saves what the wizard collects.
type Setup struct {
	Lab     config.Lab
	LabPath string
	Store   *store.Store
	Secrets Secrets
	// CheckDNS tests DNS credentials before they are saved. Nil uses the
	// provider's real check (create and delete a TXT record).
	CheckDNS func(ctx context.Context, p *dns.Provider, values map[string]string, domain string) (string, error)
	// Restart restarts Roostack so every part picks up the new settings.
	Restart func()
	// Drives lists and readies the drives backups can go on (roostack-agent).
	Drives DriveAgent
	// RestartPending reports a restart a wizard step postponed; Finished is
	// called when the wizard ends without one of its own (optional).
	RestartPending func() bool
	Finished       func()
	// RemoveOldRecords deletes the DNS records Roostack set under the lab's
	// old address once it has stopped working, with that address's login
	// (optional; dnsrecords.Manager.RemoveOld). false: not all of them, so
	// lab.yaml keeps the old address and its login, to try again at the
	// next hourly check (SPEC.md §6.6).
	RemoveOldRecords func(ctx context.Context, old config.OldAddress) bool
}

// PendingRestart reports whether settings from the wizard still wait for
// Roostack to restart (it does when the wizard finishes).
func (s *Setup) PendingRestart() bool { return s.RestartPending != nil && s.RestartPending() }

// DriveAgent is roostack-agent's backup drive operations.
type DriveAgent interface {
	Drives(ctx context.Context) ([]agent.Drive, error)
	UseDrive(ctx context.Context, id string) (agent.UsedDrive, error)
	EraseDrive(ctx context.Context, disk, label string) error
	PrepareBackupFolder(ctx context.Context, dir string) (string, error)
}

// Progress is how far the wizard got.
type Progress struct {
	TimezoneDone  bool
	SSHDone       bool
	DomainDone    bool
	DomainLater   bool
	BackupDone    bool
	BackupLater   bool
	RecoverySaved bool
}

// Next returns the wizard page still to do, or "" when finished.
func (p Progress) Next() string {
	switch {
	case !p.TimezoneDone:
		return "/setup/timezone"
	case !p.SSHDone:
		return "/setup/ssh"
	case !p.DomainDone && !p.DomainLater:
		return "/setup/domain"
	case !p.BackupDone && !p.BackupLater:
		return "/setup/backup"
	case p.BackupDone && !p.RecoverySaved:
		return "/setup/recovery-key"
	}
	return ""
}

// Wizard steps whose settings belong to the server (node.Settings): the
// UI saves them there, then marks the step done.
const (
	StepTimezone = "timezone"
	StepSSH      = "ssh"
)

// Done marks a server settings step of the wizard as done.
func (s *Setup) Done(ctx context.Context, step string) error {
	key, ok := map[string]string{StepTimezone: keyTimezone, StepSSH: keySSH}[step]
	if !ok {
		return fmt.Errorf("unknown setup step %q", step)
	}
	return s.Store.SetSetting(ctx, key, "done")
}

// Progress reads the wizard state.
func (s *Setup) Progress(ctx context.Context) (Progress, error) {
	var p Progress
	tz, err := s.Store.Setting(ctx, keyTimezone)
	if err != nil {
		return p, err
	}
	ssh, err := s.Store.Setting(ctx, keySSH)
	if err != nil {
		return p, err
	}
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
	p.TimezoneDone = tz == "done"
	p.SSHDone = ssh == "done"
	p.DomainDone, p.DomainLater = d == "done", d == "later"
	p.BackupDone, p.BackupLater = b == "done", b == "later"
	p.RecoverySaved = r == "confirmed"
	return p, nil
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// DomainOptions are the wizard's DNS record choices (SPEC.md §6.5.1).
type DomainOptions struct {
	// ManageRecords lets Roostack create the records services need.
	ManageRecords bool
	// AcceptRisk is the explicit opt-in for providers whose API rewrites
	// every record to change one (Namecheap).
	AcceptRisk bool
}

// SaveDomain checks and saves the lab's domain and DNS provider
// credentials, then restarts Roostack so Caddy and every service use them.
// values holds the provider's fields (internal/dns).
func (s *Setup) SaveDomain(ctx context.Context, domain, email, providerID string, values map[string]string, opts DomainOptions) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	email = strings.TrimSpace(email)
	if !domainRe.MatchString(domain) {
		return &usererr.Error{Msg: "Enter a domain like lab.example.com."}
	}
	if _, err := mail.ParseAddress(email); err != nil || strings.ContainsAny(email, " <>") {
		return &usererr.Error{Msg: "Enter an email address. Let's Encrypt uses it to warn about certificate problems."}
	}
	p := dns.Get(providerID)
	if p == nil {
		return &usererr.Error{Msg: "Choose who manages your domain's DNS."}
	}
	clean := map[string]string{}
	for _, f := range p.Fields {
		clean[f.Key] = strings.TrimSpace(values[f.Key])
	}
	set, err := p.Validate(clean)
	if err != nil {
		return &usererr.Error{Msg: capitalize(err.Error()) + "."}
	}
	check := s.CheckDNS
	if check == nil {
		check = func(ctx context.Context, p *dns.Provider, v map[string]string, d string) (string, error) {
			return p.Check(ctx, v, d)
		}
	}
	if opts.ManageRecords && p.RiskyRecords() && !opts.AcceptRisk {
		return &usererr.Error{Msg: "To let Roostack manage " + p.Title + " records, tick the box confirming you understand the risk, or untick \"Create DNS records for me\"."}
	}
	zone, err := check(ctx, p, clean, domain)
	if err != nil {
		return &usererr.Error{Msg: fmt.Sprintf("Roostack couldn't create a test DNS record for %s with those details. "+
			"Check them and the permissions they have. %s said: %s", domain, p.Title, firstLine(err.Error()))}
	}
	old, err := s.keepOldAddress(ctx, domain)
	if err != nil {
		return err
	}
	for _, k := range set {
		if err := s.Secrets.Replace(ctx, dns.SecretName(k), []byte(clean[k])); err != nil {
			return err
		}
	}
	err = config.Update(s.LabPath, func(lab *config.Lab) {
		if old != nil {
			lab.OldAddress = old
		}
		lab.Domain, lab.Email = domain, email
		lab.DNS = config.DNS{Provider: p.ID, Fields: set, Zone: zone,
			ManageRecords: opts.ManageRecords && p.CanManageRecords(), AllowRiskyRecords: opts.AcceptRisk && p.RiskyRecords(),
			LANIP: lab.DNS.LANIP}
	})
	if err != nil {
		return err
	}
	if err := s.Store.SetSetting(ctx, keyDomain, "done"); err != nil {
		return err
	}
	s.Restart()
	return nil
}

// current is the lab's settings as saved now (s.Lab is as Roostack
// started).
func (s *Setup) current() (config.Lab, error) {
	lab, err := config.Load(s.LabPath)
	if errors.Is(err, os.ErrNotExist) {
		return s.Lab, nil
	}
	return lab, err
}

// keepOldAddress, when the lab is switching from one address to another,
// keeps the one it's leaving working for config.OldAddressDays (SPEC.md
// §6.6): its DNS provider's credentials are copied to the old address's
// secrets, before the new ones replace them. It returns the old address
// to save, or nil to leave lab.yaml's as it is (no switch). Only one old
// address is kept: an earlier one stops now.
func (s *Setup) keepOldAddress(ctx context.Context, domain string) (*config.OldAddress, error) {
	cur, err := s.current()
	if err != nil {
		return nil, err
	}
	if step, err := s.Store.Setting(ctx, keyDomain); err != nil || step != "done" || cur.Domain == "" || cur.Domain == domain {
		return nil, err
	}
	// An earlier old address stops now: its records go first, with its
	// login, which the one being left replaces. Any it can't delete are
	// listed for the Owner to delete by hand.
	if o := cur.OldAddress; o != nil && o.Domain != domain && o.Domain != cur.Domain && s.RemoveOldRecords != nil {
		rctx, cancel := context.WithTimeout(ctx, removeOldFor)
		s.RemoveOldRecords(rctx, *o)
		cancel()
	}
	// An earlier old address's secrets are overwritten (or, for fields the
	// new one doesn't use, removed by cleanOldSecrets after the restart).
	for _, f := range dns.CredentialFields(cur.DNS) {
		v, err := s.Secrets.Read(ctx, dns.SecretName(f))
		if err != nil {
			return nil, fmt.Errorf("keeping %s's DNS credentials for its old addresses: %w", cur.Domain, err)
		}
		if err := s.Secrets.Replace(ctx, dns.OldSecretName(f), v); err != nil {
			return nil, err
		}
	}
	return &config.OldAddress{Domain: cur.Domain, DNS: cur.DNS,
		Until: time.Now().Add(config.OldAddressDays * 24 * time.Hour).Truncate(time.Minute)}, nil
}

// cleanOldSecrets removes the old address's secrets that lab.yaml no
// longer uses. Podman removes a secret even while a container uses it,
// but that container then can't restart (docs/ASSUMPTIONS.md C12), so
// they go only once Caddy's unit no longer names them: after the restart
// that follows a change.
func (s *Setup) cleanOldSecrets(ctx context.Context) error {
	cur, err := s.current()
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	if cur.OldAddress != nil {
		for _, f := range dns.CredentialFields(cur.OldAddress.DNS) {
			keep[dns.OldSecretName(f)] = true
		}
	}
	names, err := s.Secrets.List(ctx)
	if err != nil {
		return err
	}
	for name := range names {
		if strings.HasPrefix(name, dns.OldSecretName("")) && !keep[name] {
			if err := s.Secrets.Remove(ctx, name); err != nil {
				return err
			}
		}
	}
	return nil
}

// EndOldAddress stops the lab's previous address working now (SPEC.md
// §6.6): it ends in lab.yaml at once, its DNS records are deleted and
// Roostack restarts so Caddy stops serving it (finishOld).
func (s *Setup) EndOldAddress(ctx context.Context) error {
	cur, err := s.current()
	if err != nil || cur.OldAddress == nil {
		return err
	}
	if now := time.Now(); now.Before(cur.OldAddress.Until) {
		if err := config.Update(s.LabPath, func(lab *config.Lab) {
			if lab.OldAddress != nil {
				lab.OldAddress.Until = now
			}
		}); err != nil {
			return err
		}
	}
	return s.finishOld(ctx, true)
}

// FinishOldAddress forgets an old address that has stopped working, once
// its records are gone (or the Owner had Roostack forget them): it goes
// from lab.yaml, and its login with it.
func (s *Setup) FinishOldAddress(ctx context.Context) error {
	return s.finishOld(ctx, false)
}

// removeOldFor bounds deleting the old address's records, so a provider
// that doesn't answer can't hold up the rest (it's tried again hourly).
const removeOldFor = 2 * time.Minute

// finishOld finishes an old address whose time is up: the records
// Roostack set under it are deleted with its login (RemoveOldRecords),
// then it goes from lab.yaml; restart says Caddy still serves it, so
// Roostack restarts. The login goes after that, once nothing uses it
// (cleanOldSecrets). While records are left, lab.yaml keeps the old
// address (no longer served) and its login, to try again; a restart
// part-way through carries on from what's left.
func (s *Setup) finishOld(ctx context.Context, restart bool) error {
	cur, err := s.current()
	if err != nil || cur.OldAddress == nil || time.Now().Before(cur.OldAddress.Until) {
		return err
	}
	old := *cur.OldAddress
	done := true
	if s.RemoveOldRecords != nil {
		rctx, cancel := context.WithTimeout(ctx, removeOldFor)
		done = s.RemoveOldRecords(rctx, old)
		cancel()
	}
	if done {
		if err := config.Update(s.LabPath, func(lab *config.Lab) {
			if lab.OldAddress != nil && lab.OldAddress.Domain == old.Domain {
				lab.OldAddress = nil
			}
		}); err != nil {
			return err
		}
	}
	switch {
	case restart && s.Restart != nil:
		s.Restart()
	case done:
		return s.cleanOldSecrets(ctx)
	}
	return nil
}

// RunOldAddress ends the previous address when its time is up, and
// removes credentials no longer needed, checking every hour until ctx
// ends.
func (s *Setup) RunOldAddress(ctx context.Context, log *slog.Logger) {
	// Caddy serves the old address if it still worked when Roostack
	// started.
	served := s.Lab.Old(time.Now()) != nil
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := s.cleanOldSecrets(ctx); err != nil {
			log.Warn("removing the old address's DNS credentials", "err", err)
		}
		if cur, err := s.current(); err == nil && cur.OldAddress != nil && !time.Now().Before(cur.OldAddress.Until) {
			if served {
				log.Info("the lab's old address stops working now", "address", cur.OldAddress.Domain)
			}
			if err := s.finishOld(ctx, served); err != nil {
				log.Error("ending the lab's old address", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// UseRoostAc makes a roost.ac name from Roostack Cloud the lab's address
// (SPEC.md §6.6): its credentials (the roostack-cloud DNS provider's
// fields: its challenge record and its A records) become the DNS
// provider's, Roostack manages its records, and Roostack restarts. A
// previous address keeps working for a while, as when switching domains.
func (s *Setup) UseRoostAc(ctx context.Context, name string, creds map[string]string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if !domainRe.MatchString(name) {
		return fmt.Errorf("Roostack Cloud gave an invalid name %q", name)
	}
	p := dns.Get(config.ProviderCloud)
	if _, err := p.Validate(creds); err != nil {
		return fmt.Errorf("Roostack Cloud's credentials for %s: %w", name, err)
	}
	// The secret (the password) becomes a Podman secret; the rest goes in
	// lab.yaml, written into Caddy's config as it is (dns.Field.InConfig).
	var secrets []string
	values := map[string]string{}
	for _, f := range p.Fields {
		v := strings.TrimSpace(creds[f.Key])
		switch {
		case v == "":
		case f.InConfig && !dns.SafeValue(v):
			return fmt.Errorf("Roostack Cloud's %s for %s has characters Roostack can't use", f.Label, name)
		case f.InConfig:
			values[f.Key] = v
		default:
			secrets = append(secrets, f.Key)
		}
	}
	old, err := s.keepOldAddress(ctx, name)
	if err != nil {
		return err
	}
	for _, k := range secrets {
		if err := s.Secrets.Replace(ctx, dns.SecretName(k), []byte(strings.TrimSpace(creds[k]))); err != nil {
			return err
		}
	}
	err = config.Update(s.LabPath, func(lab *config.Lab) {
		if old != nil {
			lab.OldAddress = old
		}
		lab.Domain = name
		lab.DNS = config.DNS{Provider: p.ID, Fields: secrets, Values: values, Zone: name, ManageRecords: true, LANIP: lab.DNS.LANIP}
	})
	if err != nil {
		return err
	}
	if err := s.Store.SetSetting(ctx, keyDomain, "done"); err != nil {
		return err
	}
	s.Restart()
	return nil
}

// DropRoostAc takes away the lab's roost.ac address when its link to
// Roostack Cloud ends (it can't work without it): the lab goes back to
// having no address, and Roostack restarts. An own domain is left alone.
func (s *Setup) DropRoostAc(ctx context.Context) error {
	cur, err := s.current()
	if err != nil || !cur.RoostAc() {
		return err
	}
	err = config.Update(s.LabPath, func(lab *config.Lab) {
		lab.Domain = ""
		lab.DNS = config.DNS{LANIP: lab.DNS.LANIP}
	})
	if err != nil {
		return err
	}
	// Caddy stops at the restart (no address), so its credentials can go.
	for _, f := range dns.CredentialFields(cur.DNS) {
		if err := s.Secrets.Remove(ctx, dns.SecretName(f)); err != nil {
			return err
		}
	}
	if err := s.Store.SetSetting(ctx, keyDomain, "later"); err != nil {
		return err
	}
	s.Restart()
	return nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// DomainLater skips the domain step for now. Services can't get HTTPS
// certificates until it's done, so the dashboard keeps reminding.
func (s *Setup) DomainLater(ctx context.Context) error {
	return s.Store.SetSetting(ctx, keyDomain, "later")
}

// AdoptExisting marks the backup step done if backups were already set up
// outside the wizard (e.g. with `roostack backup init`, which showed the key).
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
	if err := s.Store.SetSetting(ctx, keyBackup, "later"); err != nil {
		return err
	}
	// The wizard's last step: its settings take effect now.
	if s.Finished != nil {
		s.Finished()
	}
	return nil
}

// RecoveryKey returns the backup recovery key to show the Owner.
func (s *Setup) RecoveryKey() (string, error) {
	data, err := os.ReadFile(backup.KeyFile(s.Lab.Paths))
	return strings.TrimSpace(string(data)), err
}

// ConfirmRecoveryKey records that the Owner saved the key. It's the
// wizard's last step, so settings that waited for the end of the wizard
// take effect now (Finished restarts Roostack if one did, e.g. the backup
// destination). After the wizard, the first backup brings the key here
// too, with nothing else waiting.
func (s *Setup) ConfirmRecoveryKey(ctx context.Context) error {
	if err := s.Store.SetSetting(ctx, keyRecovery, "confirmed"); err != nil {
		return err
	}
	if s.Finished != nil {
		s.Finished()
	}
	return nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// SetLabName names the lab. With restart, Roostack restarts so every part
// (the network, storage shared with other labs) uses the new name.
func (s *Setup) SetLabName(ctx context.Context, name string, restart bool) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 60 || strings.ContainsFunc(name, unicode.IsControl) {
		return &usererr.Error{Msg: "Give the lab a name of up to 60 characters, such as \"The Smiths' lab\"."}
	}
	if err := config.Update(s.LabPath, func(lab *config.Lab) { lab.Name = name }); err != nil {
		return err
	}
	if restart && s.Restart != nil {
		s.Restart()
	}
	return nil
}

// BackupDrives lists the drives backups can go on, best first: a USB drive
// or second disk, then a network share, then this server's own disk.
func (s *Setup) BackupDrives(ctx context.Context) ([]agent.Drive, error) {
	if s.Drives == nil {
		return nil, nil
	}
	drives, err := s.Drives.Drives(ctx)
	if err != nil {
		return nil, err
	}
	rank := func(d agent.Drive) int {
		switch {
		case d.Kind == "system":
			return 4
		case !d.Usable:
			return 3
		case d.Kind == "network":
			return 2
		}
		return 1
	}
	slices.SortStableFunc(drives, func(a, b agent.Drive) int {
		if r := rank(a) - rank(b); r != 0 {
			return r
		}
		return cmp.Compare(b.Free, a.Free)
	})
	return drives, nil
}

// EraseDrive erases a disk and prepares it for backups.
func (s *Setup) EraseDrive(ctx context.Context, disk string) error {
	if s.Drives == nil {
		return &usererr.Error{Msg: "Roostack can't see this server's drives."}
	}
	if err := s.Drives.EraseDrive(ctx, disk, "ROOSTACK"); err != nil {
		return &usererr.Error{Msg: fmt.Sprintf("Couldn't prepare the disk: %s", firstLine(err.Error()))}
	}
	return nil
}

// EnsureRecoveryKey creates the lab's recovery key (the key every backup
// can be read with) if it doesn't exist yet.
func EnsureRecoveryKey(paths config.Paths) error {
	keyFile := backup.KeyFile(paths)
	if _, err := os.Stat(keyFile); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return fsutil.WritePrivate(keyFile, []byte(backup.NewRecoveryKey()+"\n"))
}

// BackupAdded records the wizard's backup step done once a backup is set
// up; for the first one, the recovery key must then be confirmed.
func (s *Setup) BackupAdded(ctx context.Context, first bool) error {
	if err := s.Store.SetSetting(ctx, keyBackup, "done"); err != nil {
		return err
	}
	if !first {
		return nil
	}
	return s.Store.SetSetting(ctx, keyRecovery, "")
}
