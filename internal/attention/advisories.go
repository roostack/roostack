package attention

import (
	"context"
	"strings"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/registry"
)

// advisoryItems are what Home says about the service registry's security
// advisories affecting the lab's services (SPEC.md §6.16): each service's
// newest, with what Roostack did and what to do. Roostack pausing a
// service, or one left public, is waiting for someone (an update), so it's
// emailed if it lasts a day; the Owners were emailed at once anyway
// (Security). Low ones, and those that may affect a package of the Owner's
// own, are information.
func (lab *Lab) advisoryItems(ctx context.Context, services []Service, l *List) {
	all := lab.Manager.Advisories(ctx)
	for _, svc := range services {
		as := all[svc.Name]
		if len(as) == 0 {
			continue
		}
		a := as[0]
		for _, x := range as {
			if x.Paused {
				a = x // the one it's paused for comes first
				break
			}
		}
		l.add(advisoryItem(svc.Name, svc.Title, a))
	}
}

// advisoryItem is one service's advisory as an item.
func advisoryItem(name, title string, a *manager.ServiceAdvisory) Item {
	it := Item{Key: "advisory:" + name + ":" + a.ID, Link: "/services/" + name + "#advisory", LinkText: "What to do",
		Level: Warning, Group: GroupWaiting}
	what := strings.TrimRight(a.Title, ".") + "."
	fix := "There's no fixed version yet."
	if a.Fixed != "" {
		fix = "Updating it to " + manager.AppVersion(a.Fixed) + " will fix it."
	}
	switch {
	case a.Paused:
		used := ""
		if a.Exploited {
			used = " that's being used in attacks"
		}
		it.Text = "Roostack has paused public access to " + title + " because of a security problem" + used + ": " + what +
			" " + title + " is still working at home. " + fix
		if a.Fixed != "" {
			it.Text = strings.TrimSuffix(it.Text, ".") + ", and " + title + " will be public again once the update has worked."
		} else {
			it.Text += " Roostack will keep it paused until there is."
		}
	case a.KeptPublic:
		it.Text = title + " is public although the service registry warns of a security problem in it: " + what + " " + fix
	case a.Possibly:
		it.Level, it.Group = Info, ""
		it.Text = "The service registry warns of a security problem in " + a.BuiltWith + ", which " + title +
			" is built with, so it may be affected: " + what + " It's one of your own packages, so check with whoever wrote it."
	case a.Severity == registry.SeverityLow:
		it.Level, it.Group = Info, ""
		it.Text = "The service registry has a security notice about " + title + ": " + what + " " + fix
	default:
		it.Text = "The service registry warns of a security problem in " + title + ": " + what + " " + fix
	}
	return it
}
