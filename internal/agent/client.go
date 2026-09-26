package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Client calls mylab-agent from the unprivileged mylab service.
type Client struct {
	http *http.Client
}

// NewClient connects to the agent's Unix socket.
func NewClient(socket string) *Client {
	return &Client{http: &http.Client{
		// Firewall and package operations can take a while.
		Timeout: 5 * time.Minute,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
	}}
}

// Health checks the agent is reachable and returns its version.
func (c *Client) Health(ctx context.Context) (string, error) {
	var resp struct {
		Version string `json:"version"`
	}
	err := c.call(ctx, http.MethodGet, "/v1/health", nil, &resp)
	return resp.Version, err
}

// SetUnprivilegedPortStart lets rootless containers bind ports 80/443.
func (c *Client) SetUnprivilegedPortStart(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/v1/ports/unprivileged-start", nil, nil)
}

// ApplyFirewall replaces MyLab's firewall rules.
func (c *Client) ApplyFirewall(ctx context.Context, fw Firewall) error {
	return c.call(ctx, http.MethodPut, "/v1/firewall", fw, nil)
}

// SetTimezone sets the host's time zone (an IANA name like Europe/Berlin).
func (c *Client) SetTimezone(ctx context.Context, name string) error {
	return c.call(ctx, http.MethodPut, "/v1/timezone", map[string]string{"timezone": name}, nil)
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return err
		}
	}
	// The host part is ignored: the transport always dials the socket.
	req, err := http.NewRequestWithContext(ctx, method, "http://mylab-agent"+path, &body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mylab-agent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("mylab-agent %s: %s: %s", path, resp.Status, e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
