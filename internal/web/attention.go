package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/dnsrecords"
)

// Attention item levels.
const (
	levelProblem = "problem" // something is broken or unprotected
	levelWarning = "warning" // should be dealt with soon
	levelInfo    = "info"    // in progress; nothing to do
)

// dnsHeadline summarises a DNS problem for the home page.
var dnsHeadline = map[string]string{
	dnsrecords.StateBlocked:    "Devices at home can't reach your services: your router blocks their addresses.",
	dnsrecords.StateOverridden: "Devices at home are sent somewhere else when they open your services.",
	dnsrecords.StateConflict:   "A DNS record for your services points somewhere else, so MyLab can't update it.",
	dnsrecords.StateError:      "MyLab couldn't check or update your DNS records.",
}

// attentionItem is one entry in the home page's "Needs attention" list.
// Each links to the page where it's dealt with.
type attentionItem struct {
	Level    string
	Text     string
	Link     string
	LinkText string
}

// attention gathers what the current user should know about, filtered by
// role: Members see nothing they can't act on.
func (s *Server) attention(r *http.Request) []attentionItem {
	u := currentUser(r)
	owner := auth.Can(u.Role, auth.ManageHost)
	admin := auth.Can(u.Role, auth.ManageServices)
	var items []attentionItem
	add := func(level, text, link, linkText string) {
		items = append(items, attentionItem{level, text, link, linkText})
	}

	if s.RestorePending != nil && s.RestorePending() {
		add(levelInfo, "Finishing the restore: putting the services' databases back from the backup. Some services may not work until it's done.",
			"", "")
	}
	// Another lab that stopped storing this lab's backups (it said so).
	if owner && s.Manager != nil {
		for _, e := range s.Manager.EndingStorages(r.Context()) {
			add(levelWarning, e.HostName+" has stopped storing your backups. What's there is deleted on "+
				e.DeleteOn.Local().Format("2 January")+": send them somewhere else.", "/backups#destination", "Choose where backups go")
		}
	}
	if owner && s.Setup != nil {
		if p, err := s.Setup.Progress(r.Context()); err == nil && p.DomainLater && !p.DomainDone {
			add(levelProblem, "Your domain isn't set up, so services can't get secure (HTTPS) certificates.",
				"/setup/domain", "Set up your domain")
		}
	}

	if admin && s.Manager != nil {
		// Each server applies its own changes; name it once there are several.
		servers, _ := s.Manager.Servers()
		for _, mc := range servers {
			on := ""
			if len(servers) > 1 {
				on = " on " + mc.Name
			}
			// Certificates renew 120 days ahead; one this close means renewing
			// keeps failing (the server logs why).
			if left := time.Until(mc.CertExpires); owner && !mc.CertExpires.IsZero() && left < certWarning {
				level, text := levelProblem, mc.Name+"'s certificate on MyLab's network has expired, so the lab can't reach it."
				if left > 0 {
					level, text = levelWarning, mc.Name+"'s certificate on MyLab's network expires "+whenText(mc.CertExpires)+
						" and isn't renewing. Check that it's running and can reach the controller."
				}
				add(level, text, "/settings/servers#server-"+mc.ID, "View servers")
			}
			switch st := mc.Status; {
			case st.Running:
				add(levelInfo, "Applying changes"+on+"… Downloading a new service can take a few minutes.", "", "")
			case st.LastErr != nil:
				add(levelProblem, "The last change"+on+" didn't finish: "+st.LastErr.Error(), "/services", "View services")
			}
			// A server that joined has no firewall until the Owner chooses
			// who can log in over SSH (this server's is a setup step).
			if owner && !mc.This {
				if n := s.Manager.NodeByID(mc.ID); n != nil {
					if set := n.Settings(); set != nil {
						if a, err := set.SSHAccess(); err == nil && a.Choice == "" {
							add(levelWarning, mc.Name+"'s firewall is off until you choose who can log in to it over SSH.",
								"/settings/servers/"+mc.ID+"/remote-access", "Choose")
						}
					}
				}
			}
		}
		if services, err := s.Services(r.Context()); err == nil {
			for _, svc := range services {
				// Asked of the server each service runs on.
				n := s.Manager.NodeFor(svc.Name)
				if n == nil {
					continue
				}
				hs := n.InstallStatus(r.Context(), svc.Name)
				link := "/services/" + svc.Name
				switch {
				case hs.Failed != "":
					add(levelProblem, svc.Title+" didn't finish setting up.", link, "See what went wrong")
				case hs.Running || hs.Pending:
					add(levelInfo, "Finishing setting up "+svc.Title+"…", link, "View")
				}
			}
		}
	}

	if admin {
		for _, mn := range s.serverNodes(r) {
			s.backupAttention(r, mn.view, mn.node, owner, add)
		}
	}

	if owner && s.DNS != nil {
		st := s.DNS.Status()
		switch st.State {
		case dnsrecords.StateBlocked, dnsrecords.StateOverridden, dnsrecords.StateConflict, dnsrecords.StateError:
			// A headline here; the full explanation is on the Settings card.
			add(levelProblem, dnsHeadline[st.State], "/settings/home-network", "See how to fix it")
		case dnsrecords.StatePending:
			add(levelInfo, st.Message, "/settings/home-network", "View")
		case dnsrecords.StateManual:
			if !st.Reachable {
				add(levelWarning, "Devices at home can't find your services yet. "+st.Message, "/settings/home-network", "Home network settings")
			}
		}
	}

	if owner {
		for _, mn := range s.serverNodes(r) {
			s.updatesAttention(r, mn.view, mn.node, add)
		}
	}

	if u.TOTPSecret == "" && !owner {
		add(levelInfo, "Protect your account with two-factor authentication.", "/account/2fa", "Turn it on")
	}
	return items
}

// serverNode is a server with what the UI needs from it.
type serverNode struct {
	view serverRef
	node ServiceNode
}

// certWarning is how close to expiry a lab certificate is flagged.
const certWarning = 30 * 24 * time.Hour

// serverNodes lists this server, then the lab's other reachable ones.
func (s *Server) serverNodes(r *http.Request) []serverNode {
	var out []serverNode
	if s.Node != nil {
		out = append(out, serverNode{serverRef{ID: s.Node.Identity().ID, Name: s.Node.Identity().Name, This: true}, s.Node})
	}
	if s.Manager == nil {
		return out
	}
	servers, _ := s.Manager.Servers()
	for _, mc := range servers {
		if mc.This {
			continue
		}
		n := s.Manager.NodeByID(mc.ID)
		if n == nil || mc.Status.LastErr != nil && strings.HasPrefix(mc.Status.LastErr.Error(), "MyLab can't reach") {
			continue // already flagged as unreachable
		}
		out = append(out, serverNode{serverRef{ID: mc.ID, Name: mc.Name}, n})
	}
	return out
}

// on names another server in a message ("" for this one).
func (m serverRef) on() string {
	if m.This {
		return ""
	}
	return " on " + m.Name
}

func (s *Server) backupAttention(r *http.Request, m serverRef, n ServiceNode, owner bool, add func(level, text, link, linkText string)) {
	b := n.Backups()
	if b == nil {
		return
	}
	st, err := b.Status(r.Context())
	if err != nil {
		return
	}
	link := m.Backups()
	switch {
	case !st.Configured && m.This:
		setup := link
		if owner {
			setup = "/setup/backup"
		}
		add(levelProblem, "Backups aren't set up. Until they are, a failed disk means losing everything.", setup, "Set up backups")
	case !st.Configured:
		add(levelProblem, "Backups aren't set up"+m.on()+". Until they are, a failed disk there means losing its services' data.",
			link, "View")
	case st.Running:
		add(levelInfo, "Backing up"+m.on()+"…", link, "View")
	case st.Last != nil && !st.Last.OK:
		add(levelProblem, "The last backup"+m.on()+" didn't finish: "+st.Last.Error, link, "View backups")
	}
	// Extra copies (a first one is made with the next backup).
	for _, c := range st.Copies {
		switch {
		case st.Running:
		case c.Last != nil && !c.Last.OK:
			add(levelWarning, "The last copy of backups"+m.on()+" to "+c.Title+" didn't finish: "+c.Last.Error, link, "View backups")
		case c.LastOK != nil && isStale(c.LastOK.Finished):
			add(levelWarning, "Backups"+m.on()+" haven't been copied to "+c.Title+" since "+whenText(c.LastOK.Finished)+".",
				link, "View backups")
		}
	}
	switch {
	case !st.Configured || st.Running || (st.Last != nil && !st.Last.OK):
	case st.LastOK == nil:
		add(levelInfo, "The first backup"+m.on()+" runs "+whenText(st.Next)+". You can also back up now.", link, "Back up now")
	case isStale(st.LastOK.Finished):
		add(levelWarning, "No backup has finished"+m.on()+" in the last two days.", link, "View backups")
	}
	if st.Configured && st.LocalOnly {
		where := "this server"
		if !m.This {
			where = m.Name
		}
		add(levelWarning, "Backups are only stored on "+where+". Add a copy somewhere else to protect "+
			"against theft, fire or a failed disk.", link, "Improve backups")
	}
}

func (s *Server) updatesAttention(r *http.Request, m serverRef, n ServiceNode, add func(level, text, link, linkText string)) {
	u := n.Updates()
	if u == nil {
		return
	}
	if c, ok := s.hostChanges(r, m, n); ok && c.Any() && !c.Known {
		add(levelWarning, "Changes MyLab didn't make"+m.on()+": "+hostChangesText(c)+
			". They can stop updates or the firewall from working as MyLab expects.", m.Updates()+"#host-changes", "See the changes")
	}
	if last, err := u.Last(r.Context()); err == nil && last != nil && !last.OK {
		add(levelProblem, "OS updates didn't finish last time"+m.on()+". The server may be missing security fixes.",
			m.Updates(), "See what happened")
	}
	rs := u.RestartStatus()
	if !rs.Needed {
		return
	}
	mode, hour := s.Maintenance.Restart, s.Maintenance.Hour()
	if set := n.Settings(); set != nil {
		mode, hour = set.RestartPolicy()
	}
	if mode == config.RestartNotify {
		add(levelWarning, m.Label()+" needs a restart to finish installing updates.", m.Updates(), "Restart")
	} else {
		add(levelInfo, fmt.Sprintf("%s will restart around %02d:00 to finish installing updates.", m.Label(), hour),
			m.Updates(), "Details")
	}
}
