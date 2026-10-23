package web

import (
	"net/http"
	"sync"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/words"
)

// Settings that differ per server (updates and restarts, time zone,
// remote access, backups) open on a summary row per server, which also
// switches which server's settings are being edited (SPEC.md §10.1).

// serverTab is one server in an area's switcher.
type serverTab struct {
	Name, Href string
	Current    bool
	Summary    string
	// Level is "ok", "warn", "problem" or "" (as on the Settings overview).
	Level string
}

// areaSummary summarises an area's state on one server.
type areaSummary func(r *http.Request, n ServiceNode) (summary, level string)

// perServerAreas are the settings areas that differ per server, with how
// each is summarised and where each server's page is.
var perServerAreas = map[string]struct {
	summary areaSummary
	path    func(m serverRef) string
}{
	"updates":       {summarizeUpdates, serverRef.Updates},
	"time-zone":     {summarizeTimezone, serverRef.TimeZone},
	"remote-access": {summarizeSSH, serverRef.RemoteAccess},
	"backups":       {summarizeBackups, serverRef.Backups},
}

func summarizeUpdates(r *http.Request, n ServiceNode) (string, string) {
	u := n.Updates()
	if u == nil {
		return "Not available", ""
	}
	last, _ := u.Last(r.Context())
	rs, err := u.RestartStatus(r.Context())
	if err != nil {
		return "Unknown", ""
	}
	switch {
	case last != nil && !last.OK:
		return "The last update didn't finish", "problem"
	case rs.Needed:
		return "Needs a restart to finish updates", "warn"
	case last != nil:
		return "Up to date · checked " + words.When(last.At), "ok"
	}
	return "Updates install every night after the backup", ""
}

func summarizeTimezone(r *http.Request, n ServiceNode) (string, string) {
	set := n.Settings()
	if set == nil {
		return "Not available", ""
	}
	zone, _, err := set.Timezones(r.Context())
	if err != nil {
		return "Unknown", ""
	}
	return zone, ""
}

func summarizeSSH(r *http.Request, n ServiceNode) (string, string) {
	set := n.Settings()
	if set == nil {
		return "Not available", ""
	}
	a, err := set.SSHAccess(r.Context(), false)
	if err != nil {
		return "Unknown", ""
	}
	return sshSummary(a)
}

// sshSummary is a server's remote access in a few words, and its status:
// who can reach SSH, and whether they may try passwords (SPEC.md §6.10).
func sshSummary(a node.SSHAccess) (string, string) {
	text, status := "", "ok"
	switch a.Choice {
	case config.SSHFromLAN:
		text = "SSH from your home network only"
	case config.SSHFromAny:
		text, status = "SSH from anywhere", "warn"
	case config.SSHOff:
		return "SSH is off", "ok"
	default:
		return "Not chosen yet; the firewall is off", "warn"
	}
	switch {
	case a.PasswordsOpen():
		return text + ", passwords allowed", "warn"
	case a.SSHD.Installed && a.SSHD.KeysOnly:
		return text + ", keys only", status
	}
	return text, status
}

func summarizeBackups(r *http.Request, n ServiceNode) (string, string) {
	b := n.Backups()
	if b == nil {
		return "Not set up", "problem"
	}
	st, err := b.Status(r.Context())
	switch {
	case err != nil:
		return "Unknown", ""
	case !st.Configured:
		return "Not set up", "problem"
	case st.Last != nil && !st.Last.OK:
		return "The last backup didn't finish", "problem"
	case st.LastOK == nil:
		return "Not backed up yet", "warn"
	case manager.BackupStale(st.LastOK.Finished):
		return "Last backed up " + words.When(st.LastOK.Finished), "warn"
	}
	return "Backed up " + words.When(st.LastOK.Finished), "ok"
}

// serverTabs is an area's switcher, asking every server at once. Nil when
// the lab has one server (nothing to switch).
func (s *Server) serverTabs(r *http.Request, area string) []serverTab {
	a, ok := perServerAreas[area]
	if !ok {
		return nil
	}
	servers, err := s.Manager.Servers()
	if err != nil || len(servers) < 2 {
		return nil
	}
	current := s.serverRefOf(r)
	tabs := make([]serverTab, len(servers))
	var wg sync.WaitGroup
	for i, mc := range servers {
		wg.Go(func() {
			m := serverRef{ID: mc.ID, Name: mc.Name, This: mc.This}
			t := serverTab{Name: mc.Name, Href: a.path(m), Current: mc.ID == current.ID || (current.This && mc.This),
				Summary: "Can't reach it right now", Level: "problem"}
			if n := s.Manager.NodeByID(mc.ID); n != nil && (mc.This || n.Settings() != nil) {
				t.Summary, t.Level = a.summary(r, n)
			}
			tabs[i] = t
		})
	}
	wg.Wait()
	return tabs
}

// serverLine is one server's state of a per-server area, for the
// Settings overview.
type serverLine struct {
	Name, Summary, Level string
}

// serverSummaries is a per-server area's state on each server, one line
// each, and the worst level among them (nil with a single server).
func (s *Server) serverSummaries(r *http.Request, area string) ([]serverLine, string) {
	tabs := s.serverTabs(r, area)
	if tabs == nil {
		return nil, ""
	}
	rank := map[string]int{"": 0, "ok": 1, "warn": 2, "problem": 3}
	var lines []serverLine
	level := ""
	for _, t := range tabs {
		lines = append(lines, serverLine{t.Name, t.Summary, t.Level})
		if rank[t.Level] > rank[level] {
			level = t.Level
		}
	}
	return lines, level
}

// serverArea is one of a server's own settings, for its page.
type serverArea struct {
	Title, Href, Summary, Level string
}

// serverPage is one server: its card, and each of its own settings.
func (s *Server) serverPage(w http.ResponseWriter, r *http.Request) {
	n := s.serverOf(r)
	if n == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	var areas []serverArea
	for _, a := range settingsAreas {
		pa, ok := perServerAreas[a.ID]
		if !ok {
			continue
		}
		sa := serverArea{Title: a.Title, Href: pa.path(m)}
		sa.Summary, sa.Level = pa.summary(r, n)
		areas = append(areas, sa)
	}
	s.renderServers(w, r, map[string]any{"Only": m.ID, "ServerAreas": areas})
}
