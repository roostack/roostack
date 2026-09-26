package web

import (
	"net/http"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/dnsrecords"
)

// Attention item levels.
const (
	levelProblem = "problem" // something is broken or unprotected
	levelWarning = "warning" // should be dealt with soon
	levelInfo    = "info"    // in progress; nothing to do
)

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
		st := s.Manager.Status()
		switch {
		case st.Running:
			add(levelInfo, "Applying changes… Downloading a new service can take a few minutes.", "", "")
		case st.LastErr != nil:
			add(levelProblem, "The last change didn't finish: "+st.LastErr.Error(), "/services", "View services")
		}
	}

	if admin && s.Backups != nil {
		if st, err := s.Backups.Status(r.Context()); err == nil {
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
			add(levelProblem, st.Message, "/settings#dns", "Home network settings")
		case dnsrecords.StatePending:
			add(levelInfo, st.Message, "/settings#dns", "View")
		case dnsrecords.StateManual:
			if !st.Reachable {
				add(levelWarning, "Devices at home can't find your services yet. "+st.Message, "/settings#dns", "Home network settings")
			}
		}
	}

	if u.TOTPSecret == "" && !owner {
		add(levelInfo, "Protect your account with two-factor authentication.", "/account/2fa", "Turn it on")
	}
	return items
}
