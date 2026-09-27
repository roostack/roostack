package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mylab/mylab/internal/awssig"
)

// Each provider's check talks to its public HTTP API directly, using only
// the standard library. (Caddy uses its own plugins for certificates; these
// calls are only MyLab's check before saving credentials.)
//
// API endpoints and request shapes follow the providers' documentation and
// the libdns implementations Caddy's plugins use. Base URLs are variables
// so tests can point them at a fake server.
var (
	cloudflareAPI = "https://api.cloudflare.com/client/v4"
	route53API    = "https://route53.amazonaws.com"
	porkbunAPI    = "https://api.porkbun.com/api/json/v3"
	gandiAPI      = "https://api.gandi.net/v5/livedns"
	godaddyAPI    = "https://api.godaddy.com"
	namecheapAPI  = "https://api.namecheap.com/xml.response"
	spaceshipAPI  = "https://spaceship.dev/api"
	// publicIPURL finds this lab's public IP for Namecheap when it isn't
	// given (the same service Caddy's Namecheap plugin uses).
	publicIPURL = "https://icanhazip.com"
)

// probe creates a TXT record `name` (relative to zone) with value, and
// returns a function that deletes it. A probe that can't safely write
// (Namecheap) checks read access instead and returns a nil undo.
type probe func(ctx context.Context, hc *http.Client, v map[string]string, zone, name, value string) (undo func(context.Context) error, err error)

// call makes one API request. body is JSON-encoded unless it is []byte.
// On success, a JSON or XML response is decoded into out (if non-nil).
func call(ctx context.Context, hc *http.Client, method, u string, header map[string]string, body, out any) error {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(data)
		header = withDefault(header, "Content-Type", "application/json")
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return reachError(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s (%s)", resp.Status, snippet(data))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
		return xml.Unmarshal(data, out)
	}
	return json.Unmarshal(data, out)
}

// signedCall is call for AWS, signing the request with SigV4.
func signedCall(ctx context.Context, hc *http.Client, method, u string, body []byte, key, secret string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/xml")
	}
	awssig.Sign(req, body, key, secret, "us-east-1", "route53", time.Now())
	resp, err := hc.Do(req)
	if err != nil {
		return reachError(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Code, Message string } `xml:"Error"`
		}
		if xml.Unmarshal(data, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("%s: %s", e.Error.Code, e.Error.Message)
		}
		return fmt.Errorf("%s (%s)", resp.Status, snippet(data))
	}
	if out != nil {
		return xml.Unmarshal(data, out)
	}
	return nil
}

// reachError hides the request URL (Namecheap puts the API key in it).
func reachError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return errors.New("the provider didn't answer in time")
		}
		return fmt.Errorf("couldn't reach the provider: %v", ue.Err)
	}
	return err
}

// snippet shortens a response body for error messages.
func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		s = "no details"
	}
	return s
}

func withDefault(h map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for hk, hv := range h {
		out[hk] = hv
	}
	return out
}

func cloudflareProbe(ctx context.Context, hc *http.Client, v map[string]string, zone, name, value string) (func(context.Context) error, error) {
	auth := map[string]string{"Authorization": "Bearer " + v["api_token"]}
	var zones struct {
		Result []struct{ ID string } `json:"result"`
	}
	if err := call(ctx, hc, "GET", cloudflareAPI+"/zones?name="+url.QueryEscape(zone), auth, nil, &zones); err != nil {
		return nil, err
	}
	if len(zones.Result) == 0 {
		return nil, errors.New("zone not found (the token needs Zone → Zone → Read)")
	}
	zoneID := zones.Result[0].ID
	var created struct {
		Result struct{ ID string } `json:"result"`
	}
	rec := map[string]any{"type": "TXT", "name": name + "." + zone, "content": `"` + value + `"`, "ttl": 120}
	if err := call(ctx, hc, "POST", cloudflareAPI+"/zones/"+zoneID+"/dns_records", auth, rec, &created); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		return call(ctx, hc, "DELETE", cloudflareAPI+"/zones/"+zoneID+"/dns_records/"+created.Result.ID, auth, nil, nil)
	}, nil
}

func route53Probe(ctx context.Context, hc *http.Client, v map[string]string, zone, name, value string) (func(context.Context) error, error) {
	key, secret := v["access_key_id"], v["secret_access_key"]
	var list struct {
		Zones []struct {
			ID   string `xml:"Id"`
			Name string `xml:"Name"`
		} `xml:"HostedZones>HostedZone"`
	}
	u := route53API + "/2013-04-01/hostedzonesbyname?dnsname=" + url.QueryEscape(zone) + "&maxitems=1"
	if err := signedCall(ctx, hc, "GET", u, nil, key, secret, &list); err != nil {
		return nil, err
	}
	if len(list.Zones) == 0 || list.Zones[0].Name != zone+"." {
		return nil, errors.New("no hosted zone with that name")
	}
	change := func(action string) []byte {
		return []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<ChangeResourceRecordSetsRequest xmlns="https://route53.amazonaws.com/doc/2013-04-01/">` +
			`<ChangeBatch><Changes><Change><Action>` + action + `</Action><ResourceRecordSet>` +
			`<Name>` + name + "." + zone + `.</Name><Type>TXT</Type><TTL>60</TTL>` +
			`<ResourceRecords><ResourceRecord><Value>"` + value + `"</Value></ResourceRecord></ResourceRecords>` +
			`</ResourceRecordSet></Change></Changes></ChangeBatch></ChangeResourceRecordSetsRequest>`)
	}
	// The zone ID comes back as "/hostedzone/Z123".
	rrset := route53API + "/2013-04-01" + list.Zones[0].ID + "/rrset"
	if err := signedCall(ctx, hc, "POST", rrset, change("CREATE"), key, secret, nil); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		return signedCall(ctx, hc, "POST", rrset, change("DELETE"), key, secret, nil)
	}, nil
}

// porkbunResult is Porkbun's response envelope (it reports errors in the
// body, sometimes with HTTP 200).
type porkbunResult struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	ID      json.RawMessage `json:"id"`
}

func porkbunProbe(ctx context.Context, hc *http.Client, v map[string]string, zone, name, value string) (func(context.Context) error, error) {
	creds := map[string]any{"apikey": v["api_key"], "secretapikey": v["api_secret_key"]}
	body := map[string]any{"name": name, "type": "TXT", "content": value, "ttl": "600"}
	for k, val := range creds {
		body[k] = val
	}
	var res porkbunResult
	if err := call(ctx, hc, "POST", porkbunAPI+"/dns/create/"+zone, nil, body, &res); err != nil {
		return nil, err
	}
	if res.Status != "SUCCESS" {
		return nil, errors.New(res.Message)
	}
	id := strings.Trim(string(res.ID), `"`)
	return func(ctx context.Context) error {
		var del porkbunResult
		if err := call(ctx, hc, "POST", porkbunAPI+"/dns/delete/"+zone+"/"+id, nil, creds, &del); err != nil {
			return err
		}
		if del.Status != "SUCCESS" {
			return errors.New(del.Message)
		}
		return nil
	}, nil
}

func gandiProbe(ctx context.Context, hc *http.Client, v map[string]string, zone, name, value string) (func(context.Context) error, error) {
	auth := map[string]string{"Authorization": "Bearer " + v["bearer_token"]}
	u := gandiAPI + "/domains/" + zone + "/records/" + name + "/TXT"
	if err := call(ctx, hc, "PUT", u, auth, map[string]any{"rrset_values": []string{value}, "rrset_ttl": 300}, nil); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error { return call(ctx, hc, "DELETE", u, auth, nil, nil) }, nil
}

func godaddyProbe(ctx context.Context, hc *http.Client, v map[string]string, zone, name, value string) (func(context.Context) error, error) {
	auth := map[string]string{"Authorization": "sso-key " + v["api_key"] + ":" + v["api_secret"]}
	u := godaddyAPI + "/v1/domains/" + zone + "/records/TXT/" + name
	if err := call(ctx, hc, "PUT", u, auth, []map[string]any{{"data": value, "ttl": 600}}, nil); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error { return call(ctx, hc, "DELETE", u, auth, nil, nil) }, nil
}

func spaceshipProbe(ctx context.Context, hc *http.Client, v map[string]string, zone, name, value string) (func(context.Context) error, error) {
	auth := map[string]string{"X-API-Key": v["api_key"], "X-API-Secret": v["api_secret"]}
	u := spaceshipAPI + "/v1/dns/records/" + zone
	item := map[string]any{"type": "TXT", "name": name, "value": value, "ttl": 300}
	if err := call(ctx, hc, "PUT", u, auth, map[string]any{"force": false, "items": []any{item}}, nil); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		return call(ctx, hc, "DELETE", u, auth, []any{item}, nil)
	}, nil
}

// namecheapProbe only reads the zone's records. Namecheap's API can only
// replace all of a domain's records at once, so a test write would briefly
// rewrite the whole zone. Reading still proves the key, username, IP
// whitelist and access to the domain.
func namecheapProbe(ctx context.Context, hc *http.Client, v map[string]string, zone, _, _ string) (func(context.Context) error, error) {
	if !strings.Contains(zone, ".") {
		return nil, errors.New("not a domain")
	}
	if _, err := namecheapRequest(ctx, hc, v, zone, "namecheap.domains.dns.getHosts", nil); err != nil {
		ip := v["client_ip"]
		if ip == "" {
			ip, _ = publicIP(ctx, hc)
		}
		return nil, fmt.Errorf("%w (this lab's IP is %s)", err, ip)
	}
	return nil, nil
}
