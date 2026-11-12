package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/monitor"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/vitals"
)

// How each server is doing (SPEC.md §6.15.1): the Servers section on
// Monitoring, and a server's page with its figures, charts with both
// axes, and each service's share by its title.
func TestServerVitalsPages(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	h.server.Monitor = &monitor.Monitor{Checks: func(context.Context) []monitor.Check { return nil }, Store: h.store}
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "immich", Title: "Immich"}}, nil
	}
	now := time.Now().Truncate(time.Minute)
	at := now.Add(-20 * time.Minute)
	v := node.Vitals{CPUs: 4, CPUBusy: 34, Load: [3]float64{1.2, 1, 0.8}, Model: "Intel N100", MemTotal: 16_000_000_000, MemAvailable: 1_000_000_000,
		SwapTotal: 2_000_000_000, SwapFree: 1_000_000_000, NetIn: 2 << 20, NetOut: 1 << 19, Temp: 52, TempHigh: 100, TempName: "coretemp Package id 0",
		Up: 12 * 24 * time.Hour, Services: []node.ServiceUse{{Service: "immich", Memory: 2_000_000_000, CPU: 40}, {Service: "", Memory: 300_000_000, CPU: 4}}}
	c := &vitals.Collector{Store: h.store, Now: func() time.Time { return at },
		Servers: func(context.Context) []vitals.Server {
			return []vitals.Server{{ID: "n-1", Name: "nuc", Read: func(context.Context) (node.Vitals, error) { v.At = at; return v, nil }}}
		}}
	for ; !at.After(now); at = at.Add(time.Minute) {
		c.Round(context.Background())
	}
	h.server.Vitals = c

	_, body := h.get("/monitoring")
	for _, want := range []string{`id="servers"`, "nuc<small>4 processors · 16.0 GB of memory</small>", "15.0 of 16.0 GB", "52 °C",
		"Low on memory", `href="/monitoring/servers/n-1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Monitoring missing %q", want)
		}
	}
	_, body = h.get("/monitoring/servers/n-1")
	for _, want := range []string{"<h1>nuc</h1>", "Intel N100 · 4 processors", "up for 288 hours", "34%", "15.0 GB", "nuc is low on memory",
		`<svg class="chart"`, `class="chart-y"`, "<span>16.0 GB</span>", "<span>Now</span>", `aria-current="true">Day</a>`,
		`<a href="/services/immich">Immich</a>`, "Roostack and Caddy", "of all 4", "10.0%", "2.0 GB · 13%"} {
		if !strings.Contains(body, want) {
			t.Errorf("the server's page missing %q", want)
		}
	}
	if _, body := h.get("/monitoring/servers/n-1?range=month"); !strings.Contains(body, `aria-current="true">Month</a>`) {
		t.Error("the month")
	}
	if code, _ := h.get("/monitoring/servers/n-9"); code != http.StatusNotFound {
		t.Errorf("an unknown server: %d", code)
	}
}
