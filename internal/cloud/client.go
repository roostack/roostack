package cloud

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/config"
)

// ErrUnlinked: Cloud doesn't accept the lab's certificate (the lab was
// unlinked, or its certificate expired).
var ErrUnlinked = errors.New("Roostack Cloud doesn't know this lab any more")

// Error is an error Cloud reported, with its sentence for people.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// refusal is Cloud's message when it knows the lab but refuses what it
// asked (403, or 423 while the account is suspended): the link is kept.
func refusal(err error) (string, bool) {
	var e *Error
	if errors.As(err, &e) && (e.Status == http.StatusForbidden || e.Status == http.StatusLocked) {
		return e.Message, true
	}
	return "", false
}

// Unreachable wraps a failure to reach Cloud at all (network, TLS).
type Unreachable struct{ Err error }

func (e *Unreachable) Error() string { return "Roostack Cloud can't be reached: " + e.Err.Error() }
func (e *Unreachable) Unwrap() error { return e.Err }

// client makes requests to Cloud: anonymous ones (linking, names) and
// the lab's own, with its certificate. They use separate connections, so
// a connection opened without the certificate is never reused for a
// request that needs it.
type client struct {
	base   *url.URL
	anon   *http.Client
	authed *http.Client
	authT  *http.Transport
	// log records each request (never its body).
	log func(method, path string, status int, err error)
}

// newClient reaches Cloud at cfg's address; cert gives the lab's current
// certificate (nil before linking).
func newClient(cfg config.Cloud, cert func() *tls.Certificate) (*client, error) {
	base, err := url.Parse(strings.TrimRight(cfg.Address(), "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, fmt.Errorf("cloud url %q: must be an https:// address", cfg.Address())
	}
	var roots *x509.CertPool
	if cfg.CAFile != "" {
		data, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("cloud ca_file: %w", err)
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("cloud ca_file %s: no certificates in it", cfg.CAFile)
		}
	}
	transport := func(get func(*tls.CertificateRequestInfo) (*tls.Certificate, error)) *http.Transport {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, GetClientCertificate: get}
		return t
	}
	authT := transport(func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		if c := cert(); c != nil {
			return c, nil
		}
		return &tls.Certificate{}, nil
	})
	return &client{base: base,
		anon:   &http.Client{Timeout: 20 * time.Second, Transport: transport(nil)},
		authed: &http.Client{Timeout: 20 * time.Second, Transport: authT},
		authT:  authT}, nil
}

// do sends in (if any) as JSON and decodes the answer into out (if any).
func (c *client) do(ctx context.Context, authed bool, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	hc := c.anon
	if authed {
		hc = c.authed
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A certificate Cloud refuses can end the TLS handshake itself.
		if authed && refusedCert(err) {
			c.record(method, path, 0, ErrUnlinked)
			return ErrUnlinked
		}
		c.record(method, path, 0, err)
		return &Unreachable{Err: err}
	}
	defer resp.Body.Close()
	c.record(method, path, resp.StatusCode, nil)
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &Unreachable{Err: err}
	}
	// Only 401 means Cloud doesn't know the certificate. A 403 or 423
	// (Locked: the account is suspended) refuses the request but keeps
	// the lab linked, so it's an Error with Cloud's message (cloudapi).
	if authed && resp.StatusCode == http.StatusUnauthorized {
		return ErrUnlinked
	}
	if resp.StatusCode >= 300 {
		var e cloudapi.ErrorBody
		if json.Unmarshal(data, &e) != nil || e.Error == "" {
			e.Error = fmt.Sprintf("Roostack Cloud answered %s", resp.Status)
		}
		return &Error{Status: resp.StatusCode, Message: e.Error}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("Roostack Cloud's answer to %s %s: %w", method, path, err)
		}
	}
	return nil
}

// refusedCert reports whether the server ended the TLS handshake because
// of the lab's certificate. Over TCP, Go reports the server's alert only as
// text ("remote error: tls: unknown certificate authority"; its type
// isn't exported, and tls.AlertError is for QUIC), seen on a dev VM
// (2026-10-07) when the lab's certificate came from an earlier Cloud.
func refusedCert(err error) bool {
	msg := err.Error()
	for _, alert := range []string{"bad certificate", "certificate expired", "certificate revoked",
		"unknown certificate authority", "certificate required"} {
		if strings.Contains(msg, "remote error: tls: "+alert) {
			return true
		}
	}
	return false
}

func (c *client) record(method, path string, status int, err error) {
	if c.log != nil {
		c.log(method, path, status, err)
	}
}

// renewed makes new requests use the lab's current certificate (renewed,
// new after linking again, or none): connections opened with another are
// closed, since TLS sends the certificate only when a connection opens.
func (c *client) renewed() { c.authT.CloseIdleConnections() }
