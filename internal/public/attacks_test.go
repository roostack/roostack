package public

import (
	"slices"
	"testing"
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
