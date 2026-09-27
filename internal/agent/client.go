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
	"net/url"
	"os"
	"time"
)

// Client calls mylab-agent from the unprivileged mylab service.
type Client struct {
	http *http.Client
	// KeysFile is where SaveWireGuardKeys keeps the WireGuard keys for
	// backups; ApplyWireGuard offers the saved key to an interface that
	// has none (a restored lab). Optional.
	KeysFile string
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

// ApplyWireGuard configures MyLab's WireGuard interface and returns this
// server's public key.
func (c *Client) ApplyWireGuard(ctx context.Context, wg WireGuard) (string, error) {
	if c.KeysFile != "" && wg.JoinKey == "" && !wg.NewKey {
		if data, err := os.ReadFile(c.KeysFile); err == nil {
			var keys map[string]string
			if json.Unmarshal(data, &keys) == nil {
				wg.SavedKey = keys[wg.Iface()]
			}
		}
	}
	var resp struct {
		PublicKey string `json:"public_key"`
	}
	err := c.call(ctx, http.MethodPut, "/v1/wireguard", wg, &resp)
	return resp.PublicKey, err
}

// WireGuardStatus reads a WireGuard interface's configuration.
func (c *Client) WireGuardStatus(ctx context.Context, iface string) (WireGuardStatus, error) {
	var st WireGuardStatus
	err := c.call(ctx, http.MethodGet, "/v1/wireguard?interface="+url.QueryEscape(iface), nil, &st)
	return st, err
}

// RemoveWireGuard takes one of MyLab's WireGuard interfaces down and
// deletes it.
func (c *Client) RemoveWireGuard(ctx context.Context, iface string) error {
	return c.call(ctx, http.MethodDelete, "/v1/wireguard?interface="+url.QueryEscape(iface), nil, nil)
}

// HostChanges checks the host for changes MyLab didn't make.
func (c *Client) HostChanges(ctx context.Context) (HostChanges, error) {
	var hc HostChanges
	err := c.call(ctx, http.MethodGet, "/v1/host/changes", nil, &hc)
	return hc, err
}

// SaveWireGuardKeys copies MyLab's WireGuard keys into KeysFile (readable
// by MyLab only), for the backup.
func (c *Client) SaveWireGuardKeys(ctx context.Context) error {
	var keys map[string]string
	if err := c.call(ctx, http.MethodGet, "/v1/wireguard/keys", nil, &keys); err != nil {
		return err
	}
	if len(keys) == 0 {
		os.Remove(c.KeysFile)
		return nil
	}
	data, _ := json.Marshal(keys)
	if err := os.WriteFile(c.KeysFile+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(c.KeysFile+".tmp", c.KeysFile)
}
