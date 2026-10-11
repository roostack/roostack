package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
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
	for i, pp := range f.access[0].PrivatePaths {
		f.access[0].PrivatePaths[i].Open = req.Public && slices.Contains(req.OpenPaths, pp.Path)
	}
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

func (f *fakeManager) EdgeServers(context.Context) []manager.EdgeServer { return f.edges }

func (f *fakeManager) Edge(context.Context) (manager.EdgeServer, bool) {
	for _, e := range f.edges {
		if e.ID == f.edge {
			return e, true
		}
	}
	return manager.EdgeServer{ID: f.edge}, f.edge != ""
}

func (f *fakeManager) SetEdge(_ context.Context, id string) error {
	if id != "" && !slices.ContainsFunc(f.edges, func(e manager.EdgeServer) bool { return e.ID == id }) {
		return &manager.FormError{Msg: "That server can't be the way in."}
	}
	f.edge = id
	return nil
}

// TestPublicEdge: at home, the Owner chooses how visitors come in: port
// forwarding, or through a cloud server in the lab; records then say they
// point at it, and a missing edge is called out.
func TestPublicEdge(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	if _, body := h.get("/settings/public"); strings.Contains(body, "How people reach your public services") {
		t.Error("offered a way in without a cloud server")
	}
	fm.edges = []manager.EdgeServer{{ID: "n-0000000002", Name: "vps-sydney", Address: "172.105.165.39"}}
	_, body := h.get("/settings/public")
	for _, want := range []string{"How people reach your public services", `value="" required checked`, "Through vps-sydney, a cloud server in this lab",
		"<code>172.105.165.39</code>"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if code, _ := h.post("/settings/public/edge", url.Values{"edge": {"n-0000000099"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("a server that can't be the edge: %d", code)
	}
	_, body = h.post("/settings/public/edge", url.Values{"edge": {"n-0000000002"}}, h.srv.URL)
	if fm.edge != "n-0000000002" || !strings.Contains(body, `value="n-0000000002" checked`) || !strings.Contains(body, "Saved. Public addresses follow") {
		t.Errorf("choosing the edge: edge %q", fm.edge)
	}
	// The edge gone from the lab's network: said so.
	fm.edges = nil
	if _, body := h.get("/settings/public"); !strings.Contains(body, "n-0000000002 isn't connected to the lab now") {
		t.Error("a missing edge isn't called out")
	}
	h.post("/settings/public/edge", url.Values{"edge": {""}}, h.srv.URL)
	if fm.edge != "" {
		t.Error("back to port forwarding")
	}
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
	for _, want := range []string{"anyone on the internet", "Share attack reports", `name="open" value="/admin"`, "Vaultwarden&#39;s admin panel.",
		"Make passwords.lab.example.com public? Anyone on the internet will be able to reach it."} {
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
	for _, want := range []string{"CrowdSec is running", "points at 81.2.69.160", "192.168.1.20", "Make a check link",
		`<button type="button" class="quiet danger"`, `<button type="submit" class="danger">Make private</button>`,
		"Parts kept private", `name="open" value="/admin" >`} {
		if !strings.Contains(body, want) {
			t.Errorf("the checklist should show %q", want)
		}
	}
	// While it's public, the private parts can be opened (and closed).
	h.post("/services/vaultwarden/public/web", url.Values{"public": {"on"}, "open": {"/admin"}}, h.srv.URL)
	if req := fm.publicReqs[len(fm.publicReqs)-1]; !req.Public || !slices.Equal(req.OpenPaths, []string{"/admin"}) {
		t.Errorf("opening /admin: %+v", req)
	}
	if _, body := h.get("/services/vaultwarden/public/web"); !strings.Contains(body, `value="/admin" checked`) ||
		!strings.Contains(body, "(public now)") {
		t.Error("an opened part shows as public")
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

// TestEdgeDown: with visitors coming in through a cloud server that isn't
// connected, public services are down from outside, and that's a problem
// to fix; connected, nothing to say.
func TestEdgeDown(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.access = []manager.RouteAccess{{Host: "photos.smith.family", Public: true}}
	fm.edges = []manager.EdgeServer{{ID: "n-0000000002", Name: "vps-sydney", Address: "172.105.165.39"}}
	fm.edge = "n-0000000002"
	this := manager.Server{ID: "n-0123456789", Name: "nuc", Controller: true, This: true, Reachable: true}
	vps := manager.Server{ID: "n-0000000002", Name: "vps-sydney", Reachable: true}
	fm.servers = []manager.Server{this, vps}
	if _, body := h.get("/"); strings.Contains(body, "Public services can&#39;t be reached") {
		t.Error("a connected edge called out")
	}
	vps.Reachable = false
	vps.Status = manager.ApplyStatus{Finished: true, LastErr: fmt.Errorf("Roostack can't reach vps-sydney: %w", node.ErrUnreachable)}
	fm.servers = []manager.Server{this, vps}
	if _, body := h.get("/"); !strings.Contains(body, "Public services can&#39;t be reached from the internet: visitors come in through vps-sydney") ||
		!strings.Contains(body, `href="/settings/public#way-in"`) {
		t.Error("an edge that isn't connected isn't called out")
	}
}
