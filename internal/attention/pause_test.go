package attention

import (
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/manager"
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
