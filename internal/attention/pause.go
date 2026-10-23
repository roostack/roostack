package attention

import (
	"maps"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/words"
)

// pauseItems are what's paused (SPEC.md §6.16): the lab's pause of
// everything, each of its pauses by what services are built with, and
// each service's own. They're information, not problems: the person who
// paused it chose to, and the Owners heard at once by email (Security);
// Home keeps them in view and the weekly summary repeats them, since a
// pause lasts until someone lifts it. Only an Owner can lift one, so only
// Owners get the link to.
func pauseItems(pp manager.PublicPauses, owner bool) []Item {
	var out []Item
	link := func(it Item, to string) Item {
		if owner {
			it.Link, it.LinkText = to, "Open it again…"
		}
		return it
	}
	if p := pp.All; p != nil {
		out = append(out, link(Item{Key: "paused:all", Level: Info,
			Text: "Public access to everything is paused, since " + since(*p) + ". Visitors from the internet are seeing a page " +
				"saying it's closed for a while, and everything is working at home as usual."}, "/settings/public#paused"))
	}
	for _, b := range slices.Sorted(maps.Keys(pp.BuiltWith)) {
		var titles []string
		for _, s := range pp.Services {
			if slices.Contains(s.ByBuiltWith, b) {
				titles = append(titles, s.Title)
			}
		}
		covers := "none of the public services is built with it now"
		if len(titles) > 0 {
			covers = words.And(titles) + " " + words.Plural(len(titles), "is", "are") + " paused by it"
		}
		out = append(out, link(Item{Key: "paused:built-with:" + b, Level: Info,
			Text: "Public access to services built with " + b + " is paused, since " + since(pp.BuiltWith[b]) + ". " +
				words.Capitalise(covers) + "."}, "/settings/public#paused"))
	}
	for _, s := range pp.Services {
		if s.Own == nil {
			continue
		}
		out = append(out, link(Item{Key: "paused:service:" + s.Name, Level: Info,
			Text: "Public access to " + s.Title + " is paused, since " + since(*s.Own) + ". Visitors from the internet are seeing a page " +
				"saying it's closed for a while, and it's working at home as usual."}, "/services/"+s.Name+"#paused"))
	}
	return out
}

// since is when a pause began and who placed it: "today 09:14 by tom".
func since(p config.Pause) string {
	by := " by " + p.By
	if p.By == "" {
		by = " from the server's command line"
	}
	return words.When(p.Since) + by
}

// pauseSummary is the weekly summary's lines about what's paused, empty
// when nothing is.
func pauseSummary(pp manager.PublicPauses) string {
	items := pauseItems(pp, false)
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nPublic access paused:\n")
	for _, it := range items {
		b.WriteString("- " + it.Text + "\n")
	}
	return b.String()
}
