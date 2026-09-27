package node

import (
	"os"

	"cmp"
	"context"
	"fmt"
	"github.com/mylab/mylab/internal/backup"
	"net/netip"
	"slices"
	"strings"

	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/firewall"
)

// Settings are a machine's own settings: its clock, who may reach it over
// SSH, and when it may restart after updates. Changing one restarts MyLab
// on that machine so every part picks it up.
type Settings interface {
	// Timezones returns the machine's time zone and those it can use.
	Timezones() (current string, zones []string, err error)
	// SetTimezone returns true if the zone changed (MyLab then restarts:
	// Go reads the local zone once, and schedules depend on it).
	SetTimezone(ctx context.Context, name string) (changed bool, err error)
	SSHAccess() (SSHAccess, error)
	// SetSSH refuses a choice that would cut off a current SSH connection
	// unless confirmed.
	SetSSH(ctx context.Context, choice string, confirmed bool) error
	SetRestartPolicy(ctx context.Context, mode string, hour int) error
	// RestartPolicy is when the machine may restart after updates.
	RestartPolicy() (mode string, hour int)
	// SetBackupDestination points the machine's backups at a repository
	// the controller created for it (SPEC.md §10.10).
	SetBackupDestination(ctx context.Context, d BackupDestination) error
}

// BackupDestination is a machine's backup repository: where it is, the
// credentials to reach it, and the machine's own key to it. An empty Key
// keeps the machine's key file (the controller's is the recovery key).
type BackupDestination struct {
	Repository string   `json:"repository"`
	Env        []string `json:"env,omitempty"`
	Key        string   `json:"key,omitempty"`
}

// SSHAccess is the current SSH choice and who is connected now.
type SSHAccess struct {
	Choice  string // config.SSH*, or "" if not chosen yet
	Clients []netip.Addr
}

// HostSettings changes this machine's settings: the time zone through
// mylab-agent, the rest in lab.yaml.
type HostSettings struct {
	Lab     config.Lab
	LabPath string
	// SetZone sets the host's time zone (mylab-agent).
	SetZone func(ctx context.Context, name string) error
	// Zone and Zones read the host's time zone (internal/hosttime).
	Zone  func() string
	Zones func() ([]string, error)
	// SSHClients lists who is connected over SSH now (internal/firewall).
	SSHClients func() ([]netip.Addr, error)
	// Restart restarts MyLab so every part picks up the new settings.
	Restart func()
}

// Settings is this machine's settings, or nil.
func (n *Local) Settings() Settings { return n.Machine }

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
		return false, formErrorf("MyLab couldn't change the time zone: %v", err)
	}
	h.Restart()
	return true, nil
}

func (h *HostSettings) SSHAccess() (SSHAccess, error) {
	clients, err := h.SSHClients()
	return SSHAccess{Choice: h.Lab.Firewall.SSH, Clients: clients}, err
}

func (h *HostSettings) SetSSH(ctx context.Context, choice string, confirmed bool) error {
	switch choice {
	case config.SSHFromLAN, config.SSHFromAny, config.SSHOff:
	default:
		return formErrorf("Choose who can connect.")
	}
	clients, err := h.SSHClients()
	if err != nil {
		return err
	}
	if cut := firewall.CutsOff(choice, clients); len(cut) > 0 && !confirmed {
		return formErrorf("That would disconnect the SSH connection from %s. "+
			"Tick the box if that's what you want.", cut[0])
	}
	if err := config.Update(h.LabPath, func(l *config.Lab) { l.Firewall.SSH = choice }); err != nil {
		return err
	}
	// Restarting applies the firewall.
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
		l.Backup.Repository, l.Backup.Env = d.Repository, d.Env
	}); err != nil {
		return fmt.Errorf("saving the backup destination: %w", err)
	}
	// Backups are set up when MyLab starts.
	h.Restart()
	return nil
}
