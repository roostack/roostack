package web

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/hosttime"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
)

// Settings is an overview grid of areas, each with its own page; on
// desktop the area pages have a sidebar listing them all (SPEC.md §3.2,
// docs/DESIGN.md).

// settingsArea is one settings page.
type settingsArea struct {
	ID, Group, Title, Href string
}

var settingsAreas = []settingsArea{
	{"lab", "Lab", "Lab name", "/settings/lab"},
	{"people", "People and access", "People", "/settings/people"},
	{"domain", "Network", "Domain and certificates", "/settings/domain"},
	{"home-network", "Network", "Home network", "/settings/home-network"},
	{"servers", "Servers", "Servers", "/settings/servers"},
	{"storage", "Servers", "Storage for others", "/settings/storage"},
	// Settings that differ per server (switcher.go).
	{"updates", "Each server", "Updates and restarts", "/settings/updates"},
	{"time-zone", "Each server", "Time zone", "/settings/time-zone"},
	{"remote-access", "Each server", "Remote access", "/settings/remote-access"},
	{"backups", "Each server", "Backups", "/backups"},
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
	if s.server(r) != nil {
		if a, err := s.server(r).SSHAccess(); err == nil {
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
	if s.updates(r) != nil {
		last, _ := s.updates(r).Last(r.Context())
		rs := s.updates(r).RestartStatus()
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
	set("lab", s.LabName, "")
	if s.Manager != nil {
		if servers, err := s.Manager.Servers(); err == nil {
			summary, level := fmt.Sprintf("%d servers", len(servers)), "ok"
			if len(servers) == 1 {
				summary = "1 server: " + servers[0].Name
			}
			for _, mc := range servers {
				if mc.Status.LastErr != nil {
					summary, level = "A change on "+mc.Name+" didn't finish", "problem"
				}
			}
			set("servers", summary, level)
		}
	}
	clock := hosttime.Now()
	set("time-zone", clock.Zone+" · "+clock.Time+" now", "")
	if s.Node != nil {
		sum, level := summarizeBackups(r, s.Node)
		set("backups", sum, level)
	}
	set("storage", "Give family or friends space for their backups", "")
	if s.Manager != nil && s.Manager.OfferStorage(r.Context()) {
		if guests, err := s.Manager.StorageGuests(r.Context()); err == nil {
			n := 0
			for _, g := range guests {
				if g.Removed.IsZero() {
					n++
				}
			}
			set("storage", fmt.Sprintf("%d %s storing backups here", n, map[bool]string{true: "lab", false: "labs"}[n == 1]), "ok")
		}
	}
	// With several servers, per-server areas summarise each.
	for _, area := range []string{"updates", "time-zone", "remote-access", "backups"} {
		if sum, level := s.serverSummaries(r, area); sum != "" {
			set(area, sum, level)
		}
	}

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
	if s.DNS != nil {
		recs, err := s.DNS.Records(r.Context())
		if err != nil {
			s.serverError(w, err)
			return
		}
		data["Records"] = recs
	}
	s.render(w, "settings-network", withArea(data, "home-network"))
}

func (s *Server) serversPage(w http.ResponseWriter, r *http.Request) {
	s.renderServers(w, r, nil)
}

// endpointKey remembers the address other servers reach this one at.
const endpointKey = "network.endpoint"

// renderServers shows the Servers page; extra is added to its data
// (e.g. a new join code).
func (s *Server) renderServers(w http.ResponseWriter, r *http.Request, extra map[string]any) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	servers, err := s.Manager.Servers()
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Servers"] = servers
	locations := map[string]string{}
	for _, mc := range servers {
		locations[mc.ID] = s.serverLocation(r.Context(), mc.ID)
	}
	data["Locations"] = locations
	data["ApplyActivity"] = s.activityFor(r, activity.Apply)
	// Folders moves left behind, per server (asked of each at once).
	kept := make([][]node.KeptFolder, len(servers))
	var wg sync.WaitGroup
	for i, mc := range servers {
		wg.Go(func() { kept[i], _ = s.Manager.KeptData(r.Context(), mc.ID) })
	}
	wg.Wait()
	keptBy := map[string][]node.KeptFolder{}
	for i, mc := range servers {
		keptBy[mc.ID] = kept[i]
	}
	data["Kept"] = keptBy
	if s.Network != nil {
		endpoint, _ := s.Store.Setting(r.Context(), endpointKey)
		if endpoint == "" {
			endpoint = defaultEndpoint()
		}
		data["CanAdd"], data["Endpoint"] = true, endpoint
		if host, _, err := net.SplitHostPort(defaultEndpoint()); err == nil {
			data["LANAddr"] = host
		}
	}
	for k, v := range extra {
		data[k] = v
	}
	status := http.StatusOK
	if extra["Error"] != nil {
		status = http.StatusBadRequest
	}
	w.WriteHeader(status)
	s.render(w, "settings-servers", withArea(data, "servers"))
}

// joinCodeSubmit makes a join code for a new server.
func (s *Server) joinCodeSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Network == nil {
		http.NotFound(w, r)
		return
	}
	endpoint := strings.TrimSpace(r.FormValue("endpoint"))
	if host, port, err := net.SplitHostPort(endpoint); err != nil || host == "" || port == "" {
		s.renderServers(w, r, map[string]any{"Endpoint": endpoint,
			"Error": "Enter the address and port other servers reach this one at, such as 192.168.1.20:51820."})
		return
	}
	code, err := s.Network.NewJoinCode(r.Context(), endpoint)
	if err != nil {
		s.renderServers(w, r, map[string]any{"Endpoint": endpoint, "Error": "Roostack couldn't make a join code: " + err.Error()})
		return
	}
	s.Store.SetSetting(r.Context(), endpointKey, endpoint)
	s.Store.Audit(r.Context(), currentUser(r), "servers", "join code made for "+code.Addr.String())
	s.renderServers(w, r, map[string]any{"Endpoint": endpoint, "JoinCode": code.String(),
		"JoinExpires": time.Now().Add(labnet.JoinCodeLifetime)})
}

// defaultEndpoint guesses this server's address on the home network.
func defaultEndpoint() string {
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() && ipn.IP.IsPrivate() {
			return net.JoinHostPort(ipn.IP.String(), strconv.Itoa(agent.WireGuardPort))
		}
	}
	return ""
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
	if n := s.serverOf(r); n != nil {
		m := s.serverRefOf(r)
		s.forgetHostCheck(m.ID) // this page always checks afresh
		if c, ok := s.hostChanges(r, m, n); ok && c.Any() {
			data["HostChanges"] = c
		}
	}
	data["Switcher"] = s.serverTabs(r, "updates")
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, "settings-updates", withArea(data, "updates"))
}

func (s *Server) renameServerSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	id, name := r.PathValue("server"), r.FormValue("name")
	err := s.Manager.RenameServer(r.Context(), id, name)
	if msg, ok := userMessage(err); ok {
		s.renderServers(w, r, map[string]any{"Error": msg, "ErrorServer": id})
		return
	}
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "servers", "renamed "+id+" to "+strings.TrimSpace(name))
	if s.Node != nil && id == s.Node.Identity().ID {
		redirect(w, r, "/restarting") // this server reads its name at startup
		return
	}
	redirect(w, r, "/settings/servers")
}

func (s *Server) removeServerSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("server")
	err := s.Manager.RemoveServer(r.Context(), id)
	if msg, ok := userMessage(err); ok {
		s.renderServers(w, r, map[string]any{"Error": msg, "ErrorServer": id})
		return
	}
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "servers", "removed "+id)
	redirect(w, r, "/settings/servers")
}

func (s *Server) removeKeptDataSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	id, path := r.PathValue("server"), r.FormValue("path")
	err := s.Manager.RemoveKeptData(r.Context(), id, path)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "servers", "deleted kept folder "+path+" on "+id)
	redirect(w, r, "/settings/servers#server-"+id)
}
