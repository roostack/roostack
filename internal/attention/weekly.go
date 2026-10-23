package attention

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/words"
	"roostack.cloud/roostack/units"
)

// weeklySummary is the Monday summary, once a week from 8 am (empty when
// it isn't due).
func (n *Notifier) weeklySummary(ctx context.Context, lab *Lab, u *store.User, now time.Time) string {
	local := now.Local()
	if local.Weekday() != time.Monday || local.Hour() < 8 {
		return ""
	}
	if v, _ := n.Store.Setting(ctx, weeklyKey+strconv.FormatInt(u.ID, 10)); v != "" {
		if at, err := strconv.ParseInt(v, 10, 64); err == nil && now.Sub(time.Unix(at, 0)) < 6*24*time.Hour {
			return ""
		}
	}
	var b strings.Builder
	for _, mn := range lab.servers() {
		fmt.Fprintf(&b, "%s:\n", mn.view.Label())
		if st, err := mn.node.Backups().Status(ctx); err == nil && st.Configured {
			if st.LastOK != nil {
				fmt.Fprintf(&b, "- Last backup: %s\n", words.When(st.LastOK.Finished))
			} else {
				b.WriteString("- No backup has finished yet\n")
			}
		} else if err == nil {
			b.WriteString("- Backups aren't set up\n")
		}
		if d := ServerDisks(ctx, lab.Guests, mn.view, mn.node.Disks()); d != nil {
			for _, v := range d {
				fmt.Fprintf(&b, "- %s: %s free of %s\n", v.Title, units.Bytes(v.Free), units.Bytes(v.Total))
			}
		}
		// A new release series, which only the Owner can accept (SPEC.md
		// §7.2); the server is named by the heading already.
		if up := mn.node.Updates(); up != nil && auth.Can(u.Role, auth.ManageHost) {
			if ser, err := up.Series(ctx); err == nil {
				if text := seriesText(ser, Server{This: true}); text != "" {
					fmt.Fprintf(&b, "- %s%s\n", text, lab.linkLine(mn.view.updates()+"#roostack"))
				}
			}
		}
	}
	// What CrowdSec saw on the public addresses (only once a service is
	// public: before that, it doesn't run).
	if lab.Attacks != nil && lab.Protected != nil && lab.Protected(ctx) {
		// The requests AppSec refused (SPEC.md §6.10 item 4), by service.
		var refused string
		if blocks, ok := lab.blocks(ctx); ok {
			refused = wafSummary(blocks, lab.titlesByHost(ctx))
		}
		if a, err := lab.Attacks(ctx, 7*24*time.Hour); err == nil {
			b.WriteString("\nPublic addresses: ")
			switch {
			case a.Alerts == 0 && refused == "":
				b.WriteString("no attacks seen.\n")
			case a.Alerts == 0:
				b.WriteString("CrowdSec " + refused + "\n")
			default:
				fmt.Fprintf(&b, "CrowdSec saw %d %s from %d %s and blocked %d.",
					a.Alerts, words.Plural(a.Alerts, "attack", "attacks"), a.Addresses, words.Plural(a.Addresses, "address", "addresses"), a.Blocked)
				if len(a.Kinds) > 0 {
					b.WriteString(" Most were: " + strings.Join(a.Kinds, ", ") + ".")
				}
				if refused != "" {
					b.WriteString(" It also " + refused)
				}
				b.WriteString("\n")
			}
		}
	}
	// What's paused, and since when (SPEC.md §6.16): it lasts until
	// someone lifts it, so the summary says so each week.
	if auth.Can(u.Role, auth.ManageServices) {
		if pp, err := lab.Manager.PublicPauses(); err == nil {
			b.WriteString(pauseSummary(pp))
		}
	}
	var open []string
	for _, it := range lab.For(ctx, u) {
		if it.Level == Problem || it.Level == Warning {
			open = append(open, "- "+it.Text)
		}
	}
	if len(open) == 0 {
		b.WriteString("\nNothing needs attention.\n")
	} else {
		b.WriteString("\nStill needing attention:\n" + strings.Join(open, "\n") + "\n")
	}
	return b.String()
}

// titlesByHost names the service at each of the lab's web addresses.
func (lab *Lab) titlesByHost(ctx context.Context) map[string]string {
	titles := map[string]string{}
	services, err := lab.services(ctx)
	if err != nil {
		return titles
	}
	for _, sv := range services {
		for _, u := range sv.URLs {
			if pu, err := url.Parse(u); err == nil {
				titles[strings.ToLower(pu.Hostname())] = sv.Title
			}
		}
	}
	return titles
}

// wafSummary says how many requests AppSec refused, by service, for the
// weekly summary ("refused 7 requests that tried a known exploit
// (Vaultwarden 5, Immich 2)."; "" when none were): titles names each
// public address's service.
func wafSummary(blocks []public.Block, titles map[string]string) string {
	if len(blocks) == 0 {
		return ""
	}
	counts := map[string]int{}
	var order []string
	for _, b := range blocks {
		t := titles[b.Host]
		if t == "" {
			t = b.Host
		}
		if counts[t] == 0 {
			order = append(order, t)
		}
		counts[t]++
	}
	slices.SortStableFunc(order, func(a, b string) int { return counts[b] - counts[a] })
	var parts []string
	for _, t := range order {
		parts = append(parts, fmt.Sprintf("%s %d", t, counts[t]))
	}
	return fmt.Sprintf("refused %d %s that tried a known exploit (%s).",
		len(blocks), words.Plural(len(blocks), "request", "requests"), strings.Join(parts, ", "))
}

// blocks are the requests AppSec refused this week (false: CrowdSec
// didn't answer, or doesn't run).
func (lab *Lab) blocks(ctx context.Context) ([]public.Block, bool) {
	if lab.Blocks == nil {
		return nil, false
	}
	return lab.Blocks(ctx)
}
