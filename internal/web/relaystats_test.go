package web

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/manager"
)

// statsCloud is a linked Cloud whose relay statistics are given.
type statsCloud struct {
	CloudLink
	days, hours cloudapi.RelayStats
	err         error
	asked       []string
}

func (c *statsCloud) RelayStats(_ context.Context, days int, byDay bool) (cloudapi.RelayStats, error) {
	c.asked = append(c.asked, map[bool]string{true: "day", false: "hour"}[byDay])
	if c.err != nil {
		return cloudapi.RelayStats{}, c.err
	}
	if byDay {
		return c.days, nil
	}
	return c.hours, nil
}

// TestRelayStats: Owners see the card on Public access and Admins on its
// own page (linked from Services); it adds up the days, lists countries
// by visitors, and says when Cloud can't be reached. Members don't see it.
func TestRelayStats(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.relay = true
	today := time.Now().Format(time.DateOnly)
	yesterday := time.Now().AddDate(0, 0, -1).Format(time.DateOnly)
	asOf := time.Now().Add(-10 * time.Minute)
	c := &statsCloud{CloudLink: newCloud(t), days: cloudapi.RelayStats{AsOf: asOf, Periods: []cloudapi.RelayPeriod{
		{Day: yesterday, Visitors: 40, Connections: 300, Countries: map[string]int64{"AU": 35, "NZ": 3}, BytesOut: 2_000_000_000, BytesIn: 100_000_000},
		{Day: today, Visitors: 12, Connections: 90, Countries: map[string]int64{"AU": 10, "GB": 1}, Refused: map[string]int64{"banned": 2, "country": 5}},
	}}}
	h.server.Cloud = c
	fm.access = []manager.RouteAccess{{Host: "vault.smiths.roost.test", Public: true}}
	h.server.DNS = &fakeDNS{st: dnsrecords.Status{Public: []dnsrecords.PublicRecord{{Host: "vault.smiths.roost.test", IP: "198.51.100.10", State: dnsrecords.PublicOK}}}}
	if _, body := h.get("/settings/public"); !strings.Contains(body, `hx-get="/partials/relay-stats?period="`) ||
		!strings.Contains(body, "Points at Roostack Cloud's relay") {
		t.Fatal("no visitors card on Public access, or the records don't say the relay")
	}
	_, body := h.get("/partials/relay-stats?period=week")
	for _, want := range []string{"<dd>52</dd>", "<dd>390</dd>", "2.1 GB", "2.0 GB to visitors", "<dd>7</dd>", "Visitors each day",
		`<span class="rank-name">Australia</span>`, `<span class="rank-count">45</span>`, `<span class="rank-name">Elsewhere</span>`,
		`title="` + time.Now().Format("Mon 2 Jan") + `: 12 visitors"`, `aria-current="true">7 days</a>`, "As of " + whenText(asOf)} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	// Today: the hours too.
	if _, body := h.get("/partials/relay-stats?period=today"); !strings.Contains(body, "Visitors each hour") || len(c.asked) < 2 || c.asked[len(c.asked)-1] != "hour" {
		t.Errorf("today: %v\n%s", c.asked, body)
	}
	if _, body := h.get("/partials/relay-stats?period=year"); !strings.Contains(body, "Visitors each month") || !strings.Contains(body, time.Now().Format("January 2006")+": 52 visitors") {
		t.Errorf("a year:\n%s", body)
	}
	c.err = errors.New("Cloud is down")
	if _, body := h.get("/partials/relay-stats"); !strings.Contains(body, "Cloud is down") {
		t.Errorf("Cloud out of reach:\n%s", body)
	}
	c.err = nil

	// Admins: their own page, linked from Services.
	ha, fa := loggedIn(t, auth.RoleAdmin)
	fa.relay = true
	ha.server.Cloud = c
	if _, body := ha.get("/services"); !strings.Contains(body, `href="/visitors"`) {
		t.Error("no link to the visitors page for an Admin")
	}
	if code, body := ha.get("/visitors"); code != 200 || !strings.Contains(body, `hx-get="/partials/relay-stats?period="`) {
		t.Errorf("visitors page: %d", code)
	}
	if code, _ := ha.get("/partials/relay-stats"); code != 200 {
		t.Errorf("an Admin's card: %d", code)
	}
	hm, fmm := loggedIn(t, auth.RoleMember)
	fmm.relay = true
	hm.server.Cloud = c
	if code, _ := hm.get("/partials/relay-stats"); code == 200 {
		t.Error("a Member sees the visitors")
	}
}
