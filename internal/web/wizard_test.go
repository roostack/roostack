package web

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/units"
)

type fakeSetup struct {
	pending bool
	// noKey: the lab has no recovery key yet (the first backup makes it).
	noKey       bool
	drives      []agent.Drive
	erased      string
	labName     string
	restartMode string
	destination string
	p           setup.Progress
	zone        string
	restarted   bool
	ssh         string
	policy      string
	clients     []netip.Addr
	domainErr   error
	backupErr   error
	// network is where the lab is (as saved), uiTunnelOnly its UI choice.
	network      string
	uiTunnelOnly bool

	lastProvider string
	lastValues   map[string]string
	lastOpts     setup.DomainOptions
}

func (f *fakeSetup) Progress(context.Context) (setup.Progress, error) { return f.p, nil }
func (f *fakeSetup) Done(_ context.Context, step string) error {
	switch step {
	case setup.StepTimezone:
		f.p.TimezoneDone = true
	case setup.StepSSH:
		f.p.SSHDone = true
	default:
		return fmt.Errorf("unknown step %q", step)
	}
	return nil
}

// fakeSetup is also the server's settings (node.Settings).
func (f *fakeSetup) Timezones() (string, []string, error) {
	return f.zone, []string{"Australia/Melbourne", "Etc/UTC"}, nil
}
func (f *fakeSetup) SSHAccess() (node.SSHAccess, error) {
	return node.SSHAccess{Choice: f.ssh, Clients: f.clients}, nil
}
func (f *fakeSetup) SetSSH(_ context.Context, choice string, confirmed bool) error {
	if choice == "off" && len(f.clients) > 0 && !confirmed {
		return &node.FormError{Msg: "That would disconnect the SSH connection."}
	}
	f.ssh, f.restarted = choice, true
	return nil
}
func (f *fakeSetup) RestartPolicy() (string, int) {
	return cmp.Or(f.restartMode, config.RestartAuto), config.Maintenance{}.Hour()
}
func (f *fakeSetup) PendingRestart() bool { return f.pending }
func (f *fakeSetup) BackupDrives(context.Context) ([]agent.Drive, error) {
	if f.drives != nil {
		return f.drives, nil
	}
	return []agent.Drive{{ID: "system", Kind: "system", Name: "This server's own disk", Usable: true, Free: 30 * units.GB}}, nil
}
func (f *fakeSetup) EraseDrive(_ context.Context, disk string) error {
	f.erased = disk
	for i, d := range f.drives {
		if d.Disk == disk {
			f.drives[i] = agent.Drive{ID: "uuid:new", Kind: d.Kind, Name: "ROOSTACK", Disk: disk, Usable: true, Erasable: true}
		}
	}
	return nil
}
func (f *fakeSetup) SetLabName(_ context.Context, name string, restart bool) error {
	if strings.TrimSpace(name) == "" {
		return &setup.CheckError{Msg: "Give the lab a name."}
	}
	f.labName, f.restarted = name, restart
	return nil
}
func (f *fakeSetup) Network(detect func() string) setup.NetworkChoice {
	c := setup.NetworkChoice{Saved: f.network, UITunnelOnly: f.uiTunnelOnly}
	if detect != nil {
		c.Detected = detect()
	}
	return c
}
func (f *fakeSetup) SetNetwork(network string, uiTunnelOnly bool) error {
	if network != config.NetworkHome && network != config.NetworkInternet {
		return &setup.CheckError{Msg: "Choose where this lab is."}
	}
	f.network, f.uiTunnelOnly, f.restarted = network, uiTunnelOnly, true
	return nil
}
func (f *fakeSetup) PromoteBackupCopy(context.Context, string, node.BackupCopy) error { return nil }
func (f *fakeSetup) SetBackupCopies(context.Context, []node.BackupCopy) error         { return nil }
func (f *fakeSetup) SetBackupDestination(_ context.Context, d node.BackupDestination) error {
	f.destination = d.Repository
	return nil
}
func (f *fakeSetup) SetRestartPolicy(_ context.Context, mode string, hour int) error {
	if hour < 0 || hour > 23 {
		return &node.FormError{Msg: "Choose an hour between 0 and 23."}
	}
	f.policy, f.restarted = fmt.Sprintf("%s %d", mode, hour), true
	return nil
}
func (f *fakeSetup) SetTimezone(_ context.Context, name string) (bool, error) {
	if name != "Australia/Melbourne" && name != "Etc/UTC" {
		return false, &node.FormError{Msg: "Choose a time zone from the list."}
	}
	changed := name != f.zone
	f.zone, f.restarted = name, f.restarted || changed
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
func (f *fakeSetup) BackupAdded(_ context.Context, first bool) error {
	f.noKey = false
	f.p.BackupDone = true
	if first {
		f.p.RecoverySaved = false
	}
	return nil
}
func (f *fakeSetup) BackupLater(context.Context) error { f.p.BackupLater = true; return nil }
func (f *fakeSetup) RecoveryKey() (string, error) {
	if f.noKey {
		return "", os.ErrNotExist
	}
	return "the-recovery-key", nil
}
func (f *fakeSetup) ConfirmRecoveryKey(context.Context) error { f.p.RecoverySaved = true; return nil }

func TestWizardFlow(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{zone: "Etc/UTC"}
	h.setSetup(fs)

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
	if _, body := h.get("/"); fs.restarted || !strings.Contains(body, "Who can log in to this server?") {
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
	if _, body := h.get("/"); fs.ssh != "lan" || !strings.Contains(body, "<h1>Your lab's address</h1>") {
		t.Fatal("after the SSH step comes the address step")
	}

	// A new lab has no address: both choices are offered, the own domain's
	// form closed until chosen (SPEC.md §6.6). roost.ac links the lab to
	// Roostack Cloud (cloud_test.go).
	h.server.Cloud = newCloud(t)
	_, body = h.get("/setup/domain")
	for _, want := range []string{"A free roost.ac address", `action="/settings/cloud/link" target="_blank"`, "Your own domain",
		`x-data="{ choice: '' }"`, `<input name="domain" value="" placeholder="smith.family"`, "/help/settings/domain/#your-own-domain",
		`id="cloud-live"`, `hx-get="/settings/cloud/name"`} {
		if !strings.Contains(body, want) {
			t.Errorf("address step missing %q", want)
		}
	}
	// With Roostack Cloud turned off, it isn't offered.
	h.server.Cloud = nil
	if _, body := h.get("/setup/domain"); strings.Contains(body, "roost.ac") {
		t.Error("Cloud off: no roost.ac choice")
	}
	if code, _ := h.post("/settings/cloud/link", url.Values{"name": {"smiths"}}, h.srv.URL); code != http.StatusNotFound {
		t.Errorf("Cloud off: %d", code)
	}
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
	// During the wizard, no restart after each step: on to the next one
	// (the restart waits for the wizard's end).
	if _, body := h.post("/setup/domain", url.Values{"domain": {"home.example.com"}}, h.srv.URL); strings.Contains(body, "restarting") ||
		!strings.Contains(body, "Your backup plan") {
		t.Error("saving the domain should go on to the backup step")
	}

	if _, body := h.get("/"); !strings.Contains(body, "Your backup plan") || !strings.Contains(body, "Set up backups later") {
		t.Fatal("next comes the backup step")
	}
	if code, body := h.post("/setup/backup", nil, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "Add your main backup first") {
		t.Errorf("continuing with no backup: %d", code)
	}
	// A failed online account keeps the chooser open, never echoing the
	// secret key.
	code, body = h.post("/setup/backup/add", url.Values{"add": {"2"}, "choice": {"online"}, "provider": {"new"},
		"endpoint": {"https://s3.example"}, "access_key": {"AKID"}, "secret_key": {"SUPERSECRET"}}, h.srv.URL)
	if code != http.StatusBadRequest || !strings.Contains(body, "Where should your main backup go?") || strings.Contains(body, "SUPERSECRET") {
		t.Errorf("online storage error: %d", code)
	}
	h.post("/setup/backup/add", url.Values{"add": {"2"}, "choice": {"online"}, "provider": {"provider:1"}}, h.srv.URL)
	if fm.destination != "provider:1" || !fs.p.BackupDone {
		t.Fatalf("backup 1: %q, step done %v", fm.destination, fs.p.BackupDone)
	}
	h.post("/setup/backup", nil, h.srv.URL)

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
	if _, body := h.get("/"); !strings.Contains(body, "My Services") {
		t.Error("after the wizard the Owner sees the dashboard")
	}
}

func TestDomainLaterReminder(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true}}
	h.setSetup(fs)
	h.post("/setup/domain/later", nil, h.srv.URL)
	h.post("/setup/backup/later", nil, h.srv.URL)
	if _, body := h.get("/"); !strings.Contains(body, `href="/setup/domain">Give it an address`) {
		t.Error("postponed steps must stay visible on the dashboard")
	}
}

func TestWizardOwnerOnly(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	h.setSetup(&fakeSetup{})
	if _, body := h.get("/"); !strings.Contains(body, "My Services") {
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
	h.setSetup(fs)
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
	records   []dnsrecords.Record
	managing  []bool
	takenOver bool
}

func (f *fakeDNS) Status() dnsrecords.Status                            { return f.st }
func (f *fakeDNS) Records(context.Context) ([]dnsrecords.Record, error) { return f.records, nil }
func (f *fakeDNS) SetManaging(on, risk bool) error {
	if on && f.st.Risky && !risk {
		return dnsrecords.ErrNeedsRisk
	}
	f.managing = append(f.managing, on)
	return nil
}
func (f *fakeDNS) TakeOver(context.Context) error { f.takenOver = true; return nil }
func (f *fakeDNS) Refresh()                       {}

func TestDNSCard(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}})
	h.server.DNS = &fakeDNS{st: dnsrecords.Status{State: dnsrecords.StateBlocked, Record: "*.lab.example.com", IP: "192.168.1.20",
		Message: "your router's DNS rebinding protection", Checked: time.Now()}}
	_, body := h.get("/settings/home-network")
	if !strings.Contains(body, "Home network access") || !strings.Contains(body, "rebinding protection") || !strings.Contains(body, "DHCP reservation") {
		t.Error("owner should see the home network card with the problem")
	}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "your router blocks their addresses") || !strings.Contains(body, `href="/settings/home-network"`) {
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
	h.setSetup(&fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}})
	fd := &fakeDNS{st: dnsrecords.Status{State: dnsrecords.StateManual, Record: "*.lab.example.com", IP: "192.168.1.20",
		Provider: "Namecheap", CanManage: true, Risky: true, Checked: time.Now()}}
	h.server.DNS = fd

	_, body := h.get("/settings/home-network")
	if !strings.Contains(body, "Let Roostack manage DNS records at Namecheap?") || !strings.Contains(body, "one setting for the whole lab") ||
		!strings.Contains(body, "API is risky") {
		t.Error("manual mode should offer opting in, with the risk warning for Namecheap")
	}
	// Turning it on asks first, in a dialog saying what will change; for a
	// risky provider, confirming it is accepting the risk.
	if !strings.Contains(body, "<dialog") || !strings.Contains(body, "Let Roostack manage DNS records at Namecheap anyway?") ||
		!strings.Contains(body, "will point *.lab.example.com at this server (192.168.1.20) now") ||
		!strings.Contains(body, `name="accept_risk" value="on"`) {
		t.Error("turning management on should ask first, saying what will change and the risk")
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
	_, body = h.get("/settings/home-network")
	if !strings.Contains(body, "/dns/take-over") || !strings.Contains(body, "Stop managing") {
		t.Error("conflict should offer taking over; managed mode should offer stopping")
	}
	if !strings.Contains(body, "Stop managing DNS records at Cloudflare? The records stay as they are") ||
		!strings.Contains(body, `name="manage" value="off"`) {
		t.Error("stopping should ask first, saying what changes")
	}
	// The page refreshes itself only while a sync runs.
	if strings.Contains(body, `hx-get="/settings/home-network"`) {
		t.Error("no sync running: nothing to refresh")
	}
	fd.st.Syncing = true
	if _, body := h.get("/settings/home-network"); !strings.Contains(body, `hx-get="/settings/home-network"`) ||
		!strings.Contains(body, "this page updates by itself") {
		t.Error("while syncing, the page should refresh itself")
	}
	fd.st.Syncing = false
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
	h.setSetup(fs)
	h.server.BackupRepo = "/mnt/backup/roostack"

	resp, err := h.client.Get(h.srv.URL + "/setup/backup")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/backups" {
		t.Errorf("after the wizard, backups are changed on the Backups page, not %s", resp.Request.URL.Path)
	}
	var body string
	_, body = h.get("/setup/domain")
	if strings.Contains(body, "step 3 of 4") || !strings.Contains(body, "Change your lab's address") || strings.Contains(body, "/setup/domain/later") {
		t.Error("changing the domain later must not look like the wizard")
	}

	// During first-run setup the wizard framing stays.
	fs.p = setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true}
	if _, body := h.get("/setup/backup"); !strings.Contains(body, "step 4 of 4") || !strings.Contains(body, "Set up backups later") {
		t.Error("first-run setup keeps its steps")
	}
}

func TestTimezoneBanner(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{zone: "Etc/UTC", p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}}
	h.setSetup(fs)

	// The browser compares its UTC offset with the host's.
	_, body := h.get("/")
	if !strings.Contains(body, "Times in Roostack are this server's time") || !strings.Contains(body, `href="/setup/timezone"`) {
		t.Error("home page: missing the time zone banner for the Owner")
	}
	_, body = h.get("/settings")
	if !strings.Contains(body, `href="/settings/time-zone"`) || !strings.Contains(body, "Australia/Melbourne") && !strings.Contains(body, " now</span>") {
		t.Error("settings overview: missing the time zone area")
	}
	// Changing it from Settings restarts Roostack.
	if code, _ := h.post("/setup/timezone", url.Values{"timezone": {"Australia/Melbourne"}}, h.srv.URL); code >= 400 || !fs.restarted {
		t.Errorf("change zone: %d restarted=%v", code, fs.restarted)
	}
}

type fakeUpdates struct {
	changes   osupdate.HostCheck
	known     bool
	last      *osupdate.Result
	restart   osupdate.RestartStatus
	submitted int
	restarted bool
	busy      bool
}

func (f *fakeUpdates) Last(context.Context) (*osupdate.Result, error) { return f.last, nil }
func (f *fakeUpdates) Pending() bool                                  { return f.submitted > 0 }
func (f *fakeUpdates) Submit() error                                  { f.submitted++; return nil }
func (f *fakeUpdates) RestartStatus() osupdate.RestartStatus          { return f.restart }
func (f *fakeUpdates) HostChanges(context.Context) (osupdate.HostCheck, error) {
	c := f.changes
	c.Known = f.known
	return c, nil
}
func (f *fakeUpdates) AcknowledgeHostChanges(context.Context) error { f.known = true; return nil }
func (f *fakeUpdates) RestartNow(context.Context) error {
	if f.busy {
		return osupdate.ErrBusy{Job: "backup"}
	}
	f.restarted = true
	return nil
}

func TestUpdatesCard(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}}
	h.setSetup(fs)
	fu := &fakeUpdates{
		last:    &osupdate.Result{At: time.Now(), OK: false, Summary: "Updating didn't finish: exit status 100", Output: "E: Could not get lock"},
		restart: osupdate.RestartStatus{Needed: true, Packages: []string{"linux-image-amd64"}, Waiting: "reboot pending: waiting for the maintenance window"},
	}
	h.node().updates = fu

	_, body := h.get("/settings/updates")
	for _, want := range []string{`id="updates"`, "Could not get lock", "linux-image-amd64", "Restart now", `<option value="4" selected>04:00</option>`,
		"Needed", "automatic at 04:00"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings missing %q", want)
		}
	}
	_, home := h.get("/")
	if !strings.Contains(home, "OS updates didn&#39;t finish") || !strings.Contains(home, "will restart around 04:00") {
		t.Error("home: want the failed update and the planned restart")
	}
	fs.restartMode = config.RestartNotify
	if _, home := h.get("/"); !strings.Contains(home, "needs a restart to finish installing updates") {
		t.Error("home: notify-only should ask the Owner to restart")
	}

	h.post("/settings/updates/run", nil, h.srv.URL)
	if fu.submitted != 1 {
		t.Error("update now not submitted")
	}
	fu.busy = true
	if code, body := h.post("/settings/restart", nil, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "wait until backup has finished") {
		t.Errorf("restart during a backup: %d", code)
	}
	fu.busy = false
	if _, body := h.post("/settings/restart", nil, h.srv.URL); !fu.restarted || !strings.Contains(body, "Restarting this server") {
		t.Error("restart now")
	}
	h.post("/settings/restart-policy", url.Values{"restart": {"notify"}, "hour": {"3"}}, h.srv.URL)
	if fs.policy != "notify 3" {
		t.Errorf("policy = %q", fs.policy)
	}

	// Admins don't see host updates.
	ha, _ := loggedIn(t, auth.RoleAdmin)
	ha.node().updates = fu
	if _, home := ha.get("/"); strings.Contains(home, "OS updates") {
		t.Error("admins shouldn't see OS update problems")
	}
}

func TestActivity(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}})
	h.node().updates = &fakeUpdates{}
	act := h.acts().Start(activity.OSUpdates, "OS updates")
	fmt.Fprint(act, "==> Installing updates\nUnpacking curl (8.14.1-2+deb13u5)\n")

	_, body := h.get("/settings/updates")
	for _, want := range []string{"OS updates: Installing updates…", `hx-get="/activity/os-updates/status?was=running"`,
		"<summary>Details</summary>", "Unpacking curl (8.14.1-2&#43;deb13u5)", `hx-trigger="activity-done from:body"`, "Updating…"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings while updating: missing %q", want)
		}
	}

	// While it shows the same, polling answers 204: redrawing would reset
	// the spinner and lose a selection.
	get := func(have string) *http.Response {
		req, _ := http.NewRequest("GET", h.srv.URL+"/activity/os-updates/status?was=running", nil)
		if have != "" {
			req.Header.Set("X-Roostack-Have", have)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	fp := get("").Header.Get("X-Roostack-Fingerprint")
	if resp := get(fp); fp == "" || resp.StatusCode != http.StatusNoContent {
		t.Errorf("unchanged status: %d (fingerprint %q)", resp.StatusCode, fp)
	}

	act.Step("Installed 1 update: curl.")
	act.Finish(nil)
	resp, err := h.client.Get(h.srv.URL + "/activity/os-updates/status?was=running")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("HX-Trigger") != "activity-done" || !strings.Contains(string(b), "✓ OS updates finished") ||
		strings.Contains(string(b), "hx-get") {
		t.Errorf("finished status: trigger %q, body %s", resp.Header.Get("HX-Trigger"), b)
	}
	if _, log := h.get("/activity/os-updates/log"); !strings.Contains(log, "==&gt; Done") || strings.Contains(log, "hx-get") {
		t.Errorf("finished log should stop polling:\n%s", log)
	}

	// Admins can't see host activity.
	ha, _ := loggedIn(t, auth.RoleAdmin)
	ha.server.Node.(*fakeManager).acts = h.acts()
	if code, _ := ha.get("/activity/os-updates/log"); code != http.StatusNotFound {
		t.Errorf("admin reading OS updates: %d", code)
	}
}

func TestDNSRecordsList(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}})
	fd := &fakeDNS{st: dnsrecords.Status{State: dnsrecords.StateOK, Record: "*.lab.example.com", IP: "192.168.1.20",
		Provider: "Cloudflare", Managing: true},
		records: []dnsrecords.Record{{Name: "*.lab.example.com", Type: "A", Value: "192.168.1.20"}}}
	h.server.DNS = fd
	_, body := h.get("/settings/home-network")
	for _, want := range []string{"DNS records Roostack manages", "<code>*.lab.example.com</code>", "<code>192.168.1.20</code>",
		"Works from home", "_acme-challenge"} {
		if !strings.Contains(body, want) {
			t.Errorf("home network page missing %q", want)
		}
	}
	fd.records, fd.st.Managing = nil, false
	if _, body := h.get("/settings/home-network"); !strings.Contains(body, "Record management is off") {
		t.Error("with management off, say so")
	}
}

func TestRestartingWaitsForJob(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	if _, body := h.get("/restarting"); !strings.Contains(body, "in a few seconds") || strings.Contains(body, "Times in Roostack are this server") {
		t.Error("no job: restarts right away")
	}
	h.server.RunningJob = func() string { return "OS updates" }
	if _, body := h.get("/restarting"); !strings.Contains(body, "once the current job (OS updates) has finished") {
		t.Errorf("should say what it waits for:\n%s", body)
	}
}

// The backup step lists the server's drives, best first and chosen, and
// can prepare a blank disk.
func TestBackupDrivePicker(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true}}
	fs.drives = []agent.Drive{
		{ID: "uuid:64B1", Kind: "usb", Name: "BACKUP", Model: "SanDisk Ultra", Disk: "/dev/sdb", Size: 128 * units.GB, Free: -1, FSType: "exfat", Usable: true, Erasable: true},
		{ID: "disk:/dev/nvme1n1", Kind: "disk", Name: "Samsung SSD 980", Disk: "/dev/nvme1n1", Size: 1000 * units.GB, Free: -1, Erasable: true},
		{ID: "system", Kind: "system", Name: "This server's own disk", Size: 40 * units.GB, Free: 30 * units.GB, Usable: true},
	}
	h.setSetup(fs)
	_, body := h.get("/setup/backup")
	if strings.Contains(body, `value="uuid:64B1"`) || !strings.Contains(body, "a drive at home") || !strings.Contains(body, `href="/setup/backup?add=2#plan"`) {
		t.Error("the plan starts empty, suggesting a drive at home")
	}
	_, body = h.get("/setup/backup?add=2")
	for _, want := range []string{`value="uuid:64B1" checked`, "USB drive", "Second disk", "Prepare this disk", "Erase it and start afresh",
		`value="disk:/dev/nvme1n1" disabled`, "it helps with mistakes, not a failed disk or ransomware", "sets it up so it's there after a restart"} {
		if !strings.Contains(body, want) {
			t.Errorf("picker missing %q", want)
		}
	}
	if strings.Contains(body, "Another server") {
		t.Error("the wizard doesn't offer other servers")
	}
	// Refreshing keeps the choice.
	if _, part := h.get("/setup/backup/drives?drive=system"); !strings.Contains(part, `value="system" checked`) || strings.Contains(part, `value="uuid:64B1" checked`) {
		t.Errorf("refresh should keep the chosen drive:\n%s", part)
	}
	// Preparing the blank disk chooses it.
	_, body = h.post("/setup/backup/erase", url.Values{"add": {"2"}, "disk": {"/dev/nvme1n1"}}, h.srv.URL)
	if fs.erased != "/dev/nvme1n1" || !strings.Contains(body, `value="uuid:new" checked`) {
		t.Errorf("erase: %q\n%s", fs.erased, body)
	}
	h.post("/setup/backup/add", url.Values{"add": {"2"}, "choice": {"drive"}, "drive": {"uuid:new"}}, h.srv.URL)
	if fm.destination != "drive:uuid:new" {
		t.Errorf("backup 1: %q", fm.destination)
	}
	// The drive in use isn't offered again; backup 3 suggests going away from home.
	if _, body := h.get("/setup/backup"); !strings.Contains(body, "A backup away from home") || strings.Contains(body, "(optional)") {
		t.Error("the next slot suggests what's missing; no extra backup until the three copies are there")
	}
	_, body = h.get("/setup/backup?add=3")
	// One chooser, for backup 3 only.
	if strings.Contains(body, `value="uuid:new"`) || !strings.Contains(body, "Where should backup 3 go?") || strings.Count(body, "Where should") != 1 {
		t.Errorf("backup 3:\n%s", body)
	}
}

// Backups later ends the wizard: settings its steps saved take effect then.
func TestWizardEndsWithOneRestart(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true}, pending: true}
	h.setSetup(fs)
	if _, body := h.post("/setup/backup/later", nil, h.srv.URL); !strings.Contains(body, "Applying your settings") {
		t.Errorf("after the last step, the restarting page:\n%s", body)
	}
	fs.pending = false
	if _, body := h.post("/setup/backup/later", nil, h.srv.URL); strings.Contains(body, "Applying your settings") {
		t.Error("no restart when nothing waits for one")
	}
}
