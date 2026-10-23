package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/manager"
)

// Junk the controller removed from a backup shows under Needs attention
// and on its page until the Owner clears it; what it couldn't remove is
// a problem.
func TestJunkReported(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	name := strings.Repeat("ab", 32)
	fm.junk = []manager.JunkReport{
		{Found: time.Now(), ServerID: "n-2", Server: "vps", Provider: "server:n-1", Where: "nuc's disk",
			Repository: "rest:http://100.96.0.1:8000/n-2/lab-1", Writers: []string{"vps", "nuc"},
			Files: []backup.Junk{{Kind: "locks", Name: name, Size: 512, Why: "ciphertext verification failed", Removed: true}}},
		{Found: time.Now(), ServerID: "n-1", Server: "nuc", Provider: "provider:3", Where: "Wasabi", Copy: true,
			Writers: []string{"nuc", "anyone with Wasabi's key"}, Err: "Roostack has no access of its own there to remove them.",
			Files: []backup.Junk{{Kind: "index", Name: name, Size: -1, Why: "ciphertext verification failed"}}},
	}
	_, body := h.get("/")
	for _, want := range []string{"Roostack removed a file it can&#39;t read from vps&#39;s backups on nuc&#39;s disk",
		"it stopped those backups working.", "Only vps or nuc could have put it there",
		"The copy of nuc&#39;s backups on Wasabi holds a file Roostack can&#39;t read, which is stopping those backups working, and Roostack couldn&#39;t remove it.", `href="/backups/junk"`} {
		if !strings.Contains(body, want) {
			t.Errorf("home page without %q", want)
		}
	}
	code, body := h.get("/backups/junk")
	for _, want := range []string{"locks/" + name, "512 B", "removed", "not removed", "no access of its own",
		"nuc or anyone with Wasabi&#39;s key", "100.96.0.1:8000/n-2/lab-1"} {
		if code != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("page (%d) without %q", code, want)
		}
	}
	h.post("/backups/junk/dismiss", nil, h.srv.URL)
	if _, body := h.get("/"); strings.Contains(body, "/backups/junk") {
		t.Error("still shown after clearing")
	}
	if _, body := h.get("/backups/junk"); !strings.Contains(body, "hasn't found any") {
		t.Error("cleared page")
	}

	// Only the Owner sees it.
	a, afm := loggedIn(t, auth.RoleAdmin)
	afm.junk = []manager.JunkReport{{Server: "vps", Where: "nuc's disk"}}
	if _, body := a.get("/"); strings.Contains(body, "/backups/junk") {
		t.Error("an Admin is told")
	}
	if code, _ := a.get("/backups/junk"); code == http.StatusOK {
		t.Error("an Admin sees the page")
	}
}
