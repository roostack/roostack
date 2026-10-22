package dns

import (
	"context"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/dns/dnstest"
)

// TestCheck: each provider's check, against its fake API (dnstest), finds
// the zone lab.example.com is in and sends what the records code sends:
// a TXT record set and deleted again, through the same requests
// (Namecheap: its records only read). It leaves nothing behind.
func TestCheck(t *testing.T) {
	// Each API's credentials, as its requests show them.
	creds := map[string]string{
		"cloudflare": "auth=Bearer t", "route53": "auth=AWS4-HMAC-SHA256 a", "porkbun": `"apikey":"k"`,
		"gandi": "auth=Bearer t", "godaddy": "auth=sso-key k:s", "spaceship": "auth=k/s", "namecheap": "ApiKey=k",
	}
	ctx := context.Background()
	const value = "roostack-check-t0k"
	for _, p := range Providers() {
		t.Run(p.ID, func(t *testing.T) {
			v := testValues[p.ID]
			fake := dnstest.Start(t)
			zone, err := p.check(ctx, fake.Client(), v, "lab.example.com", value)
			if err != nil || zone != "example.com" {
				t.Fatalf("zone %q err %v\nrequests:\n%s", zone, err, strings.Join(fake.Requests(), "\n"))
			}
			if got := fake.Get("example.com", CheckName+".lab", "TXT"); len(got) != 0 {
				t.Errorf("test record left behind: %v", got)
			}
			if !strings.Contains(strings.Join(fake.Requests(), "\n"), creds[p.ID]) {
				t.Errorf("no request with %q:\n%s", creds[p.ID], strings.Join(fake.Requests(), "\n"))
			}

			// The records code, doing the same: the domain itself isn't a
			// zone, so it tries its parent.
			records := dnstest.Start(t)
			hc := records.Client()
			if p.ID == "namecheap" {
				if _, err := p.GetRecord(ctx, hc, v, "lab.example.com", CheckName, "TXT"); err == nil {
					t.Fatal("lab.example.com is no zone at the fake")
				}
				if _, err := p.GetRecord(ctx, hc, v, "example.com", CheckName+".lab", "TXT"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := p.SetRecord(ctx, hc, v, "lab.example.com", CheckName, "TXT", value); err == nil {
					t.Fatal("lab.example.com is no zone at the fake")
				}
				if err := p.SetRecord(ctx, hc, v, "example.com", CheckName+".lab", "TXT", value); err != nil {
					t.Fatal(err)
				}
				if err := p.DeleteRecord(ctx, hc, v, "example.com", CheckName+".lab", "TXT"); err != nil {
					t.Fatal(err)
				}
			}
			if got, want := fake.Requests(), records.Requests(); !slices.Equal(got, want) {
				t.Errorf("the check sent:\n%s\nthe records code sends:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// A failure says what to look at, where the provider's answer doesn't.
func TestCheckHints(t *testing.T) {
	ctx := context.Background()
	fake := dnstest.Start(t)
	_, err := Get("cloudflare").check(ctx, fake.Client(), testValues["cloudflare"], "lab.example.org", "x")
	if err == nil || !strings.Contains(err.Error(), "example.org: zone not found (the token needs Zone → Zone → Read)") {
		t.Errorf("Cloudflare: %v", err)
	}

	fake.Refuse("namecheap", true)
	_, err = Get("namecheap").check(ctx, fake.Client(),
		map[string]string{"user": "me", "api_key": "bad-key", "client_ip": "198.51.100.1"}, "example.com", "x")
	if err == nil || !strings.Contains(err.Error(), "Invalid request IP (error 1011150) (this lab's IP is 198.51.100.1)") {
		t.Errorf("Namecheap: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "bad-key") {
		t.Error("errors must not contain the API key")
	}
	// Without the IP given, the one found is said.
	_, err = Get("namecheap").check(ctx, fake.Client(), map[string]string{"user": "me", "api_key": "k"}, "example.com", "x")
	if err == nil || !strings.Contains(err.Error(), "(this lab's IP is 203.0.113.7)") {
		t.Errorf("Namecheap, its IP found: %v", err)
	}
}

// A provider Roostack can't write and delete records with has no check
// to make (Roostack Cloud's: a roost.ac name's credentials come from
// claiming it).
func TestCheckWithoutRecords(t *testing.T) {
	if _, err := Get("roostack-cloud").check(context.Background(), dnstest.Start(t).Client(), nil, "smiths.roost.ac", "x"); err == nil {
		t.Error("expected an error, not a crash")
	}
}
