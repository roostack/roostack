// Package setup is the business logic behind the first-run setup wizard:
// checking and saving the lab's domain, DNS token and backup destination.
package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CloudflareAPI is Cloudflare's API base URL (tests point it elsewhere).
var CloudflareAPI = "https://api.cloudflare.com/client/v4"

// CheckError is a problem with what the user entered, safe to show them.
type CheckError struct{ Msg string }

func (e *CheckError) Error() string { return e.Msg }

// CheckCloudflare confirms a Cloudflare API token is active and can see
// the DNS zone for domain, so Caddy can get certificates. A bad token would
// otherwise stop Caddy entirely (docs/ASSUMPTIONS.md L5).
//
// It returns the zone name found (e.g. example.com for lab.example.com).
func CheckCloudflare(ctx context.Context, client *http.Client, token, domain string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", &CheckError{"Enter your Cloudflare API token."}
	}
	var verify struct {
		Success bool `json:"success"`
		Result  struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	status, err := cfGet(ctx, client, token, "/user/tokens/verify", &verify)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden || !verify.Success {
		return "", &CheckError{"Cloudflare didn't accept that token. Check you copied all of it."}
	}
	if verify.Result.Status != "active" {
		return "", &CheckError{fmt.Sprintf("That token is %s, not active. Create a new one in Cloudflare.", verify.Result.Status)}
	}

	// The zone is the domain itself or one of its parents
	// (lab.example.com usually lives in the example.com zone).
	labels := strings.Split(strings.Trim(domain, "."), ".")
	for i := 0; i+2 <= len(labels); i++ {
		zone := strings.Join(labels[i:], ".")
		var zones struct {
			Success bool              `json:"success"`
			Result  []json.RawMessage `json:"result"`
		}
		if _, err := cfGet(ctx, client, token, "/zones?name="+url.QueryEscape(zone), &zones); err != nil {
			return "", err
		}
		if zones.Success && len(zones.Result) > 0 {
			return zone, nil
		}
	}
	return "", &CheckError{fmt.Sprintf("That token can't see a Cloudflare zone for %s. "+
		"Make sure the domain is in your Cloudflare account and the token has Zone → Zone → Read "+
		"and Zone → DNS → Edit permissions for it.", domain)}
}

func cfGet(ctx context.Context, client *http.Client, token, path string, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CloudflareAPI+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			// Don't wrap the raw error: it may repeat the request URL, which
			// is fine, but keep the message plain for the user.
			return 0, &CheckError{"Couldn't reach Cloudflare. Check this machine's internet connection and try again."}
		}
		return 0, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && resp.StatusCode == http.StatusOK {
		return resp.StatusCode, fmt.Errorf("cloudflare %s: %w", path, err)
	}
	return resp.StatusCode, nil
}
