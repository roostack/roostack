package attention

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/vitals"
	"roostack.cloud/roostack/internal/words"
	"roostack.cloud/roostack/units"
)

// Vitals is how each server is doing (internal/vitals).
type Vitals interface {
	Problems() []vitals.Problem
}

// vitals adds what's wrong with how a server is doing (SPEC.md §6.15.1),
// each a problem (the maintainer, 2026-11-12): memory running out, heavy
// swapping, a sensor too hot.
func (lab *Lab) vitals(ctx context.Context, l *List) {
	if lab.Vitals == nil {
		return
	}
	titles := map[string]string{}
	if lab.Services != nil {
		services, _ := lab.Services(ctx)
		for _, s := range services {
			titles[s.Name] = s.Title
		}
	}
	problems := lab.Vitals.Problems()
	slices.SortFunc(problems, func(a, b vitals.Problem) int { return strings.Compare(a.Name+a.Kind, b.Name+b.Kind) })
	for _, p := range problems {
		l.add(Item{Level: Problem, Key: "vitals:" + p.Server + ":" + p.Kind, Text: VitalsText(p, titles, time.Now()),
			Link: "/monitoring/servers/" + p.Server, LinkText: "See what's using it"})
	}
}

// VitalsText says what's wrong with a server, and what's using the most
// (by its title, from titles).
func VitalsText(p vitals.Problem, titles map[string]string, now time.Time) string {
	v := p.Reading
	for_ := words.RoughDuration(now.Sub(p.Since))
	switch p.Kind {
	case vitals.LowMemory:
		t := fmt.Sprintf("%s is low on memory: it has had less than 10%% of its memory free for %s, so its services may slow down, "+
			"or the system may stop one to free some.", p.Name, for_)
		if len(v.Services) > 0 && v.Services[0].Service != "" {
			s := v.Services[0].Service
			t += fmt.Sprintf(" %s is using the most (%s).", cmp.Or(titles[s], s), units.Bytes(v.Services[0].Memory))
		}
		return t
	case vitals.Swapping:
		return fmt.Sprintf("%s is swapping heavily: it has been reading memory back from its disk for %s, which slows its services down. "+
			"It needs more memory, or fewer services.", p.Name, for_)
	case vitals.Hot:
		name := v.TempName
		if name == "" {
			name = "a sensor"
		}
		return fmt.Sprintf("%s is too hot: %s is at %.0f °C, at or above its high mark of %.0f °C, for %s. "+
			"Check that its fans and vents are clear.", p.Name, name, v.Temp, v.TempHigh, for_)
	}
	return p.Name + " needs attention."
}
