package dns

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckFindsParentZone(t *testing.T) {
	var tried []string
	deleted := false
	pr := func(_ context.Context, _ *http.Client, _ map[string]string, zone, name, value string) (func(context.Context) error, error) {
		tried = append(tried, zone+" "+name)
		if zone != "example.com" {
			return nil, errors.New("zone not found")
		}
		return func(context.Context) error { deleted = true; return nil }, nil
	}
	zone, err := check(context.Background(), http.DefaultClient, pr, nil, "Lab.Example.com")
	if err != nil || zone != "example.com" {
		t.Fatalf("zone %q err %v", zone, err)
	}
	if strings.Join(tried, ",") != "lab.example.com _roostack-check,example.com _roostack-check.lab" {
		t.Errorf("tried %v: most specific zone first, name relative to the zone", tried)
	}
	if !deleted {
		t.Error("the test record must be deleted")
	}
}

func TestCheckErrors(t *testing.T) {
	fail := func(context.Context, *http.Client, map[string]string, string, string, string) (func(context.Context) error, error) {
		return nil, errors.New("403 Forbidden")
	}
	if _, err := check(context.Background(), http.DefaultClient, fail, nil, "lab.example.com"); err == nil ||
		!strings.Contains(err.Error(), "example.com: 403 Forbidden") {
		t.Errorf("no zone worked: %v", err)
	}
	undoFails := func(context.Context, *http.Client, map[string]string, string, string, string) (func(context.Context) error, error) {
		return func(context.Context) error { return errors.New("permission denied") }, nil
	}
	if _, err := check(context.Background(), http.DefaultClient, undoFails, nil, "example.com"); err == nil ||
		!strings.Contains(err.Error(), "remove _roostack-check.example.com by hand") {
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
		if p.probe == nil {
			t.Errorf("%s: no check", id)
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

// The Caddy image must include every provider's plugin, and pin
// everything it builds from (SPEC.md §8.1): each plugin by version, each
// base image by digest.
func TestContainerfileHasPlugins(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "build", "caddy", "Containerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Providers() {
		if !strings.Contains(string(data), "--with "+p.Plugin+"@v") {
			t.Errorf("build/caddy/Containerfile is missing %s, or doesn't pin its version", p.Plugin)
		}
	}
	for line := range strings.Lines(string(data)) {
		if strings.HasPrefix(line, "FROM ") && !strings.Contains(line, "@sha256:") {
			t.Errorf("base image not pinned by digest: %s", strings.TrimSpace(line))
		}
	}
}
