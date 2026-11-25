// Package labdns is Roostack's own DNS server for the home network and
// the lab's VPN devices (SPEC.md §6.5.3). It answers the lab's names with
// the home address of the server that serves each, and passes every other
// question on to the DNS servers the server's own network gave it.
//
// It runs as its own service, roostack-dns (`roostack dns`), apart from
// Roostack's daemon: it never sees Roostack's state or secrets, only a
// names file the daemon writes (Config), and it answers the daemon's
// questions for the check on Your account on a Unix socket (Client).
// systemd starts it while the names file is there (roostack-dns.path),
// and it stops by itself once the file is gone.
package labdns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/fsutil"
)

// Where its files are. The names file is under Roostack's state, which
// only the daemon writes; the socket and the clients it counts are the
// service's own (RuntimeDirectory and StateDirectory in its unit).
const (
	NamesFile = "/var/lib/roostack/dns/names.json"
	Socket    = "/run/roostack-dns/dns.sock"
	StateDir  = "/var/lib/roostack-dns"
)

// Port is the host port it listens on. The firewall sends the home
// network's and the lab's VPN devices' port 53 to it, as for a DNS
// service's outside_port (Pi-hole's is 5053, AdGuard Home's 5054).
const Port = 5055

// Fallback is the public DNS server it passes questions to when the
// server's own network gives none it can use (Quad9: a non-profit that
// keeps no addresses).
const Fallback = "9.9.9.9"

// CheckPrefix starts the names the check on Your account looks up
// (check-<token>.<domain>): nobody has asked for one before, so whether
// the lab's DNS was asked for it says whether a device uses it.
const CheckPrefix = "check-"

// Config is the names file: what the daemon tells the DNS server.
type Config struct {
	// Domain is the lab's, for the check's names.
	Domain string `json:"domain"`
	// Names are the lab's names it answers itself, each with the IPv4
	// address it answers (lower case, no trailing dot).
	Names map[string]string `json:"names,omitempty"`
	// CheckAddr is what the check's names are answered with, so that
	// they work while the internet is down ("" passes them on).
	CheckAddr string `json:"check_addr,omitempty"`
	// Fallback is the DNS server used when the server's own give none it
	// can use (Fallback when empty).
	Fallback string `json:"fallback,omitempty"`
}

// Write saves the names file where the DNS server reads it: atomically,
// so that it never reads half a file.
func Write(path string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteFile(path, append(b, '\n'), 0o644)
}

// Remove deletes the names file, which stops the DNS server on this
// server. Gone already is fine.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Read loads the names file.
func Read(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Status is how the DNS server is doing, for Settings.
type Status struct {
	// Upstreams are the DNS servers it passes questions to now.
	Upstreams []string `json:"upstreams,omitempty"`
	// Fallback: they're the public fallback, for Why.
	Fallback bool   `json:"fallback,omitempty"`
	Why      string `json:"why,omitempty"`
	// Devices is how many home addresses asked it something in the last
	// 7 days.
	Devices int       `json:"devices"`
	Since   time.Time `json:"since"`
}

// Client asks the DNS server on this server, over its socket.
type Client struct {
	// Socket is its path (Socket when empty).
	Socket string
}

func (c Client) http() *http.Client {
	sock := c.Socket
	if sock == "" {
		sock = Socket
	}
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}}
}

func (c Client) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://dns"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("the lab's DNS server isn't answering on this server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("the lab's DNS server answered %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// Status asks how the DNS server is doing.
func (c Client) Status(ctx context.Context) (Status, error) {
	var st Status
	err := c.get(ctx, "/status", &st)
	return st, err
}

// Seen says whether the DNS server was asked for name in the last ten
// minutes (a check's name, CheckName).
func (c Client) Seen(ctx context.Context, name string) (bool, error) {
	var v struct {
		Seen bool `json:"seen"`
	}
	err := c.get(ctx, "/seen?name="+url.QueryEscape(name), &v)
	return v.Seen, err
}

// CheckName is the name a check with token looks up.
func CheckName(token, domain string) string {
	return CheckPrefix + token + "." + domain
}
