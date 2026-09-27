package web

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/mylab/mylab/internal/config"
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
	path    func(m machineView) string
}{
	"updates":       {summarizeUpdates, machineView.Updates},
	"time-zone":     {summarizeTimezone, machineView.TimeZone},
	"remote-access": {summarizeSSH, machineView.RemoteAccess},
	"backups":       {summarizeBackups, machineView.Backups},
}

func summarizeUpdates(r *http.Request, n ServiceNode) (string, string) {
	u := n.Updates()
	if u == nil {
		return "Not available", ""
	}
	last, _ := u.Last(r.Context())
	switch rs := u.RestartStatus(); {
	case last != nil && !last.OK:
		return "The last update didn't finish", "problem"
	case rs.Needed:
		return "Needs a restart to finish updates", "warn"
	case last != nil:
		return "Up to date · checked " + whenText(last.At), "ok"
	}
	return "Updates install every night after the backup", ""
}

func summarizeTimezone(r *http.Request, n ServiceNode) (string, string) {
	set := n.Settings()
	if set == nil {
		return "Not available", ""
	}
	zone, _, err := set.Timezones()
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
	a, err := set.SSHAccess()
	if err != nil {
		return "Unknown", ""
	}
	switch a.Choice {
	case config.SSHFromLAN:
		return "SSH from your home network only", "ok"
	case config.SSHFromAny:
		return "SSH from anywhere", "warn"
	case config.SSHOff:
		return "SSH is off", "ok"
	}
	return "Not chosen yet; the firewall is off", "warn"
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
	case isStale(st.LastOK.Finished):
		return "Last backed up " + whenText(st.LastOK.Finished), "warn"
	}
	return "Backed up " + whenText(st.LastOK.Finished), "ok"
}

// serverTabs is an area's switcher, asking every server at once. Nil when
// the lab has one server (nothing to switch).
func (s *Server) serverTabs(r *http.Request, area string) []serverTab {
	a, ok := perServerAreas[area]
	if !ok || s.Manager == nil {
		return nil
	}
	machines, err := s.Manager.Machines()
	if err != nil || len(machines) < 2 {
		return nil
	}
	current := s.machineViewOf(r)
	tabs := make([]serverTab, len(machines))
	var wg sync.WaitGroup
	for i, mc := range machines {
		wg.Go(func() {
			m := machineView{ID: mc.ID, Name: mc.Name, This: mc.This}
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

// serverSummaries is an area's state on every server, for the Settings
// overview: one line per server, and the worst level.
func (s *Server) serverSummaries(r *http.Request, area string) (string, string) {
	tabs := s.serverTabs(r, area)
	if tabs == nil {
		return "", ""
	}
	rank := map[string]int{"": 0, "ok": 1, "warn": 2, "problem": 3}
	summary, level := "", ""
	for i, t := range tabs {
		if i > 0 {
			summary += " · "
		}
		summary += fmt.Sprintf("%s: %s", t.Name, t.Summary)
		if rank[t.Level] > rank[level] {
			level = t.Level
		}
	}
	return summary, level
}

// serverArea is one of a server's own settings, for its page.
type serverArea struct {
	Title, Href, Summary, Level string
}

// serverPage is one server: its card, and each of its own settings.
func (s *Server) serverPage(w http.ResponseWriter, r *http.Request) {
	n := s.machineOf(r)
	if n == nil {
		http.NotFound(w, r)
		return
	}
	m := s.machineViewOf(r)
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
	s.renderMachines(w, r, map[string]any{"Only": m.ID, "ServerAreas": areas})
}
