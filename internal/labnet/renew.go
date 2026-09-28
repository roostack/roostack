package labnet

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"roostack.cloud/roostack/internal/store"
)

// Certificates on Roostack's network (SPEC.md §10.3): the controller records
// the one certificate each server may use, so removing a server revokes
// its certificate, and servers renew theirs before they expire.

// errRevoked is a certificate the controller no longer accepts.
var errRevoked = errors.New("this server's certificate isn't accepted by the lab (was it removed?)")

// Authorize checks a lab API caller on the controller: a server that is
// still in the lab, using the certificate recorded for it (or the one
// that replaced it, until its first use). It returns the server's ID.
func (h *Hub) Authorize(r *http.Request) (string, error) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		return "", errors.New("no lab certificate")
	}
	leaf := r.TLS.VerifiedChains[0][0]
	id, serial := leaf.Subject.CommonName, Serial(leaf)
	s, ok, err := h.server(r.Context(), id)
	if err != nil {
		return "", err
	}
	switch {
	case !ok:
		return "", errRevoked
	case s.CertSerial == "": // joined before serials were kept
		return id, nil
	case serial == s.CertSerial:
		if s.PrevSerial != "" { // the new certificate is in use: retire the old one
			h.Store.SetServerCert(r.Context(), id, s.CertSerial, "", s.CertExpires)
		}
		return id, nil
	case serial == s.PrevSerial:
		return id, nil
	}
	return "", errRevoked
}

func (h *Hub) server(ctx context.Context, id string) (store.Server, bool, error) {
	servers, err := h.Store.Servers(ctx)
	if err != nil {
		return store.Server{}, false, err
	}
	for _, s := range servers {
		if s.ID == id {
			return s, true, nil
		}
	}
	return store.Server{}, false, nil
}

// Require serves a lab API route only to servers Authorize accepts.
func (h *Hub) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := h.Authorize(r); err != nil {
			h.Log.Warn("lab API request refused", "remote", r.RemoteAddr, "path", r.URL.Path, "err", err)
			WriteError(w, http.StatusForbidden, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RenewRequest asks the controller for a new certificate.
type RenewRequest struct {
	CSR []byte `json:"csr"`
}

// RenewHandler serves POST /v1/renew (behind Require).
func (h *Hub) RenewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req RenewRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		leaf := r.TLS.VerifiedChains[0][0]
		resp, err := h.Renew(r.Context(), leaf.Subject.CommonName, Serial(leaf), req.CSR)
		if err != nil {
			h.Log.Error("renewing a server's certificate", "id", leaf.Subject.CommonName, "err", err)
			WriteError(w, http.StatusInternalServerError, err)
			return
		}
		WriteJSON(w, http.StatusOK, resp)
	}
}

// Renew signs a new certificate for a server that called with the
// certificate numbered used. That one stays accepted until the new one is
// first used, in case the reply is lost.
func (h *Hub) Renew(ctx context.Context, id, used string, csr []byte) (JoinResponse, error) {
	s, ok, err := h.server(ctx, id)
	if err != nil {
		return JoinResponse{}, err
	}
	if !ok {
		return JoinResponse{}, errRevoked
	}
	ca, err := LoadOrCreateCA(CADir(h.StateDir), h.LabName)
	if err != nil {
		return JoinResponse{}, err
	}
	addr, err := netip.ParseAddr(s.Addr)
	if err != nil {
		return JoinResponse{}, err
	}
	cert, c, err := ca.Issue(csr, id, addr)
	if err != nil {
		return JoinResponse{}, err
	}
	if err := h.Store.SetServerCert(ctx, id, Serial(c), used, c.NotAfter); err != nil {
		return JoinResponse{}, err
	}
	h.Log.Info("renewed a server's certificate", "id", id, "until", c.NotAfter.Format(time.DateOnly))
	return JoinResponse{Cert: cert, CA: CertPEM(ca.Cert)}, nil
}

// RenewSelf renews the controller's own certificate with its CA.
func (h *Hub) RenewSelf(_ context.Context, csr []byte) ([]byte, error) {
	ca, err := LoadOrCreateCA(CADir(h.StateDir), h.LabName)
	if err != nil {
		return nil, err
	}
	cert, _, err := ca.Issue(csr, h.Self.ID, ControllerAddr)
	return cert, err
}

// RenewFromController asks the controller for a new certificate (on a
// node), calling with the current one. url is the controller's lab API
// (ControllerURL).
func RenewFromController(url, controllerID string, id Identity) func(context.Context, []byte) ([]byte, error) {
	return func(ctx context.Context, csr []byte) ([]byte, error) {
		tlsConf, err := id.ClientTLS(controllerID)
		if err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConf}}
		body, _ := json.Marshal(RenewRequest{CSR: csr})
		var resp JoinResponse
		if err := post(ctx, client, url+"/v1/renew", body, &resp); err != nil {
			return nil, err
		}
		return resp.Cert, nil
	}
}

// ControllerURL is the controller's lab API.
func ControllerURL(m Membership) string {
	return "https://" + net.JoinHostPort(m.ControllerAddr.String(), strconv.Itoa(Port))
}

// renewCheck is how often a server checks its certificate; renewRetry
// how soon it tries again after a failure.
var renewCheck, renewRetry = 12 * time.Hour, time.Hour

// KeepRenewed renews this server's certificate when it's due, until ctx
// ends. renew signs a request (the controller's CA, directly or over the
// lab API).
func KeepRenewed(ctx context.Context, id Identity, nodeID string, renew func(context.Context, []byte) ([]byte, error), log *slog.Logger) {
	for {
		wait := renewCheck
		if err := RenewIfDue(ctx, id, nodeID, time.Now(), renew); err != nil {
			log.Error("renewing this server's lab certificate", "err", err)
			wait = renewRetry
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// RenewIfDue renews the certificate if NeedsRenewal says so.
func RenewIfDue(ctx context.Context, id Identity, nodeID string, now time.Time, renew func(context.Context, []byte) ([]byte, error)) error {
	if !id.Exists() { // no network yet
		return nil
	}
	due, err := id.NeedsRenewal(now)
	if err != nil || !due {
		return err
	}
	csr, err := id.CSR(nodeID)
	if err != nil {
		return err
	}
	certPEM, err := renew(ctx, csr)
	if err != nil {
		return err
	}
	c, err := parseCertPEM(certPEM)
	if err != nil {
		return err
	}
	if c.Subject.CommonName != nodeID {
		return fmt.Errorf("the renewed certificate names %q", c.Subject.CommonName)
	}
	return writeFile(id.certFile(), certPEM)
}

func parseCertPEM(data []byte) (*x509.Certificate, error) {
	b, _ := pem.Decode(data)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(b.Bytes)
}

// CertExpiries is when each server's certificate expires, this one's
// included (as far as the controller knows: a server that joined before
// serials were kept reports its expiry once it renews).
func (h *Hub) CertExpiries(ctx context.Context) map[string]time.Time {
	out := map[string]time.Time{}
	if c, err := (Identity{Dir: IdentityDir(h.StateDir)}).Cert(); err == nil {
		out[h.Self.ID] = c.NotAfter
	}
	servers, err := h.Store.Servers(ctx)
	if err != nil {
		return out
	}
	for _, s := range servers {
		if !s.CertExpires.IsZero() {
			out[s.ID] = s.CertExpires
		}
	}
	return out
}
