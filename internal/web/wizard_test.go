package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/setup"
)

type fakeSetup struct {
	p          setup.Progress
	domainErr  error
	backupErr  error
	lastBackup setup.BackupChoice

	lastProvider string
	lastValues   map[string]string
}

func (f *fakeSetup) Progress(context.Context) (setup.Progress, error) { return f.p, nil }
func (f *fakeSetup) SaveDomain(_ context.Context, d, e, provider string, values map[string]string) error {
	f.lastProvider, f.lastValues = provider, values
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
	fs := &fakeSetup{}
	h.server.Setup = fs

	if _, body := h.get("/"); !strings.Contains(body, "Your domain") {
		t.Fatal("an Owner with setup unfinished should land on the domain step")
	}

	_, body := h.get("/setup/domain")
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
	fs := &fakeSetup{}
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
	for _, path := range []string{"/setup/domain", "/setup/backup", "/setup/recovery-key"} {
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
