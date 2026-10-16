package web

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
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
	"roostack.cloud/roostack/internal/public"
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
	{"history", "Lab", "History", "/settings/history"},
	{"people", "People and access", "People", "/settings/people"},
	{"email", "People and access", "Email", "/settings/email"},
	{"domain", "Network", "Your lab's address", "/settings/domain"},
	{"home-network", "Network", "Where it is and DNS", "/settings/home-network"},
	{"public", "Network", "Public access", "/settings/public"},
	{"cloud", "Network", "Roostack Cloud", "/settings/cloud"},
	{"servers", "Servers", "Servers", "/servers"},
	{"storage", "Servers", "Storage for others", "/settings/storage"},
	// Settings that differ per server (switcher.go).
	{"updates", "Each server", "Updates and restarts", "/settings/updates"},
	{"time-zone", "Each server", "Time zone", "/settings/time-zone"},
	{"remote-access", "Each server", "Remote access", "/settings/remote-access"},
	{"backups", "Each server", "Backups", "/backups"},
}

// sidebarSettings are the settings pages people come back to, listed in
// the sidebar under Settings; the rest are a click away in All settings,
// and the list shows them all while you're in Settings (the Owner's
// choice, 2026-10-15).
var sidebarSettings = []string{"people", "domain", "updates", "history"}

// Icon is the area's icon in the sidebar (icons.go).
func (a settingsArea) Icon() string {
	switch a.ID {
	case "lab":
		return "tag"
	case "history":
		return "history"
	case "people":
		return "users"
	case "email":
		return "at"
	case "domain":
		return "certificate"
	case "home-network":
		return "location"
	case "public":
		return "globe"
	case "cloud":
		return "cloud"
	case "storage":
		return "disk"
	case "updates":
		return "changes"
	case "time-zone":
		return "clock"
	case "remote-access":
		return "terminal"
	}
	return "manage"
}

// settingsGroup is a heading and its areas, for the sidebar and overview.
type settingsGroup struct {
	Title string
	Areas []settingsArea
}

// settingsGroups are the areas in groups, leaving out those in hide.
func settingsGroups(hide ...string) []settingsGroup {
	var groups []settingsGroup
	for _, a := range settingsAreas {
		if slices.Contains(hide, a.ID) {
			continue
		}
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
	// Servers, with several servers, is a per-server area's state on
	// each, one line each (instead of Summary).
	Servers []serverLine
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
	set("history", fmt.Sprintf("Kept for %d days", s.historyKeepDays(r.Context())), "")
	if s.Mail != nil {
		if m, _ := s.Mail.Settings(); m.Configured() {
			set("email", "Sends from "+m.From, "ok")
		} else {
			set("email", "Not set up: Roostack can't send email", "")
		}
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
		set("domain", "No address yet: services can't be reached", "problem")
	}
	if s.Cloud != nil {
		summary, level := cloudSummary(s.Cloud.Status())
		set("cloud", summary, level)
	}
	// Where it is, then how DNS is doing (SPEC.md §6.12).
	internet := s.onInternet()
	where := map[bool]string{true: "On the internet", false: "At home"}[internet]
	works := map[bool]string{true: "addresses lead here", false: "devices at home can reach your services"}[internet]
	if c := s.network(); c.Saved == "" && c.Detected == config.NetworkInternet {
		set("home-network", "Looks like a cloud server: say where it is", "warn")
	} else if s.DNS != nil {
		st := s.DNS.Status()
		switch st.State {
		case dnsrecords.StateOK:
			set("home-network", where+" · "+works, "ok")
		case dnsrecords.StatePending, "":
			set("home-network", where+" · checking DNS…", "")
		case dnsrecords.StateManual:
			if st.Reachable {
				set("home-network", where+" · "+works, "ok")
			} else {
				set("home-network", where+" · a DNS record needs adding", "warn")
			}
		case dnsrecords.StateUnsupported:
			set("home-network", where+" · add a DNS record by hand", "warn")
		default:
			set("home-network", dnsHeadline[st.State], "problem")
		}
	} else {
		set("home-network", where+" · set up a domain first", "")
	}
	if s.Manager != nil {
		if hosts := s.Manager.PublicHosts(); len(hosts) == 0 {
			set("public", map[bool]string{true: "Nothing is public: services are only reachable through an SSH tunnel",
				false: "Nothing is public: services are for your home network"}[internet], "")
		} else if s.Protected != nil && !s.Protected(r.Context()) {
			set("public", fmt.Sprintf("%d public; CrowdSec isn't running", len(hosts)), "problem")
		} else {
			set("public", fmt.Sprintf("%d public, protected by CrowdSec", len(hosts)), "ok")
		}
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
	set("lab", s.labName(), "")
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
		if lines, level := s.serverSummaries(r, area); lines != nil {
			c := cards[area]
			c.Servers, c.Level = lines, level
			cards[area] = c
		}
	}

	var groups []map[string]any
	for _, g := range s.settingsGroups() {
		var cs []areaCard
		for _, a := range g.Areas {
			cs = append(cs, cards[a.ID])
		}
		groups = append(groups, map[string]any{"Title": g.Title, "Cards": cs})
	}
	data["Groups"] = groups
	s.render(w, "settings", data)
}

// settingsGroups are this lab's settings areas: Roostack Cloud only
// while it's in use.
func (s *Server) settingsGroups() []settingsGroup {
	if s.Cloud == nil {
		return settingsGroups("cloud")
	}
	return settingsGroups()
}

// withArea marks a page as a settings area, so the sidebar marks it.
func withArea(data map[string]any, id string) map[string]any {
	data["AreaNav"] = areaNav{Label: "Settings", Active: id}
	return data
}

// areaNav is a set of area pages and the current one: Settings' (listed
// in the sidebar, docs/DESIGN.md "Layout") or your account's (Tabs: a row
// of tabs at the top of each).
type areaNav struct {
	Label  string
	Groups []settingsGroup
	Active string
	Tabs   bool
}

func (s *Server) domainAreaPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["LabDomain"] = s.Domain
	data["UIHost"] = s.uiHost()
	data["DomainSet"] = s.Domain != "" && s.Setup != nil && s.domainDone(r)
	if p := dns.Get(s.DNSProvider); p != nil {
		data["ProviderTitle"] = p.Title
	}
	if o := s.oldAddress(); o != nil {
		data["Old"], data["OldRows"] = o, s.oldAddressRows(r, o)
		if p := dns.Get(o.DNS.Provider); p != nil {
			data["OldProvider"] = p.Title
		}
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
	data["NetworkForm"] = s.networkForm(r, false)
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
	// ?remove=<id> opens that server's remove section (from "Remove it").
	data["Remove"] = r.FormValue("remove")
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
	links := map[string]string{}
	for _, mc := range servers {
		links[mc.ID] = s.Manager.LinkAddress(r.Context(), mc.ID)
	}
	data["LinkAddrs"] = links
	// Each server's latest apply: why it ran and what it did (SPEC.md §6.14).
	applies := map[string]*activityView{}
	for _, mn := range s.serverNodes(r) {
		if snap, ok := mn.node.LatestActivity(activity.Apply); ok {
			v := &activityView{Kind: activity.Apply, Snapshot: snap}
			if !mn.view.This {
				v.Server = mn.view.ID
			}
			applies[mn.view.ID] = v
		}
	}
	data["ApplyActivities"] = applies
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
	// Each server's disk space, asked of each at once.
	spaces := make([]*serverDisks, len(servers))
	for i, mc := range servers {
		if n := s.Manager.NodeByID(mc.ID); n != nil {
			wg.Go(func() { spaces[i] = s.serverSpaces(r.Context(), serverRef{ID: mc.ID, Name: mc.Name, This: mc.This}, n) })
		}
	}
	wg.Wait()
	spaceBy := map[string]*serverDisks{}
	for i, mc := range servers {
		spaceBy[mc.ID] = spaces[i]
	}
	data["Spaces"] = spaceBy
	if s.Network != nil {
		endpoint, _ := s.Store.Setting(r.Context(), endpointKey)
		if v, ok := extra["Endpoint"].(string); ok {
			endpoint = v // as it was submitted
		}
		data["CanAdd"] = true
		choice := s.addressChoices(r, "Address the new server reaches this one at", agent.WireGuardPort, true, true, endpoint)
		// At home, the controller can dial a cloud server instead.
		if !s.onInternet() {
			choice.Dial = true
			if d, ok := extra["Dial"].(string); ok {
				choice.Selected, choice.DialAddr = "dial", d
			}
		}
		data["AddressChoice"] = choice
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
	if r.FormValue("address") == "dial" {
		s.joinCodeDial(w, r)
		return
	}
	endpoint := addressFromForm(r)
	if host, port, err := net.SplitHostPort(endpoint); err != nil || host == "" || port == "" {
		s.renderServers(w, r, map[string]any{"Endpoint": endpoint,
			"Error": "Choose the address other servers reach this one at, or enter one with its port, such as 192.168.1.20:51820."})
		return
	}
	code, err := s.Network.NewJoinCode(r.Context(), endpoint, "")
	if err != nil {
		s.renderServers(w, r, map[string]any{"Endpoint": endpoint, "Error": "Roostack couldn't make a join code: " + err.Error()})
		return
	}
	s.Store.SetSetting(r.Context(), endpointKey, endpoint)
	s.useAddress(r.Context(), endpoint)
	s.Store.Audit(r.Context(), currentUser(r), "servers", "join code made for "+code.Addr.String())
	w.Header().Set("Cache-Control", "no-store") // the join code
	s.renderServers(w, r, map[string]any{"Endpoint": endpoint, "JoinCode": code.String(),
		"JoinExpires": time.Now().Add(labnet.JoinCodeLifetime)})
}

// joinCodeDial makes a join code for a server this one dials, because it
// can be reached and this one can't (SPEC.md §10.2).
func (s *Server) joinCodeDial(w http.ResponseWriter, r *http.Request) {
	typed := strings.TrimSpace(r.FormValue("address_dial"))
	dial, err := dialAddress(typed)
	if err != nil {
		s.renderServers(w, r, map[string]any{"Dial": typed, "Error": err.Error()})
		return
	}
	code, err := s.Network.NewJoinCode(r.Context(), "", dial)
	if err != nil {
		s.renderServers(w, r, map[string]any{"Dial": typed, "Error": "Roostack couldn't make a join code: " + err.Error()})
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "servers", "join code made for "+code.Addr.String()+", connecting to "+dial)
	w.Header().Set("Cache-Control", "no-store") // the join code
	s.renderServers(w, r, map[string]any{"JoinCode": code.String(), "JoinDials": dial,
		"JoinExpires": time.Now().Add(labnet.JoinCodeLifetime)})
}

// dialAddress is a typed public address or name, with WireGuard's port
// unless one is given. A home-network address isn't one: a server there
// can be reached the usual way.
func dialAddress(typed string) (string, error) {
	host, port := typed, strconv.Itoa(agent.WireGuardPort)
	if h, p, err := net.SplitHostPort(typed); err == nil {
		host, port = h, p
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", errors.New("That port isn't a number from 1 to 65535.")
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !public.IsPublic(ip) {
			return "", errors.New(host + " isn't a public address. A server at home can join the usual way: choose this server's home-network address.")
		}
	} else if host == "" || strings.ContainsAny(host, " /:") || !strings.Contains(host, ".") {
		return "", errors.New("Enter the new server's public address, such as 203.0.113.7, or a name for it.")
	}
	return net.JoinHostPort(host, port), nil
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
	// The registry is the lab's, kept by the controller: on its page.
	if s.serverRefOf(r).This {
		data["Registry"] = s.registryView()
	}
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
	redirect(w, r, "/servers")
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
	redirect(w, r, "/servers")
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
	redirect(w, r, "/servers#server-"+id)
}

// linkAddressSubmit sets where other servers reach a server directly
// (SPEC.md §10.10).
func (s *Server) linkAddressSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	id, addr := r.PathValue("server"), strings.TrimSpace(r.FormValue("address"))
	err := s.Manager.SetLinkAddress(r.Context(), id, addr)
	if msg, ok := userMessage(err); ok {
		w.WriteHeader(http.StatusBadRequest)
		s.renderServers(w, r, map[string]any{"LinkError": msg, "LinkErrorServer": id})
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
	what := "cleared " + id + "'s address for direct links"
	if addr != "" {
		what = id + " is reached directly at " + addr
	}
	s.Store.Audit(r.Context(), currentUser(r), "servers", what)
	redirect(w, r, "/servers#server-"+id)
}
