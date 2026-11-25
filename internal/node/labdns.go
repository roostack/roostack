package node

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"roostack.cloud/roostack/internal/labdns"
)

// Roostack's own DNS server on the server that runs it (SPEC.md §6.5.3):
// the controller asks it how it's doing for Settings, and whether it was
// asked a check's name for Your account.

// LabDNS is the DNS server on a server.
type LabDNS interface {
	LabDNSStatus(ctx context.Context) (labdns.Status, error)
	LabDNSSeen(ctx context.Context, name string) (bool, error)
}

// LabDNS is this server's, through its socket.
func (n *Local) LabDNS() LabDNS { return n }

// LabDNS asks the server.
func (r *Remote) LabDNS() LabDNS { return r }

var (
	_ LabDNS = (*Local)(nil)
	_ LabDNS = (*Remote)(nil)
)

func (n *Local) LabDNSStatus(ctx context.Context) (labdns.Status, error) {
	return n.DNSClient.Status(ctx)
}

func (n *Local) LabDNSSeen(ctx context.Context, name string) (bool, error) {
	return n.DNSClient.Seen(ctx, name)
}

func (r *Remote) LabDNSStatus(ctx context.Context) (labdns.Status, error) {
	var out labdns.Status
	err := r.call(ctx, http.MethodGet, "/v1/lab-dns", nil, &out)
	return out, err
}

func (r *Remote) LabDNSSeen(ctx context.Context, name string) (bool, error) {
	var out struct {
		Seen bool `json:"seen"`
	}
	err := r.call(ctx, http.MethodGet, "/v1/lab-dns/seen?"+url.Values{"name": {name}}.Encode(), nil, &out)
	return out.Seen, err
}

// labDNSRoutes serves LabDNS on the lab API.
func (s *Server) labDNSRoutes(handle handleFunc) {
	dns := capRoute(handle, offered(s.Node.LabDNS))
	dns("GET /v1/lab-dns", func(d LabDNS, r *http.Request) (any, error) { return d.LabDNSStatus(r.Context()) })
	dns("GET /v1/lab-dns/seen", func(d LabDNS, r *http.Request) (any, error) {
		name := r.URL.Query().Get("name")
		if name == "" {
			return nil, errors.New("no name to look for")
		}
		seen, err := d.LabDNSSeen(r.Context(), name)
		return map[string]bool{"seen": seen}, err
	})
}

// applyLabDNS writes the names file where Roostack's own DNS server
// should run on this server, which starts it (roostack-dns.path), and
// deletes it elsewhere, which stops it.
func (n *Local) applyLabDNS(path string, c *labdns.Config, printf func(string, ...any)) error {
	if c == nil {
		if n.labDNSOn {
			printf("Roostack's own DNS server will stop on this server")
		}
		n.labDNSOn = false
		return labdns.Remove(path)
	}
	old, err := labdns.Read(path)
	if err == nil && sameDNSConfig(old, *c) {
		n.labDNSOn = true
		return nil
	}
	if !n.labDNSOn {
		printf("Roostack's own DNS server will answer the lab's names on this server (%d of them)", len(c.Names))
	}
	n.labDNSOn = true
	return labdns.Write(path, *c)
}

func sameDNSConfig(a, b labdns.Config) bool {
	if a.Domain != b.Domain || a.CheckAddr != b.CheckAddr || a.Fallback != b.Fallback || len(a.Names) != len(b.Names) {
		return false
	}
	for k, v := range a.Names {
		if b.Names[k] != v {
			return false
		}
	}
	return true
}
