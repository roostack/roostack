package web

import (
	"fmt"
	"net/http"

	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/dns"
	"github.com/mylab/mylab/internal/dnsrecords"
	"github.com/mylab/mylab/internal/hosttime"
)

// Settings is an overview grid of areas, each with its own page; on
// desktop the area pages have a sidebar listing them all (SPEC.md §3.2,
// docs/DESIGN.md).

// settingsArea is one settings page.
type settingsArea struct {
	ID, Group, Title, Href string
}

var settingsAreas = []settingsArea{
	{"people", "People and access", "People", "/settings/people"},
	{"remote-access", "People and access", "Remote access", "/settings/remote-access"},
	{"domain", "Network", "Domain and certificates", "/settings/domain"},
	{"home-network", "Network", "Home network", "/settings/home-network"},
	{"updates", "This machine", "Updates and restarts", "/settings/updates"},
	{"time-zone", "This machine", "Time zone", "/settings/time-zone"},
}

// settingsGroup is a heading and its areas, for the sidebar and overview.
type settingsGroup struct {
	Title string
	Areas []settingsArea
}

func settingsGroups() []settingsGroup {
	var groups []settingsGroup
	for _, a := range settingsAreas {
		if len(groups) == 0 || groups[len(groups)-1].Title != a.Group {
			groups = append(groups, settingsGroup{Title: a.Group})
		}
		g := &groups[len(groups)-1]
		g.Areas = append(g.Areas, a)
	}
	return groups
}

// areaCard is one area on the overview: its current state in a line, and
// how healthy that is.
type areaCard struct {
	settingsArea
	Summary string
	// Level is "ok", "warn", "problem" or "" (nothing to judge).
	Level string
}

// settingsPage is the overview grid.
func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	cards := map[string]areaCard{}
	for _, a := range settingsAreas {
		cards[a.ID] = areaCard{settingsArea: a}
	}
	set := func(id, summary, level string) {
		c := cards[id]
		c.Summary, c.Level = summary, level
		cards[id] = c
	}

	if users, err := s.Store.ListUsers(r.Context()); err == nil {
		set("people", fmt.Sprintf("%d %s can log in", len(users), map[bool]string{true: "person", false: "people"}[len(users) == 1]), "")
	}
	if s.Setup != nil {
		if a, err := s.Setup.SSHAccess(); err == nil {
			switch a.Choice {
			case config.SSHFromLAN:
				set("remote-access", "SSH from your home network only", "ok")
			case config.SSHFromAny:
				set("remote-access", "SSH from anywhere", "warn")
			case config.SSHOff:
				set("remote-access", "SSH is off", "ok")
			default:
				set("remote-access", "Not chosen yet; the firewall is off", "warn")
			}
		}
	}
	if s.Domain != "" && s.Setup != nil && s.domainDone(r) {
		set("domain", s.Domain, "ok")
	} else {
		set("domain", "Not set up: services can't get HTTPS certificates", "problem")
	}
	if s.DNS != nil {
		st := s.DNS.Status()
		switch st.State {
		case dnsrecords.StateOK:
			set("home-network", "Devices at home can reach your services", "ok")
		case dnsrecords.StatePending:
			set("home-network", "Checking…", "")
		case dnsrecords.StateManual:
			if st.Reachable {
				set("home-network", "Devices at home can reach your services", "ok")
			} else {
				set("home-network", "A DNS record needs adding", "warn")
			}
		case "":
			set("home-network", "Checking…", "")
		case dnsrecords.StateUnsupported:
			set("home-network", "Add a DNS record by hand", "warn")
		default:
			set("home-network", dnsHeadline[st.State], "problem")
		}
	} else {
		set("home-network", "Set up a domain first", "")
	}
	if s.Updates != nil {
		last, _ := s.Updates.Last(r.Context())
		rs := s.Updates.RestartStatus()
		switch {
		case last != nil && !last.OK:
			set("updates", "The last update didn't finish", "problem")
		case rs.Needed && s.Maintenance.Restart == config.RestartNotify:
			set("updates", "A restart is needed to finish installing updates", "warn")
		case rs.Needed:
			set("updates", fmt.Sprintf("Restarting around %02d:00 to finish updates", s.Maintenance.Hour()), "ok")
		case last != nil:
			set("updates", "Up to date · checked "+whenText(last.At), "ok")
		default:
			set("updates", "Updates install every night after the backup", "")
		}
	}
	clock := hosttime.Now()
	set("time-zone", clock.Zone+" · "+clock.Time+" now", "")

	var groups []map[string]any
	for _, g := range settingsGroups() {
		var cs []areaCard
		for _, a := range g.Areas {
			cs = append(cs, cards[a.ID])
		}
		groups = append(groups, map[string]any{"Title": g.Title, "Cards": cs})
	}
	data["Groups"] = groups
	s.render(w, "settings", data)
}

// withArea marks a page as a settings area, so it gets the sidebar.
func withArea(data map[string]any, id string) map[string]any {
	data["SettingsArea"] = id
	data["SettingsGroups"] = settingsGroups()
	return data
}

func (s *Server) domainAreaPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["LabDomain"] = s.Domain
	data["DomainSet"] = s.Domain != "" && s.Setup != nil && s.domainDone(r)
	if p := dns.Get(s.DNSProvider); p != nil {
		data["ProviderTitle"] = p.Title
	}
	s.render(w, "settings-domain", withArea(data, "domain"))
}

func (s *Server) homeNetworkPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["DNS"] = s.dnsView(r)
	s.render(w, "settings-network", withArea(data, "home-network"))
}

func (s *Server) updatesPage(w http.ResponseWriter, r *http.Request) {
	s.renderUpdates(w, r, "")
}

// renderUpdates shows the updates page; errMsg is shown on it (400).
func (s *Server) renderUpdates(w http.ResponseWriter, r *http.Request, errMsg string) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Updates"] = s.updatesView(r, errMsg)
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, "settings-updates", withArea(data, "updates"))
}
