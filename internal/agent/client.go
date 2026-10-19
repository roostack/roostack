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

// Client calls roostack-agent from the unprivileged roostack service.
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

// ApplyFirewall replaces Roostack's firewall rules.
func (c *Client) ApplyFirewall(ctx context.Context, fw Firewall) error {
	return c.call(ctx, http.MethodPut, "/v1/firewall", fw, nil)
}

// AllowWireGuardJoin opens WireGuard's port until the firewall is next
// applied, for a join where the controller dials this server.
func (c *Client) AllowWireGuardJoin(ctx context.Context) error {
	return c.call(ctx, http.MethodPost, "/v1/firewall/wireguard-join", nil, nil)
}

// SetTimezone sets the host's time zone (an IANA name like Europe/Berlin).
func (c *Client) SetTimezone(ctx context.Context, name string) error {
	return c.call(ctx, http.MethodPut, "/v1/timezone", map[string]string{"timezone": name}, nil)
}

// RunUpdates installs available OS updates, writing their output to w as
// it arrives. A series first moves Roostack to that release series
// (SPEC.md §7); "" leaves it on its own.
func (c *Client) RunUpdates(ctx context.Context, series string, w io.Writer) error {
	var body io.Reader = http.NoBody
	if series != "" {
		data, err := json.Marshal(map[string]string{"series": series})
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://roostack-agent/v1/updates/run", body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("roostack-agent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("roostack-agent: %s: %s", resp.Status, e.Error)
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Line  *string `json:"line"`
			Done  bool    `json:"done"`
			Error string  `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			return fmt.Errorf("%w: %w", ErrUpdatesCut, err)
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

// ErrUpdatesCut means the agent stopped sending a run of updates before
// saying how it ended: it restarted, as upgrading Roostack makes it do.
// The run carries on without it; UpdatesStatus says how it ends.
var ErrUpdatesCut = errors.New("roostack-agent stopped following the OS updates")

// UpdatesStatus reads the latest run of OS updates (agent.UpdatesRun).
func (c *Client) UpdatesStatus(ctx context.Context) (UpdatesRun, error) {
	var run UpdatesRun
	err := c.call(ctx, http.MethodGet, "/v1/updates/last", nil, &run)
	return run, err
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
	req, err := http.NewRequestWithContext(ctx, method, "http://roostack-agent"+path, &body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("roostack-agent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("roostack-agent %s: %s: %s", path, resp.Status, e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// ApplyWireGuard configures Roostack's WireGuard interface and returns this
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

// ApplyLinks sets this server's direct links to other servers.
func (c *Client) ApplyLinks(ctx context.Context, l Links) error {
	return c.call(ctx, http.MethodPut, "/v1/wireguard/links", l, nil)
}

// RemoveWireGuard takes one of Roostack's WireGuard interfaces down and
// deletes it.
func (c *Client) RemoveWireGuard(ctx context.Context, iface string) error {
	return c.call(ctx, http.MethodDelete, "/v1/wireguard?interface="+url.QueryEscape(iface), nil, nil)
}

// Drives lists the places backups can go.
func (c *Client) Drives(ctx context.Context) ([]Drive, error) {
	var d []Drive
	err := c.call(ctx, http.MethodGet, "/v1/drives", nil, &d)
	return d, err
}

// UseDrive readies a drive for backups (mounting it for good if needed).
func (c *Client) UseDrive(ctx context.Context, id string) (UsedDrive, error) {
	var u UsedDrive
	err := c.call(ctx, http.MethodPost, "/v1/drives/use", map[string]string{"id": id}, &u)
	return u, err
}

// EraseDrive erases a disk and prepares it for backups.
func (c *Client) EraseDrive(ctx context.Context, disk, label string) error {
	return c.call(ctx, http.MethodPost, "/v1/drives/erase", map[string]string{"disk": disk, "label": label}, nil)
}

// Libraries lists the media libraries mounted on this server.
func (c *Client) Libraries(ctx context.Context) ([]LibraryStatus, error) {
	var libs []LibraryStatus
	err := c.call(ctx, http.MethodGet, "/v1/libraries", nil, &libs)
	return libs, err
}

// LibraryFolders lists the folders where a media library could be, and
// how many files there Roostack could read.
func (c *Client) LibraryFolders(ctx context.Context, l Library) (FolderList, error) {
	var list FolderList
	err := c.call(ctx, http.MethodPost, "/v1/libraries/folders", l, &list)
	return list, err
}

// MountLibrary mounts a media library for good (replacing one of its name).
func (c *Client) MountLibrary(ctx context.Context, l Library) error {
	return c.call(ctx, http.MethodPut, "/v1/libraries/"+url.PathEscape(l.Name), l, nil)
}

// UnmountLibrary unmounts a media library and forgets it.
func (c *Client) UnmountLibrary(ctx context.Context, name string) error {
	return c.call(ctx, http.MethodDelete, "/v1/libraries/"+url.PathEscape(name), nil, nil)
}

// SetLibraryViews sets the media libraries a service may read.
func (c *Client) SetLibraryViews(ctx context.Context, service string, libraries []string) error {
	return c.call(ctx, http.MethodPut, "/v1/library-views/"+url.PathEscape(service), map[string][]string{"libraries": libraries}, nil)
}

// RemoveLibraryViews removes a service's library views.
func (c *Client) RemoveLibraryViews(ctx context.Context, service string) error {
	return c.call(ctx, http.MethodDelete, "/v1/library-views/"+url.PathEscape(service), nil, nil)
}

// PrepareBackupFolder makes a roostack folder that Roostack may write to
// in a backup folder that exists, and returns it.
func (c *Client) PrepareBackupFolder(ctx context.Context, dir string) (string, error) {
	var out struct {
		Dir string `json:"dir"`
	}
	err := c.call(ctx, http.MethodPost, "/v1/backup-folder", map[string]string{"dir": dir}, &out)
	return out.Dir, err
}

// HostChanges checks the host for changes Roostack didn't make.
func (c *Client) HostChanges(ctx context.Context) (HostChanges, error) {
	var hc HostChanges
	err := c.call(ctx, http.MethodGet, "/v1/host/changes", nil, &hc)
	return hc, err
}

// SSHStatus reads what the server's SSH server does (SPEC.md §6.10).
func (c *Client) SSHStatus(ctx context.Context) (SSHStatus, error) {
	var st SSHStatus
	if err := c.call(ctx, http.MethodGet, "/v1/ssh", nil, &st); err != nil {
		return st, err
	}
	if st.Error != "" {
		return st, errors.New(st.Error)
	}
	return st, nil
}

// SetSSHKeysOnly turns SSH's keys-only settings on or off.
func (c *Client) SetSSHKeysOnly(ctx context.Context, on bool) error {
	return c.call(ctx, http.MethodPut, "/v1/ssh", map[string]bool{"keys_only": on}, nil)
}

// SaveWireGuardKeys copies Roostack's WireGuard keys into KeysFile (readable
// by Roostack only), for the backup.
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
