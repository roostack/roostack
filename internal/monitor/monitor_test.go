package monitor

import (
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// scripted is a monitor whose one check answers as told, a minute apart.
type scripted struct {
	m      *Monitor
	now    time.Time
	up     bool
	paused string
}

func newScripted(t *testing.T, s *store.Store) *scripted {
	sc := &scripted{now: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC), up: true}
	sc.m = &Monitor{Store: s, Now: func() time.Time { return sc.now },
		Checks: func(context.Context) []Check {
			return []Check{{Key: "service:vault:web", Kind: KindService, Name: "Vaultwarden", Service: "vault", URL: "https://vault.test/", Paused: sc.paused, Alert: true}}
		},
		Prober: func(_ context.Context, c Check) store.MonitorResult {
			if sc.up {
				return store.MonitorResult{Up: true, Status: 200, Millis: 120}
			}
			return store.MonitorResult{Err: "no answer within 10 seconds"}
		}}
	return sc
}

func (sc *scripted) round(t *testing.T) Status {
	t.Helper()
	sc.m.Round(context.Background())
	sc.now = sc.now.Add(time.Minute)
	st, ok := sc.m.Status("service:vault:web")
	if !ok {
		t.Fatal("no status")
	}
	return st
}

// A check is down after three failures in a row, not before; paused
// results count neither way; the outage is recorded from its first
// failure, and ends at the first success.
func TestDownAfterThree(t *testing.T) {
	s := openStore(t)
	sc := newScripted(t, s)
	if st := sc.round(t); st.State != StateUp || st.Last.Millis != 120 {
		t.Fatalf("first: %+v", st)
	}
	sc.up = false
	failedAt := sc.now
	for i := 1; i < DownAfter; i++ {
		if st := sc.round(t); st.State != StateUp || st.Fails != i {
			t.Fatalf("after %d failures: %s, %d", i, st.State, st.Fails)
		}
	}
	sc.paused = "it's updating"
	if st := sc.round(t); st.State != StatePaused || st.Fails != DownAfter-1 {
		t.Fatalf("paused: %s, %d", st.State, st.Fails)
	}
	sc.paused = ""
	st := sc.round(t)
	if st.State != StateDown || !st.DownSince.Equal(failedAt) || st.Last.Err != "no answer within 10 seconds" {
		t.Fatalf("down: %+v", st)
	}
	o, _ := s.MonitorOutages(context.Background(), "service:vault:web", time.Time{})
	if len(o) != 1 || !o[0].Started.Equal(failedAt) || !o[0].Ended.IsZero() {
		t.Fatalf("outage %+v", o)
	}

	// Roostack restarts: the new monitor remembers it's down.
	sc2 := newScripted(t, s)
	sc2.now, sc2.up = sc.now, false
	if st := sc2.round(t); st.State != StateDown || st.Fails != DownAfter+1 {
		t.Fatalf("after a restart: %s, %d", st.State, st.Fails)
	}
	sc2.up = true
	if st := sc2.round(t); st.State != StateUp || st.Fails != 0 {
		t.Fatalf("back up: %+v", st)
	}
	o, _ = s.MonitorOutages(context.Background(), "service:vault:web", time.Time{})
	if len(o) != 1 || o[0].Ended.IsZero() {
		t.Errorf("outage after recovery %+v", o)
	}
}

// Monitoring switched off checks nothing and lists nothing.
func TestOff(t *testing.T) {
	sc := newScripted(t, openStore(t))
	sc.round(t)
	sc.m.Enabled = func(context.Context) bool { return false }
	sc.m.Round(context.Background())
	if len(sc.m.Statuses()) != 0 {
		t.Errorf("off, it lists %+v", sc.m.Statuses())
	}
}

// A certificate with less than a fifth of its life left warns.
func TestCertWarning(t *testing.T) {
	sc := newScripted(t, openStore(t))
	issued := sc.now.AddDate(0, 0, -70)
	expires := sc.now.AddDate(0, 0, 20)
	sc.m.Prober = func(context.Context, Check) store.MonitorResult {
		return store.MonitorResult{Up: true, Status: 200, CertIssued: issued, CertExpires: expires}
	}
	if st := sc.round(t); st.CertWarning || st.CertDaysLeft(sc.now) != 19 {
		t.Errorf("20 days of 90 left: warning %v, %d days", st.CertWarning, st.CertDaysLeft(sc.now))
	}
	expires = sc.now.AddDate(0, 0, 15)
	if st := sc.round(t); !st.CertWarning {
		t.Errorf("15 days of 85 left: no warning")
	}
}

// Days on the bar: up all day, down a few minutes, down for hours, not
// checked; uptime shares and answer times from the summaries.
func TestHistory(t *testing.T) {
	s, ctx := openStore(t), context.Background()
	now := time.Date(2026, 10, 19, 12, 30, 0, 0, time.UTC)
	m := &Monitor{Store: s, Now: func() time.Time { return now }}
	add := func(at time.Time, up bool, ms int) {
		s.AddMonitorResult(ctx, store.MonitorResult{Key: "k", At: at, Up: up, Millis: ms})
	}
	today := midnight(now)
	for i := 0; i < 60; i++ { // today: 60 checks up
		add(today.Add(time.Duration(i)*time.Minute), true, 100+i)
	}
	yesterday := today.AddDate(0, 0, -1)
	for i := 0; i < 60; i++ { // yesterday: 5 minutes down
		add(yesterday.Add(time.Duration(i)*time.Minute), i >= 5, 100)
	}
	before := today.AddDate(0, 0, -2)
	for i := 0; i < 120; i++ { // the day before: down 90 minutes
		add(before.Add(time.Duration(i)*time.Minute), i >= 90, 100)
	}
	h, err := m.History(ctx, "k", 30, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Days) != 30 || h.Days[29].State != DayOK || h.Days[28].State != DayWarn || h.Days[27].State != DayBad ||
		h.Days[26].State != DayNone || !h.Days[29].Date.Equal(today) {
		t.Fatalf("days %+v", h.Days[26:])
	}
	if h.Days[28].DownMinutes() != 5 {
		t.Errorf("yesterday down %d minutes", h.Days[28].DownMinutes())
	}
	if h.Day1 != 1 {
		t.Errorf("24 hours (today's checks only): %v", h.Day1)
	}
	if want := Uptime(60.0+55+30) / 240; h.Month < want-0.001 || h.Month > want+0.001 {
		t.Errorf("30 days: %v, want %v", h.Month, want)
	}
	if h.MaxMillis != 159 || h.AvgMillis < 100 || len(h.Recent) == 0 {
		t.Errorf("times: avg %d, max %d, %d recent", h.AvgMillis, h.MaxMillis, len(h.Recent))
	}
}

// A web check: up below 500 or on a listed status; a redirect isn't
// followed; a certificate for another name fails unless skipped, the
// answer still recorded.
func TestProbeHTTP(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/alive":
			w.WriteHeader(http.StatusOK)
		case "/login":
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		case "/locked":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	Roots = x509.NewCertPool()
	Roots.AddCert(srv.Certificate())
	defer func() { Roots = nil }()
	addr := srv.Listener.Addr().String()
	check := func(url string, c Check) store.MonitorResult {
		c.URL, c.Dial = url, addr
		return Probe(context.Background(), c)
	}
	if r := check("https://example.com/alive", Check{}); !r.Up || r.Status != 200 || r.Millis == 0 || r.CertExpires.IsZero() {
		t.Errorf("alive: %+v", r)
	}
	if r := check("https://example.com/login", Check{}); !r.Up || r.Status != 302 {
		t.Errorf("a login redirect is up: %+v", r)
	}
	if r := check("https://example.com/locked", Check{Statuses: []int{200}}); r.Up || r.Err != "it answered 401 Unauthorized" {
		t.Errorf("401 when only 200 counts: %+v", r)
	}
	if r := check("https://example.com/nope", Check{}); r.Up || r.Err != "it answered 502 Bad Gateway" {
		t.Errorf("502: %+v", r)
	}
	if r := check("https://vault.test/alive", Check{}); r.Up || r.Status != 200 || r.Err != "its certificate isn't valid: it's for another name" {
		t.Errorf("another name: %+v", r)
	}
	if r := check("https://vault.test/alive", Check{SkipVerify: true}); !r.Up {
		t.Errorf("skipping the certificate check: %+v", r)
	}
}

// TCP: up when something listens; a closed port says so.
func TestProbeTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if r := Probe(context.Background(), Check{TCP: addr}); !r.Up {
		t.Errorf("listening: %+v", r)
	}
	ln.Close()
	if r := Probe(context.Background(), Check{TCP: addr}); r.Up || r.Err != "nothing is listening there" {
		t.Errorf("closed: %+v", r)
	}
}

// A ping to this machine (where ping sockets are allowed).
func TestProbePing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ping reads replies as Linux's ping sockets give them: macOS's put the IP header first")
	}
	r := Probe(context.Background(), Check{Ping: "127.0.0.1"})
	if strings.Contains(r.Err, "isn't allowed") {
		t.Skip("ping sockets aren't allowed here:", r.Err)
	}
	if !r.Up {
		t.Errorf("ping 127.0.0.1: %+v", r)
	}
	if c := checksum([]byte{8, 0, 0, 0, 0, 0, 0, 1}); c != 0xf7fe {
		t.Errorf("checksum %#x", c)
	}
}

// Uptime reads rounded down, so a little down time never shows as 100%.
func TestUptimeText(t *testing.T) {
	for u, want := range map[Uptime]string{1: "100%", 0.99999: "99.99%", 0.9997: "99.97%", 0.5: "50.00%", 0: "0.00%", -1: ""} {
		if got := u.Text(); got != want {
			t.Errorf("%v: %q, want %q", float64(u), got, want)
		}
	}
}

// Own checks' addresses: web, host and port, or a host to ping.
func TestParseAddress(t *testing.T) {
	for addr, want := range map[string]Check{
		"https://nas.local:5001":     {URL: "https://nas.local:5001/"},
		" http://192.168.1.1/status": {URL: "http://192.168.1.1/status"},
		"printer.local:631":          {TCP: "printer.local:631"},
		"[fd00::1]:22":               {TCP: "[fd00::1]:22"},
		"192.168.1.1":                {Ping: "192.168.1.1"},
		"router.lan":                 {Ping: "router.lan"},
		"fd00::1":                    {Ping: "fd00::1"},
	} {
		got, err := ParseAddress(addr)
		if err != nil || got.URL != want.URL || got.TCP != want.TCP || got.Ping != want.Ping {
			t.Errorf("%q: %+v, %v", addr, got, err)
		}
	}
	for _, bad := range []string{"", "ftp://nas.local", "https://", "https://user:pw@nas.local", "nas.local:99999", "nas local", "https://nas.local:0", "-x.lan"} {
		if c, err := ParseAddress(bad); err == nil {
			t.Errorf("%q was accepted: %+v", bad, c)
		}
	}
}
