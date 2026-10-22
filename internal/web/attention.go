package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/store"
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
	Level string
	// Group is the email group it's in (notify.go): notifyProblems, or
	// notifyWaiting for what waits for a person rather than being broken.
	Group    string
	Text     string
	Link     string
	LinkText string
	// Key identifies the problem for email whatever its words: an error's
	// text, a size or a time can change while it's the same problem.
	Key string
}

// attentionList is what needs attention, as the rules below find it.
type attentionList []attentionItem

// add adds an item, in the Problems group unless it says otherwise.
func (l *attentionList) add(it attentionItem) {
	it.Group = cmp.Or(it.Group, notifyProblems)
	*l = append(*l, it)
}

// goneAfter is how long a server can go unreached before Roostack asks
// whether it's gone for good.
const goneAfter = 3 * 24 * time.Hour

// attention gathers what u should know about, filtered by role: Members
// see nothing they can't act on.
func (s *Server) attention(ctx context.Context, u *store.User) []attentionItem {
	owner := auth.Can(u.Role, auth.ManageHost)
	admin := auth.Can(u.Role, auth.ManageServices)
	var l attentionList

	if s.RestorePending != nil && s.RestorePending() {
		l.add(attentionItem{Key: "restore-pending", Level: levelInfo,
			Text: "Finishing the restore: putting the services' databases back from the backup. Some services may not work until it's done."})
	}
	// Files the controller found in backups and couldn't read (junk.go).
	if owner {
		s.junkAttention(ctx, &l)
	}
	// Another lab that stopped storing this lab's backups (it said so).
	if owner {
		for _, e := range s.Manager.EndingStorages(ctx) {
			l.add(attentionItem{Key: "ending-storage:" + e.HostName, Level: levelWarning,
				Text: e.HostName + " has stopped storing your backups. What's there is deleted on " +
					e.DeleteOn.Local().Format("2 January") + ": send them somewhere else.",
				Link: "/backups#destination", LinkText: "Choose where backups go"})
		}
	}
	if owner {
		if p, err := s.Setup.Progress(ctx); err == nil && p.DomainLater && !p.DomainDone {
			l.add(attentionItem{Key: "no-address", Level: levelProblem,
				Text: "Your lab has no address yet, so its services can't be reached on the web.",
				Link: "/setup/domain", LinkText: "Give it an address"})
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
		l.add(attentionItem{Key: "old-address:" + o.Domain, Level: level,
			Text: text,
			Link: link, LinkText: map[bool]string{true: "What to update", false: ""}[owner]})
	}
	// The link to Roostack Cloud: gone (unlinked there, or restored from
	// before it was unlinked), or Cloud out of reach for days (SPEC.md §6.6).
	if owner && s.Cloud != nil {
		switch st := s.Cloud.Status(); {
		case !st.Forgotten.IsZero():
			l.add(attentionItem{Key: "cloud-forgotten", Level: levelWarning,
				Text: "This lab is no longer linked to Roostack Cloud: Cloud stopped accepting its certificate.",
				Link: "/settings/cloud", LinkText: "See why"})
		case st.Linked && st.Refused != "":
			l.add(attentionItem{Key: "cloud-refused", Level: levelWarning,
				Text: "Roostack Cloud refuses this lab's requests: " + st.Refused,
				Link: "/settings/cloud", LinkText: "Roostack Cloud"})
		case st.Linked && st.LastError != "" && time.Since(st.LastContact) > cloudUnreachableFor:
			l.add(attentionItem{Key: "cloud-unreachable", Level: levelWarning,
				Text: "Roostack Cloud hasn't been reachable since " + whenText(st.LastContact) + ".",
				Link: "/settings/cloud", LinkText: "Roostack Cloud"})
		}
		s.cloudStanding(&l)
	}
	// No list from the service registry for a while (SPEC.md §8.3.3).
	if owner {
		s.registryAttention(&l)
	}

	if admin {
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
				level, key, text := levelProblem, "cert-expired:"+mc.ID, mc.Name+"'s certificate on Roostack's network has expired, so the lab can't reach it."
				if left > 0 {
					level, key, text = levelWarning, "cert-expiring:"+mc.ID, mc.Name+"'s certificate on Roostack's network expires "+
						whenText(mc.CertExpires)+" and isn't renewing. Check that it's running and can reach the controller."
				}
				l.add(attentionItem{Key: key, Level: level,
					Text: text,
					Link: "/servers#server-" + mc.ID, LinkText: "View servers"})
			}
			switch st := mc.Status; {
			case st.Running:
				l.add(attentionItem{Key: "applying:" + mc.ID, Level: levelInfo,
					Text: "Applying changes" + on + "… Downloading a new service can take a few minutes.",
					Link: "/servers#changes-" + mc.ID, LinkText: "See what it's doing"})
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
				l.add(item)
			case st.LastErr != nil:
				l.add(attentionItem{Level: levelProblem, Key: "apply:" + mc.ID,
					Text: "The last change" + on + " didn't finish: " + firstLine(st.LastErr.Error()), Link: "/services", LinkText: "View services"})
			}
			// A server that joined has no firewall until the Owner chooses
			// who can log in over SSH (this server's is a setup step).
			if owner && !mc.This {
				if n := s.Manager.NodeByID(mc.ID); n != nil {
					if set := n.Settings(); set != nil {
						if a, err := set.SSHAccess(ctx, false); err == nil && a.Choice == "" {
							l.add(attentionItem{Key: "firewall-off:" + mc.ID, Level: levelWarning,
								Text: mc.Name + "'s firewall is off until you choose who can log in to it over SSH.",
								Link: "/settings/servers/" + mc.ID + "/remote-access", LinkText: "Choose"})
						}
					}
				}
			}
			// SSH takes passwords where people can reach it: a warning that
			// lasts until it's keys only (SPEC.md §6.10 item 3).
			if owner {
				if n := s.Manager.NodeByID(mc.ID); n != nil {
					if set := n.Settings(); set != nil {
						if a, err := set.SSHAccess(ctx, false); err == nil && a.PasswordsOpen() {
							s.sshPasswordsAttention(&l, mc.This, len(servers) > 1, mc.Name, mc.ID, a.Choice)
						}
					}
				}
			}
		}
		// Visitors come in through a cloud server that isn't connected:
		// public services are down from outside (SPEC.md §10.5).
		if owner && !s.onInternet() && len(s.Manager.PublicHosts()) > 0 {
			if e, ok := s.Manager.Edge(ctx); ok {
				up := false
				for _, mc := range servers {
					if mc.ID == e.ID {
						up = !errors.Is(mc.Status.LastErr, node.ErrUnreachable)
					}
				}
				if !up || e.Address == "" {
					name := cmp.Or(e.Name, e.ID)
					l.add(attentionItem{Level: levelProblem, Key: "edge:" + e.ID,
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
					l.add(attentionItem{Level: levelProblem, Key: "relay-proof-missing:" + d.Domain,
						Text: "Roostack Cloud can't find the record proving " + d.Domain + " is this lab's any more. " +
							"Public services stop coming through the relay a day after it went: put it back.",
						Link: "/settings/public#way-in", LinkText: "Public access"})
				case !d.Verified:
					l.add(attentionItem{Level: levelWarning, Key: "relay-proof:" + d.Domain,
						Text: "Public services aren't coming through Roostack Cloud's relay yet: it's waiting for the record " +
							"proving " + d.Domain + " is this lab's.",
						Link: "/settings/public#way-in", LinkText: "Public access"})
				}
			}
		}
		if services, err := s.Services(ctx); err == nil {
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
				l.add(attentionItem{Key: "waiting-restore", Level: levelInfo,
					Text: text + ".",
					Link: "/backups#restoring", LinkText: "Restore"})
			}
			// Versions the service registry withdrew (SPEC.md §8.3.4).
			s.withdrawalAttention(ctx, services, &l)
			// Restores and moves that didn't finish (SPEC.md §6.9, §10.10).
			s.holdAttention(ctx, services, &l)
			// Updates to newer versions (SPEC.md §7).
			s.serviceUpdatesAttention(ctx, services, &l)
			for _, svc := range services {
				if svc.Status == StatusWaiting {
					continue
				}
				// A library at a place of its own whose drive is gone: it
				// restarts once the drive is back (SPEC.md §8.4).
				if s.Libraries != nil {
					if c, err := s.Libraries.ServiceLibraries(ctx, svc.Name); err == nil && len(c.Gone) > 0 {
						l.add(attentionItem{Key: "library-gone:" + svc.Name, Level: levelWarning,
							Text: fmt.Sprintf("%s can't see %s: %s isn't connected. Plug it back in, and Roostack restarts %s by itself.",
								svc.Title, joinAnd(c.Gone), plural(len(c.Gone), "its drive", "their drives"), svc.Title),
							Link: "/services/" + svc.Name, LinkText: "View"})
					}
				}
				// Asked of the server each service runs on.
				n := s.Manager.NodeFor(svc.Name)
				if n == nil {
					continue
				}
				hs := n.InstallStatus(ctx, svc.Name)
				link := "/services/" + svc.Name
				switch {
				case hs.Failed != "":
					l.add(attentionItem{Key: "setup-failed:" + svc.Name, Level: levelProblem,
						Text: svc.Title + " didn't finish setting up.",
						Link: link, LinkText: "See what went wrong"})
				case hs.Running || hs.Pending:
					l.add(attentionItem{Key: "setting-up:" + svc.Name, Level: levelInfo,
						Text: "Finishing setting up " + svc.Title + "…",
						Link: link, LinkText: "View"})
				case svc.Status == "failed":
					// systemd gave up restarting it (SPEC.md §6.13: emailed
					// once it has lasted a few minutes).
					l.add(attentionItem{Key: "stopped:" + svc.Name, Level: levelProblem,
						Text: svc.Title + " has stopped working.",
						Link: link, LinkText: "See what's wrong"})
				}
			}
		}
	}

	if admin {
		for _, mn := range s.serverNodes() {
			s.backupAttention(ctx, mn.view, mn.node, owner, &l)
		}
		// What monitoring found down (SPEC.md §6.15).
		s.monitorAttention(&l)
	}
	if owner {
		s.spaceAttention(ctx, &l)
	}

	// A server with a public address of its own, in a lab Roostack treats
	// as at home: probably a cloud server nobody has told it about.
	if owner {
		if c := s.network(); c.Saved == "" && c.Detected == config.NetworkInternet {
			l.add(attentionItem{Key: "looks-like-cloud", Level: levelWarning,
				Text: "This server looks like a cloud server: it has a public address of its own. Tell Roostack, " +
					"so private services and the public access checklist fit.",
				Link: "/settings/home-network", LinkText: "Where it is"})
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
			l.add(attentionItem{Key: "dns:" + st.State, Level: levelProblem,
				Text: headline,
				Link: "/settings/home-network", LinkText: "See how to fix it"})
		case dnsrecords.StatePending:
			l.add(attentionItem{Key: "dns-pending", Level: levelInfo,
				Text: st.Message,
				Link: "/settings/home-network", LinkText: "View"})
		case dnsrecords.StateManual:
			if !st.Reachable {
				first := map[bool]string{true: "Your services' addresses don't lead here yet. ",
					false: "Devices at home can't find your services yet. "}[st.Internet]
				l.add(attentionItem{Key: "dns-unreachable", Level: levelWarning,
					Text: first + st.Message,
					Link: "/settings/home-network", LinkText: "DNS settings"})
			}
		}
	}

	// DNS records Roostack couldn't update or delete (SPEC.md §6.6, §6.11).
	if owner && s.DNS != nil {
		st := s.DNS.Status()
		if len(st.NotDeleted) > 0 {
			l.add(attentionItem{Key: "dns-not-deleted", Level: levelWarning,
				Text: "Roostack couldn't delete the DNS record of an address that isn't public any more, so it still " +
					"points at your home. It keeps trying.",
				Link: "/settings/public#not-deleted", LinkText: "View"})
		}
		switch o := st.Old; {
		case o != nil && o.Ended && o.Message != "" && len(o.Records) > 0:
			l.add(attentionItem{Key: "dns-old-not-deleted", Level: levelWarning,
				Text: "Roostack couldn't delete the DNS records it set under your lab's old address, " + o.Domain + ".",
				Link: "/settings/domain#old-records", LinkText: "View"})
		case o != nil && !o.Ended && o.Message != "":
			l.add(attentionItem{Key: "dns-old-not-updated", Level: levelWarning,
				Text: "Roostack couldn't update the DNS records of your lab's old address, " + o.Domain +
					", so some old addresses may stop working early.",
				Link: "/settings/domain#old-address", LinkText: "View"})
		}
		if groups := leftGroups(st.Left); len(groups) > 0 {
			where := "your DNS provider"
			if len(groups) == 1 && groups[0].Provider != "" {
				where = groups[0].Provider
			}
			l.add(attentionItem{Key: "dns-left", Level: levelWarning,
				Text: "DNS records Roostack set under an address your lab no longer uses are still there: " +
					"delete them at " + where + ".",
				Link: "/settings/domain#old-records", LinkText: "View"})
		}
	}

	if owner {
		for _, mn := range s.serverNodes() {
			s.hostUpdatesAttention(ctx, mn.view, mn.node, &l)
		}
	}

	if u.TOTPSecret == "" && !owner {
		l.add(attentionItem{Key: "two-factor", Level: levelInfo,
			Text: "Protect your account with two-factor authentication.",
			Link: "/account/2fa", LinkText: "Turn it on"})
	}
	return l
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
func (s *Server) serverNodes() []serverNode {
	var out []serverNode
	if s.Node != nil {
		out = append(out, serverNode{serverRef{ID: s.Node.Identity().ID, Name: s.Node.Identity().Name, This: true}, s.Node})
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

func (s *Server) backupAttention(ctx context.Context, m serverRef, n ServiceNode, owner bool, l *attentionList) {
	b := n.Backups()
	if b == nil {
		return
	}
	st, err := b.Status(ctx)
	if err != nil {
		return
	}
	link := m.Backups()
	if f := st.Fenced; f != nil {
		l.add(attentionItem{Key: "backup-fenced:" + m.ID, Level: levelProblem,
			Text: f.Message() + m.on() + ".",
			Link: link, LinkText: "What to do"})
		return
	}
	// The main backup's server left the lab: its nightly backups there
	// fail until it has a new place, so say why at once rather than each
	// failure.
	lost := false
	if gone, ok := s.Manager.LostMainBackup(ctx, m.ID); ok {
		lost = true
		l.add(attentionItem{Key: "backup-lost-main:" + m.ID, Level: levelProblem,
			Text: "The main backup" + m.on() + " was on " + gone.HostName + "'s disk, and " + gone.HostName +
				" has left the lab. Choose a new place for it: until then, nightly backups there fail.",
			Link: link + "#plan", LinkText: "Choose a new place"})
	}
	switch {
	case !st.Configured && m.This:
		setup := link
		if owner {
			setup = "/setup/backup"
		}
		l.add(attentionItem{Key: "backup-unset:" + m.ID, Level: levelProblem,
			Text: "Backups aren't set up. Until they are, a failed disk means losing everything.",
			Link: setup, LinkText: "Set up backups"})
	case !st.Configured:
		l.add(attentionItem{Key: "backup-unset:" + m.ID, Level: levelProblem,
			Text: "Backups aren't set up" + m.on() + ". Until they are, a failed disk there means losing its services' data.",
			Link: link, LinkText: "View"})
	case st.Running:
		l.add(attentionItem{Key: "backing-up:" + m.ID, Level: levelInfo,
			Text: "Backing up" + m.on() + "…",
			Link: link, LinkText: "View"})
	case st.Last != nil && !st.Last.OK && retrySoon(st.Retry):
		// Roostack tries again within hours: only once that fails too.
	case lost:
	case st.Last != nil && !st.Last.OK:
		l.add(attentionItem{Key: "backup-failed:" + m.ID, Level: levelProblem,
			Text: "The last backup" + m.on() + " didn't finish: " + firstLine(st.Last.Error) + retryText(st.Retry),
			Link: link, LinkText: "View backups"})
	}
	// Extra copies (a first one is made with the next backup).
	for _, c := range st.Copies {
		switch {
		case st.Running:
		case c.Last != nil && !c.Last.OK && retrySoon(c.Retry):
		case c.Last != nil && !c.Last.OK:
			l.add(attentionItem{Key: "copy-failed:" + m.ID + ":" + c.Name, Level: levelWarning,
				Text: "The last copy of backups" + m.on() + " to " + c.Title + " didn't finish: " + firstLine(c.Last.Error) + retryText(c.Retry),
				Link: link, LinkText: "View backups"})
		case c.LastOK != nil && isStale(c.LastOK.Finished):
			l.add(attentionItem{Key: "copy-stale:" + m.ID + ":" + c.Name, Level: levelWarning,
				Text: "Backups" + m.on() + " haven't been copied to " + c.Title + " since " + whenText(c.LastOK.Finished) + ".",
				Link: link, LinkText: "View backups"})
		}
	}
	switch {
	case !st.Configured || st.Running || (st.Last != nil && !st.Last.OK) || lost:
	case st.LastOK == nil && st.Queued:
		l.add(attentionItem{Key: "backup-first-queued:" + m.ID, Level: levelInfo,
			Text: "The first backup" + m.on() + " is waiting to start.",
			Link: link, LinkText: "View backups"})
	case st.LastOK == nil:
		l.add(attentionItem{Key: "backup-first:" + m.ID, Level: levelInfo,
			Text: "The first backup" + m.on() + " runs " + whenText(st.Next) + ". You can also back up now.",
			Link: link, LinkText: "Back up now"})
	case isStale(st.LastOK.Finished):
		l.add(attentionItem{Key: "backup-stale:" + m.ID, Level: levelWarning,
			Text: "No backup has finished" + m.on() + " in the last two days.",
			Link: link, LinkText: "View backups"})
	}
	if st.Configured && st.LocalOnly {
		where := "this server"
		if !m.This {
			where = m.Name
		}
		l.add(attentionItem{Key: "backup-local-only:" + m.ID, Level: levelWarning,
			Text: "Backups are only stored on " + where + ". Add a copy somewhere else to protect " +
				"against theft, fire or a failed disk.",
			Link: link, LinkText: "Improve backups"})
	}
}

// hostUpdatesAttention is a server's OS updates, changes Roostack didn't
// make, Roostack's new release series, and restarts to finish updates.
func (s *Server) hostUpdatesAttention(ctx context.Context, m serverRef, n ServiceNode, l *attentionList) {
	u := n.Updates()
	if u == nil {
		return
	}
	if c, ok := s.hostChanges(ctx, m, n); ok && c.Any() && !c.Known {
		l.add(attentionItem{Key: "host-changes:" + m.ID, Level: levelWarning,
			Text: "Changes Roostack didn't make" + m.on() + ": " + hostChangesText(c) +
				". They can stop updates or the firewall from working as Roostack expects.",
			Link: m.Updates() + "#host-changes", LinkText: "See the changes"})
	}
	if last, err := u.Last(ctx); err == nil && last != nil && !last.OK {
		l.add(attentionItem{Key: "os-updates-failed:" + m.ID, Level: levelProblem,
			Text: "OS updates didn't finish last time" + m.on() + ". The server may be missing security fixes.",
			Link: m.Updates(), LinkText: "See what happened"})
	}
	if ser, err := u.Series(ctx); err == nil {
		if text := seriesText(ser, m); text != "" {
			l.add(attentionItem{Key: "series:" + m.ID, Level: levelInfo,
				Text: text,
				Link: m.Updates() + "#roostack", LinkText: map[bool]string{true: "Details", false: "See what's new"}[ser.Accepted != ""]})
		}
	}
	rs, err := u.RestartStatus(ctx)
	if err != nil || !rs.Needed {
		return // a server that doesn't answer has its own item
	}
	mode, hour := s.Maintenance.Restart, s.Maintenance.Hour()
	if set := n.Settings(); set != nil {
		if m, h, err := set.RestartPolicy(ctx); err == nil {
			mode, hour = m, h
		}
	}
	if mode == config.RestartNotify {
		l.add(attentionItem{Key: "restart-needed:" + m.ID, Level: levelWarning, Group: notifyWaiting,
			Text: m.Label() + " needs a restart to finish installing updates.",
			Link: m.Updates(), LinkText: "Restart"})
	} else {
		l.add(attentionItem{Key: "restart-planned:" + m.ID, Level: levelInfo,
			Text: fmt.Sprintf("%s will restart around %02d:00 to finish installing updates.", m.Label(), hour),
			Link: m.Updates(), LinkText: "Details"})
	}
}

// seriesText is a line about a newer release series of Roostack on a
// server (SPEC.md §7.2): on offer, waiting for the Owner's go-ahead; or
// accepted, installing with its next OS updates. "" when there's none.
// Information only: Roostack carries on working on the series it has.
func seriesText(ser osupdate.Series, m serverRef) string {
	switch {
	case ser.Accepted != "" && ser.Waiting != "":
		return "Roostack " + ser.Accepted + " is waiting to install" + m.on() + ": " + ser.Waiting + "."
	case ser.Accepted != "":
		return "Roostack " + ser.Accepted + " installs with the next OS updates" + m.on() + ", tonight after the backup."
	case ser.Newest != "":
		return "Roostack " + ser.NewestVersion + " is available" + m.on() + ". It starts a new series of releases, " +
			"so it waits for you to say yes."
	}
	return ""
}

// sshPasswordsAttention warns that a server's SSH takes passwords: a
// problem when it's open to anywhere, a warning from the home network.
func (s *Server) sshPasswordsAttention(l *attentionList, this, several bool, name, id, choice string) {
	who := "SSH on this server"
	if several {
		who = "SSH on " + name
	}
	link := "/settings/servers/" + id + "/remote-access"
	if this {
		link = "/settings/remote-access"
	}
	if choice == config.SSHFromAny {
		l.add(attentionItem{Key: "ssh-passwords-open:" + id, Level: levelProblem,
			Text: who + " accepts passwords from anywhere, so anyone who can reach it can try to guess one.",
			Link: link, LinkText: "Turn passwords off"})
		return
	}
	where := "your home network"
	if this && s.network().Internet() {
		where = "its private network"
	}
	l.add(attentionItem{Key: "ssh-passwords:" + id, Level: levelWarning,
		Text: who + " still accepts passwords: anyone on " + where + " can try to guess one.",
		Link: link, LinkText: "Turn passwords off"})
}
