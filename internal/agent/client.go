package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		Timeout: 75 * time.Minute, // OS updates can take a while
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

// RunUpdates installs available OS updates, writing their output to w as
// it arrives.
func (c *Client) RunUpdates(ctx context.Context, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://mylab-agent/v1/updates/run", nil)
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
		return fmt.Errorf("mylab-agent: %s: %s", resp.Status, e.Error)
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Line  *string `json:"line"`
			Done  bool    `json:"done"`
			Error string  `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			return fmt.Errorf("mylab-agent: updates stopped unexpectedly: %w", err)
		}
		switch {
		case msg.Line != nil:
			fmt.Fprintln(w, *msg.Line)
		case msg.Done && msg.Error != "":
			return errors.New(msg.Error)
		case msg.Done:
			return nil
		}
	}
}

// Reboot restarts the host a few seconds after answering.
func (c *Client) Reboot(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/v1/reboot", nil, nil)
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
