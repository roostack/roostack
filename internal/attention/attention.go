// Package attention is what needs attention in the lab (SPEC.md §6.13):
// the rules that make each "Needs attention" item on the home page, and
// the notifier that emails them, with security events and the weekly
// summary, to each person who chose them. Roostack's pages show the items
// (internal/web); the rules read the lab through Lab, and the notifier
// runs on its own (Notifier).
package attention

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
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/monitor"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/words"
)

// Item levels.
const (
	Problem = "problem" // something is broken or unprotected
	Warning = "warning" // should be dealt with soon
	Info    = "info"    // in progress; nothing to do
)

// DNSHeadline summarises a DNS problem for the home page and Settings.
var DNSHeadline = map[string]string{
	dnsrecords.StateBlocked:    "Devices at home can't reach your services: your router is blocking their addresses.",
	dnsrecords.StateOverridden: "Devices at home are sent somewhere else when they open your services.",
	dnsrecords.StateConflict:   "A DNS record for your services points somewhere else, so Roostack can't update it.",
	dnsrecords.StateError:      "Roostack couldn't check or update your DNS records.",
}

// Item is one entry in the home page's "Needs attention" list. Each links
// to the page where it's dealt with.
type Item struct {
	Level string
	// Group is the email group it's in: GroupProblems, or GroupWaiting
	// for what waits for a person rather than being broken.
	Group    string
	Text     string
	Link     string
	LinkText string
	// Key identifies the problem for email whatever its words: an error's
	// text, a size or a time can change while it's the same problem.
	Key string
}

// List is what needs attention, as the rules below find it.
type List []Item

// add adds an item, in the Problems group unless it says otherwise.
func (l *List) add(it Item) {
	it.Group = cmp.Or(it.Group, GroupProblems)
	*l = append(*l, it)
}

// goneAfter is how long a server can go unreached before Roostack asks
// whether it's gone for good.
const goneAfter = 3 * 24 * time.Hour

// For gathers what u should know about, filtered by role: Members see
// nothing they can't act on.
func (lab *Lab) For(ctx context.Context, u *store.User) []Item {
	owner := auth.Can(u.Role, auth.ManageHost)
	admin := auth.Can(u.Role, auth.ManageServices)
	var l List

	if lab.RestorePending != nil && lab.RestorePending() {
		l.add(Item{Key: "restore-pending", Level: Info,
			Text: "Finishing the restore: putting the services' databases back from the backup. Some services may not work until it's done."})
	}
	// Files the controller found in backups and couldn't read (junk.go).
	if owner {
		lab.junk(ctx, &l)
	}
	// Another lab that stopped storing this lab's backups (it said so).
	if owner {
		for _, e := range lab.Manager.EndingStorages(ctx) {
			l.add(Item{Key: "ending-storage:" + e.HostName, Level: Warning,
				Text: e.HostName + " has stopped storing your backups. What's there will be deleted on " +
					e.DeleteOn.Local().Format("2 January") + ": send them somewhere else.",
				Link: "/backups#destination", LinkText: "Choose where backups go"})
		}
	}
	if owner {
		if p, err := lab.Setup.Progress(ctx); err == nil && p.DomainLater && !p.DomainDone {
			l.add(Item{Key: "no-address", Level: Problem,
				Text: "Your lab has no address yet, so its services can't be reached on the web.",
				Link: "/setup/domain", LinkText: "Give it an address"})
		}
	}
	// The lab's previous address, while it still works (SPEC.md §6.6):
	// everyone has bookmarks and apps to update.
	if o := lab.oldAddress(); o != nil {
		level, link, text := Info, "", "Your lab's address changed to "+o.Now+". The old addresses ("+o.Domain+
			") will keep working until "+o.Until.Local().Format("2 January")+": update bookmarks and the server address in apps."
		if o.Soon() {
			level = Warning
		}
		if owner {
			link = "/settings/domain#old-address"
		}
		l.add(Item{Key: "old-address:" + o.Domain, Level: level,
			Text: text,
			Link: link, LinkText: map[bool]string{true: "What to update", false: ""}[owner]})
	}
	// The link to Roostack Cloud: gone (unlinked there, or restored from
	// before it was unlinked), or Cloud out of reach for days (SPEC.md §6.6).
	if owner && lab.Cloud != nil {
		switch st := lab.Cloud.Status(); {
		case !st.Forgotten.IsZero():
			l.add(Item{Key: "cloud-forgotten", Level: Warning,
				Text: "This lab is no longer linked to Roostack Cloud: Cloud stopped accepting its certificate.",
				Link: "/settings/cloud", LinkText: "See why"})
		case st.Linked && st.Refused != "":
			l.add(Item{Key: "cloud-refused", Level: Warning,
				Text: "Roostack Cloud is refusing this lab's requests: " + st.Refused,
				Link: "/settings/cloud", LinkText: "Roostack Cloud"})
		case st.Linked && st.LastError != "" && time.Since(st.LastContact) > CloudUnreachableFor:
			l.add(Item{Key: "cloud-unreachable", Level: Warning,
				Text: "Roostack Cloud hasn't been reachable since " + words.When(st.LastContact) + ".",
				Link: "/settings/cloud", LinkText: "Roostack Cloud"})
		}
		lab.cloudStanding(&l)
	}
	// No list from the service registry for a while (SPEC.md §8.3.3).
	if owner {
		lab.registry(&l)
	}

	if admin {
		// Each server applies its own changes; name it once there are several.
		servers, _ := lab.Manager.Servers()
		for _, mc := range servers {
			on := ""
			if len(servers) > 1 {
				on = " on " + mc.Name
			}
			// Certificates renew 120 days ahead; one this close means renewing
			// keeps failing (the server logs why).
			if left := time.Until(mc.CertExpires); owner && !mc.CertExpires.IsZero() && left < certWarning {
				level, key, text := Problem, "cert-expired:"+mc.ID, mc.Name+"'s certificate on Roostack's network has expired, so the lab can't reach it."
				if left > 0 {
					level, key, text = Warning, "cert-expiring:"+mc.ID, mc.Name+"'s certificate on Roostack's network will expire "+
						words.When(mc.CertExpires)+" and isn't renewing. Check that it's running and can reach the controller."
				}
				l.add(Item{Key: key, Level: level, Text: text, Link: "/servers#server-" + mc.ID, LinkText: "View servers"})
			}
			switch st := mc.Status; {
			case st.Running:
				l.add(Item{Key: "applying:" + mc.ID, Level: Info,
					Text: "Applying changes" + on + "… Downloading a new service can take a few minutes.",
					Link: "/servers#changes-" + mc.ID, LinkText: "See what it's doing"})
			case errors.Is(st.LastErr, node.ErrUnreachable):
				// Its own item, in words that don't change with each attempt's
				// error (they made emails flap, 2026-10-08).
				item := Item{Level: Problem, Key: "unreachable:" + mc.ID,
					Text: "Roostack can't reach " + mc.Name + ". It may be switched off, or its network down.",
					Link: "/servers#server-" + mc.ID, LinkText: "View servers"}
				// Days later it may be gone for good (a reinstalled test VM
				// was): say so plainly, and offer what to do about it.
				if since := mc.UnreachableSince; owner && !since.IsZero() && time.Since(since) > goneAfter {
					item.Text = "Roostack hasn't reached " + mc.Name + " since " + words.When(since) + ". If it's gone for good " +
						"(reinstalled or switched off for good), "
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
				l.add(Item{Level: Problem, Key: "apply:" + mc.ID,
					Text: "The last change" + on + " didn't finish: " + words.FirstLine(st.LastErr.Error()), Link: "/services", LinkText: "View services"})
			}
			// A server that joined has no firewall until the Owner chooses
			// who can log in over SSH (this server's is a setup step).
			if owner && !mc.This {
				if n := lab.Manager.NodeByID(mc.ID); n != nil {
					if set := n.Settings(); set != nil {
						if a, err := set.SSHAccess(ctx, false); err == nil && a.Choice == "" {
							l.add(Item{Key: "firewall-off:" + mc.ID, Level: Warning,
								Text: mc.Name + "'s firewall is off until you choose who can log in to it over SSH.",
								Link: "/settings/servers/" + mc.ID + "/remote-access", LinkText: "Choose"})
						}
					}
				}
			}
			// SSH takes passwords where people can reach it: a warning that
			// lasts until it's keys only (SPEC.md §6.10 item 3).
			if owner {
				if n := lab.Manager.NodeByID(mc.ID); n != nil {
					if set := n.Settings(); set != nil {
						if a, err := set.SSHAccess(ctx, false); err == nil && a.PasswordsOpen() {
							lab.sshPasswords(&l, mc.This, len(servers) > 1, mc.Name, mc.ID, a.Choice)
						}
					}
				}
			}
		}
		// Visitors come in through a cloud server that isn't connected:
		// public services are down from outside (SPEC.md §10.5).
		if owner && !lab.onInternet() && len(lab.Manager.PublicHosts()) > 0 {
			if e, ok := lab.Manager.Edge(ctx); ok {
				up := false
				for _, mc := range servers {
					if mc.ID == e.ID {
						up = !errors.Is(mc.Status.LastErr, node.ErrUnreachable)
					}
				}
				if !up || e.Address == "" {
					name := cmp.Or(e.Name, e.ID)
					l.add(Item{Level: Problem, Key: "edge:" + e.ID,
						Text: "Public services can't be reached from the internet: visitors come in through " + name +
							", which isn't connected to the lab. Check it's running or choose port forwarding meanwhile.",
						Link: "/settings/public#way-in", LinkText: "Public access"})
				}
			}
		}
		// The lab's own domain through the relay, not proven (yet, or any
		// more): its public addresses aren't (or soon won't be) relayed.
		if owner && len(lab.Manager.PublicHosts()) > 0 {
			if d := lab.relayDomain(); d != nil {
				switch {
				case d.Verified && d.Missing:
					l.add(Item{Level: Problem, Key: "relay-proof-missing:" + d.Domain,
						Text: "Roostack Cloud can't find the record proving " + d.Domain + " is this lab's any more, " +
							"so public services will stop coming through the relay a day after it went: put it back.",
						Link: "/settings/public#way-in", LinkText: "Public access"})
				case !d.Verified:
					l.add(Item{Level: Warning, Key: "relay-proof:" + d.Domain,
						Text: "Public services aren't coming through Roostack Cloud's relay yet: it's waiting for the record " +
							"proving " + d.Domain + " is this lab's.",
						Link: "/settings/public#way-in", LinkText: "Public access"})
				}
			}
		}
		if services, err := lab.services(ctx); err == nil {
			// Services whose server is gone: information, not a problem (SPEC.md
			// §10.10): they wait until the Owner restores or removes them.
			var waiting []string
			var from []string
			for _, svc := range services {
				if svc.Waiting {
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
				l.add(Item{Key: "waiting-restore", Level: Info,
					Text: text + ".",
					Link: "/backups#restoring", LinkText: "Restore"})
			}
			// Public access paused (SPEC.md §6.16).
			if pp, err := lab.Manager.PublicPauses(); err == nil {
				for _, it := range pauseItems(pp, owner) {
					l.add(it)
				}
			}
			// Security advisories (SPEC.md §6.16).
			lab.advisoryItems(ctx, services, &l)
			// Versions the service registry withdrew (SPEC.md §8.3.4).
			lab.withdrawals(ctx, services, &l)
			// Restores and moves that didn't finish (SPEC.md §6.9, §10.10).
			lab.holds(ctx, services, &l)
			// Updates to newer versions (SPEC.md §7).
			lab.serviceUpdates(ctx, services, &l)
			for _, svc := range services {
				if svc.Waiting {
					continue
				}
				// A library at a place of its own whose drive is gone: it
				// restarts once the drive is back (SPEC.md §8.4).
				if lab.Libraries != nil {
					if c, err := lab.Libraries.ServiceLibraries(ctx, svc.Name); err == nil && len(c.Gone) > 0 {
						l.add(Item{Key: "library-gone:" + svc.Name, Level: Warning,
							Text: fmt.Sprintf("%s can't see %s: %s isn't connected. Plug it back in and Roostack will restart %s by itself.",
								svc.Title, words.And(c.Gone), words.Plural(len(c.Gone), "its drive", "their drives"), svc.Title),
							Link: "/services/" + svc.Name, LinkText: "View"})
					}
				}
				// Asked of the server each service runs on.
				n := lab.Manager.NodeFor(svc.Name)
				if n == nil {
					continue
				}
				hs := n.InstallStatus(ctx, svc.Name)
				link := "/services/" + svc.Name
				switch {
				case hs.Failed != "":
					l.add(Item{Key: "setup-failed:" + svc.Name, Level: Problem,
						Text: svc.Title + " didn't finish setting up.",
						Link: link, LinkText: "See what went wrong"})
				case hs.Running || hs.Pending:
					l.add(Item{Key: "setting-up:" + svc.Name, Level: Info,
						Text: "Finishing setting up " + svc.Title + "…",
						Link: link, LinkText: "View"})
				case svc.Failed:
					// systemd gave up restarting it (SPEC.md §6.13: emailed
					// once it has lasted a few minutes).
					l.add(Item{Key: "stopped:" + svc.Name, Level: Problem,
						Text: svc.Title + " has stopped working.",
						Link: link, LinkText: "See what's wrong"})
				}
			}
		}
	}

	if admin {
		for _, mn := range lab.servers() {
			lab.backups(ctx, mn.view, mn.node, owner, &l)
		}
		// What monitoring found down (SPEC.md §6.15).
		lab.monitor(&l)
	}
	if owner {
		lab.space(ctx, &l)
	}

	// A server with a public address of its own, in a lab Roostack treats
	// as at home: probably a cloud server nobody has told it about.
	if owner {
		if c := lab.network(); c.Saved == "" && c.Detected == config.NetworkInternet {
			l.add(Item{Key: "looks-like-cloud", Level: Warning,
				Text: "This server looks like a cloud server, as it has a public address of its own. Tell Roostack where it is " +
					"so that private services and the public access checklist fit it.",
				Link: "/settings/home-network", LinkText: "Where it is"})
		}
	}

	if owner && lab.DNS != nil {
		st := lab.DNS.Status()
		switch st.State {
		case dnsrecords.StateBlocked, dnsrecords.StateOverridden, dnsrecords.StateConflict, dnsrecords.StateError:
			// A headline here; the full explanation is on the Settings card.
			headline := DNSHeadline[st.State]
			if st.Internet && st.State == dnsrecords.StateOverridden {
				headline = "This server's DNS is sending your services' addresses somewhere else."
			}
			l.add(Item{Key: "dns:" + st.State, Level: Problem,
				Text: headline,
				Link: "/settings/home-network", LinkText: "See how to fix it"})
		case dnsrecords.StatePending:
			l.add(Item{Key: "dns-pending", Level: Info,
				Text: st.Message,
				Link: "/settings/home-network", LinkText: "View"})
		case dnsrecords.StateManual:
			if !st.Reachable {
				first := map[bool]string{true: "Your services' addresses don't lead here yet. ",
					false: "Devices at home can't find your services yet. "}[st.Internet]
				l.add(Item{Key: "dns-unreachable", Level: Warning,
					Text: first + st.Message,
					Link: "/settings/home-network", LinkText: "DNS settings"})
			}
		}
	}

	// DNS records Roostack couldn't update or delete (SPEC.md §6.6, §6.11).
	if owner && lab.DNS != nil {
		st := lab.DNS.Status()
		if len(st.NotDeleted) > 0 {
			l.add(Item{Key: "dns-not-deleted", Level: Warning,
				Text: "Roostack couldn't delete the DNS record of an address that isn't public any more, so it still " +
					"points at your home. Roostack will keep trying.",
				Link: "/settings/public#not-deleted", LinkText: "View"})
		}
		switch o := st.Old; {
		case o != nil && o.Ended && o.Message != "" && len(o.Records) > 0:
			l.add(Item{Key: "dns-old-not-deleted", Level: Warning,
				Text: "Roostack couldn't delete the DNS records it set under your lab's old address, " + o.Domain + ".",
				Link: "/settings/domain#old-records", LinkText: "View"})
		case o != nil && !o.Ended && o.Message != "":
			l.add(Item{Key: "dns-old-not-updated", Level: Warning,
				Text: "Roostack couldn't update the DNS records of your lab's old address, " + o.Domain +
					", so some old addresses may stop working early.",
				Link: "/settings/domain#old-address", LinkText: "View"})
		}
		if len(st.Left) > 0 {
			where := "your DNS provider"
			if p := leftAt(st.Left); p != "" {
				where = p
			}
			l.add(Item{Key: "dns-left", Level: Warning,
				Text: "DNS records Roostack set under an address your lab no longer uses are still there: " +
					"delete them at " + where + ".",
				Link: "/settings/domain#old-records", LinkText: "View"})
		}
	}

	if owner {
		for _, mn := range lab.servers() {
			lab.hostUpdates(ctx, mn.view, mn.node, &l)
		}
	}

	if u.TOTPSecret == "" && !owner {
		l.add(Item{Key: "two-factor", Level: Info,
			Text: "Protect your account with two-factor authentication.",
			Link: "/account/2fa", LinkText: "Turn it on"})
	}
	return l
}

// nextRun says when the next nightly backup will run, as the backup plan
// does: "tonight" when the server hasn't said.
func nextRun(t time.Time) string {
	if t.IsZero() {
		return "tonight"
	}
	return words.When(t)
}

// certWarning is how close to expiry a lab certificate is flagged.
const certWarning = 30 * 24 * time.Hour

// retrySoon reports a failed backup Roostack tries again by itself
// within hours (not one waiting for someone to plug its drive in).
func retrySoon(r *backup.Retry) bool { return r != nil && !r.Drive }

func (lab *Lab) backups(ctx context.Context, m Server, n Node, owner bool, l *List) {
	b := n.Backups()
	if b == nil {
		return
	}
	st, err := b.Status(ctx)
	if err != nil {
		return
	}
	link := m.backups()
	if f := st.Fenced; f != nil {
		l.add(Item{Key: "backup-fenced:" + m.ID, Level: Problem,
			Text: f.Message() + m.on() + ".",
			Link: link, LinkText: "What to do"})
		return
	}
	// The main backup's server left the lab: its nightly backups there
	// fail until it has a new place, so say why at once rather than each
	// failure.
	lost := false
	if gone, ok := lab.Manager.LostMainBackup(ctx, m.ID); ok {
		lost = true
		l.add(Item{Key: "backup-lost-main:" + m.ID, Level: Problem,
			Text: "The main backup" + m.on() + " was on " + gone.HostName + "'s disk, and " + gone.HostName +
				" has left the lab. Choose a new place for it: until then, nightly backups there will fail.",
			Link: link + "#plan", LinkText: "Choose a new place"})
	}
	switch {
	case !st.Configured && m.This:
		setup := link
		if owner {
			setup = "/setup/backup"
		}
		l.add(Item{Key: "backup-unset:" + m.ID, Level: Problem,
			Text: "Backups aren't set up. Until they are, a failed disk means losing everything.",
			Link: setup, LinkText: "Set up backups"})
	case !st.Configured:
		l.add(Item{Key: "backup-unset:" + m.ID, Level: Problem,
			Text: "Backups aren't set up" + m.on() + ". Until they are, a failed disk there means losing its services' data.",
			Link: link, LinkText: "View"})
	case st.Running:
		l.add(Item{Key: "backing-up:" + m.ID, Level: Info,
			Text: "Backing up" + m.on() + "…",
			Link: link, LinkText: "View"})
	case st.Last != nil && !st.Last.OK && retrySoon(st.Retry):
		// Roostack tries again within hours: only once that fails too.
	case lost:
	case st.Last != nil && !st.Last.OK:
		l.add(Item{Key: "backup-failed:" + m.ID, Level: Problem,
			Text: "The last backup" + m.on() + " didn't finish: " + words.FirstLine(st.Last.Error) + RetryText(st.Retry),
			Link: link, LinkText: "View backups"})
	}
	// Extra copies (a first one is made with the next backup).
	for _, c := range st.Copies {
		switch {
		case st.Running:
		case c.Last != nil && !c.Last.OK && retrySoon(c.Retry):
		case c.Last != nil && !c.Last.OK:
			l.add(Item{Key: "copy-failed:" + m.ID + ":" + c.Name, Level: Warning,
				Text: "The last copy of backups" + m.on() + " to " + c.Title + " didn't finish: " + words.FirstLine(c.Last.Error) + RetryText(c.Retry),
				Link: link, LinkText: "View backups"})
		case c.LastOK != nil && manager.BackupStale(c.LastOK.Finished):
			l.add(Item{Key: "copy-stale:" + m.ID + ":" + c.Name, Level: Warning,
				Text: "Backups" + m.on() + " haven't been copied to " + c.Title + " since " + words.When(c.LastOK.Finished) + ".",
				Link: link, LinkText: "View backups"})
		}
	}
	switch {
	case !st.Configured || st.Running || (st.Last != nil && !st.Last.OK) || lost:
	case st.LastOK == nil && st.Queued:
		l.add(Item{Key: "backup-first-queued:" + m.ID, Level: Info,
			Text: "The first backup" + m.on() + " is waiting to start.",
			Link: link, LinkText: "View backups"})
	case st.LastOK == nil:
		l.add(Item{Key: "backup-first:" + m.ID, Level: Info,
			Text: "The first backup" + m.on() + " will run " + nextRun(st.Next) + ". You can also back up now.",
			Link: link, LinkText: "Back up now"})
	case manager.BackupStale(st.LastOK.Finished):
		l.add(Item{Key: "backup-stale:" + m.ID, Level: Warning,
			Text: "No backup has finished" + m.on() + " in the last two days.",
			Link: link, LinkText: "View backups"})
	}
	if st.Configured && st.LocalOnly {
		where := "this server"
		if !m.This {
			where = m.Name
		}
		l.add(Item{Key: "backup-local-only:" + m.ID, Level: Warning,
			Text: "Backups are only stored on " + where + ". Add a copy somewhere else to protect " +
				"against theft, fire, or a failed disk.",
			Link: link, LinkText: "Improve backups"})
	}
}

// hostUpdatesAttention is a server's OS updates, changes Roostack didn't
// make, Roostack's new release series, and restarts to finish updates.
func (lab *Lab) hostUpdates(ctx context.Context, m Server, n Node, l *List) {
	u := n.Updates()
	if u == nil {
		return
	}
	if c, ok := lab.HostChecks.Get(ctx, m.ID, u); ok && c.Any() && !c.Known {
		l.add(Item{Key: "host-changes:" + m.ID, Level: Warning,
			Text: "Changes Roostack didn't make" + m.on() + ": " + hostChangesText(c) +
				". They can stop updates or the firewall from working as Roostack expects.",
			Link: m.updates() + "#host-changes", LinkText: "See the changes"})
	}
	if last, err := u.Last(ctx); err == nil && last != nil && !last.OK {
		l.add(Item{Key: "os-updates-failed:" + m.ID, Level: Problem,
			Text: "OS updates didn't finish last time" + m.on() + ". The server may be missing security fixes.",
			Link: m.updates(), LinkText: "See what happened"})
	}
	if ser, err := u.Series(ctx); err == nil {
		if text := seriesText(ser, m); text != "" {
			l.add(Item{Key: "series:" + m.ID, Level: Info,
				Text: text,
				Link: m.updates() + "#roostack", LinkText: map[bool]string{true: "Details", false: "See what's new"}[ser.Accepted != ""]})
		}
	}
	rs, err := u.RestartStatus(ctx)
	if err != nil || !rs.Needed {
		return // a server that doesn't answer has its own item
	}
	mode, hour := lab.Maintenance.Restart, lab.Maintenance.Hour()
	if set := n.Settings(); set != nil {
		if m, h, err := set.RestartPolicy(ctx); err == nil {
			mode, hour = m, h
		}
	}
	if mode == config.RestartNotify {
		l.add(Item{Key: "restart-needed:" + m.ID, Level: Warning, Group: GroupWaiting,
			Text: m.Label() + " needs a restart to finish installing updates.",
			Link: m.updates(), LinkText: "Restart"})
	} else {
		l.add(Item{Key: "restart-planned:" + m.ID, Level: Info,
			Text: fmt.Sprintf("%s will restart around %02d:00 to finish installing updates.", m.Label(), hour),
			Link: m.updates(), LinkText: "Details"})
	}
}

// seriesText is a line about a newer release series of Roostack on a
// server (SPEC.md §7.2): on offer, waiting for the Owner's go-ahead; or
// accepted, installing with its next OS updates. "" when there's none.
// Information only: Roostack carries on working on the series it has.
func seriesText(ser osupdate.Series, m Server) string {
	switch {
	case ser.Accepted != "" && ser.Waiting != "":
		return "Roostack " + ser.Accepted + " is waiting to install" + m.on() + ": " + ser.Waiting + "."
	case ser.Accepted != "":
		return "Roostack " + ser.Accepted + " will install with the next OS updates" + m.on() + ", tonight after the backup."
	case ser.Newest != "":
		return "Roostack " + ser.NewestVersion + " is available" + m.on() + ". It starts a new series of releases, " +
			"so it's waiting for you to say yes."
	}
	return ""
}

// sshPasswordsAttention warns that a server's SSH takes passwords: a
// problem when it's open to anywhere, a warning from the home network.
func (lab *Lab) sshPasswords(l *List, this, several bool, name, id, choice string) {
	who := "SSH on this server"
	if several {
		who = "SSH on " + name
	}
	link := "/settings/servers/" + id + "/remote-access"
	if this {
		link = "/settings/remote-access"
	}
	if choice == config.SSHFromAny {
		l.add(Item{Key: "ssh-passwords-open:" + id, Level: Problem,
			Text: who + " accepts passwords from anywhere, so anyone who can reach it can try to guess one.",
			Link: link, LinkText: "Turn passwords off"})
		return
	}
	where := "your home network"
	if this && lab.network().Internet() {
		where = "its private network"
	}
	l.add(Item{Key: "ssh-passwords:" + id, Level: Warning,
		Text: who + " still accepts passwords: anyone on " + where + " can try to guess one.",
		Link: link, LinkText: "Turn passwords off"})
}

// services are the lab's services (none without a list).
func (lab *Lab) services(ctx context.Context) ([]Service, error) {
	if lab.Services == nil {
		return nil, errors.New("no list of services")
	}
	return lab.Services(ctx)
}

// oldWarnFor is how long before the lab's old address stops that the
// home page's notice becomes a warning.
const oldWarnFor = 7 * 24 * time.Hour

// oldAddress is the lab's previous address while it still works (SPEC.md
// §6.6), with the address it changed to (Now); nil when there's none.
type oldAddress struct {
	config.OldAddress
	Now string
}

func (lab *Lab) oldAddress() *oldAddress {
	if lab.OldAddress == nil || lab.Domain == "" || !time.Now().Before(lab.OldAddress.Until) {
		return nil
	}
	return &oldAddress{OldAddress: *lab.OldAddress, Now: lab.Domain}
}

// Soon: it stops within oldWarnFor.
func (o *oldAddress) Soon() bool { return time.Until(o.Until) < oldWarnFor }

// leftAt names the DNS provider records left under an address the lab no
// longer uses are at, when they're all at one Roostack noted ("" if not).
func leftAt(recs []dnsrecords.Record) string {
	var at []string
	for _, r := range recs {
		title := ""
		if p := dns.Get(r.Via); p != nil {
			title = p.Title
		}
		if !slices.Contains(at, title) {
			at = append(at, title)
		}
	}
	if len(at) == 1 {
		return at[0]
	}
	return ""
}

// monitor adds what monitoring found down (SPEC.md §6.15): a check down
// after three failures, a certificate not being renewed. Servers aren't
// repeated: "can't reach" says so already.
func (lab *Lab) monitor(l *List) {
	if lab.Monitor == nil {
		return
	}
	now := time.Now()
	for _, st := range lab.Monitor.Statuses() {
		if !st.Alert {
			continue
		}
		switch {
		case st.State == monitor.StateDown:
			l.add(Item{Level: Problem, Key: "monitor:" + st.Key,
				Text: st.DownText(), Link: checkHref(st.Key), LinkText: "See what's wrong"})
		case st.CertWarning && st.State == monitor.StateUp:
			l.add(Item{Level: Warning, Key: "monitor-cert:" + st.Key,
				Text: words.Capitalise(st.Name + "'s address: " + st.CertWarningText(now) + "."), Link: checkHref(st.Key), LinkText: "Details"})
		}
	}
}

// checkHref is a check's page on Monitoring.
func checkHref(key string) string { return "/monitoring/check/" + key }

// registry warns the Owner when the lab hasn't had a list from the
// registry for a while (the Owner, 2026-10-14: a warning, not a refusal):
// withdrawals since then are unknown to it.
func (lab *Lab) registry(l *List) {
	if lab.Registry == nil || !lab.Registry.On() {
		return
	}
	st := lab.Registry.Status()
	if st.Checked.IsZero() || !st.Stale(time.Now()) {
		return
	}
	text := "Roostack can't get the list of services from the service registry, so you can't install services."
	if st.Serial != 0 {
		text = "Roostack's list of services is from " + words.When(st.Date) +
			": it hasn't been able to get a newer one, so it doesn't know of fixes or withdrawn versions since."
	}
	l.add(Item{Key: "registry-stale", Level: Warning, Text: text, Link: "/settings/updates#registry", LinkText: "See why"})
}
