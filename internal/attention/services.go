package attention

import (
	"context"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/words"
)

// What needs attention about the lab's services: the registry's
// withdrawals (SPEC.md §8.3.4), restores and moves that didn't finish
// (SPEC.md §6.9, §10.10), and updates (SPEC.md §7).

// withdrawals adds a "Needs attention" item for each installed
// service whose version the registry withdrew. Stops are problems (and
// emailed like other problems); warnings, a stop the Owner turned off
// or overrode, and notices say what's wrong and what to do.
func (lab *Lab) withdrawals(ctx context.Context, services []Service, l *List) {
	ws := lab.Manager.Withdrawals(ctx)
	for _, svc := range services {
		w := ws[svc.Name]
		if w == nil {
			continue
		}
		link := "/services/" + svc.Name + "#withdrawn"
		item := Item{Link: link, Key: "withdrawn:" + svc.Name + ":" + w.Version + ":" + w.Shown}
		switch {
		case w.Stopped:
			item.Level, item.LinkText = Problem, "See what to do"
			item.Text = "Roostack stopped " + svc.Title + ": the service registry says the version it has is dangerous. Its data is kept."
		case w.StartedAnyway:
			item.Level, item.LinkText = Warning, "View"
			item.Text = svc.Title + " is running a version the service registry says is dangerous: you started it anyway."
		case w.Level == registry.LevelStop:
			item.Level, item.LinkText = Warning, "What to do"
			item.Text = "The service registry says the version of " + svc.Title + " you have is dangerous: " + registryReason(w) +
				" Roostack would stop it, but you've turned that off."
		case w.Shown == registry.LevelWarning:
			item.Level, item.LinkText = Warning, "What to do"
			item.Text = "The version of " + svc.Title + " you have has been withdrawn by the service registry: " + registryReason(w)
		default:
			item.Level, item.LinkText = Info, "Read it"
			item.Text = "The service registry has a notice about " + svc.Title + " " + w.Version + ": " + registryReason(w)
		}
		l.add(item)
	}
}

// registryReason is the registry's reason as the end of a sentence.
func registryReason(w *manager.Withdrawal) string {
	return strings.TrimRight(strings.TrimSpace(w.Reason), ".") + "."
}

// holds is a problem for each held service, and a warning for
// each move that didn't finish and went back.
func (lab *Lab) holds(ctx context.Context, services []Service, l *List) {
	for _, svc := range services {
		v := lab.held(svc)
		if v == nil {
			continue
		}
		item := Item{Level: Problem, Link: "/services/" + svc.Name + "#held", LinkText: "What to do",
			Key: "held:" + svc.Name + ":" + v.At.Format(time.RFC3339)}
		if v.MovedFrom != "" {
			item.Text = svc.Title + " is stopped on " + v.On + ": its database didn't load there, and moving it back to " +
				v.From + " failed too."
		} else {
			item.Text = svc.Title + " is stopped: its files are back from the backup of " + v.TakenText +
				", but its database couldn't be loaded."
		}
		l.add(item)
	}
	moves := lab.Manager.FailedMoves(ctx)
	for _, svc := range services {
		if f, ok := moves[svc.Name]; ok && svc.Held == nil {
			l.add(Item{Level: Warning, Link: "/services/" + svc.Name + "/move", LinkText: "See what went wrong",
				Key:  "move-failed:" + svc.Name + ":" + f.At.Format(time.RFC3339),
				Text: svc.Title + " couldn't be moved to " + f.ToName + ". It's running on " + f.FromName + ", as before."})
		}
	}
}

// serviceUpdates adds "Needs attention" items for updates to
// services: a failed one is a problem (emailed like the others); the
// services with an update waiting for a person share one line.
func (lab *Lab) serviceUpdates(ctx context.Context, services []Service, l *List) {
	ups := lab.Manager.AvailableUpdates(ctx)
	ws := lab.Manager.Withdrawals(ctx)
	var waiting []string
	link := ""
	for _, svc := range services {
		if lab.Manager.Updating(svc.Name) != nil {
			continue
		}
		u := ups[svc.Name]
		if rec := lab.Manager.LastUpdate(ctx, svc.Name); rec != nil && rec.Result != manager.UpdateDone && rec.Shown(u) {
			text := "Updating " + svc.Title + " to " + manager.AppVersion(rec.To, rec.From) + " didn't work"
			if rec.Result == manager.UpdateRolledBack {
				text += "; Roostack went back to " + manager.AppVersion(rec.From, rec.To) + "."
			} else {
				text += "."
			}
			l.add(Item{Level: Problem, Text: text, Link: "/services/" + svc.Name + "#update",
				LinkText: "See what happened", Key: "update:" + svc.Name + ":" + rec.To})
			continue
		}
		if u == nil || ws[svc.Name] != nil {
			continue // a withdrawal says it already, with the update
		}
		waiting = append(waiting, svc.Title+" ("+u.ToApp()+")")
		link = "/services/" + svc.Name + "#update"
	}
	switch len(waiting) {
	case 0:
		return
	case 1:
		l.add(Item{Level: Info, Key: "updates-available", Text: "An update is available for " + waiting[0] + ".", Link: link, LinkText: "See it"})
	default:
		l.add(Item{Level: Info, Key: "updates-available", Text: "Updates are available for " + words.And(waiting) + ".", Link: "/services",
			LinkText: "See them"})
	}
}

// held is how a held service's hold reads (SPEC.md §6.9), or nil: the
// server it's held on (On) and, after a move that couldn't go back, the
// one it came from (From); when the backup was made (TakenText).
func (lab *Lab) held(svc Service) *heldText {
	if svc.Held == nil {
		return nil
	}
	v := &heldText{Hold: *svc.Held, On: svc.Server, TakenText: svc.Held.Taken.Local().Format("Mon 2 Jan 15:04")}
	if svc.Held.MovedFrom != "" {
		v.From = svc.Held.MovedFrom
		if n := lab.Manager.NodeByID(svc.Held.MovedFrom); n != nil {
			v.From = n.Identity().Name
		}
	}
	return v
}

type heldText struct {
	instance.Hold
	On, From, TakenText string
}
