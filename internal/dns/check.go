package dns

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/libdns/libdns"
)

// CheckName is the temporary TXT record the check creates.
const CheckName = "_mylab-check"

// Check proves the credentials can do what certificates need: it creates a
// temporary TXT record for domain and deletes it again, using the same
// library Caddy uses. Zones are tried from the domain itself up to its
// registrable parent (lab.example.com usually lives in example.com).
//
// It returns the zone that worked.
func (p *Provider) Check(ctx context.Context, values map[string]string, domain string) (string, error) {
	return check(ctx, p.client(values), domain)
}

func check(ctx context.Context, c Client, domain string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	token := make([]byte, 8)
	rand.Read(token)
	value := "mylab-check-" + hex.EncodeToString(token)

	labels := strings.Split(strings.Trim(strings.ToLower(domain), "."), ".")
	var errs []error
	for i := 0; i+2 <= len(labels); i++ {
		zone := strings.Join(labels[i:], ".") + "."
		name := CheckName
		if i > 0 {
			name += "." + strings.Join(labels[:i], ".")
		}
		created, err := c.AppendRecords(ctx, zone, []libdns.Record{libdns.TXT{Name: name, Text: value, TTL: 60 * time.Second}})
		if err != nil {
			errs = append(errs, fmt.Errorf("zone %s: %w", strings.TrimSuffix(zone, "."), err))
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if len(created) == 0 {
			created = []libdns.Record{libdns.TXT{Name: name, Text: value}}
		}
		if _, err := c.DeleteRecords(ctx, zone, created); err != nil {
			return "", fmt.Errorf("created a test record in %s but couldn't delete it (%v); remove %s.%s by hand",
				strings.TrimSuffix(zone, "."), err, name, strings.TrimSuffix(zone, "."))
		}
		return strings.TrimSuffix(zone, "."), nil
	}
	if len(errs) == 0 {
		return "", fmt.Errorf("%q is not a valid domain", domain)
	}
	return "", errors.Join(errs...)
}
