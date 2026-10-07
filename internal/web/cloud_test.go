package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/cloud"
	"roostack.cloud/roostack/internal/cloud/cloudtest"
	"roostack.cloud/roostack/internal/setup"
)

// newCloudWith is a real link to a fake Roostack Cloud.
func newCloudWith(t *testing.T) (*cloud.Service, *cloudtest.Fake) {
	t.Helper()
	cloud.PollEvery = 10 * time.Millisecond
	f, cfg := cloudtest.Start(t)
	s, err := cloud.New(t.TempDir(), cfg, "test", func() string { return "Smith family" }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

func newCloud(t *testing.T) *cloud.Service { s, _ := newCloudWith(t); return s }

var codeRE = regexp.MustCompile(`class="link-code">([A-Z0-9-]+)<`)

func TestCloudLinking(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	link, f := newCloudWith(t)
	h.server.Cloud = link
	h.server.Setup = &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupDone: true, RecoverySaved: true}}
	// The name becomes the lab's address (setup.UseRoostAc, faked), and
	// goes with the link.
	var used []cloud.NameClaim
	dropped := 0
	link.UseName = func(_ context.Context, c cloud.NameClaim) error { used = append(used, c); return nil }
	link.Dropped = func(context.Context) { dropped++ }

	if _, body := h.get("/settings"); !strings.Contains(body, "Roostack Cloud") || !strings.Contains(body, "Not linked") {
		t.Error("the overview shows Roostack Cloud, not linked")
	}
	if _, body := h.get("/settings/cloud"); !strings.Contains(body, `action="/settings/cloud/link" target="_blank"`) {
		t.Error("not linked: a button to link")
	}
	// Names are checked as they're typed.
	for name, want := range map[string]string{"taken": "taken.roost.ac is taken", "smiths": "smiths.roost.ac is free", "-x": "3 to 40"} {
		if _, body := h.get("/settings/cloud/name?name=" + name); !strings.Contains(body, want) {
			t.Errorf("name %s: %q", name, body)
		}
	}
	if code, body := h.post("/settings/cloud/link", url.Values{"name": {"taken"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "taken.roost.ac is taken") {
		t.Errorf("taken name: %d", code)
	}

	// Linking: the new window goes on to Cloud's page; this one waits.
	code, body := h.post("/settings/cloud/link", url.Values{"name": {"smiths"}}, h.srv.URL)
	if code != http.StatusOK || !strings.Contains(body, `<meta http-equiv="refresh" content="0;url=https://127.0.0.1:`) ||
		!strings.Contains(body, "/link/") {
		t.Fatalf("open Cloud: %d %s", code, body)
	}
	_, live := h.get("/settings/cloud/live?in=setup")
	m := codeRE.FindStringSubmatch(live)
	if m == nil || !strings.Contains(live, "Waiting for you in Roostack Cloud") || !strings.Contains(live, "every 2s") ||
		!strings.Contains(live, "smiths.roost.ac") {
		t.Fatalf("waiting: %s", live)
	}
	if err := f.Confirm(m[1]); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300 && !link.Status().Linked; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	for i := 0; i < 300 && link.Status().Address == ""; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if len(used) != 1 || used[0].Name != "smiths.roost.test" || used[0].Credentials["password"] == "" {
		t.Fatalf("name used: %+v", used)
	}
	if _, live := h.get("/settings/cloud/live?in=setup"); !strings.Contains(live, "Your lab&#39;s address is smiths.roost.test.") ||
		strings.Contains(live, "every 2s") {
		t.Errorf("linked, in the wizard: %s", live)
	}
	_, body = h.get("/settings/cloud")
	for _, want := range []string{"Linked</span> since", "o••••@example.com", "Your lab's address, smiths.roost.test",
		"Fingerprint <code>", "lab.key", "Recent requests to Roostack Cloud", "POST /v1/links", "Unlink this lab from Roostack Cloud?"} {
		if !strings.Contains(body, want) {
			t.Errorf("linked page missing %q", want)
		}
	}
	if code, _ := h.post("/settings/cloud/unlink", nil, h.srv.URL); code != http.StatusOK || link.Status().Linked || len(f.Labs()) != 0 {
		t.Errorf("unlink: %d %+v", code, link.Status())
	}
	if _, body := h.get("/settings/cloud?unlinked=told"); !strings.Contains(body, "Unlinked from Roostack Cloud.") || dropped != 1 {
		t.Errorf("unlinked notice; address dropped %d times", dropped)
	}

	// Unlinked from Cloud's side: the next check forgets it, and Home says so.
	h.post("/settings/cloud/link", nil, h.srv.URL)
	f.Confirm(f.Codes()[0])
	for i := 0; i < 300 && !link.Status().Linked; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	// Linked without a name: the address step then claims one directly.
	if _, body := h.get("/setup/domain"); !strings.Contains(body, `action="/setup/address/roost-ac"`) || !strings.Contains(body, "Use this address") {
		t.Error("linked: the address step uses a name directly")
	}
	if code, body := h.post("/setup/address/roost-ac", url.Values{"name": {"taken"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "taken.roost.ac is taken") {
		t.Errorf("taken: %d", code)
	}
	used = nil
	if code, _ := h.post("/setup/address/roost-ac", url.Values{"name": {"jones"}}, h.srv.URL); code != http.StatusOK ||
		len(used) != 1 || used[0].Name != "jones.roost.test" {
		t.Errorf("claim: %d %+v", code, used)
	}
	f.Unlink(link.Status().LabID)
	link.Check(context.Background())
	if _, body := h.get("/"); !strings.Contains(body, "This lab is no longer linked to Roostack Cloud") {
		t.Error("home: no longer linked")
	}
	if _, body := h.get("/settings/cloud"); !strings.Contains(body, "Cloud stopped accepting") {
		t.Error("settings: no longer linked")
	}
	h.post("/settings/cloud/dismiss", nil, h.srv.URL)
	if _, body := h.get("/"); strings.Contains(body, "no longer linked") {
		t.Error("dismissed")
	}

	// Cloud turned off: nothing about it.
	h.server.Cloud = nil
	if code, _ := h.get("/settings/cloud"); code != http.StatusNotFound {
		t.Errorf("Cloud off: %d", code)
	}
	if _, body := h.get("/settings"); strings.Contains(body, "Roostack Cloud") {
		t.Error("Cloud off: not in Settings")
	}
}

func TestCloudOwnerOnly(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	h.server.Cloud = newCloud(t)
	if code, _ := h.get("/settings/cloud"); code != http.StatusForbidden {
		t.Errorf("admin: %d", code)
	}
}
