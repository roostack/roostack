package public

import "testing"

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
