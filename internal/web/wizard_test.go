package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/units"
	"roostack.cloud/roostack/internal/usererr"
)

func TestWizardFlow(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{zone: "Etc/UTC"}
	h.setSetup(fs)

	_, body := h.get("/")
	if !strings.Contains(body, `<span>Setup</span><strong aria-current="page">Step 2 of 5</strong>`) || !strings.Contains(body, `data-zone="Etc/UTC"`) ||
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
	for _, want := range []string{"A free roost.test address", `<form method="post" action="/settings/cloud/link"><input type="hidden" name="in" value="setup">`, "Continue to Roostack Cloud", "Your own domain",
		`x-data="{ choice: '' }"`, `<input name="domain" value="" placeholder="smith.family"`, "/help/settings/domain/#your-own-domain",
		`id="cloud-live"`, `hx-get="/settings/cloud/name"`} {
		if !strings.Contains(body, want) {
			t.Errorf("address step missing %q", want)
		}
	}
	// A lab on a roost.ac name (its DNS provider Roostack Cloud's, not
	// offered for an own domain) starts the own domain's form on the
	// first provider, with its fields shown (found on VM3, 2026-10-13).
	h.server.DNSProvider = config.ProviderCloud
	_, body = h.get("/setup/domain")
	if first := "provider: '" + dns.Providers()[0].ID + "'"; !strings.Contains(body, first) {
		t.Errorf("a roost.ac lab's own-domain form should start on %q", first)
	}
	h.server.DNSProvider = ""
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

	fs.domainErr = &usererr.Error{Msg: "Route 53 didn't accept those details."}
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
	if code, body := h.post("/setup/recovery-key", nil, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, `<span>Setup</span><strong aria-current="page">Step 5 of 5</strong>`) {
		t.Errorf("continuing without ticking the box: %d (the backup step's breadcrumbs)", code)
	}
	h.post("/setup/recovery-key", url.Values{"saved": {"on"}}, h.srv.URL)
	if _, body := h.get("/"); !strings.Contains(body, "<title>Home · Roostack</title>") {
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
	if _, body := h.get("/"); !strings.Contains(body, "<title>Home · Roostack</title>") {
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
	if strings.Contains(body, "Step 4 of 5") || !strings.Contains(body, "Change your lab's address") || strings.Contains(body, "/setup/domain/later") ||
		!strings.Contains(body, `class="sidebar"`) {
		t.Error("changing the domain later must not look like the wizard")
	}

	// During first-run setup the wizard framing stays.
	fs.p = setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true}
	if _, body := h.get("/setup/backup"); !strings.Contains(body, `<span>Setup</span><strong aria-current="page">Step 5 of 5</strong>`) || !strings.Contains(body, "Set up backups later") {
		t.Error("first-run setup keeps its steps")
	}
}

// TestTwoFactorWizardStep: while the wizard lasts, the Owner turns on
// two-factor as its first step, shown like its others (no sidebar, its
// step, Log out), and the code goes on to the next step; the account's
// page sends them there meanwhile. After the wizard, two-factor is the
// account's page again (SPEC.md §6.3).
func TestTwoFactorWizardStep(t *testing.T) {
	h := newHarness(t, "")
	fm := &fakeManager{}
	h.server.Manager, h.server.Node = fm, fm
	fs := &fakeSetup{zone: "Etc/UTC"}
	h.setSetup(fs)
	ctx := context.Background()
	hash, _ := authkit.HashPassword("a long password")
	id, _ := h.store.CreateUser(ctx, store.User{Username: "tyris", PasswordHash: hash, Role: auth.RoleOwner, Email: "tyris@example.com"})
	h.post("/login", url.Values{"username": {"tyris"}, "password": {"a long password"}}, h.srv.URL)
	// landsOn gets path and says where it ended up.
	landsOn := func(path string) (string, string) {
		t.Helper()
		resp, err := h.client.Get(h.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.Request.URL.Path, string(body)
	}

	for _, path := range []string{"/", "/setup/timezone", "/account/2fa", "/settings/people"} {
		if at, _ := landsOn(path); at != "/setup/two-factor" {
			t.Errorf("%s without two-factor, in the wizard: ended at %s", path, at)
		}
	}
	_, body := landsOn("/setup/two-factor")
	if !strings.Contains(body, `<span>Setup</span><strong aria-current="page">Step 1 of 5</strong>`) ||
		!strings.Contains(body, `action="/setup/two-factor"`) || !strings.Contains(body, `action="/logout"`) ||
		strings.Contains(body, `class="sidebar"`) || strings.Contains(body, "Your account") || strings.Contains(body, "tz-banner") {
		t.Fatalf("the wizard's two-factor step:\n%.4000s", body)
	}
	secret := totpSecretOf(t, body)
	code, body := h.post("/setup/two-factor", url.Values{"secret": {secret}, "code": {wrongCode(secret)}}, h.srv.URL)
	if code != http.StatusBadRequest || !strings.Contains(body, "That code didn") || !strings.Contains(body, "Step 1 of 5") {
		t.Errorf("a wrong code: %d, want the wizard's step again:\n%.4000s", code, body)
	}
	totp, _ := authkit.TOTPCode(secret, time.Now())
	h.post("/setup/two-factor", url.Values{"secret": {secret}, "code": {totp}}, h.srv.URL)
	if u, _ := h.store.UserByUsername(ctx, "tyris"); u.TOTPSecret != secret {
		t.Fatal("two-factor isn't on")
	}
	at, body := landsOn("/setup/two-factor")
	if at != "/setup/timezone" || !strings.Contains(body, "Step 2 of 5") || strings.Contains(body, `class="sidebar"`) {
		t.Errorf("with two-factor on, the wizard goes on to the time zone; ended at %s", at)
	}

	// After the wizard, an Owner whose two-factor was reset turns it on
	// again on the account's page.
	fs.p = setup.Progress{TimezoneDone: true, SSHDone: true, DomainLater: true, BackupLater: true}
	h.store.ResetTOTP(ctx, id)
	h.post("/logout", nil, h.srv.URL)
	h.post("/login", url.Values{"username": {"tyris"}, "password": {"a long password"}}, h.srv.URL)
	for _, path := range []string{"/", "/setup/two-factor"} {
		if at, _ := landsOn(path); at != "/account/2fa" {
			t.Errorf("%s after the wizard: ended at %s, want the account's page", path, at)
		}
	}
	if _, body := landsOn("/account/2fa"); !strings.Contains(body, `action="/account/2fa"`) || !strings.Contains(body, `class="sidebar"`) ||
		strings.Contains(body, "Step 1 of 5") {
		t.Errorf("two-factor after the wizard isn't the account's page:\n%.4000s", body)
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

// A new release series of Roostack waits for the Owner (SPEC.md §7).
func TestRoostackSeries(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}})
	fu := &fakeUpdates{series: osupdate.Series{Current: "0.4", Source: "https://apt.roostack.cloud"}}
	h.node().updates = fu

	_, body := h.get("/settings/updates")
	if !strings.Contains(body, "The 0.4 releases: their fixes install by themselves") || strings.Contains(body, "is available") {
		t.Errorf("no new series: page wrong")
	}
	fu.series.Newest, fu.series.NewestVersion = "0.5", "0.5.0"
	_, body = h.get("/settings/updates")
	for _, want := range []string{"Roostack 0.5.0 is available", `action="/settings/updates/series"`, `name="series" value="0.5"`,
		"Update to Roostack 0.5.0", "backed up through Roostack, so there won",
		`href="https://help.roostack.cloud/whats-new/#roostack-0.5"`, "What's new in 0.5"} {
		if !strings.Contains(body, want) {
			t.Errorf("offer: page lacks %q", want)
		}
	}
	// Home says so too, as information.
	if _, home := h.get("/"); !strings.Contains(home, "Roostack 0.5.0 is available.") || !strings.Contains(home, "It starts a new series of releases") ||
		!strings.Contains(home, `href="/settings/updates#roostack"`) {
		t.Error("offer: not on Home")
	}
	// The page went stale: another series is on offer now.
	if code, body := h.post("/settings/updates/series", url.Values{"series": {"0.6"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "on offer any more") || fu.accepted != "" {
		t.Errorf("stale series: %d, accepted %q", code, fu.accepted)
	}
	if code, _ := h.post("/settings/updates/series", url.Values{"series": {"0.5"}}, h.srv.URL); code >= 400 || fu.accepted != "0.5" {
		t.Errorf("accept: %d, accepted %q", code, fu.accepted)
	}
	fu.series.Waiting = "the last backup didn't work"
	_, body = h.get("/settings/updates")
	if !strings.Contains(body, "Roostack 0.5 installs with this server's next updates") || !strings.Contains(body, "waiting: the last backup didn&#39;t work.") ||
		strings.Contains(body, "Update to Roostack") || !strings.Contains(body, "What's new in 0.5") {
		t.Error("accepted: page wrong")
	}
	if _, home := h.get("/"); !strings.Contains(home, "Roostack 0.5 is waiting to install: the last backup didn&#39;t work.") {
		t.Error("accepted, waiting: not on Home")
	}
	// A development build follows nothing.
	fu.series, fu.accepted = osupdate.Series{}, ""
	if _, body = h.get("/settings/updates"); !strings.Contains(body, "Doesn't update itself") {
		t.Error("development build: page wrong")
	}

	// Only the Owner accepts.
	ha, _ := loggedIn(t, auth.RoleAdmin)
	ha.node().updates = fu
	fu.series = osupdate.Series{Current: "0.4", Newest: "0.5", NewestVersion: "0.5.0"}
	if code, _ := ha.post("/settings/updates/series", url.Values{"series": {"0.5"}}, ha.srv.URL); code < 400 || fu.accepted != "" {
		t.Errorf("admin accepted: %d", code)
	}
	if _, home := ha.get("/"); strings.Contains(home, "Roostack 0.5.0 is available") {
		t.Error("admins are told about a series they can't accept")
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
	// A log that went with a restart (a backup copy's new settings): the
	// page reloads rather than spinning on its last answer.
	resp, err = h.client.Get(h.srv.URL + "/activity/backup-setup.n-1/status?was=running")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("HX-Refresh") != "true" {
		t.Errorf("a log gone with a restart: %d, HX-Refresh %q", resp.StatusCode, resp.Header.Get("HX-Refresh"))
	}
	if code, _ := h.get("/activity/backup-setup.n-1/status"); code != http.StatusNotFound {
		t.Errorf("no such log: %d", code)
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
		{ID: "uuid:64B1", Kind: "usb", Name: "BACKUP", Model: "SanDisk Ultra", Disk: "/dev/sdb", Size: 128 * units.GB, Free: -1, FSType: "exfat", Usable: true},
		{ID: "disk:/dev/nvme1n1", Kind: "disk", Name: "Samsung SSD 980", Disk: "/dev/nvme1n1", Size: 1000 * units.GB, Free: -1, Erasable: true},
		{ID: "system", Kind: "system", Name: "This server's own disk", Size: 40 * units.GB, Free: 30 * units.GB, Usable: true},
	}
	h.setSetup(fs)
	_, body := h.get("/setup/backup")
	if strings.Contains(body, `value="uuid:64B1"`) || !strings.Contains(body, "a drive at home") || !strings.Contains(body, `href="/setup/backup?add=2#plan"`) {
		t.Error("the plan starts empty, suggesting a drive at home")
	}
	_, body = h.get("/setup/backup?add=2")
	for _, want := range []string{`value="uuid:64B1" checked`, "USB drive", "Second disk", "Prepare this disk",
		`value="disk:/dev/nvme1n1" disabled`, "it helps with mistakes, not a failed disk or ransomware", "sets it up so it's there after a restart"} {
		if !strings.Contains(body, want) {
			t.Errorf("picker missing %q", want)
		}
	}
	if strings.Contains(body, "Erase it and start afresh") {
		t.Error("a drive with something usable on it is never offered for erasing (the agent refuses it)")
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
