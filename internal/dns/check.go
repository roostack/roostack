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

// Check proves the credentials can do what certificates need: it creates a
// temporary TXT record for domain and deletes it again (Namecheap: reads
// the zone instead). Zones are tried from the domain itself up to its
// registrable parent (lab.example.com usually lives in example.com).
//
// It returns the zone that worked.
func (p *Provider) Check(ctx context.Context, values map[string]string, domain string) (string, error) {
	return check(ctx, &http.Client{Timeout: 30 * time.Second}, p.probe, values, domain)
}

func check(ctx context.Context, hc *http.Client, pr probe, values map[string]string, domain string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	token := make([]byte, 8)
	rand.Read(token)
	value := "roostack-check-" + hex.EncodeToString(token)

	labels := strings.Split(strings.Trim(strings.ToLower(domain), "."), ".")
	var errs []error
	for i := 0; i+2 <= len(labels); i++ {
		zone := strings.Join(labels[i:], ".")
		name := CheckName
		if i > 0 {
			name += "." + strings.Join(labels[:i], ".")
		}
		undo, err := pr(ctx, hc, values, zone, name, value)
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
