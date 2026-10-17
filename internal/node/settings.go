package node

import (
	"cmp"
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/firewall"
)

// Settings are a server's own settings: its clock, who may reach it over
// SSH, and when it may restart after updates. Changing one restarts Roostack
// on that server so every part picks it up.
type Settings interface {
	// Timezones returns the server's time zone and those it can use.
	Timezones() (current string, zones []string, err error)
	// SetTimezone returns true if the zone changed (Roostack then restarts:
	// Go reads the local zone once, and schedules depend on it).
	SetTimezone(ctx context.Context, name string) (changed bool, err error)
	// SSHAccess reads who may log in over SSH, and how. fresh reads the SSH
	// server's state again rather than one read up to sshCache ago.
	SSHAccess(fresh bool) (SSHAccess, error)
	// SetSSH refuses a choice that would cut off a current SSH connection
	// unless confirmed, and keys only unless a key is known to work
	// (SPEC.md §6.10).
	SetSSH(ctx context.Context, c SSHChange) error
	SetRestartPolicy(ctx context.Context, mode string, hour int) error
	// RestartPolicy is when the server may restart after updates.
	RestartPolicy() (mode string, hour int)
	// SetBackupDestination points the server's backups at a repository
	// the controller created for it (SPEC.md §10.10).
	SetBackupDestination(ctx context.Context, d BackupDestination) error
	// SetBackupCopies sets the extra copies of its backups (SPEC.md §6.9);
	// the server restarts Roostack to use them. A copy it already has keeps
	// its key and credentials when none come with it.
	SetBackupCopies(ctx context.Context, copies []BackupCopy) error
	// PromoteBackupCopy makes a copy the main backup, and the main backup a
	// copy (old names it), keeping both repositories and their keys.
	PromoteBackupCopy(ctx context.Context, name string, old BackupCopy) error
}

// BackupCopy is an extra copy of a server's backups, somewhere else.
type BackupCopy struct {
	Name       string   `json:"name"`
	Title      string   `json:"title"`
	Repository string   `json:"repository"`
	Env        []string `json:"env,omitempty"`
	Cloud      bool     `json:"cloud,omitempty"`
	Key        string   `json:"key,omitempty"`
	// Drive is the drive it's on, if it's one.
	Drive config.BackupDrive `json:"drive,omitzero"`
}

// BackupDestination is a server's backup repository: where it is, the
// credentials to reach it, and the server's own key to it. An empty Key
// keeps the server's key file (the controller's is the recovery key).
type BackupDestination struct {
	Repository string   `json:"repository"`
	Env        []string `json:"env,omitempty"`
	// Cloud: in Roostack Cloud backup, with short-lived keys (no Env).
	Cloud bool   `json:"cloud,omitempty"`
	Key   string `json:"key,omitempty"`
	// PrunedByController: this server only adds backups.
	PrunedByController bool `json:"pruned_by_controller,omitempty"`
	// Drive is the drive it's on, if it's one (backups check it's there).
	Drive config.BackupDrive `json:"drive,omitzero"`
}

// SSHAccess is the current SSH choice and who is connected now.
type SSHAccess struct {
	Choice  string // config.SSH*, or "" if not chosen yet
	Login   string // config.SSHLogin*, or "" if not chosen yet
	Clients []netip.Addr
	// SSHD is what the SSH server does now (roostack-agent); SSHDError
	// says why it couldn't be read.
	SSHD      agent.SSHStatus
	SSHDError string `json:",omitempty"`
}

// PasswordsOpen reports whether people who can reach SSH may try
// passwords: what the lasting warning is about (SPEC.md §6.10 item 3).
func (a SSHAccess) PasswordsOpen() bool {
	return (a.Choice == config.SSHFromLAN || a.Choice == config.SSHFromAny) && a.SSHD.Installed && a.SSHD.Passwords
}

// SSHChange is a new choice of who may log in over SSH, and how.
type SSHChange struct {
	Choice string // config.SSH*
	Login  string // config.SSHLogin*; "Nobody" always means keys only
	// Confirmed: cutting off current SSH connections is meant.
	Confirmed bool
}

// SSHServer reads and changes the SSH server's settings (roostack-agent).
type SSHServer interface {
	SSHStatus(ctx context.Context) (agent.SSHStatus, error)
	SetSSHKeysOnly(ctx context.Context, on bool) error
}

// sshCache is how long a read of the SSH server's state is used for pages
// that poll (the home page's Needs attention list).
const sshCache = 30 * time.Second

// HostSettings changes this server's settings: the time zone through
// roostack-agent, the rest in lab.yaml.
type HostSettings struct {
	Lab     config.Lab
	LabPath string
	// SetZone sets the host's time zone (roostack-agent).
	SetZone func(ctx context.Context, name string) error
	// Zone and Zones read the host's time zone (internal/hosttime).
	Zone  func() string
	Zones func() ([]string, error)
	// SSHClients lists who is connected over SSH now (internal/firewall).
	SSHClients func() ([]netip.Addr, error)
	// SSHServer changes the SSH server's settings (roostack-agent).
	SSHServer SSHServer
	// Restart restarts Roostack so every part picks up the new settings.
	Restart func()
	// UseZone switches Roostack itself to a new time zone without a restart
	// (optional).
	UseZone func(name string)
	// UseSSH applies a new SSH choice without a restart: the firewall is
	// rebuilt at once (Local.UseSSH). Without it, Roostack restarts.
	UseSSH func(choice string)

	// mu guards Lab's SSH choice, which changes while running, and the
	// last read of the SSH server.
	mu     sync.Mutex
	sshd   agent.SSHStatus
	sshErr error
	sshAt  time.Time
}

// Settings is this server's settings, or nil.
func (n *Local) Settings() Settings { return n.Host }

func (h *HostSettings) Timezones() (string, []string, error) {
	zones, err := h.Zones()
	return h.Zone(), zones, err
}

func (h *HostSettings) SetTimezone(ctx context.Context, name string) (bool, error) {
	name = strings.TrimSpace(name)
	zones, err := h.Zones()
	if err != nil {
		return false, err
	}
	if !slices.Contains(zones, name) {
		return false, formErrorf("Choose a time zone from the list, such as Europe/London.")
	}
	if name == h.Zone() {
		return false, nil
	}
	if err := h.SetZone(ctx, name); err != nil {
		return false, formErrorf("Roostack couldn't change the time zone: %v", err)
	}
	// Pages show the new zone at once; schedules and containers follow at
	// the restart (postponed to the end of the setup wizard).
	if h.UseZone != nil {
		h.UseZone(name)
	}
	h.Restart()
	return true, nil
}

func (h *HostSettings) SSHAccess(fresh bool) (SSHAccess, error) {
	clients, err := h.SSHClients()
	sshd, serr := h.sshStatus(context.Background(), fresh)
	h.mu.Lock()
	a := SSHAccess{Choice: h.Lab.Firewall.SSH, Login: h.Lab.Firewall.SSHLogin, Clients: clients, SSHD: sshd}
	h.mu.Unlock()
	if serr != nil {
		a.SSHDError = serr.Error()
	}
	return a, err
}

// sshStatus reads the SSH server's state, or uses a read up to sshCache
// old unless fresh.
func (h *HostSettings) sshStatus(ctx context.Context, fresh bool) (agent.SSHStatus, error) {
	if h.SSHServer == nil {
		return agent.SSHStatus{}, nil
	}
	h.mu.Lock()
	if !fresh && !h.sshAt.IsZero() && time.Since(h.sshAt) < sshCache {
		defer h.mu.Unlock()
		return h.sshd, h.sshErr
	}
	h.mu.Unlock()
	st, err := h.SSHServer.SSHStatus(ctx)
	h.mu.Lock()
	h.sshd, h.sshErr, h.sshAt = st, err, time.Now()
	h.mu.Unlock()
	return st, err
}

func (h *HostSettings) SetSSH(ctx context.Context, c SSHChange) error {
	switch c.Choice {
	case config.SSHFromLAN, config.SSHFromAny, config.SSHOff:
	default:
		return formErrorf("Choose who can connect.")
	}
	sshd, err := h.sshStatus(ctx, true)
	switch {
	case !sshd.Installed && err == nil:
		c.Login = "" // no SSH server: nothing to choose
	case c.Choice == config.SSHOff:
		// Nobody can reach SSH, so keys only can't lock anyone out.
		c.Login = config.SSHLoginKey
	case c.Login != config.SSHLoginKey && c.Login != config.SSHLoginPassword:
		return formErrorf("Choose how people log in over SSH.")
	}
	clients, err := h.SSHClients()
	if err != nil {
		return err
	}
	if cut := firewall.CutsOff(c.Choice, clients); len(cut) > 0 && !c.Confirmed {
		return formErrorf("That would disconnect the SSH connection from %s. "+
			"Tick the box if that's what you want.", cut[0])
	}
	if keys := c.Login == config.SSHLoginKey; c.Login != "" && keys != sshd.KeysOnly {
		// Turning passwords and root logins off needs proof that someone
		// can still log in and use sudo (SPEC.md §6.10 item 3).
		if _, ok := sshd.SudoKeyLogin(); keys && c.Choice != config.SSHOff && !ok {
			return formErrorf("Roostack hasn't seen anyone log in with an SSH key and an account that can use sudo, " +
				"so turning passwords off could lock you out. Log in once with your key, then try again, " +
				"or choose \"With a key or a password\".")
		}
		err := h.SSHServer.SetSSHKeysOnly(ctx, keys)
		h.mu.Lock()
		h.sshAt = time.Time{} // read it again next time
		h.mu.Unlock()
		if err != nil {
			return formErrorf("Roostack couldn't change SSH's settings, so they're as they were: %v", err)
		}
	}
	if err := config.Update(h.LabPath, func(l *config.Lab) { l.Firewall.SSH, l.Firewall.SSHLogin = c.Choice, c.Login }); err != nil {
		return err
	}
	h.mu.Lock()
	h.Lab.Firewall.SSH, h.Lab.Firewall.SSHLogin = c.Choice, c.Login
	h.mu.Unlock()
	// The firewall is rebuilt at once: no restart, so the server stays
	// reachable (another server's is managed from the controller).
	if h.UseSSH != nil {
		h.UseSSH(c.Choice)
		return nil
	}
	h.Restart()
	return nil
}

func (h *HostSettings) RestartPolicy() (string, int) {
	return cmp.Or(h.Lab.Maintenance.Restart, config.RestartAuto), h.Lab.Maintenance.Hour()
}

func (h *HostSettings) SetRestartPolicy(ctx context.Context, mode string, hour int) error {
	if mode != config.RestartAuto && mode != config.RestartNotify {
		return formErrorf("Choose whether to restart automatically.")
	}
	if hour < 0 || hour > 23 {
		return formErrorf("Choose an hour between 0 and 23.")
	}
	if err := config.Update(h.LabPath, func(l *config.Lab) {
		l.Maintenance.Restart, l.Maintenance.RestartHour = mode, &hour
	}); err != nil {
		return fmt.Errorf("saving the restart policy: %w", err)
	}
	h.Restart()
	return nil
}

func (h *HostSettings) SetBackupDestination(ctx context.Context, d BackupDestination) error {
	if d.Repository == "" {
		return formErrorf("No backup repository.")
	}
	if d.Key != "" {
		file := backup.KeyFile(h.Lab.Paths)
		if err := os.WriteFile(file+".tmp", []byte(d.Key+"\n"), 0o600); err != nil {
			return err
		}
		if err := os.Rename(file+".tmp", file); err != nil {
			return err
		}
	}
	if err := config.Update(h.LabPath, func(l *config.Lab) {
		l.Backup.Repository, l.Backup.Env, l.Backup.PrunedByController, l.Backup.Cloud = d.Repository, d.Env, d.PrunedByController, d.Cloud
		l.Backup.Drive = d.Drive
	}); err != nil {
		return fmt.Errorf("saving the backup destination: %w", err)
	}
	// Backups are set up when Roostack starts.
	h.Restart()
	return nil
}

func (h *HostSettings) SetBackupCopies(ctx context.Context, copies []BackupCopy) error {
	lab, err := config.Load(h.LabPath)
	if err != nil {
		return err
	}
	have := map[string]config.BackupCopy{}
	for _, c := range lab.Backup.Copies {
		have[c.Name] = c
	}
	dir := filepath.Join(h.Lab.Paths.StateDir, "backup-copies")
	var out []config.BackupCopy
	for _, c := range copies {
		if c.Repository == "" || c.Name == "" {
			return formErrorf("A backup copy needs a name and a repository.")
		}
		old, known := have[c.Name]
		keyFile := old.KeyFile
		if known && c.Key == "" && len(c.Env) == 0 {
			c.Env = old.Env // a copy it has keeps its credentials
		}
		if c.Key != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			keyFile = filepath.Join(dir, fileName(c.Name)+".key")
			if err := os.WriteFile(keyFile+".tmp", []byte(c.Key+"\n"), 0o600); err != nil {
				return err
			}
			if err := os.Rename(keyFile+".tmp", keyFile); err != nil {
				return err
			}
		}
		if c.Drive == (config.BackupDrive{}) && known {
			c.Drive = old.Drive
		}
		out = append(out, config.BackupCopy{Name: c.Name, Title: c.Title, Repository: c.Repository, Env: c.Env, Cloud: c.Cloud, KeyFile: keyFile, Drive: c.Drive})
	}
	if err := config.Update(h.LabPath, func(l *config.Lab) { l.Backup.Copies = out }); err != nil {
		return fmt.Errorf("saving the backup copies: %w", err)
	}
	h.Restart()
	return nil
}

// fileName makes a destination reference ("provider:3") safe as a file name.
func fileName(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '_'
	}, name)
}

func (h *HostSettings) PromoteBackupCopy(ctx context.Context, name string, old BackupCopy) error {
	lab, err := config.Load(h.LabPath)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(lab.Backup.Copies, func(c config.BackupCopy) bool { return c.Name == name })
	if i < 0 || old.Name == "" {
		return formErrorf("That backup isn't one of this server's copies.")
	}
	c := lab.Backup.Copies[i]
	mainKey := backup.KeyFile(h.Lab.Paths)
	demoted := config.BackupCopy{Name: old.Name, Title: old.Title, Repository: lab.Backup.Repository, Env: lab.Backup.Env,
		Cloud: lab.Backup.Cloud, Drive: lab.Backup.Drive}
	// A copy with a key of its own (not the main key file) swaps keys with
	// the main backup.
	if c.KeyFile != "" {
		copyKey, err := os.ReadFile(c.KeyFile)
		if err != nil {
			return err
		}
		mainData, err := os.ReadFile(mainKey)
		if err != nil {
			return err
		}
		dir := filepath.Join(h.Lab.Paths.StateDir, "backup-copies")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		demoted.KeyFile = filepath.Join(dir, fileName(old.Name)+".key")
		if err := writePrivate(demoted.KeyFile, mainData); err != nil {
			return err
		}
		if err := writePrivate(mainKey, copyKey); err != nil {
			return err
		}
	}
	if err := config.Update(h.LabPath, func(l *config.Lab) {
		l.Backup.Repository, l.Backup.Env, l.Backup.Drive, l.Backup.Cloud = c.Repository, c.Env, c.Drive, c.Cloud
		copies := slices.Clone(l.Backup.Copies)
		if j := slices.IndexFunc(copies, func(x config.BackupCopy) bool { return x.Name == name }); j >= 0 {
			copies[j] = demoted
		}
		l.Backup.Copies = copies
	}); err != nil {
		return err
	}
	h.Restart()
	return nil
}

func writePrivate(file string, data []byte) error {
	if err := os.WriteFile(file+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(file+".tmp", file)
}
