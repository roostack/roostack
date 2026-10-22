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

	"roostack.cloud/roostack/awssig"
)

// Roostack talks to each provider's public HTTP API directly, using only
// the standard library, to check credentials and manage records. (Caddy
// uses its own plugins for certificates.)
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

// conn is one zone at a provider, reached with a lab's credentials (v:
// the provider's fields, by key).
type conn struct {
	hc   *http.Client
	v    map[string]string
	zone string
}

// statusError is an API's answer other than success.
type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string { return e.msg }

// isNotFound lets get and remove functions treat a 404 as "no record".
func isNotFound(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.code == http.StatusNotFound
}

// call makes one API request. body is JSON-encoded unless it is []byte.
// On success, a JSON or XML response is decoded into out (if non-nil).
func (c conn) call(ctx context.Context, method, u string, header map[string]string, body, out any) error {
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
	resp, err := c.hc.Do(req)
	if err != nil {
		return reachError(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return &statusError{resp.StatusCode, fmt.Sprintf("%s (%s)", resp.Status, snippet(data))}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
		return xml.Unmarshal(data, out)
	}
	return json.Unmarshal(data, out)
}

// signedCall is call for Route 53, signing the request with SigV4.
func (c conn) signedCall(ctx context.Context, method, u string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/xml")
	}
	awssig.Sign(req, body, c.v["access_key_id"], c.v["secret_access_key"], "us-east-1", "route53", time.Now())
	resp, err := c.hc.Do(req)
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
			return &statusError{resp.StatusCode, fmt.Sprintf("%s: %s", e.Error.Code, e.Error.Message)}
		}
		return &statusError{resp.StatusCode, fmt.Sprintf("%s (%s)", resp.Status, snippet(data))}
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

// publicIP asks an external service for this server's public IP (used
// by Namecheap, whose API needs it).
func publicIP(ctx context.Context, hc *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", publicIPURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("couldn't find this lab's public IP address; enter it instead (%v)", reachError(err))
	}
	defer resp.Body.Close()
	var b [64]byte
	n, _ := resp.Body.Read(b[:])
	return strings.TrimSpace(string(b[:n])), nil
}
