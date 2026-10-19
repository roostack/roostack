package dns

import (
	"context"
	"net/http"
	"testing"

	"roostack.cloud/roostack/internal/dns/dnstest"
)

// apis are the package's API addresses, which tests point at the fake.
func apis() map[*string]string {
	return map[*string]string{
		&cloudflareAPI: "https://api.cloudflare.com/client/v4", &route53API: "https://route53.amazonaws.com",
		&porkbunAPI: "https://api.porkbun.com/api/json/v3", &gandiAPI: "https://api.gandi.net/v5/livedns",
		&godaddyAPI: "https://api.godaddy.com", &spaceshipAPI: "https://spaceship.dev/api",
		&namecheapAPI: "https://api.namecheap.com/xml.response", &publicIPURL: "https://icanhazip.com",
	}
}

// startRecordAPIs runs the fake APIs (dnstest) and points this package
// at them until the test ends.
func startRecordAPIs(t *testing.T) *dnstest.Fake {
	t.Helper()
	st := dnstest.Start(t)
	for ptr, real := range apis() {
		old := *ptr
		*ptr = st.URL + dnstest.Paths[real]
		t.Cleanup(func() { *ptr = old })
	}
	return st
}

// TestFakeAddresses: the fake knows each API by the address this package
// really uses, so other packages' tests can reach it through its client.
func TestFakeAddresses(t *testing.T) {
	for ptr, real := range apis() {
		if *ptr != real {
			t.Errorf("an API's address is %s, not %s: update apis() and dnstest.Paths", *ptr, real)
		}
		if dnstest.Paths[real] == "" {
			t.Errorf("dnstest has no fake for %s", real)
		}
	}
	if len(dnstest.Paths) != len(apis()) {
		t.Errorf("dnstest.Paths has %d addresses, this package %d", len(dnstest.Paths), len(apis()))
	}
}

func TestRecordsCreateAndUpdate(t *testing.T) {
	values := map[string]map[string]string{
		"cloudflare": {"api_token": "t"}, "route53": {"access_key_id": "a", "secret_access_key": "s"},
		"porkbun": {"api_key": "k", "api_secret_key": "s"}, "gandi": {"bearer_token": "t"},
		"godaddy": {"api_key": "k", "api_secret": "s"}, "spaceship": {"api_key": "k", "api_secret": "s"},
		"namecheap": {"user": "u", "api_key": "k"},
	}
	ctx := context.Background()
	for _, p := range Providers() {
		t.Run(p.ID, func(t *testing.T) {
			st := startRecordAPIs(t)
			v := values[p.ID]
			hc := http.DefaultClient
			if got, err := p.GetRecord(ctx, hc, v, "example.com", "*.lab", "A"); err != nil || len(got) != 0 {
				t.Fatalf("before: %v %v", got, err)
			}
			for _, ip := range []string{"192.168.1.20", "192.168.1.21"} { // create, then update
				if err := p.SetRecord(ctx, hc, v, "example.com", "*.lab", "A", ip); err != nil {
					t.Fatalf("set %s: %v", ip, err)
				}
				got, err := p.GetRecord(ctx, hc, v, "example.com", "*.lab", "A")
				if err != nil || len(got) != 1 || got[0] != ip {
					t.Fatalf("after setting %s: %v %v", ip, got, err)
				}
			}
			switch p.ID {
			case "cloudflare":
				if st.CFComment != ManagedComment {
					t.Error("Cloudflare records should carry the Roostack comment")
				}
			case "namecheap":
				if st.NCEmail != "FWD" || len(st.NCHosts) != 3 {
					t.Errorf("Namecheap rewrite lost data: email %q hosts %+v", st.NCEmail, st.NCHosts)
				}
				if st.NCHosts[0].MXPref != "10" || st.NCHosts[1].Address != "example.com." {
					t.Errorf("existing records changed: %+v", st.NCHosts)
				}
			}
			if ok, risky := p.ManagesRecords(); !ok || risky != (p.ID == "namecheap") {
				t.Errorf("ManagesRecords = %v, %v", ok, risky)
			}
		})
	}
}

// TestRecordTypes: CNAME and TXT records are written in each API's own
// form (its quotes and final dots, Spaceship's field per type), read back
// plain, and deleted; deleting what isn't there is no error.
func TestRecordTypes(t *testing.T) {
	values := map[string]map[string]string{
		"cloudflare": {"api_token": "t"}, "route53": {"access_key_id": "a", "secret_access_key": "s"},
		"porkbun": {"api_key": "k", "api_secret_key": "s"}, "gandi": {"bearer_token": "t"},
		"godaddy": {"api_key": "k", "api_secret": "s"}, "spaceship": {"api_key": "k", "api_secret": "s"},
		"namecheap": {"user": "u", "api_key": "k"},
	}
	// How each API holds them: the CNAME, then the TXT.
	wire := map[string][2]string{
		"cloudflare": {"r-abc.relay.roost.ac", `"roostack-proof=t0k"`},
		"route53":    {"r-abc.relay.roost.ac.", `"roostack-proof=t0k"`},
		"porkbun":    {"r-abc.relay.roost.ac", "roostack-proof=t0k"},
		"gandi":      {"r-abc.relay.roost.ac.", `"roostack-proof=t0k"`},
		"godaddy":    {"r-abc.relay.roost.ac", "roostack-proof=t0k"},
		"spaceship":  {"cname=r-abc.relay.roost.ac", "value=roostack-proof=t0k"},
		"namecheap":  {"r-abc.relay.roost.ac.", "roostack-proof=t0k"},
	}
	ctx := context.Background()
	for _, p := range Providers() {
		t.Run(p.ID, func(t *testing.T) {
			st := startRecordAPIs(t)
			v, hc := values[p.ID], http.DefaultClient
			stored := func(name, typ string) string {
				if p.ID == "namecheap" {
					for _, h := range st.NCHosts {
						if h.Name == name && h.Type == typ {
							return h.Address
						}
					}
					return ""
				}
				if got := st.Get("example.com", name, typ); len(got) == 1 {
					return got[0]
				}
				return ""
			}
			if err := p.SetRecord(ctx, hc, v, "example.com", "vault.lab", "A", "203.0.113.7"); err != nil {
				t.Fatal(err)
			}
			if err := p.DeleteRecord(ctx, hc, v, "example.com", "vault.lab", "A"); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if got, _ := p.GetRecord(ctx, hc, v, "example.com", "vault.lab", "A"); len(got) != 0 {
				t.Errorf("still there: %v", got)
			}
			if err := p.DeleteRecord(ctx, hc, v, "example.com", "vault.lab", "A"); err != nil {
				t.Errorf("deleting what isn't there: %v", err)
			}
			if err := p.SetRecord(ctx, hc, v, "example.com", "vault.lab", "CNAME", "r-abc.relay.roost.ac"); err != nil {
				t.Fatal(err)
			}
			if err := p.SetRecord(ctx, hc, v, "example.com", "_roostack.lab", "TXT", "roostack-proof=t0k"); err != nil {
				t.Fatal(err)
			}
			if got := stored("vault.lab", "CNAME"); got != wire[p.ID][0] {
				t.Errorf("the CNAME as sent: %q, want %q", got, wire[p.ID][0])
			}
			if got := stored("_roostack.lab", "TXT"); got != wire[p.ID][1] {
				t.Errorf("the TXT as sent: %q, want %q", got, wire[p.ID][1])
			}
			if got, err := p.GetRecord(ctx, hc, v, "example.com", "vault.lab", "CNAME"); err != nil || len(got) != 1 || got[0] != "r-abc.relay.roost.ac" {
				t.Errorf("the CNAME read back: %v %v", got, err)
			}
			if got, err := p.GetRecord(ctx, hc, v, "example.com", "_roostack.lab", "TXT"); err != nil || len(got) != 1 || got[0] != "roostack-proof=t0k" {
				t.Errorf("the TXT read back: %v %v", got, err)
			}
			if err := p.DeleteRecord(ctx, hc, v, "example.com", "_roostack.lab", "TXT"); err != nil {
				t.Errorf("deleting the TXT: %v", err)
			}
			if p.ID == "namecheap" && (st.NCEmail != "FWD" || len(st.NCHosts) != 3) {
				t.Errorf("Namecheap's rewrites lost data: %+v", st.NCHosts)
			}
		})
	}
}
