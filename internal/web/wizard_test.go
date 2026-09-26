package web

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/dnsrecords"
	"github.com/mylab/mylab/internal/setup"
)

type fakeSetup struct {
	p          setup.Progress
	zone       string
	restarted  bool
	ssh        string
	clients    []netip.Addr
	domainErr  error
	backupErr  error
	lastBackup setup.BackupChoice

	lastProvider string
	lastValues   map[string]string
	lastOpts     setup.DomainOptions
}

func (f *fakeSetup) Progress(context.Context) (setup.Progress, error) { return f.p, nil }
func (f *fakeSetup) Timezones() (string, []string, error) {
	return f.zone, []string{"Australia/Melbourne", "Etc/UTC"}, nil
}
func (f *fakeSetup) SSHAccess() (setup.SSHAccess, error) {
	return setup.SSHAccess{Choice: f.ssh, Clients: f.clients}, nil
}
func (f *fakeSetup) SaveSSH(_ context.Context, choice string, confirmed bool) error {
	if choice == "off" && len(f.clients) > 0 && !confirmed {
		return &setup.CheckError{Msg: "That would disconnect the SSH connection."}
	}
	f.ssh, f.p.SSHDone, f.restarted = choice, true, true
	return nil
}
func (f *fakeSetup) SaveTimezone(_ context.Context, name string) (bool, error) {
	if name != "Australia/Melbourne" && name != "Etc/UTC" {
		return false, &setup.CheckError{Msg: "Choose a time zone from the list."}
	}
	changed := name != f.zone
	f.zone, f.p.TimezoneDone, f.restarted = name, true, f.restarted || changed
	return changed, nil
}
func (f *fakeSetup) SaveDomain(_ context.Context, d, e, provider string, values map[string]string, opts setup.DomainOptions) error {
	f.lastProvider, f.lastValues, f.lastOpts = provider, values, opts
	if f.domainErr != nil {
		return f.domainErr
	}
	f.p.DomainDone = true
	return nil
}
func (f *fakeSetup) DomainLater(context.Context) error { f.p.DomainLater = true; return nil }
func (f *fakeSetup) SaveBackup(_ context.Context, c setup.BackupChoice) error {
	f.lastBackup = c
	if f.backupErr != nil {
		return f.backupErr
	}
	f.p.BackupDone = true
	return nil
}
func (f *fakeSetup) BackupLater(context.Context) error        { f.p.BackupLater = true; return nil }
func (f *fakeSetup) RecoveryKey() (string, error)             { return "the-recovery-key", nil }
func (f *fakeSetup) ConfirmRecoveryKey(context.Context) error { f.p.RecoverySaved = true; return nil }

func TestWizardFlow(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{zone: "Etc/UTC"}
	h.server.Setup = fs

	_, body := h.get("/")
	if !strings.Contains(body, "Setup · step 1 of 4") || !strings.Contains(body, `data-zone="Etc/UTC"`) ||
		!strings.Contains(body, `<option value="Australia/Melbourne">`) {
		t.Fatalf("an Owner with setup unfinished should land on the time zone step:\n%.3000s", body)
	}
	if code, body := h.post("/setup/timezone", url.Values{"timezone": {"Mars/Base"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "Choose a time zone") || !strings.Contains(body, `data-zone="Mars/Base"`) {
		t.Errorf("bad zone: %d", code)
	}
	// Keeping the zone goes straight on to the next step.
	h.post("/setup/timezone", url.Values{"timezone": {"Etc/UTC"}}, h.srv.URL)
	if _, body := h.get("/"); fs.restarted || !strings.Contains(body, "Who can log in to this machine?") {
		t.Fatal("after the time zone comes the SSH step, without a restart")
	}
	fs.clients = []netip.Addr{netip.MustParseAddr("192.168.86.20")}
	_, body = h.get("/setup/ssh")
	if !strings.Contains(body, "<code>192.168.86.20</code> (your home network)") || !strings.Contains(body, `data-choice="lan"`) {
		t.Error("SSH step: should list current connections and default to home network only")
	}
	if code, body := h.post("/setup/ssh", url.Values{"ssh": {"off"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "would disconnect") || !strings.Contains(body, `data-choice="off"`) {
		t.Errorf("cutting off a connection must be confirmed: %d", code)
	}
	h.post("/setup/ssh", url.Values{"ssh": {"lan"}}, h.srv.URL)
	if _, body := h.get("/"); fs.ssh != "lan" || !strings.Contains(body, "Your domain") {
		t.Fatal("after the SSH step comes the domain step")
	}

	_, body = h.get("/setup/domain")
	for _, want := range []string{"Amazon Route 53", "Porkbun", "Gandi", "GoDaddy", "Namecheap", "Spaceship",
		`name="dns.route53.secret_access_key"`} {
		if !strings.Contains(body, want) {
			t.Errorf("domain page missing %s", want)
		}
	}

	fs.domainErr = &setup.CheckError{Msg: "Route 53 didn't accept those details."}
	code, body := h.post("/setup/domain", url.Values{"domain": {"home.example.com"}, "email": {"a@b.c"}, "provider": {"route53"},
		"dns.route53.access_key_id": {"AKIAEXAMPLE"}, "dns.route53.secret_access_key": {"secret-value"},
		"dns.cloudflare.api_token": {"ignored"}}, h.srv.URL)
	if code != http.StatusBadRequest || !strings.Contains(body, "didn&#39;t accept") || !strings.Contains(body, `value="home.example.com"`) {
		t.Errorf("domain error: %d", code)
	}
	if fs.lastProvider != "route53" || fs.lastValues["secret_access_key"] != "secret-value" || fs.lastValues["api_token"] != "" {
		t.Errorf("provider %q values %v: only the chosen provider's fields count", fs.lastProvider, fs.lastValues)
	}
	if strings.Contains(body, "secret-value") || !strings.Contains(body, `value="AKIAEXAMPLE"`) {
		t.Error("keep non-sensitive fields; never echo sensitive ones")
	}
	fs.domainErr = nil
	if _, body := h.post("/setup/domain", url.Values{"domain": {"home.example.com"}}, h.srv.URL); !strings.Contains(body, "restarting") {
		t.Error("saving the domain should go to the restarting page")
	}

	if _, body := h.get("/"); !strings.Contains(body, "Where should backups go") {
		t.Fatal("next comes the backup step")
	}
	fs.backupErr = &setup.CheckError{Msg: "Couldn't create the backup there"}
	_, body = h.post("/setup/backup", url.Values{"kind": {"s3"}, "endpoint": {"https://s3.example"}, "bucket": {"b"},
		"access_key": {"AKID"}, "secret_key": {"SUPERSECRET"}}, h.srv.URL)
	if !strings.Contains(body, "Couldn&#39;t create") || !strings.Contains(body, `value="AKID"`) || strings.Contains(body, "SUPERSECRET") {
		t.Error("backup error: keep fields, never echo the secret key")
	}
	if fs.lastBackup.SecretKey != "SUPERSECRET" || fs.lastBackup.Kind != "s3" {
		t.Errorf("choice passed on: %+v", fs.lastBackup)
	}
	fs.backupErr = nil
	h.post("/setup/backup", url.Values{"kind": {"local"}, "path": {"/mnt/backup"}}, h.srv.URL)

	resp, err := h.client.Get(h.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/setup/recovery-key" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("recovery key page: %s, cache %q", resp.Request.URL.Path, resp.Header.Get("Cache-Control"))
	}
	if code, _ := h.post("/setup/recovery-key", nil, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("continuing without ticking the box: %d", code)
	}
	h.post("/setup/recovery-key", url.Values{"saved": {"on"}}, h.srv.URL)
	if _, body := h.get("/"); !strings.Contains(body, "Your services") {
		t.Error("after the wizard the Owner sees the dashboard")
	}
}

func TestDomainLaterReminder(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true}}
	h.server.Setup = fs
	h.post("/setup/domain/later", nil, h.srv.URL)
	h.post("/setup/backup/later", nil, h.srv.URL)
	if _, body := h.get("/"); !strings.Contains(body, `href="/setup/domain">Set up your domain`) {
		t.Error("postponed steps must stay visible on the dashboard")
	}
}

func TestWizardOwnerOnly(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	h.server.Setup = &fakeSetup{}
	if _, body := h.get("/"); !strings.Contains(body, "Your services") {
		t.Error("admins are not sent into the wizard")
	}
	for _, path := range []string{"/setup/timezone", "/setup/domain", "/setup/backup", "/setup/recovery-key"} {
		if code, _ := h.get(path); code != http.StatusForbidden {
			t.Errorf("GET %s as admin: %d", path, code)
		}
	}
}

func TestHealthzBootID(t *testing.T) {
	h := newHarness(t, "")
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/healthz?since="+h.server.bootID, nil)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.Header.Get("HX-Redirect") != "" {
		t.Error("same process: keep waiting")
	}
	resp, _ = http.Get(h.srv.URL + "/healthz?since=an-older-boot")
	resp.Body.Close()
	if resp.Header.Get("HX-Redirect") != "/" {
		t.Error("new process: move on")
	}
}

func TestDomainRecordOptions(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{}
	h.server.Setup = fs
	_, body := h.get("/setup/domain")
	if !strings.Contains(body, `name="manage_records" x-model="manage" checked`) || !strings.Contains(body, "Namecheap&#39;s API is risky") &&
		!strings.Contains(body, "Namecheap's API is risky") {
		t.Error("domain page should offer record management (on by default) and the Namecheap warning")
	}
	h.post("/setup/domain", url.Values{"domain": {"home.example.com"}, "email": {"a@b.c"}, "provider": {"namecheap"},
		"manage_records": {"on"}, "accept_risk": {"on"}}, h.srv.URL)
	if !fs.lastOpts.ManageRecords || !fs.lastOpts.AcceptRisk {
		t.Errorf("options not passed on: %+v", fs.lastOpts)
	}
}

type fakeDNS struct {
	st        dnsrecords.Status
	managing  []bool
	takenOver bool
}

func (f *fakeDNS) Status() dnsrecords.Status { return f.st }
func (f *fakeDNS) SetManaging(on, risk bool) error {
	if on && f.st.Risky && !risk {
		return dnsrecords.ErrNeedsRisk
	}
	f.managing = append(f.managing, on)
	return nil
}
func (f *fakeDNS) TakeOver(context.Context) error { f.takenOver = true; return nil }

func TestDNSCard(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.server.Setup = &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}}
	h.server.DNS = &fakeDNS{st: dnsrecords.Status{State: dnsrecords.StateBlocked, Record: "*.lab.example.com", IP: "192.168.1.20",
		Message: "your router's DNS rebinding protection", Checked: time.Now()}}
	_, body := h.get("/settings")
	if !strings.Contains(body, "Home network access") || !strings.Contains(body, "rebinding protection") || !strings.Contains(body, "DHCP reservation") {
		t.Error("owner should see the home network card with the problem")
	}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "rebinding protection") || !strings.Contains(body, `href="/settings#dns"`) {
		t.Error("the problem should also be on the home page's attention list, linking to settings")
	}

	a, _ := loggedIn(t, auth.RoleAdmin)
	a.server.DNS = h.server.DNS
	if _, body := a.get("/partials/home"); strings.Contains(body, "rebinding") {
		t.Error("DNS problems are for the Owner only")
	}
}

func TestDNSControls(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.server.Setup = &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}}
	fd := &fakeDNS{st: dnsrecords.Status{State: dnsrecords.StateManual, Record: "*.lab.example.com", IP: "192.168.1.20",
		Provider: "Namecheap", CanManage: true, Risky: true, Checked: time.Now()}}
	h.server.DNS = fd

	_, body := h.get("/settings")
	if !strings.Contains(body, "Let MyLab manage DNS records at Namecheap?") || !strings.Contains(body, "one setting for the whole lab") ||
		!strings.Contains(body, "API is risky") {
		t.Error("manual mode should offer opting in, with the risk warning for Namecheap")
	}
	if code, _ := h.post("/dns/manage", url.Values{"manage": {"on"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("opting in without accepting the risk: %d", code)
	}
	h.post("/dns/manage", url.Values{"manage": {"on"}, "accept_risk": {"on"}}, h.srv.URL)
	if len(fd.managing) != 1 || !fd.managing[0] {
		t.Errorf("managing = %v", fd.managing)
	}

	// A conflict offers taking over, which needs confirmation.
	fd.st = dnsrecords.Status{State: dnsrecords.StateConflict, Record: "*.lab.example.com", IP: "192.168.1.20",
		Current: "203.0.113.5", Managing: true, CanManage: true, Provider: "Cloudflare", Checked: time.Now()}
	if _, body := h.get("/settings"); !strings.Contains(body, "/dns/take-over") || !strings.Contains(body, "Stop managing") {
		t.Error("conflict should offer taking over; managed mode should offer stopping")
	}
	_, body = h.get("/dns/take-over")
	if !strings.Contains(body, "203.0.113.5") || !strings.Contains(body, "192.168.1.20") || !strings.Contains(body, "stop working") {
		t.Error("the take-over page must show old and new values and the warning")
	}
	if code, _ := h.post("/dns/take-over", nil, h.srv.URL); code != http.StatusBadRequest || fd.takenOver {
		t.Error("taking over needs the confirmation box")
	}
	h.post("/dns/take-over", url.Values{"confirm": {"on"}}, h.srv.URL)
	if !fd.takenOver {
		t.Error("confirmed take-over not applied")
	}

	a, _ := loggedIn(t, auth.RoleAdmin)
	a.server.DNS = fd
	if code, _ := a.post("/dns/manage", url.Values{"manage": {"off"}}, a.srv.URL); code != http.StatusForbidden {
		t.Errorf("admins can't change DNS settings: %d", code)
	}
}

func TestSetupFormsAsSettingsPages(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupDone: true, RecoverySaved: true}}
	h.server.Setup = fs
	h.server.BackupRepo = "/mnt/backup/mylab"

	_, body := h.get("/setup/backup")
	if strings.Contains(body, "step 2 of 2") || strings.Contains(body, "Set this up later") {
		t.Error("changing backups later must not look like the first-run wizard")
	}
	if !strings.Contains(body, "Change where backups go") || !strings.Contains(body, "/mnt/backup/mylab") || !strings.Contains(body, `href="/backups"`) {
		t.Error("the settings version should say Change, show the current destination and link back")
	}
	_, body = h.get("/setup/domain")
	if strings.Contains(body, "step 3 of 4") || !strings.Contains(body, "Change your domain") || strings.Contains(body, "/setup/domain/later") {
		t.Error("changing the domain later must not look like the wizard")
	}

	// A postponed step is "Set up", still without the wizard's framing.
	fs.p = setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}
	if _, body := h.get("/setup/backup"); !strings.Contains(body, "<h1>Set up backups</h1>") || strings.Contains(body, "step 2") {
		t.Error("a postponed step should read as setting it up now")
	}

	// During first-run setup the wizard framing stays.
	fs.p = setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true}
	if _, body := h.get("/setup/backup"); !strings.Contains(body, "step 4 of 4") || !strings.Contains(body, "Set this up later") {
		t.Error("first-run setup keeps its steps")
	}
}

func TestTimezoneBanner(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{zone: "Etc/UTC", p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}}
	h.server.Setup = fs

	// The browser compares its UTC offset with the host's.
	_, body := h.get("/")
	if !strings.Contains(body, "Times in MyLab are this machine's time") || !strings.Contains(body, `href="/setup/timezone"`) {
		t.Error("home page: missing the time zone banner for the Owner")
	}
	_, body = h.get("/settings")
	if !strings.Contains(body, "<h2>Time zone</h2>") {
		t.Error("settings: missing the time zone card")
	}
	// Changing it from Settings restarts MyLab.
	if code, _ := h.post("/setup/timezone", url.Values{"timezone": {"Australia/Melbourne"}}, h.srv.URL); code >= 400 || !fs.restarted {
		t.Errorf("change zone: %d restarted=%v", code, fs.restarted)
	}
}
