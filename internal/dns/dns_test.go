package dns

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/libdns/libdns"
)

// fakeDNS hosts one zone and records every change.
type fakeDNS struct {
	mu        sync.Mutex
	zone      string
	records   map[string]string
	failDel   bool
	appended  []string
	zonesSeen []string
}

func (f *fakeDNS) AppendRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zonesSeen = append(f.zonesSeen, zone)
	if zone != f.zone {
		return nil, errors.New("zone not found")
	}
	for _, r := range recs {
		rr := r.RR()
		f.records[rr.Name] = rr.Data
		f.appended = append(f.appended, rr.Name)
	}
	return recs, nil
}

func (f *fakeDNS) DeleteRecords(_ context.Context, zone string, recs []libdns.Record) ([]libdns.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDel {
		return nil, errors.New("permission denied")
	}
	for _, r := range recs {
		delete(f.records, r.RR().Name)
	}
	return recs, nil
}

func TestCheckFindsParentZone(t *testing.T) {
	f := &fakeDNS{zone: "example.com.", records: map[string]string{}}
	zone, err := check(context.Background(), f, "Lab.Example.com")
	if err != nil || zone != "example.com" {
		t.Fatalf("zone %q err %v", zone, err)
	}
	if f.appended[0] != "_mylab-check.lab" {
		t.Errorf("record name %q, want it relative to the zone", f.appended[0])
	}
	if len(f.records) != 0 {
		t.Error("the test record must be deleted")
	}
	if f.zonesSeen[0] != "lab.example.com." {
		t.Error("the most specific zone should be tried first")
	}
}

func TestCheckErrors(t *testing.T) {
	f := &fakeDNS{zone: "other.org.", records: map[string]string{}}
	if _, err := check(context.Background(), f, "lab.example.com"); err == nil || !strings.Contains(err.Error(), "zone example.com") {
		t.Errorf("no matching zone: %v", err)
	}
	f = &fakeDNS{zone: "example.com.", records: map[string]string{}, failDel: true}
	if _, err := check(context.Background(), f, "example.com"); err == nil || !strings.Contains(err.Error(), "remove _mylab-check.example.com by hand") {
		t.Errorf("delete failure: %v", err)
	}
}

func TestRegistry(t *testing.T) {
	want := []string{"cloudflare", "route53", "porkbun", "gandi", "godaddy", "namecheap", "spaceship"}
	for _, id := range want {
		p := Get(id)
		if p == nil {
			t.Fatalf("missing provider %s", id)
		}
		// Every provider builds a client from its fields.
		values := map[string]string{}
		for _, f := range p.Fields {
			values[f.Key] = "x"
		}
		if p.client(values) == nil {
			t.Errorf("%s: no client", id)
		}
	}
	if Get("nope") != nil {
		t.Error("unknown provider")
	}
}

func TestDirectives(t *testing.T) {
	cases := map[string]struct {
		set  []string
		want string
	}{
		"cloudflare": {nil, "dns cloudflare {env.DNS_API_TOKEN}"},
		"godaddy":    {nil, "dns godaddy {env.DNS_API_KEY}:{env.DNS_API_SECRET}"},
		"route53": {[]string{"access_key_id", "secret_access_key"},
			"dns route53 {\n\taccess_key_id {env.DNS_ACCESS_KEY_ID}\n\tsecret_access_key {env.DNS_SECRET_ACCESS_KEY}\n}"},
		"namecheap": {[]string{"api_key", "user", "client_ip"},
			"dns namecheap {\n\tapi_key {env.DNS_API_KEY}\n\tuser {env.DNS_USER}\n\tclient_ip {env.DNS_CLIENT_IP}\n}"},
	}
	for id, c := range cases {
		if got := Get(id).Directive(c.set); got != c.want {
			t.Errorf("%s:\n%s\nwant\n%s", id, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	p := Get("route53")
	set, err := p.Validate(map[string]string{"access_key_id": "a", "secret_access_key": " s "})
	if err != nil || strings.Join(set, ",") != "access_key_id,secret_access_key" {
		t.Errorf("set %v err %v (region is optional)", set, err)
	}
	if _, err := p.Validate(map[string]string{"access_key_id": "a"}); err == nil || !strings.Contains(err.Error(), "secret access key") {
		t.Errorf("missing required field: %v", err)
	}
}

// The Caddy image must include every provider's plugin.
func TestContainerfileHasPlugins(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "build", "caddy", "Containerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Providers() {
		if !strings.Contains(string(data), "--with "+p.Plugin) {
			t.Errorf("build/caddy/Containerfile is missing %s", p.Plugin)
		}
	}
}
