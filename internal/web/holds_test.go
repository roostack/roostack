package web

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/manager"
)

// heldServices has Immich held after a restore that didn't finish, and
// Forgejo after a move that couldn't go back.
func heldServices(context.Context) ([]ServiceView, error) {
	taken := time.Date(2026, 10, 20, 3, 0, 0, 0, time.Local)
	at := time.Date(2026, 10, 22, 14, 12, 0, 0, time.Local)
	return []ServiceView{
		{Name: "immich", Title: "Immich", Status: StatusHeld, Server: "nuc", URLs: []string{"https://photos.example.org"},
			Held: &instance.Hold{Backup: "backup:n-0123456789@9a6817f7aaaa", Taken: taken, At: at,
				Error: "pg_restore: error: could not write to the database\nmore detail",
				Kept:  "/srv/roostack/services/immich.before-restore-20261022-141200", KeptByRestore: true}},
		{Name: "forgejo", Title: "Forgejo", Status: StatusHeld, Server: "vps",
			Held: &instance.Hold{Error: "nuc didn't finish applying in time", At: at, MovedFrom: "n-0123456789"}},
		{Name: "vaultwarden", Title: "Vaultwarden", Status: "running", Server: "nuc"},
	}, nil
}

// A service held after a restore that didn't finish says so, on its page
// and under Needs attention, in the Owner's words (SPEC.md §6.9); only the
// Owner gets the choices, and each is recorded.
func TestHeldService(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.server.Services = heldServices
	fm.failedMoves = map[string]manager.FailedMove{"vaultwarden": {To: "n-2", ToName: "vps", FromName: "nuc"}}
	_, body := h.get("/")
	for _, want := range []string{
		"Immich is stopped: its files are back from the backup of Tue 20 Oct 03:00, but its database couldn&#39;t be loaded.",
		`href="/services/immich#held"`, "What to do",
		"Forgejo is stopped on vps: its database didn&#39;t load there, and moving it back to nuc failed too.",
		"Vaultwarden couldn&#39;t be moved to vps.</strong><span>It&#39;s running on nuc, as before.", `href="/services/vaultwarden/move"`,
		"See what went wrong",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home missing %q", want)
		}
	}

	_, body = h.get("/services/immich")
	for _, want := range []string{
		"<strong>Immich is stopped: its restore didn't finish.</strong> Its files were put back from the backup of Tue 20 Oct 03:00",
		"<em>pg_restore: error: could not write to the database</em>",
		"So Roostack will keep it stopped until you choose.", `href="/backups/restore/immich">See the restore's log`,
		`href="/backups/restore/immich?backup=backup%3An-0123456789%409a6817f7aaaa">Restore again…`,
		"Put back Immich&#39;s data from before the restore, as it was at Thu 22 Oct 14:12? Immich will start again as it was before you restored it.",
		`action="/services/immich/held/start"`, "Start it anyway",
		"Start Immich anyway, without its database from the backup?", "Its restore didn't finish",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("service page missing %q", want)
		}
	}
	if strings.Contains(body, "more detail") || strings.Contains(body, ">Open Immich") {
		t.Error("the page shows the error's first line, and no Open button while it's held")
	}
	_, body = h.get("/services/forgejo")
	for _, want := range []string{"Forgejo is stopped on vps: its database didn't load there, and moving it back to nuc failed too:",
		`href="/services/forgejo/move">Move it again…`} {
		if !strings.Contains(body, want) {
			t.Errorf("held move's page missing %q", want)
		}
	}
	if strings.Contains(body, "Restore again") || strings.Contains(body, "held/put-back") {
		t.Error("a held move offers moving it again, not restoring")
	}

	// Admins see it, but only the Owner chooses.
	ha, fa := loggedIn(t, auth.RoleAdmin)
	ha.server.Services = heldServices
	_ = fa
	if _, body := ha.get("/services/immich"); strings.Contains(body, "held/start") || !strings.Contains(body, "The Owner can restore it again or start it anyway.") {
		t.Error("admins see the hold without the choices")
	}
	if code, _ := ha.post("/services/immich/held/start", url.Values{}, ha.srv.URL); code != http.StatusForbidden {
		t.Errorf("admin start: %d", code)
	}

	h.post("/services/immich/held/start", url.Values{}, h.srv.URL)
	h.post("/services/immich/held/put-back", url.Values{}, h.srv.URL)
	if strings.Join(fm.held, ", ") != "start immich, put-back immich" {
		t.Errorf("choices %v", fm.held)
	}
	entries, _ := h.store.RecentAudit(context.Background(), 5)
	var audit []string
	for _, e := range entries {
		audit = append(audit, e.Detail)
	}
	if !strings.Contains(strings.ToLower(strings.Join(audit, "|")), "started immich after its restore didn't finish") {
		t.Errorf("audit %v", audit)
	}
}

// "Restore again…" opens the restore page with the same backup chosen.
func TestRestoreAgainChoosesTheBackup(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	newest, older := time.Now().Add(-time.Hour), time.Now().Add(-30*time.Hour)
	place := func(id string, at time.Time) []manager.ServiceBackup {
		return []manager.ServiceBackup{{Ref: "backup:n-0123456789", ServerID: "n-0123456789", ServerName: "nuc", Where: "USB drive BACKUP",
			Main: true, Snapshot: backup.Snapshot{ID: id, Short: id[:8], Time: at}}}
	}
	fm.otherBackups = []manager.BackupMoment{{ServerID: "n-0123456789", ServerName: "nuc", Time: newest, Places: place("9a6817f7aaaa", newest)},
		{ServerID: "n-0123456789", ServerName: "nuc", Time: older, Places: place("8550fc0abbbb", older)}}
	_, body := h.get("/backups/restore/vaultwarden?backup=" + url.QueryEscape("backup:n-0123456789@8550fc0abbbb"))
	if !strings.Contains(body, `data-moment="n-0123456789@`) && !strings.Contains(body, "Restoring from") {
		t.Fatal("no restore form")
	}
	if !strings.Contains(body, html.EscapeString(momentLabel(older, time.Now()))) || !strings.Contains(body, `value="backup:n-0123456789@8550fc0abbbb"`) {
		t.Error("the older backup should be listed")
	}
	if i := strings.Index(body, `<strong x-text="label">`); i < 0 || !strings.HasPrefix(body[i+len(`<strong x-text="label">`):], html.EscapeString(momentLabel(older, time.Now()))) {
		t.Error("the backup asked for should be chosen")
	}
}
