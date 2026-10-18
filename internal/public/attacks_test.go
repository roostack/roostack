package public

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"
)

// cscli's alerts are summed: attacks, from how many addresses, how many
// blocked, and the commonest kinds.
func TestParseAlerts(t *testing.T) {
	a, err := parseAlerts([]byte(`[
		{"scenario":"crowdsecurity/http-probing","source":{"ip":"203.0.113.5"},"decisions":[{"type":"ban"}]},
		{"scenario":"crowdsecurity/http-probing","source":{"ip":"203.0.113.6"},"decisions":[{"type":"ban"}]},
		{"scenario":"crowdsecurity/http-bad-user-agent","source":{"ip":"203.0.113.5"},"decisions":[]}]`))
	if err != nil || a.Alerts != 3 || a.Addresses != 2 || a.Blocked != 2 || len(a.Kinds) != 2 || a.Kinds[0] != "http-probing" {
		t.Errorf("%+v %v", a, err)
	}
	if a, err := parseAlerts([]byte(`[]`)); err != nil || a.Alerts != 0 {
		t.Errorf("none: %+v %v", a, err)
	}
	if _, err := parseAlerts([]byte(`nonsense`)); err == nil {
		t.Error("bad output")
	}
}

// TestParseBans: the lab's own bans (CrowdSec's and by hand), newest
// first, without the community's, simulated ones or other kinds.
func TestParseBans(t *testing.T) {
	out := `[
 {"created_at": "2026-10-11T11:31:57Z", "decisions": [{"origin": "cscli", "scope": "Ip", "type": "ban", "value": "203.0.113.50"}]},
 {"created_at": "2026-10-11T11:31:58Z", "decisions": [{"origin": "crowdsec", "scope": "Range", "type": "ban", "value": "198.51.100.0/24"},
   {"origin": "crowdsec", "scope": "Ip", "type": "captcha", "value": "192.0.2.1"}]},
 {"created_at": "2026-10-11T11:32:00Z", "decisions": [{"origin": "CAPI", "scope": "Ip", "type": "ban", "value": "192.0.2.2"},
   {"origin": "crowdsec", "scope": "Ip", "type": "ban", "value": "192.0.2.3", "simulated": true},
   {"origin": "crowdsec", "scope": "Country", "type": "ban", "value": "XX"}]}
]`
	got, err := parseBans([]byte(out))
	if err != nil || !slices.Equal(got, []string{"198.51.100.0/24", "203.0.113.50"}) {
		t.Errorf("got %v, %v", got, err)
	}
	if got, err := parseBans([]byte("null")); err != nil || len(got) != 0 {
		t.Errorf("none: %v, %v", got, err)
	}
}

// TestParseBlocks: AppSec's alerts (as cscli 1.8.1 gives them, trimmed),
// newest first, named after the rule the alert is; the path loses its
// query and the host its port. Other kinds of alert aren't requests it
// refused.
func TestParseBlocks(t *testing.T) {
	out := `[
 {"kind": "waf", "scenario": "crowdsecurity/vpatch-env-access", "start_at": "2026-10-18T13:46:57Z",
  "source": {"ip": "203.0.113.10", "value": "203.0.113.10"},
  "events": [{"meta": [{"key": "rule_name", "value": "crowdsecurity/vpatch-env-access"}, {"key": "message", "value": "Detect access to .env files"},
   {"key": "uri", "value": "/.env?token=secret"}, {"key": "data"}, {"key": "target_fqdn", "value": "Vault.Lab.Example.com:443"}]}]},
 {"kind": "waf", "scenario": "crowdsecurity/vpatch-CVE-2017-9841", "start_at": "2026-10-18T13:47:00Z", "source": {"value": "203.0.113.11"},
  "events": [{"meta": [{"key": "rule_name", "value": "crowdsecurity/base-config"}, {"key": "uri", "value": "/x"}, {"key": "target_fqdn", "value": "a.example.com"}]},
   {"meta": [{"key": "rule_name", "value": "crowdsecurity/vpatch-CVE-2017-9841"}, {"key": "message", "value": "PHPUnit RCE (CVE-2017-9841)"},
   {"key": "uri", "value": "/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php"}, {"key": "target_fqdn", "value": "vault.lab.example.com"}]}]},
 {"kind": "crowdsec", "scenario": "crowdsecurity/appsec-vpatch", "start_at": "2026-10-18T13:47:01Z", "source": {"ip": "203.0.113.10"}}
]`
	got, err := parseBlocks([]byte(out))
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	want := []Block{
		{At: time.Date(2026, 10, 18, 13, 47, 0, 0, time.UTC), Host: "vault.lab.example.com", Rule: "vpatch-CVE-2017-9841",
			What: "PHPUnit RCE (CVE-2017-9841)", Path: "/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php", From: "203.0.113.11"},
		{At: time.Date(2026, 10, 18, 13, 46, 57, 0, time.UTC), Host: "vault.lab.example.com", Rule: "vpatch-env-access",
			What: "Detect access to .env files", Path: "/.env", From: "203.0.113.10"},
	}
	for i := range want {
		if !got[i].At.Equal(want[i].At) || got[i].Host != want[i].Host || got[i].Rule != want[i].Rule || got[i].What != want[i].What ||
			got[i].Path != want[i].Path || got[i].From != want[i].From {
			t.Errorf("block %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if got, err := parseBlocks([]byte("null")); err != nil || len(got) != 0 {
		t.Errorf("none: %v, %v", got, err)
	}
	// The weekly attacks leave AppSec's alerts to these.
	if a, err := parseAlerts([]byte(out)); err != nil || a.Alerts != 1 || a.Kinds[0] != "appsec-vpatch" {
		t.Errorf("attacks: %+v, %v", a, err)
	}
}

// TestAppSecUp: a listener that waits for a request is up; one that
// closes the connection at once (pasta with nothing behind it) and no
// listener at all aren't.
func TestAppSecUp(t *testing.T) {
	appSecWait = 50 * time.Millisecond
	listen := func(closeAtOnce bool) string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				if closeAtOnce {
					c.Close()
				} else {
					t.Cleanup(func() { c.Close() })
				}
			}
		}()
		return l.Addr().String()
	}
	ctx := context.Background()
	if !AppSecUp(ctx, listen(false)) {
		t.Error("a listener waiting for a request is up")
	}
	if AppSecUp(ctx, listen(true)) {
		t.Error("a connection closed at once isn't")
	}
	if AppSecUp(ctx, "127.0.0.1:1") {
		t.Error("nothing listening isn't")
	}
}
