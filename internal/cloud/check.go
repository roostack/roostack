package cloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/cloudapi"
)

// CheckWithin is how long a claimed name's test challenge value may take
// to show on its zone's own name servers, looked for every CheckEvery.
var (
	CheckWithin = 45 * time.Second
	CheckEvery  = 3 * time.Second
)

// checkClaim proves a claimed name's credentials do what certificates
// need, before the lab uses the name: a test value written through
// Cloud's challenge API (as Caddy's acmedns module writes them), then,
// for a name in public DNS, the same value seen at _acme-challenge.<name>
// on its zone's own name servers (no resolver's cache in between). Names
// under .test never reach public DNS (the fake, a development Cloud), so
// only the write is checked there. Without this, a lab keeps using a
// cached certificate, and broken credentials would show only when it
// next renews, two months later (found 2026-10-10).
func (s *Service) checkClaim(ctx context.Context, claim cloudapi.NameClaim) error {
	c := claim.Credentials
	server, sub := strings.TrimRight(c[cloudapi.CredServerURL], "/"), c[cloudapi.CredSubdomain]
	if server == "" || sub == "" || c[cloudapi.CredUsername] == "" || c[cloudapi.CredPassword] == "" {
		return errors.New("Roostack Cloud's answer was missing part of the name's challenge login")
	}
	b := make([]byte, 32)
	rand.Read(b)
	value := base64.RawURLEncoding.EncodeToString(b) // 43 characters, as Let's Encrypt's are
	body, _ := json.Marshal(cloudapi.ACMEUpdate{Subdomain: sub, TXT: value})
	ctx2, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx2, http.MethodPost, server+"/update", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(cloudapi.HeaderUser, c[cloudapi.CredUsername])
	req.Header.Set(cloudapi.HeaderKey, c[cloudapi.CredPassword])
	resp, err := s.challengeClient().Do(req)
	if err != nil {
		s.client.record(http.MethodPost, server+"/update", body, 0, err)
		return fmt.Errorf("Roostack Cloud's challenge server didn't answer (%v): certificates for %s need it. Try again in a minute.", err, claim.Name)
	}
	resp.Body.Close()
	s.client.record(http.MethodPost, server+"/update", body, resp.StatusCode, nil)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Roostack Cloud's challenge server refused the name's login (status %d): certificates for %s need it. Try again; if it keeps happening, unlink and link the lab again.", resp.StatusCode, claim.Name)
	}
	if strings.HasSuffix(claim.Name, ".test") {
		return nil
	}
	name := "_acme-challenge." + claim.Name
	lookup := s.LookupTXT
	if lookup == nil {
		lookup = authoritativeTXT
	}
	deadline := time.Now().Add(CheckWithin)
	for {
		got, err := lookup(ctx, name)
		if err == nil && slices.Contains(got, value) {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			why := "it didn't appear"
			if err != nil {
				why = err.Error()
			}
			return fmt.Errorf("a test challenge value for %s didn't show in public DNS within %d seconds (%s): Let's Encrypt needs to see it there to issue certificates. Try again in a few minutes.",
				claim.Name, int(CheckWithin.Seconds()), why)
		}
		time.Sleep(CheckEvery)
	}
}

// challengeClient talks to Cloud's challenge server: no lab certificate,
// and the same trust as Cloud's API (public, or the configured test CA).
func (s *Service) challengeClient() *http.Client { return s.client.anon }

// authoritativeTXT reads name's TXT records from its zone's own name
// servers: the zone is the nearest parent with NS records.
func authoritativeTXT(ctx context.Context, name string) ([]string, error) {
	labels := strings.Split(strings.TrimSuffix(name, "."), ".")
	var ns []*net.NS
	for i := 1; i+1 < len(labels) && len(ns) == 0; i++ {
		ns, _ = net.DefaultResolver.LookupNS(ctx, strings.Join(labels[i:], "."))
	}
	if len(ns) == 0 {
		return nil, fmt.Errorf("no name servers found for %s", name)
	}
	var errs []error
	for _, n := range ns {
		server := net.JoinHostPort(strings.TrimSuffix(n.Host, "."), "53")
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, server)
		}}
		txt, err := r.LookupTXT(ctx, name)
		if err == nil {
			return txt, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}
