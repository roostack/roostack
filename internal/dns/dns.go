// Package dns knows the DNS providers Roostack supports for the DNS-01
// challenge (SPEC.md §6.5): what credentials each needs, how Caddy is
// configured for it, and how to check the credentials before saving.
//
// Every credential is stored as a Podman secret and given to Caddy as an
// environment variable, so no credential is ever written into the
// Caddyfile.
package dns

import (
	"fmt"
	"slices"
	"strings"
)

// Field is one value the user enters for a provider.
type Field struct {
	Key      string // e.g. "api_token"; also names the secret and env var
	Label    string
	Help     string
	Optional bool
	// Sensitive fields are password inputs and never shown back.
	Sensitive bool
}

// Provider is a supported DNS provider.
type Provider struct {
	ID    string
	Title string
	// Help explains how to create the credentials.
	Help string
	// Plugin is the Caddy DNS module built into Roostack's Caddy image.
	Plugin string
	Fields []Field
	// directive is the Caddyfile "dns" line; set lists the fields given.
	directive func(set []string) string
	// probe checks credentials against the provider's API (probes.go).
	probe probe
	// records manages DNS records (records.go); nil if unsupported.
	records *recordsAPI
}

// SecretName is the Podman secret holding a field's value.
func SecretName(field string) string { return "roostack-caddy-dns-" + field }

// EnvName is the environment variable Caddy reads a field from.
func EnvName(field string) string { return "DNS_" + strings.ToUpper(field) }

func env(field string) string { return "{env." + EnvName(field) + "}" }

// block renders a Caddyfile dns block with one line per field given.
func block(name string, set []string, keys ...string) string {
	var b strings.Builder
	b.WriteString("dns " + name + " {\n")
	for _, k := range keys {
		if slices.Contains(set, k) {
			fmt.Fprintf(&b, "\t%s %s\n", k, env(k))
		}
	}
	b.WriteString("}")
	return b.String()
}

var providers = []*Provider{
	{
		ID: "cloudflare", Title: "Cloudflare", Plugin: "github.com/caddy-dns/cloudflare",
		Help: "In Cloudflare, go to My Profile → API Tokens → Create Token and use the \"Edit zone DNS\" " +
			"template for your domain. Also add the permission Zone → Zone → Read.",
		Fields:    []Field{{Key: "api_token", Label: "API token", Sensitive: true}},
		directive: func([]string) string { return "dns cloudflare " + env("api_token") },
		probe:     cloudflareProbe,
		records:   cloudflareRecords,
	},
	{
		ID: "route53", Title: "Amazon Route 53 (AWS)", Plugin: "github.com/caddy-dns/route53",
		Help: "Create an IAM user with an access key, allowed to use route53:ListHostedZonesByName, " +
			"route53:ListResourceRecordSets, route53:ChangeResourceRecordSets and route53:GetChange.",
		Fields: []Field{
			{Key: "access_key_id", Label: "Access key ID"},
			{Key: "secret_access_key", Label: "Secret access key", Sensitive: true},
			{Key: "region", Label: "Region", Help: "Optional, e.g. us-east-1.", Optional: true},
		},
		directive: func(set []string) string {
			return block("route53", set, "access_key_id", "secret_access_key", "region")
		},
		probe:   route53Probe,
		records: route53Records,
	},
	{
		ID: "porkbun", Title: "Porkbun", Plugin: "github.com/caddy-dns/porkbun",
		Help: "In Porkbun, go to Account → API Access and create a key. Then turn on \"API Access\" " +
			"for your domain in the domain's settings.",
		Fields: []Field{
			{Key: "api_key", Label: "API key"},
			{Key: "api_secret_key", Label: "Secret API key", Sensitive: true},
		},
		directive: func(set []string) string { return block("porkbun", set, "api_key", "api_secret_key") },
		probe:     porkbunProbe,
		records:   porkbunRecords,
	},
	{
		ID: "gandi", Title: "Gandi", Plugin: "github.com/caddy-dns/gandi",
		Help: "In Gandi, go to your account's Authentication options → Personal Access Tokens and create " +
			"one that can manage your domain's technical configuration.",
		Fields:    []Field{{Key: "bearer_token", Label: "Personal access token", Sensitive: true}},
		directive: func([]string) string { return "dns gandi " + env("bearer_token") },
		probe:     gandiProbe,
		records:   gandiRecords,
	},
	{
		ID: "godaddy", Title: "GoDaddy", Plugin: "github.com/caddy-dns/godaddy",
		Help: "Create a Production API key at developer.godaddy.com. Note: GoDaddy only allows DNS " +
			"changes through its API for some accounts (for example those with many domains).",
		Fields: []Field{
			{Key: "api_key", Label: "API key"},
			{Key: "api_secret", Label: "API secret", Sensitive: true},
		},
		// The plugin expects "key:secret" in one value.
		directive: func([]string) string { return "dns godaddy " + env("api_key") + ":" + env("api_secret") },
		probe:     godaddyProbe,
		records:   godaddyRecords,
	},
	{
		ID: "namecheap", Title: "Namecheap", Plugin: "github.com/caddy-dns/namecheap",
		Help: "In Namecheap, go to Profile → Tools → Namecheap API Access, turn it on, and add this " +
			"lab's public IP address to the whitelist. Namecheap only enables the API for some accounts. " +
			"(Roostack's check only reads your DNS records: Namecheap's API can't add one record on its own.)",
		Fields: []Field{
			{Key: "user", Label: "Namecheap username"},
			{Key: "api_key", Label: "API key", Sensitive: true},
			{Key: "client_ip", Label: "Public IP address", Optional: true,
				Help: "Optional: detected automatically if left empty."},
		},
		directive: func(set []string) string { return block("namecheap", set, "api_key", "user", "client_ip") },
		probe:     namecheapProbe,
		records:   namecheapRecords,
	},
	{
		ID: "spaceship", Title: "Spaceship", Plugin: "github.com/caddy-dns/spaceship",
		Help: "In Spaceship, open the API Manager and create a key with permission to read and write " +
			"DNS records.",
		Fields: []Field{
			{Key: "api_key", Label: "API key"},
			{Key: "api_secret", Label: "API secret", Sensitive: true},
		},
		directive: func(set []string) string { return block("spaceship", set, "api_key", "api_secret") },
		probe:     spaceshipProbe,
		records:   spaceshipRecords,
	},
}

// Providers lists every supported provider, in display order.
func Providers() []*Provider { return providers }

// Get returns a provider by ID, or nil.
func Get(id string) *Provider {
	for _, p := range providers {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// Directive returns the Caddyfile tls "dns" directive for the given fields.
func (p *Provider) Directive(set []string) string { return p.directive(set) }

// Validate checks every required field has a value and returns the keys
// that are set, in field order.
func (p *Provider) Validate(values map[string]string) ([]string, error) {
	var set []string
	for _, f := range p.Fields {
		v := strings.TrimSpace(values[f.Key])
		if v == "" {
			if !f.Optional {
				return nil, fmt.Errorf("enter the %s", strings.ToLower(f.Label[:1])+f.Label[1:])
			}
			continue
		}
		set = append(set, f.Key)
	}
	return set, nil
}
