package dns

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CheckName is the temporary TXT record the check creates.
const CheckName = "_roostack-check"

// probe creates a TXT record `name` in c's zone with value, and returns a
// function that deletes it. A probe that can't safely write (Namecheap)
// checks read access instead and returns a nil undo.
type probe func(ctx context.Context, c conn, name, value string) (undo func(context.Context) error, err error)

// Check proves the credentials can do what certificates need: it creates a
// temporary TXT record for domain and deletes it again, as Roostack
// manages records (Namecheap: reads the zone instead). Zones are tried
// from the domain itself up to its registrable parent (lab.example.com
// usually lives in example.com).
//
// It returns the zone that worked.
func (p *Provider) Check(ctx context.Context, values map[string]string, domain string) (string, error) {
	token := make([]byte, 8)
	rand.Read(token)
	return p.check(ctx, &http.Client{Timeout: 30 * time.Second}, values, domain, "roostack-check-"+hex.EncodeToString(token))
}

func (p *Provider) check(ctx context.Context, hc *http.Client, values map[string]string, domain, value string) (string, error) {
	pr := p.probe
	if pr == nil && p.CanDeleteRecords() {
		pr = writeProbe(p.records)
	}
	if pr == nil {
		return "", errors.New("Roostack can't check " + p.Title + " credentials")
	}
	return check(ctx, hc, pr, values, domain, value)
}

func check(ctx context.Context, hc *http.Client, pr probe, values map[string]string, domain, value string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	labels := strings.Split(strings.Trim(strings.ToLower(domain), "."), ".")
	var errs []error
	for i := 0; i+2 <= len(labels); i++ {
		zone := strings.Join(labels[i:], ".")
		name := CheckName
		if i > 0 {
			name += "." + strings.Join(labels[:i], ".")
		}
		undo, err := pr(ctx, conn{hc, values, zone}, name, value)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", zone, err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if undo != nil {
			if err := undo(ctx); err != nil {
				return "", fmt.Errorf("created a test record in %s but couldn't delete it (%v); remove %s.%s by hand",
					zone, err, name, zone)
			}
		}
		return zone, nil
	}
	if len(errs) == 0 {
		return "", fmt.Errorf("%q is not a valid domain", domain)
	}
	return "", errors.Join(errs...)
}

// writeProbe is the check for a provider whose records Roostack writes:
// the TXT record set and deleted through the records code itself.
func writeProbe(r *recordsAPI) probe {
	return func(ctx context.Context, c conn, name, value string) (func(context.Context) error, error) {
		if err := r.upsert(ctx, c, name, "TXT", value); err != nil {
			return nil, err
		}
		return func(ctx context.Context) error { return r.remove(ctx, c, name, "TXT") }, nil
	}
}

// namecheapProbe only reads the zone's records. Namecheap's API can only
// replace all of a domain's records at once, so a test write would briefly
// rewrite the whole zone. Reading still proves the key, username, IP
// whitelist and access to the domain.
func namecheapProbe(ctx context.Context, c conn, _, _ string) (func(context.Context) error, error) {
	if !strings.Contains(c.zone, ".") {
		return nil, errors.New("not a domain")
	}
	if _, err := namecheapRequest(ctx, c, "namecheap.domains.dns.getHosts", nil); err != nil {
		ip := c.v["client_ip"]
		if ip == "" {
			ip, _ = publicIP(ctx, c.hc)
		}
		return nil, fmt.Errorf("%w (this lab's IP is %s)", err, ip)
	}
	return nil, nil
}
