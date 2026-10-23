package attention

import (
	"context"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/words"
)

// What's paused (SPEC.md §6.16) is information on Home: the lab's pause
// of everything, each pause by what services are built with with the
// services it covers, and each service's own, saying since when and by
// whom; only Owners, who can lift them, get the link.
func TestPauseItems(t *testing.T) {
	at := time.Now().Add(-time.Hour)
	pp := manager.PublicPauses{
		All:       &config.Pause{Since: at, By: "tom"},
		BuiltWith: map[string]config.Pause{"php": {Since: at, By: "sam"}, "log4j": {Since: at}},
		Services: []manager.PublicService{
			{Name: "lychee", Title: "Lychee", ByAll: true, ByBuiltWith: []string{"php"}, Own: &config.Pause{Since: at, By: "kim"}},
			{Name: "ghost", Title: "Ghost", ByAll: true},
		},
	}
	items := pauseItems(pp, true)
	want := []string{
		"Public access to everything is paused, since " + words.When(at) + " by tom. Visitors from the internet are seeing a page",
		"Public access to services built with log4j is paused, since " + words.When(at) + " from the server's command line. None of the public services is built with it now.",
		"Public access to services built with php is paused, since " + words.When(at) + " by sam. Lychee is paused by it.",
		"Public access to Lychee is paused, since " + words.When(at) + " by kim.",
	}
	if len(items) != len(want) {
		t.Fatalf("items: %+v", items)
	}
	for i, w := range want {
		if it := items[i]; !strings.HasPrefix(it.Text, w) || it.Level != Info || it.LinkText != "Open it again…" {
			t.Errorf("item %d: %+v, want %q", i, it, w)
		}
	}
	if items[3].Link != "/services/lychee#paused" || items[0].Link != "/settings/public#paused" {
		t.Errorf("links: %q, %q", items[3].Link, items[0].Link)
	}
	for _, it := range pauseItems(pp, false) {
		if it.Link != "" {
			t.Errorf("an Admin can't lift it: %+v", it)
		}
	}
	if s := pauseSummary(pp); !strings.HasPrefix(s, "\nPublic access paused:\n- Public access to everything") || strings.Count(s, "\n- ") != 4 {
		t.Errorf("the weekly summary's lines:\n%s", s)
	}
	if pauseSummary(manager.PublicPauses{}) != "" || len(pauseItems(manager.PublicPauses{}, true)) != 0 {
		t.Error("nothing paused says nothing")
	}
}

// What Home says about each service's newest advisory, the one it's
// paused for first (SPEC.md §6.16): what Roostack did and what to do.
func TestAdvisoryItems(t *testing.T) {
	adv := func(sev string, exploited bool) *registry.Advisory {
		return &registry.Advisory{ID: "2026-0007", Title: "PHP lets anyone run code.", Severity: sev, Exploited: exploited, BuiltWith: "php"}
	}
	for _, c := range []struct {
		a     *manager.ServiceAdvisory
		level string
		text  string
	}{
		{&manager.ServiceAdvisory{Advisory: adv("critical", true), Fixed: "7.11.2-roostack.1", Paused: true}, Warning,
			"Roostack has paused public access to Lychee because of a security problem that's being used in attacks: PHP lets anyone run code. " +
				"Lychee is still working at home. Updating it to 7.11.2 will fix it, and Lychee will be public again once the update has worked."},
		{&manager.ServiceAdvisory{Advisory: adv("critical", false), Paused: true}, Warning,
			"Roostack has paused public access to Lychee because of a security problem: PHP lets anyone run code. Lychee is still working at home. " +
				"There's no fixed version yet. Roostack will keep it paused until there is."},
		{&manager.ServiceAdvisory{Advisory: adv("high", false), Fixed: "7.11.2-roostack.1"}, Warning,
			"The service registry warns of a security problem in Lychee: PHP lets anyone run code. Updating it to 7.11.2 will fix it."},
		{&manager.ServiceAdvisory{Advisory: adv("critical", true), KeptPublic: true}, Warning,
			"Lychee is public although the service registry warns of a security problem in it: PHP lets anyone run code. There's no fixed version yet."},
		{&manager.ServiceAdvisory{Advisory: adv("critical", true), Possibly: true}, Info,
			"The service registry warns of a security problem in php, which Lychee is built with, so it may be affected"},
		{&manager.ServiceAdvisory{Advisory: adv("low", false)}, Info, "The service registry has a security notice about Lychee"},
	} {
		it := advisoryItem("lychee", "Lychee", c.a)
		if it.Level != c.level || !strings.HasPrefix(it.Text, c.text) || it.Link != "/services/lychee#advisory" {
			t.Errorf("%+v:\n%+v", c.a, it)
		}
	}
}

// Closing public access completely (SPEC.md §6.16) is information on
// Home, like a pause, saying it will open again by itself when it was
// closed for an advisory about Roostack. Such an advisory is a problem
// asking the Owner to close, until it's closed.
func TestClosedItems(t *testing.T) {
	at := time.Now().Add(-time.Hour)
	c := &config.Closed{Pause: config.Pause{Since: at, By: "tom"}, Advisory: "2026-0009"}
	items := pauseItems(manager.PublicPauses{Closed: c}, true)
	if len(items) != 1 || items[0].Level != Info || items[0].Link != "/settings/public#paused" ||
		items[0].Text != "Public access is closed completely, since "+words.When(at)+" by tom. Visitors from the internet can't connect at all, "+
			"and everything is working at home as usual. Roostack will open it again by itself once it's updated past security advisory 2026-0009." {
		t.Errorf("closed: %+v", items)
	}

	fm := &fakeManager{roostack: []*manager.ServiceAdvisory{{Advisory: &registry.Advisory{ID: "2026-0009",
		Title: "Caddy lets anyone read any file.", Severity: registry.SeverityCritical}, Version: "0.4.1", Fixed: "0.4.2"}}}
	lab := &Lab{Manager: fm}
	var l List
	lab.roostackItems(context.Background(), nil, &l)
	if len(l) != 1 || l[0].Level != Problem || l[0].Link != "/pause/close?advisory=2026-0009" ||
		l[0].Text != "The service registry warns of a security problem in Roostack 0.4.1 itself: Caddy lets anyone read any file (2026-0009). "+
			"Pausing public access can't keep visitors from it, so close public access completely until Roostack is updated. Roostack 0.4.2 will fix it." {
		t.Errorf("Roostack's advisory: %+v", l)
	}
	l = List{}
	lab.roostackItems(context.Background(), c, &l)
	if len(l) != 0 {
		t.Errorf("asked again while closed: %+v", l)
	}
}
