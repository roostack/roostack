package web

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/public"
)

func (f *fakeManager) Access(name string) ([]manager.RouteAccess, error) {
	if name != "vaultwarden" {
		return nil, manager.ErrNotFound
	}
	if f.access == nil {
		f.access = []manager.RouteAccess{{Route: "web", Host: "passwords.lab.example.com",
			PrivatePaths: []manager.PathAccess{{Path: "/admin", Reason: "Vaultwarden's admin panel."}}}}
	}
	return f.access, nil
}

func (f *fakeManager) SetPublic(_ context.Context, name string, req manager.PublicRequest) error {
	access, err := f.Access(name)
	if err != nil {
		return err
	}
	if req.Public && access[0].Unsafe != "" && !req.AcceptUnsafe {
		return &manager.FormError{Msg: "Tick the box to confirm you understand why this address isn't safe to make public."}
	}
	f.publicReqs = append(f.publicReqs, req)
	f.access[0].Public = req.Public
	return nil
}

func (f *fakeManager) PublicHosts() []string {
	if len(f.access) > 0 && f.access[0].Public {
		return []string{f.access[0].Host}
	}
	return nil
}

func (f *fakeManager) SignalSharing() (bool, bool) { return f.sharingAsked, f.share }

func (f *fakeManager) SetSignalSharing(share bool) error {
	f.sharingAsked, f.share = true, share
	return nil
}

// The Owner makes an address public (SPEC.md §6.11): a warning, the
// sharing question the first time, private paths left home-only unless
// ticked; then a checklist, and a check link that ticks itself when it's
// opened from outside.
func TestMakePublic(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.server.Checks = &public.Checks{Record: func(host string, at time.Time, from string) error {
		return RecordPublicCheck(context.Background(), h.store, host, at, from)
	}}
	h.server.Protected = func(context.Context) bool { return true }
	h.server.DNS = &fakeDNS{st: dnsrecords.Status{IP: "192.168.1.20", PublicIP: "81.2.69.160",
		Public: []dnsrecords.PublicRecord{{Host: "passwords.lab.example.com", IP: "81.2.69.160", State: dnsrecords.PublicOK,
			Message: "passwords.lab.example.com points at 81.2.69.160."}}}}

	if _, body := h.get("/services/vaultwarden"); !strings.Contains(body, "Your home network (or VPN)") ||
		!strings.Contains(body, `href="/services/vaultwarden/public/web"`) {
		t.Fatal("the service page should say who can reach it, with a way to make it public")
	}
	_, body := h.get("/services/vaultwarden/public/web")
	for _, want := range []string{"anyone on the internet", "Share attack reports", `name="open" value="/admin"`, "Vaultwarden&#39;s admin panel."} {
		if !strings.Contains(body, want) {
			t.Errorf("the page should show %q", want)
		}
	}
	// The sharing question must be answered.
	if status, _ := h.post("/services/vaultwarden/public/web", url.Values{"public": {"on"}}, h.srv.URL); status != http.StatusBadRequest {
		t.Errorf("without the sharing answer: %d", status)
	}
	h.post("/services/vaultwarden/public/web", url.Values{"public": {"on"}, "share": {"no"}}, h.srv.URL)
	if len(fm.publicReqs) != 1 || !fm.publicReqs[0].Public || len(fm.publicReqs[0].OpenPaths) != 0 || !fm.sharingAsked || fm.share {
		t.Fatalf("made public: %+v, sharing asked %v share %v", fm.publicReqs, fm.sharingAsked, fm.share)
	}
	logs, _ := h.store.RecentAudit(context.Background(), 5)
	if len(logs) == 0 || logs[0].Detail != "passwords.lab.example.com made public" {
		t.Errorf("audit: %+v", logs)
	}

	_, body = h.get("/services/vaultwarden/public/web")
	for _, want := range []string{"CrowdSec is running", "points at 81.2.69.160", "192.168.1.20", "Make a check link", "Make private"} {
		if !strings.Contains(body, want) {
			t.Errorf("the checklist should show %q", want)
		}
	}
	h.post("/services/vaultwarden/public/web/check", nil, h.srv.URL)
	_, body = h.get("/services/vaultwarden/public/web")
	token := regexp.MustCompile(`roostack-check/([0-9a-f]{32})`).FindStringSubmatch(body)
	if token == nil || !strings.Contains(body, "<svg") {
		t.Fatal("no check link and QR code")
	}

	visit := func(from string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/.well-known/roostack-check/"+token[1], nil)
		req.Host = "passwords.lab.example.com"
		req.Header.Set("X-Forwarded-For", from)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := new(strings.Builder)
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		b.Write(buf[:n])
		return resp.StatusCode, b.String()
	}
	if _, msg := visit("192.168.1.30"); !strings.Contains(msg, "Turn Wi-Fi off") {
		t.Errorf("from home: %s", msg)
	}
	if status, msg := visit("81.2.69.200"); status != http.StatusOK || !strings.Contains(msg, "It works") {
		t.Errorf("from outside: %d %s", status, msg)
	}
	if status, _ := visit("81.2.69.200"); status != http.StatusNotFound {
		t.Errorf("a used link: %d", status)
	}
	if _, body := h.get("/services/vaultwarden/public/web"); !strings.Contains(body, "It worked from outside") || !strings.Contains(body, "81.2.69.200") {
		t.Error("the check should be ticked")
	}

	// Private again.
	h.post("/services/vaultwarden/public/web", url.Values{"public": {"off"}}, h.srv.URL)
	if last := fm.publicReqs[len(fm.publicReqs)-1]; last.Public {
		t.Error("should be private again")
	}
}

// An address the package calls unsafe needs a tick; only Owners get the
// pages.
func TestMakePublicUnsafeAndOwnerOnly(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.sharingAsked = true
	fm.access = []manager.RouteAccess{{Route: "web", Host: "passwords.lab.example.com", Unsafe: "It's an admin tool."}}
	if _, body := h.get("/services/vaultwarden/public/web"); !strings.Contains(body, "It&#39;s an admin tool.") ||
		strings.Contains(body, "Share attack reports") {
		t.Error("the package's reason, and no sharing question once answered")
	}
	status, body := h.post("/services/vaultwarden/public/web", url.Values{"public": {"on"}}, h.srv.URL)
	if status != http.StatusBadRequest || !strings.Contains(body, "Tick the box") || len(fm.publicReqs) != 0 {
		t.Errorf("unsafe without the tick: %d", status)
	}
	h.post("/services/vaultwarden/public/web", url.Values{"public": {"on"}, "accept_unsafe": {"on"}, "open": {"/nope"}}, h.srv.URL)
	if len(fm.publicReqs) != 1 {
		t.Error("with the tick it goes public")
	}

	ha, _ := loggedIn(t, auth.RoleAdmin)
	for _, path := range []string{"/services/vaultwarden/public/web", "/settings/public"} {
		if status, _ := ha.get(path); status != http.StatusForbidden {
			t.Errorf("%s as an Admin: %d", path, status)
		}
	}
	if _, body := ha.get("/services/vaultwarden"); strings.Contains(body, "/public/web") {
		t.Error("Admins shouldn't be offered it")
	}
}

// Settings → Public access lists what's public and changes the sharing
// choice.
func TestPublicSettings(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	if _, body := h.get("/settings"); !strings.Contains(body, "Nothing is public") {
		t.Error("overview card")
	}
	if _, body := h.get("/settings/public"); !strings.Contains(body, "Nothing is public") || !strings.Contains(body, "Not chosen yet") {
		t.Error("public settings page")
	} else if strings.Contains(body, `name="share" value="yes" required checked`) || strings.Contains(body, `value="no" checked`) {
		t.Error("nothing is ticked until the Owner chooses")
	}
	h.post("/settings/public/sharing", url.Values{"share": {"no"}}, h.srv.URL)
	if !fm.sharingAsked || fm.share {
		t.Error("sharing not saved")
	}
	if status, _ := h.post("/settings/public/sharing", url.Values{"share": {"maybe"}}, h.srv.URL); status != http.StatusBadRequest {
		t.Errorf("a bad answer: %d", status)
	}
}
