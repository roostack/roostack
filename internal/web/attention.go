package web

import (
	"fmt"
	"net/http"

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

	if owner && s.Setup != nil {
		if p, err := s.Setup.Progress(r.Context()); err == nil && p.DomainLater && !p.DomainDone {
			add(levelProblem, "Your domain isn't set up, so services can't get secure (HTTPS) certificates.",
				"/setup/domain", "Set up your domain")
		}
	}

	if admin && s.Manager != nil {
		// Each machine applies its own changes; name it once there are several.
		machines, _ := s.Manager.Machines()
		for _, mc := range machines {
			on := ""
			if len(machines) > 1 {
				on = " on " + mc.Name
			}
			switch st := mc.Status; {
			case st.Running:
				add(levelInfo, "Applying changes"+on+"… Downloading a new service can take a few minutes.", "", "")
			case st.LastErr != nil:
				add(levelProblem, "The last change"+on+" didn't finish: "+st.LastErr.Error(), "/services", "View services")
			}
		}
		if services, err := s.Services(r.Context()); err == nil {
			for _, svc := range services {
				// Asked of the machine each service runs on.
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

	if admin && s.backups(r) != nil {
		if st, err := s.backups(r).Status(r.Context()); err == nil {
			backupLink := "/backups"
			switch {
			case !st.Configured:
				if owner {
					backupLink = "/setup/backup"
				}
				add(levelProblem, "Backups aren't set up. Until they are, a failed disk means losing everything.",
					backupLink, "Set up backups")
			case st.Running:
				add(levelInfo, "Backing up…", "/backups", "View")
			case st.Last != nil && !st.Last.OK:
				add(levelProblem, "The last backup didn't finish: "+st.Last.Error, "/backups", "View backups")
			}
			switch {
			case !st.Configured || st.Running || (st.Last != nil && !st.Last.OK):
			case st.LastOK == nil:
				add(levelInfo, "The first backup runs "+whenText(st.Next)+". You can also back up now.", "/backups", "Back up now")
			case isStale(st.LastOK.Finished):
				add(levelWarning, "No backup has finished in the last two days.", "/backups", "View backups")
			}
			if st.Configured && st.LocalOnly {
				add(levelWarning, "Backups are only stored on this machine. Add a copy somewhere else to protect "+
					"against theft, fire or a failed disk.", "/backups", "Improve backups")
			}
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

	if owner && s.updates(r) != nil {
		if last, err := s.updates(r).Last(r.Context()); err == nil && last != nil && !last.OK {
			add(levelProblem, "OS updates didn't finish last time. The machine may be missing security fixes.",
				"/settings/updates", "See what happened")
		}
		if rs := s.updates(r).RestartStatus(); rs.Needed {
			if s.Maintenance.Restart == config.RestartNotify {
				add(levelWarning, "This machine needs a restart to finish installing updates.", "/settings/updates", "Restart")
			} else {
				add(levelInfo, fmt.Sprintf("This machine will restart around %02d:00 to finish installing updates.",
					s.Maintenance.Hour()), "/settings/updates", "Details")
			}
		}
	}

	if u.TOTPSecret == "" && !owner {
		add(levelInfo, "Protect your account with two-factor authentication.", "/account/2fa", "Turn it on")
	}
	return items
}
