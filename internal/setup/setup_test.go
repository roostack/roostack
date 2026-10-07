package setup

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/store"
)

type fakeSecrets map[string][]byte

func (f fakeSecrets) Replace(_ context.Context, name string, v []byte) error {
	f[name] = v
	return nil
}

func (f fakeSecrets) Read(_ context.Context, name string) ([]byte, error) {
	v, ok := f[name]
	if !ok {
		return nil, errors.New("no such secret")
	}
	return v, nil
}

func (f fakeSecrets) Remove(_ context.Context, name string) error {
	delete(f, name)
	return nil
}

func (f fakeSecrets) List(context.Context) (map[string]bool, error) {
	names := map[string]bool{}
	for n := range f {
		names[n] = true
	}
	return names, nil
}

func newSetup(t *testing.T) (*Setup, fakeSecrets, *int) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	lab := config.Default()
	lab.Domain = "lab.example.com" // the placeholder from the example config
	lab.Paths.StateDir = dir
	lab.NodeID = "n-0000000001"
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

// Backup 1 finishes the backup step; the recovery key is shown before
// the wizard's one restart, which confirming it brings (Finished restarts
// only if a step's settings are waiting for it).
func TestBackupAdded(t *testing.T) {
	s, _, restarts := newSetup(t)
	finished := 0
	s.Finished = func() { finished++ }
	ctx := context.Background()
	for _, k := range []string{keyTimezone, keySSH, keyDomain} {
		s.Store.SetSetting(ctx, k, "done")
	}
	if err := EnsureRecoveryKey(s.Lab.Paths); err != nil {
		t.Fatal(err)
	}
	key, err := s.RecoveryKey()
	if err != nil || len(key) < 40 {
		t.Errorf("recovery key %q: %v", key, err)
	}
	if err := s.BackupAdded(ctx, true); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Progress(ctx); p.Next() != "/setup/recovery-key" || *restarts != 0 {
		t.Errorf("next %q restarts %d: the key must be shown before restarting", p.Next(), *restarts)
	}
	// Backups 2 and 3 don't ask for the key again.
	if err := s.ConfirmRecoveryKey(ctx); err != nil {
		t.Fatal(err)
	}
	s.BackupAdded(ctx, false)
	if p, _ := s.Progress(ctx); p.Next() != "" || finished != 1 || *restarts != 0 {
		t.Errorf("after confirming: next %q, finished %d, restarts %d", p.Next(), finished, *restarts)
	}
	if err := EnsureRecoveryKey(s.Lab.Paths); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.RecoveryKey(); again != key {
		t.Error("the recovery key must not change")
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

type fakeDrives struct{ dir string }

func (f fakeDrives) Drives(context.Context) ([]agent.Drive, error) {
	return []agent.Drive{{ID: "system", Kind: "system", Usable: true},
		{ID: "uuid:64B1", Kind: "usb", Name: "BACKUP", Usable: true, Free: -1}}, nil
}
func (f fakeDrives) UseDrive(_ context.Context, id string) (agent.UsedDrive, error) {
	return agent.UsedDrive{Dir: f.dir, UUID: "64B1"}, nil
}
func (f fakeDrives) EraseDrive(context.Context, string, string) error { return nil }
func (f fakeDrives) PrepareBackupFolder(_ context.Context, dir string) (string, error) {
	return dir + "/roostack", nil
}

// Choosing a drive readies it (roostack-agent), puts the repository in its
// roostack folder and records the drive, so backups check it's there.
func TestBackupDrivesRanked(t *testing.T) {
	s, _, _ := newSetup(t)
	s.Drives = fakeDrives{dir: t.TempDir()}
	drives, _ := s.BackupDrives(context.Background())
	if drives[0].ID != "uuid:64B1" || drives[1].ID != "system" {
		t.Errorf("a USB drive comes before this server's own disk: %+v", drives)
	}
}

func TestBackupLaterFinishesTheWizard(t *testing.T) {
	s, _, _ := newSetup(t)
	finished := 0
	s.Finished = func() { finished++ }
	if err := s.BackupLater(context.Background()); err != nil || finished != 1 {
		t.Errorf("finished = %d, %v", finished, err)
	}
}

// Where the lab is (SPEC.md §6.12): a guess until the Owner answers, then
// saved in lab.yaml (read back before the restart that applies it).
func TestNetworkChoice(t *testing.T) {
	s, _, restarts := newSetup(t)
	internet := func() string { return config.NetworkInternet }
	if c := s.Network(internet); c.Saved != "" || !c.Internet() {
		t.Fatalf("not asked yet: %+v", c)
	}
	if err := s.SetNetwork("moon", false); err == nil {
		t.Error("only home or internet")
	}
	if err := s.SetNetwork(config.NetworkHome, true); err != nil {
		t.Fatal(err)
	}
	if c := s.Network(internet); c.Saved != config.NetworkHome || c.Internet() || !c.UITunnelOnly || *restarts != 1 {
		t.Errorf("saved: %+v, restarts %d", c, *restarts)
	}
}

// Switching address keeps the old one working for 30 days, with its
// provider's credentials (SPEC.md §6.6).
func TestSwitchKeepsOldAddress(t *testing.T) {
	s, secrets, restarts := newSetup(t)
	ctx := context.Background()
	save := func(domain, provider string, v map[string]string) {
		t.Helper()
		if err := s.SaveDomain(ctx, domain, "me@example.com", provider, v, DomainOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	load := func() config.Lab {
		t.Helper()
		lab, err := config.Load(s.LabPath)
		if err != nil {
			t.Fatal(err)
		}
		return lab
	}
	// The first address: nothing old (the placeholder isn't an address).
	save("home.example.com", "cloudflare", map[string]string{"api_token": "good"})
	if load().OldAddress != nil {
		t.Fatal("first address: no old one")
	}
	secrets[dns.SecretName("api_token")] = []byte("token-1")
	// Saving it again (e.g. a new token) isn't a switch.
	save("home.example.com", "cloudflare", map[string]string{"api_token": "good"})
	if load().OldAddress != nil {
		t.Fatal("same address: no old one")
	}
	secrets[dns.SecretName("api_token")] = []byte("token-1")
	save("smith.family", "porkbun", map[string]string{"api_key": "good", "api_secret_key": "good"})
	old := load().OldAddress
	if old == nil || old.Domain != "home.example.com" || old.DNS.Provider != "cloudflare" ||
		time.Until(old.Until) < 29*24*time.Hour || time.Until(old.Until) > 31*24*time.Hour {
		t.Fatalf("old = %+v", old)
	}
	if string(secrets[dns.OldSecretName("api_token")]) != "token-1" {
		t.Error("the old provider's token is kept")
	}

	// Ending it: lab.yaml first and a restart; the credentials go after.
	before := *restarts
	if err := s.EndOldAddress(ctx); err != nil || load().OldAddress != nil || *restarts != before+1 {
		t.Fatalf("end: %v %+v", err, load().OldAddress)
	}
	if err := s.cleanOldSecrets(ctx); err != nil || secrets[dns.OldSecretName("api_token")] != nil {
		t.Errorf("old secrets left: %v", err)
	}
	if secrets[dns.SecretName("api_key")] == nil {
		t.Error("the current provider's secrets stay")
	}
}

// A roost.ac name becomes the lab's address with Cloud's credentials,
// and goes when the link does (SPEC.md §6.6).
func TestRoostAc(t *testing.T) {
	s, secrets, restarts := newSetup(t)
	ctx := context.Background()
	creds := map[string]string{"server_url": "https://acme.example", "username": "u", "password": "p", "subdomain": "s",
		"records_url": "https://cloud.example/v1/records"}
	if err := s.UseRoostAc(ctx, "smiths.roost.ac", creds); err != nil {
		t.Fatal(err)
	}
	lab, err := config.Load(s.LabPath)
	if err != nil {
		t.Fatal(err)
	}
	if lab.Domain != "smiths.roost.ac" || !lab.RoostAc() || lab.UIHost() != "smiths.roost.ac" || !lab.DNS.ManageRecords ||
		lab.DNS.Zone != "smiths.roost.ac" || string(secrets[dns.SecretName("password")]) != "p" || *restarts != 1 {
		t.Fatalf("lab = %+v, restarts %d", lab.DNS, *restarts)
	}
	if p, _ := s.Progress(ctx); !p.DomainDone {
		t.Error("the address step is done")
	}
	if err := s.DropRoostAc(ctx); err != nil {
		t.Fatal(err)
	}
	lab, _ = config.Load(s.LabPath)
	if lab.HasAddress() || lab.DNS.Provider != "" || secrets[dns.SecretName("password")] != nil || *restarts != 2 {
		t.Fatalf("after dropping: %+v", lab)
	}
	if p, _ := s.Progress(ctx); p.DomainDone || !p.DomainLater {
		t.Error("back to no address yet")
	}
}
