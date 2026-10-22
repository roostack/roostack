package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/store"
)

func notifyHarness(t *testing.T, role string) (*harness, *fakeMail, *fakeBackups) {
	t.Helper()
	h, _ := loggedIn(t, role)
	fm := &fakeMail{m: config.Mail{Host: "smtp.example.com", Port: 587, Security: config.MailSTARTTLS, From: "roostack@lab.example.com"}}
	h.server.Mail = fm
	fb := &fakeBackups{status: backup.Status{Configured: true, Progress: -1}}
	h.node().backups = fb
	u, _ := h.store.UserByUsername(context.Background(), "u")
	h.store.SetEmail(context.Background(), u.ID, "sam@example.com")
	return h, fm, fb
}

// A problem is emailed once it has lasted a few minutes, then once a day
// while it lasts, and "fixed" when it clears; numbers changing in it don't
// make it a new problem.
func TestNotifyProblems(t *testing.T) {
	h, fm, fb := notifyHarness(t, auth.RoleOwner)
	ctx := context.Background()
	t0 := time.Now()
	h.server.notify(ctx, t0) // starts following the audit log

	fb.status.Last = &store.BackupRun{OK: false, Finished: t0, Error: "couldn't reach the backup storage in 2m0s"}
	if h.server.notify(ctx, t0); len(fm.sent) != 0 {
		t.Fatalf("too new to email: %+v", fm.sent)
	}
	h.server.notify(ctx, t0.Add(11*time.Minute))
	if len(fm.sent) != 1 || !strings.HasPrefix(fm.sent[0].Subject, "[") || !strings.Contains(fm.sent[0].Subject, "Needs attention: The last backup didn't finish") ||
		!strings.Contains(fm.sent[0].Body, "https://roost.lab.example.com/backups") || fm.sent[0].To[0] != "sam@example.com" {
		t.Fatalf("first email: %+v", fm.sent)
	}
	// Its HTML says the same, with the notice in its section and a link to its page.
	if e := fm.sent[0].HTML; e == nil || e.Text() != fm.sent[0].Body {
		t.Fatalf("the HTML isn't made from what the text says: %+v", e)
	} else if html, err := e.HTML(); err != nil || !strings.Contains(html, `href="https://roost.lab.example.com/backups"`) ||
		!strings.Contains(html, "Needs attention</h2>") {
		t.Errorf("the HTML has no section with a link to the backups page (%v):\n%s", err, html)
	}
	// The time in it changes; still the same problem, and nothing new to say.
	fb.status.Last.Finished = t0.Add(5 * time.Minute)
	if h.server.notify(ctx, t0.Add(20*time.Minute)); len(fm.sent) != 1 {
		t.Fatalf("no repeat: %+v", fm.sent)
	}
	if h.server.notify(ctx, t0.Add(36*time.Hour)); len(fm.sent) != 2 || !strings.Contains(fm.sent[1].Subject, "Still needs attention") {
		t.Fatalf("reminder: %+v", fm.sent[len(fm.sent)-1])
	}
	fb.status.Last = &store.BackupRun{OK: true, Finished: t0.Add(37 * time.Hour)}
	fb.status.LastOK = fb.status.Last
	// Fixed once it has stayed fixed as long as a problem must last.
	if h.server.notify(ctx, t0.Add(37*time.Hour)); len(fm.sent) != 2 {
		t.Fatalf("fixed too soon: %+v", fm.sent[len(fm.sent)-1])
	}
	if h.server.notify(ctx, t0.Add(37*time.Hour+11*time.Minute)); len(fm.sent) != 3 || !strings.Contains(fm.sent[2].Subject, "Fixed: The last backup") {
		t.Fatalf("fixed: %+v", fm.sent[len(fm.sent)-1])
	}
	if h.server.notify(ctx, t0.Add(38*time.Hour)); len(fm.sent) != 3 {
		t.Error("nothing more once fixed")
	}

	// Someone who chose no problems gets none.
	u, _ := h.store.UserByUsername(ctx, "u")
	h.store.SetNotify(ctx, u.ID, "security")
	fb.status.Last = &store.BackupRun{OK: false, Finished: t0, Error: "boom"}
	h.server.notify(ctx, t0.Add(40*time.Hour))
	if h.server.notify(ctx, t0.Add(41*time.Hour)); len(fm.sent) != 3 {
		t.Errorf("chose no problems: %+v", fm.sent[len(fm.sent)-1])
	}
}

// A server that can't be reached is one problem, whatever each attempt's
// error says, and a moment's gap between retries (the server shown as
// applying) isn't "fixed": they made emails alternate all day (2026-10-08).
func TestNotifyNoFlapping(t *testing.T) {
	h, fm, _ := notifyHarness(t, auth.RoleOwner)
	ctx := context.Background()
	t0 := time.Now()
	this := manager.Server{ID: "n-0123456789", Name: "nuc", Controller: true, This: true, Reachable: true}
	pi := func(st manager.ApplyStatus) {
		h.node().servers = []manager.Server{this, {ID: "n-pi00000001", Name: "pi", Status: st}}
	}
	refused := fmt.Errorf("Roostack can't reach pi: %w", node.ErrUnreachable)
	pi(manager.ApplyStatus{Finished: true, LastErr: refused})
	h.server.notify(ctx, t0)
	h.server.notify(ctx, t0.Add(11*time.Minute))
	if len(fm.sent) != 1 || !strings.Contains(fm.sent[0].Subject, "Needs attention: Roostack can't reach pi") {
		t.Fatalf("first email: %+v", fm.sent)
	}
	// Between retries: applying for a moment, then failing again with
	// other words for the same thing.
	pi(manager.ApplyStatus{Running: true})
	h.server.notify(ctx, t0.Add(12*time.Minute))
	pi(manager.ApplyStatus{Finished: true, LastErr: fmt.Errorf("Roostack can't reach pi right now: %w", node.ErrUnreachable)})
	h.server.notify(ctx, t0.Add(14*time.Minute))
	h.server.notify(ctx, t0.Add(30*time.Minute))
	if len(fm.sent) != 1 {
		t.Errorf("flapped: %d emails, the last %q", len(fm.sent), fm.sent[len(fm.sent)-1].Subject)
	}
	// A failed change's error can change too: still one problem.
	pi(manager.ApplyStatus{Finished: true, LastErr: errors.New("restarting vaultwarden: exit status 1")})
	h.server.notify(ctx, t0.Add(41*time.Minute))
	pi(manager.ApplyStatus{Finished: true, LastErr: errors.New("restarting vaultwarden: timed out")})
	h.server.notify(ctx, t0.Add(52*time.Minute))
	h.server.notify(ctx, t0.Add(53*time.Minute))
	var apply int
	for _, m := range fm.sent {
		if strings.Contains(m.Subject, "didn't finish") {
			apply++
		}
	}
	if apply != 1 {
		t.Errorf("a changing error emailed %d times", apply)
	}
}

// A restart to finish installing updates waits for a person: it's in the
// "Waiting for you" group, emailed once it has waited a day, and not to
// someone who chose only Problems.
func TestNotifyWaiting(t *testing.T) {
	h, fm, _ := notifyHarness(t, auth.RoleOwner)
	ctx := context.Background()
	t0 := time.Now()
	h.node().updates = &fakeUpdates{restart: osupdate.RestartStatus{Needed: true}}
	h.node().settings = &fakeSetup{restartMode: config.RestartNotify}
	h.server.notify(ctx, t0)
	if h.server.notify(ctx, t0.Add(11*time.Minute)); len(fm.sent) != 0 {
		t.Fatalf("emailed like a problem: %+v", fm.sent)
	}
	if h.server.notify(ctx, t0.Add(25*time.Hour)); len(fm.sent) != 1 || !strings.Contains(fm.sent[0].Subject, "needs a restart") {
		t.Fatalf("after a day: %+v", fm.sent)
	}

	o, om, _ := notifyHarness(t, auth.RoleOwner)
	u, _ := o.store.UserByUsername(ctx, "u")
	o.store.SetNotify(ctx, u.ID, notifyProblems)
	o.node().updates = &fakeUpdates{restart: osupdate.RestartStatus{Needed: true}}
	o.node().settings = &fakeSetup{restartMode: config.RestartNotify}
	o.server.notify(ctx, t0)
	if o.server.notify(ctx, t0.Add(25*time.Hour)); len(om.sent) != 0 {
		t.Errorf("chose only Problems, and got: %+v", om.sent)
	}
}

// Security events come from the audit log: about your own account always,
// the lab's to Owners; Members can't choose lab groups.
func TestNotifySecurity(t *testing.T) {
	h, fm, _ := notifyHarness(t, auth.RoleOwner)
	ctx := context.Background()
	h.server.notify(ctx, time.Now())
	u, _ := h.store.UserByUsername(ctx, "u")
	h.store.Audit(ctx, u, "password changed", "")
	h.store.Audit(ctx, nil, AuditServerJoined, "vps")
	h.store.SetNotify(ctx, u.ID, "none")
	u, _ = h.store.UserByUsername(ctx, "u")
	h.server.notify(ctx, time.Now())
	if len(fm.sent) != 1 || !strings.Contains(fm.sent[0].Body, "Your password was changed") || strings.Contains(fm.sent[0].Body, "vps") {
		t.Fatalf("own account only, with no groups chosen: %+v", fm.sent)
	}
	h.store.SetNotify(ctx, u.ID, "security")
	h.store.Audit(ctx, nil, AuditServerJoined, "vps")
	if h.server.notify(ctx, time.Now()); len(fm.sent) != 2 || !strings.Contains(fm.sent[1].Body, "A server joined the lab: vps") {
		t.Fatalf("lab security: %+v", fm.sent)
	}

	// Members choose nothing about the lab.
	hm, _ := loggedIn(t, auth.RoleMember)
	_, body := hm.get("/account/notifications")
	if strings.Contains(body, `value="problems"`) || strings.Contains(body, `value="security"`) || !strings.Contains(body, "always come") {
		t.Error("members choose no lab groups")
	}
}

// A login from a new browser, for someone who has logged in before, is a
// security event; the same browser again isn't.
func TestNewDeviceLogin(t *testing.T) {
	h := newHarness(t, "")
	ctx := context.Background()
	hash, _ := authkit.HashPassword("a long password")
	h.store.CreateUser(ctx, store.User{Username: "sam", PasswordHash: hash, Role: auth.RoleAdmin})
	login := func(c *http.Client) {
		req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/login", strings.NewReader(url.Values{"username": {"sam"}, "password": {"a long password"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", h.srv.URL)
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	newDevices := func() int {
		entries, _ := h.store.RecentAudit(ctx, 50)
		n := 0
		for _, e := range entries {
			if e.Action == auditNewDevice {
				n++
				if !strings.Contains(e.Detail, "Firefox on Linux") {
					t.Errorf("detail %q", e.Detail)
				}
			}
		}
		return n
	}
	jar, _ := cookiejar.New(nil)
	laptop := &http.Client{Jar: jar}
	login(laptop)
	login(laptop)
	if n := newDevices(); n != 0 {
		t.Fatalf("first browser, twice: %d", n)
	}
	jar2, _ := cookiejar.New(nil)
	login(&http.Client{Jar: jar2})
	if n := newDevices(); n != 1 {
		t.Errorf("a second browser: %d", n)
	}
}

// The Monday summary has each server's backups and disks, what CrowdSec
// saw on the public addresses, and what still needs attention; it comes
// once a week.
func TestWeeklySummary(t *testing.T) {
	h, fm, fb := notifyHarness(t, auth.RoleOwner)
	ctx := context.Background()
	u, _ := h.store.UserByUsername(ctx, "u")
	h.store.SetNotify(ctx, u.ID, "weekly")
	fb.status.LastOK = &store.BackupRun{OK: true, Finished: time.Now()}
	fb.status.Last = fb.status.LastOK
	h.server.Protected = func(context.Context) bool { return true }
	h.server.Attacks = func(context.Context, time.Duration) (public.Attacks, error) {
		return public.Attacks{Alerts: 3, Addresses: 2, Blocked: 2, Kinds: []string{"http-probing"}}, nil
	}
	h.server.Blocks = func(context.Context, time.Duration) ([]public.Block, error) {
		return []public.Block{{Host: "passwords.lab.example.com"}}, nil
	}
	// A new release series is information, not something needing
	// attention: it has a line of its own.
	h.node().updates = &fakeUpdates{series: osupdate.Series{Current: "0.4", Newest: "0.5", NewestVersion: "0.5.0"}}
	monday := time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local)
	h.server.notify(ctx, monday.Add(-48*time.Hour)) // Saturday: not due
	if len(fm.sent) != 0 {
		t.Fatalf("not Monday: %+v", fm.sent)
	}
	h.server.notify(ctx, monday)
	if len(fm.sent) != 1 || !strings.Contains(fm.sent[0].Subject, "This week") {
		t.Fatalf("Monday: %+v", fm.sent)
	}
	for _, want := range []string{"Last backup:", "CrowdSec saw 3 attacks from 2 addresses, and blocked 2. Most were: http-probing. It also refused 1 request that tried a known exploit (Vaultwarden 1).",
		"- Roostack 0.5.0 is available. It starts a new series of releases, so it waits for you to say yes.\n  https://roost.lab.example.com/settings/updates#roostack",
		"Nothing needs attention."} {
		if !strings.Contains(fm.sent[0].Body, want) {
			t.Errorf("summary missing %q:\n%s", want, fm.sent[0].Body)
		}
	}
	if h.server.notify(ctx, monday.Add(time.Hour)); len(fm.sent) != 1 {
		t.Error("once a week")
	}
}

// Settings → Email lists who gets what.
func TestWhoGetsWhat(t *testing.T) {
	h, _, _ := notifyHarness(t, auth.RoleOwner)
	_, body := h.get("/settings/email")
	if !strings.Contains(body, "Who gets what") || !strings.Contains(body, "sam@example.com · Problems, Waiting for you, Security for the lab") {
		t.Errorf("who gets what:\n%s", body[strings.Index(body, "<main"):])
	}
}

// TestServerGoneForGood: a server unreached for days is said plainly,
// with what to do: remove it, or restore its services elsewhere if it
// had any. The Servers page says since when, and opens its remove part.
func TestServerGoneForGood(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	this := manager.Server{ID: "n-0123456789", Name: "nuc", Controller: true, This: true, Reachable: true}
	refused := manager.ApplyStatus{Finished: true, LastErr: fmt.Errorf("Roostack can't reach pi: %w", node.ErrUnreachable)}
	pi := manager.Server{ID: "n-pi00000001", Name: "pi", Status: refused, UnreachableSince: time.Now().Add(-time.Hour)}
	fm.servers = []manager.Server{this, pi}
	if _, body := h.get("/"); !strings.Contains(body, "Roostack can&#39;t reach pi.") || strings.Contains(body, "hasn&#39;t reached pi") {
		t.Error("an hour: the usual words")
	}
	pi.UnreachableSince = time.Now().Add(-4 * 24 * time.Hour)
	fm.servers = []manager.Server{this, pi}
	_, body := h.get("/")
	if !strings.Contains(body, "Roostack hasn&#39;t reached pi since") || !strings.Contains(body, "remove it from the lab") ||
		!strings.Contains(body, `href="/servers?remove=n-pi00000001#server-n-pi00000001"`) {
		t.Errorf("four days, no services: %s", body[strings.Index(body, "Needs attention"):][:600])
	}
	pi.Services = []string{"immich"}
	fm.servers = []manager.Server{this, pi}
	if _, body := h.get("/"); !strings.Contains(body, "its services can come back from their backups") || !strings.Contains(body, `href="/settings/servers/n-pi00000001/gone"`) {
		t.Error("four days, with services: restore them")
	}
	pi.Services = nil
	fm.servers = []manager.Server{this, pi}
	_, body = h.get("/servers?remove=n-pi00000001")
	if !strings.Contains(body, "Not connected since") || !strings.Contains(body, "Remove it from the lab") {
		t.Error("the Servers page doesn't say since when")
	}
	if i := strings.Index(body, `id="server-n-pi00000001"`); i < 0 || !strings.Contains(body[i:], "<details open>") {
		t.Error("the remove part isn't open")
	}
}

// A backup error with restic's stack trace in it shows only its first line
// under Needs attention (the trace has no break points and widened a
// phone's page, 2026-10-17); the backups page keeps the rest under Details.
func TestAttentionFirstErrorLine(t *testing.T) {
	h, _, fb := notifyHarness(t, auth.RoleOwner)
	fb.status.Last = &store.BackupRun{OK: false, Finished: time.Now(), Error: "restic backup: exit status 1: Error loading snapshot 1a2b3c: ciphertext verification failed\n" +
		"github.com/restic/restic/internal/repository.(*Repository).LoadUnpacked\n\t/restic/internal/repository/repository.go:290"}
	_, body := h.get("/")
	if !strings.Contains(body, "ciphertext verification failed") || strings.Contains(body, "LoadUnpacked") {
		t.Errorf("home: %s", body[strings.Index(body, "Needs attention"):][:600])
	}
	_, body = h.get("/backups")
	if i := strings.Index(body, `class="plan-error"`); i < 0 || !strings.Contains(body[i:], "LoadUnpacked") {
		t.Error("the backups page has no Details with the rest")
	}
}

// Every item that needs attention has a key of its own and a group, so
// email follows each problem whatever its words, and two problems are
// never mistaken for one.
func TestAttentionKeys(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	ctx := context.Background()
	h.setSetup(&fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainLater: true, BackupLater: true},
		restartMode: config.RestartNotify, ssh: config.SSHFromAny})
	fm.backups = &fakeBackups{status: backup.Status{Configured: true, Progress: -1, LocalOnly: true,
		Last: &store.BackupRun{OK: false, Error: "boom"}}}
	fm.updates = &fakeUpdates{restart: osupdate.RestartStatus{Needed: true}, last: &osupdate.Result{OK: false}}
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true},
		{ID: "n-2", Name: "pi", Status: manager.ApplyStatus{Finished: true, LastErr: errors.New("restarting forgejo: exit status 1")},
			CertExpires: time.Now().Add(time.Hour)},
		{ID: "n-3", Name: "vps", Status: manager.ApplyStatus{Finished: true, LastErr: fmt.Errorf("Roostack can't reach vps: %w", node.ErrUnreachable)}}}
	fm.junk = []manager.JunkReport{{ServerID: "n-2", Server: "pi", Provider: "server:n-0123456789", Where: "nuc's disk",
		Files: []backup.Junk{{Kind: "locks", Name: "ab", Removed: true}}},
		{ServerID: "n-0123456789", Server: "nuc", Provider: "provider:3", Where: "Wasabi", Copy: true,
			Files: []backup.Junk{{Kind: "index", Name: "cd"}}}}
	h.server.DNS = &fakeDNS{st: dnsrecords.Status{State: dnsrecords.StateError, NotDeleted: []dnsrecords.PublicRecord{{}}}}
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "immich", Title: "Immich", Status: StatusWaiting, WasOn: "vps"},
			{Name: "vaultwarden", Title: "Vaultwarden", Status: "failed"}}, nil
	}
	u, _ := h.store.UserByUsername(ctx, "u")
	items := h.server.attention(ctx, u)
	if len(items) < 12 {
		t.Fatalf("only %d items: %+v", len(items), items)
	}
	keys := map[string]bool{}
	for _, it := range items {
		if it.Key == "" || it.Group != notifyProblems && it.Group != notifyWaiting {
			t.Errorf("%q: key %q, group %q", it.Text, it.Key, it.Group)
		}
		if keys[it.Key] {
			t.Errorf("two items have the key %q", it.Key)
		}
		keys[it.Key] = true
	}
}
