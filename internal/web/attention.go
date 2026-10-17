package web

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/node"
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
	dnsrecords.StateConflict:   "A DNS record for your services points somewhere else, so Roostack can't update it.",
	dnsrecords.StateError:      "Roostack couldn't check or update your DNS records.",
}

// attentionItem is one entry in the home page's "Needs attention" list.
// Each links to the page where it's dealt with.
type attentionItem struct {
	Level    string
	Text     string
	Link     string
	LinkText string
	// Key, when set, identifies the problem for email whatever its words
	// (noticeKey): an error's text can change between attempts.
	Key string
}

// attention gathers what the current user should know about, filtered by
// role: Members see nothing they can't act on.

// goneAfter is how long a server can go unreached before Roostack asks
// whether it's gone for good.
const goneAfter = 3 * 24 * time.Hour

func (s *Server) attention(r *http.Request) []attentionItem {
	u := currentUser(r)
	owner := auth.Can(u.Role, auth.ManageHost)
	admin := auth.Can(u.Role, auth.ManageServices)
	var items []attentionItem
	add := func(level, text, link, linkText string) {
		items = append(items, attentionItem{Level: level, Text: text, Link: link, LinkText: linkText})
	}

	if s.RestorePending != nil && s.RestorePending() {
		add(levelInfo, "Finishing the restore: putting the services' databases back from the backup. Some services may not work until it's done.",
			"", "")
	}
	// Files the controller found in backups and couldn't read (junk.go).
	if owner && s.Manager != nil {
		s.junkAttention(r.Context(), add)
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
			add(levelProblem, "Your lab has no address yet, so its services can't be reached on the web.",
				"/setup/domain", "Give it an address")
		}
	}
	// The lab's previous address, while it still works (SPEC.md §6.6):
	// everyone has bookmarks and apps to update.
	if o := s.oldAddress(); o != nil {
		level, link, text := levelInfo, "", "Your lab's address changed to "+o.Now+". The old addresses ("+o.Domain+
			") keep working until "+o.Until.Local().Format("2 January")+": update bookmarks, and the server address in apps."
		if o.Soon() {
			level = levelWarning
		}
		if owner {
			link = "/settings/domain#old-address"
		}
		add(level, text, link, map[bool]string{true: "What to update", false: ""}[owner])
	}
	// The link to Roostack Cloud: gone (unlinked there, or restored from
	// before it was unlinked), or Cloud out of reach for days (SPEC.md §6.6).
	if owner && s.Cloud != nil {
		switch st := s.Cloud.Status(); {
		case !st.Forgotten.IsZero():
			add(levelWarning, "This lab is no longer linked to Roostack Cloud: Cloud stopped accepting its certificate.",
				"/settings/cloud", "See why")
		case st.Linked && st.Refused != "":
			add(levelWarning, "Roostack Cloud refuses this lab's requests: "+st.Refused, "/settings/cloud", "Roostack Cloud")
		case st.Linked && st.LastError != "" && time.Since(st.LastContact) > cloudUnreachableFor:
			add(levelWarning, "Roostack Cloud hasn't been reachable since "+whenText(st.LastContact)+".",
				"/settings/cloud", "Roostack Cloud")
		}
	}
	// No list from the service registry for a while (SPEC.md §8.3.3).
	if owner {
		s.registryAttention(add)
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
				level, text := levelProblem, mc.Name+"'s certificate on Roostack's network has expired, so the lab can't reach it."
				if left > 0 {
					level, text = levelWarning, mc.Name+"'s certificate on Roostack's network expires "+whenText(mc.CertExpires)+
						" and isn't renewing. Check that it's running and can reach the controller."
				}
				add(level, text, "/servers#server-"+mc.ID, "View servers")
			}
			switch st := mc.Status; {
			case st.Running:
				add(levelInfo, "Applying changes"+on+"… Downloading a new service can take a few minutes.",
					"/servers#changes-"+mc.ID, "See what it's doing")
			case errors.Is(st.LastErr, node.ErrUnreachable):
				// Its own item, in words that don't change with each attempt's
				// error (they made emails flap, 2026-10-08).
				item := attentionItem{Level: levelProblem, Key: "unreachable:" + mc.ID,
					Text: "Roostack can't reach " + mc.Name + ". It may be switched off, or its network down.",
					Link: "/servers#server-" + mc.ID, LinkText: "View servers"}
				// Days later it may be gone for good (a reinstalled test VM
				// was): say so plainly, and offer what to do about it.
				if since := mc.UnreachableSince; owner && !since.IsZero() && time.Since(since) > goneAfter {
					item.Text = "Roostack hasn't reached " + mc.Name + " since " + whenText(since) + ". If it's gone for good " +
						"(reinstalled, or switched off for good), "
					if len(mc.Services) > 0 || len(mc.Waiting) > 0 {
						item.Text += "its services can come back from their backups on another server."
						item.Link, item.LinkText = "/settings/servers/"+mc.ID+"/gone", "Restore its services"
					} else {
						item.Text += "remove it from the lab."
						item.Link, item.LinkText = "/servers?remove="+mc.ID+"#server-"+mc.ID, "Remove it"
					}
				}
				items = append(items, item)
			case st.LastErr != nil:
				items = append(items, attentionItem{Level: levelProblem, Key: "apply:" + mc.ID,
					Text: "The last change" + on + " didn't finish: " + firstLine(st.LastErr.Error()), Link: "/services", LinkText: "View services"})
			}
			// A server that joined has no firewall until the Owner chooses
			// who can log in over SSH (this server's is a setup step).
			if owner && !mc.This {
				if n := s.Manager.NodeByID(mc.ID); n != nil {
					if set := n.Settings(); set != nil {
						if a, err := set.SSHAccess(false); err == nil && a.Choice == "" {
							add(levelWarning, mc.Name+"'s firewall is off until you choose who can log in to it over SSH.",
								"/settings/servers/"+mc.ID+"/remote-access", "Choose")
						}
					}
				}
			}
			// SSH takes passwords where people can reach it: a warning that
			// lasts until it's keys only (SPEC.md §6.10 item 3).
			if owner {
				if n := s.Manager.NodeByID(mc.ID); n != nil {
					if set := n.Settings(); set != nil {
						if a, err := set.SSHAccess(false); err == nil && a.PasswordsOpen() {
							s.sshPasswordsAttention(add, mc.This, len(servers) > 1, mc.Name, mc.ID, a.Choice)
						}
					}
				}
			}
		}
		// Visitors come in through a cloud server that isn't connected:
		// public services are down from outside (SPEC.md §10.5).
		if owner && !s.onInternet() && len(s.Manager.PublicHosts()) > 0 {
			if e, ok := s.Manager.Edge(r.Context()); ok {
				up := false
				for _, mc := range servers {
					if mc.ID == e.ID {
						up = !errors.Is(mc.Status.LastErr, node.ErrUnreachable)
					}
				}
				if !up || e.Address == "" {
					name := cmp.Or(e.Name, e.ID)
					items = append(items, attentionItem{Level: levelProblem, Key: "edge:" + e.ID,
						Text: "Public services can't be reached from the internet: visitors come in through " + name +
							", which isn't connected to the lab. Check it's running, or choose port forwarding meanwhile.",
						Link: "/settings/public#way-in", LinkText: "Public access"})
				}
			}
		}
		// The lab's own domain through the relay, not proven (yet, or any
		// more): its public addresses aren't (or soon won't be) relayed.
		if owner && len(s.Manager.PublicHosts()) > 0 {
			if d := s.relayDomain(); d != nil {
				switch {
				case d.Verified && d.Missing:
					items = append(items, attentionItem{Level: levelProblem, Key: "relay-proof-missing:" + d.Domain,
						Text: "Roostack Cloud can't find the record proving " + d.Domain + " is this lab's any more. " +
							"Public services stop coming through the relay a day after it went: put it back.",
						Link: "/settings/public#way-in", LinkText: "Public access"})
				case !d.Verified:
					items = append(items, attentionItem{Level: levelWarning, Key: "relay-proof:" + d.Domain,
						Text: "Public services aren't coming through Roostack Cloud's relay yet: it's waiting for the record " +
							"proving " + d.Domain + " is this lab's.",
						Link: "/settings/public#way-in", LinkText: "Public access"})
				}
			}
		}
		if services, err := s.Services(r.Context()); err == nil {
			// Services whose server is gone: information, not a problem (SPEC.md
			// §10.10): they wait until the Owner restores or removes them.
			var waiting []string
			var from []string
			for _, svc := range services {
				if svc.Status == StatusWaiting {
					waiting = append(waiting, svc.Title)
					if svc.WasOn != "" && !slices.Contains(from, svc.WasOn) {
						from = append(from, svc.WasOn)
					}
				}
			}
			if owner && len(waiting) > 0 {
				text := fmt.Sprintf("%d services are waiting to be restored", len(waiting))
				if len(waiting) == 1 {
					text = waiting[0] + " is waiting to be restored"
				}
				if len(from) > 0 {
					text += " (they were on " + strings.Join(from, ", ") + ")"
					if len(waiting) == 1 {
						text = strings.Replace(text, "(they were", "(it was", 1)
					}
				}
				add(levelInfo, text+".", "/backups#restoring", "Restore")
			}
			// Versions the service registry withdrew (SPEC.md §8.3.4).
			s.withdrawalAttention(r, services, func(it attentionItem) { items = append(items, it) })
			// Updates to newer versions (SPEC.md §7).
			s.updateAttention(r, services, func(it attentionItem) { items = append(items, it) })
			for _, svc := range services {
				if svc.Status == StatusWaiting {
					continue
				}
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
				case svc.Status == "failed":
					// systemd gave up restarting it (SPEC.md §6.13: emailed
					// once it has lasted a few minutes).
					add(levelProblem, svc.Title+" has stopped working.", link, "See what's wrong")
				}
			}
		}
	}

	if admin {
		for _, mn := range s.serverNodes(r) {
			s.backupAttention(r, mn.view, mn.node, owner, add)
		}
	}
	if owner {
		s.spaceAttention(r, add)
	}

	// A server with a public address of its own, in a lab Roostack treats
	// as at home: probably a cloud server nobody has told it about.
	if owner && s.Setup != nil {
		if c := s.network(); c.Saved == "" && c.Detected == config.NetworkInternet {
			add(levelWarning, "This server looks like a cloud server: it has a public address of its own. Tell Roostack, "+
				"so private services and the public access checklist fit.", "/settings/home-network", "Where it is")
		}
	}

	if owner && s.DNS != nil {
		st := s.DNS.Status()
		switch st.State {
		case dnsrecords.StateBlocked, dnsrecords.StateOverridden, dnsrecords.StateConflict, dnsrecords.StateError:
			// A headline here; the full explanation is on the Settings card.
			headline := dnsHeadline[st.State]
			if st.Internet && st.State == dnsrecords.StateOverridden {
				headline = "This server's DNS sends your services' addresses somewhere else."
			}
			add(levelProblem, headline, "/settings/home-network", "See how to fix it")
		case dnsrecords.StatePending:
			add(levelInfo, st.Message, "/settings/home-network", "View")
		case dnsrecords.StateManual:
			if !st.Reachable {
				first := map[bool]string{true: "Your services' addresses don't lead here yet. ",
					false: "Devices at home can't find your services yet. "}[st.Internet]
				add(levelWarning, first+st.Message, "/settings/home-network", "DNS settings")
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

// retrySoon reports a failed backup Roostack tries again by itself
// within hours (not one waiting for someone to plug its drive in).
func retrySoon(r *backup.Retry) bool { return r != nil && !r.Drive }

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
		if n == nil || mc.Status.LastErr != nil && strings.HasPrefix(mc.Status.LastErr.Error(), "Roostack can't reach") {
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
	if f := st.Fenced; f != nil {
		add(levelProblem, f.Message()+m.on()+".", link, "What to do")
		return
	}
	// The main backup's server left the lab: its nightly backups there
	// fail until it has a new place, so say why at once rather than each
	// failure.
	lost := false
	if s.Manager != nil {
		if l, ok := s.Manager.LostMainBackup(r.Context(), m.ID); ok {
			lost = true
			add(levelProblem, "The main backup"+m.on()+" was on "+l.HostName+"'s disk, and "+l.HostName+
				" has left the lab. Choose a new place for it: until then, nightly backups there fail.", link+"#plan", "Choose a new place")
		}
	}
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
	case st.Last != nil && !st.Last.OK && retrySoon(st.Retry):
		// Roostack tries again within hours: only once that fails too.
	case lost:
	case st.Last != nil && !st.Last.OK:
		add(levelProblem, "The last backup"+m.on()+" didn't finish: "+firstLine(st.Last.Error)+retryText(st.Retry), link, "View backups")
	}
	// Extra copies (a first one is made with the next backup).
	for _, c := range st.Copies {
		switch {
		case st.Running:
		case c.Last != nil && !c.Last.OK && retrySoon(c.Retry):
		case c.Last != nil && !c.Last.OK:
			add(levelWarning, "The last copy of backups"+m.on()+" to "+c.Title+" didn't finish: "+firstLine(c.Last.Error)+retryText(c.Retry), link, "View backups")
		case c.LastOK != nil && isStale(c.LastOK.Finished):
			add(levelWarning, "Backups"+m.on()+" haven't been copied to "+c.Title+" since "+whenText(c.LastOK.Finished)+".",
				link, "View backups")
		}
	}
	switch {
	case !st.Configured || st.Running || (st.Last != nil && !st.Last.OK) || lost:
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
		add(levelWarning, "Changes Roostack didn't make"+m.on()+": "+hostChangesText(c)+
			". They can stop updates or the firewall from working as Roostack expects.", m.Updates()+"#host-changes", "See the changes")
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

// sshPasswordsAttention warns that a server's SSH takes passwords: a
// problem when it's open to anywhere, a warning from the home network.
func (s *Server) sshPasswordsAttention(add func(level, text, link, linkText string), this, several bool, name, id, choice string) {
	who := "SSH on this server"
	if several {
		who = "SSH on " + name
	}
	link := "/settings/servers/" + id + "/remote-access"
	if this {
		link = "/settings/remote-access"
	}
	if choice == config.SSHFromAny {
		add(levelProblem, who+" accepts passwords from anywhere, so anyone who can reach it can try to guess one.",
			link, "Turn passwords off")
		return
	}
	where := "your home network"
	if this && s.network().Internet() {
		where = "its private network"
	}
	add(levelWarning, who+" still accepts passwords: anyone on "+where+" can try to guess one.", link, "Turn passwords off")
}
