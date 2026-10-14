package manager

import (
	"context"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/state"
)

// Withdrawals for installed services (SPEC.md §8.3.4). The registry's
// index can withdraw a version, or a whole package, at three levels:
// notice, warning and stop. Nothing here is saved except the Owner's
// choices: whether a service is stopped is worked out again for every
// assignment from the lab's copy of the index, the Owner's opt-out
// (registry.no_stops) and the service's started_anyway. So a stop the
// registry lifts (one published by mistake, or with a stolen key) lets the
// service start again by itself, and a restored lab works it out afresh.
// Roostack never removes a service or deletes data because of the
// registry: a stop is the most it does.

// stopKey + service name, in the controller's settings: "<version>
// <unix time>", when Roostack first stopped it. For the pages ("since")
// and the last backup of databases on the package's own images.
const stopKey = "registry-stop:"

// lastBackupCheck is how often the controller checks whether a stopped
// service's server has finished its last backup, so the databases on the
// package's own images can stop too (a variable for tests).
var lastBackupCheck = time.Minute

// Withdrawal is the registry's withdrawal of an installed service's
// version, and what Roostack does about it.
type Withdrawal struct {
	// Level, Reason and Link are the registry's.
	registry.Withdrawal
	// Version is the service's version the registry withdrew (or the
	// whole package's).
	Version string
	// Shown is the level the lab acts on: a stop is shown as a warning
	// when the Owner turned stops off or started the service anyway.
	Shown string
	// Stopped: Roostack stopped the service (Shown is stop), Since when.
	Stopped bool
	Since   time.Time
	// LastBackup: databases on the package's own images still run until a
	// backup of the service's server has finished.
	LastBackup bool
	// StartedAnyway: the Owner chose to run this version despite a stop.
	StartedAnyway bool
	// NoStops: the Owner turned stops off (a stop shows as a warning).
	NoStops bool
	// Public: one of the service's addresses or ports is public, so
	// starting it anyway offers to make them private.
	Public bool
}

// StopsOn says whether Roostack may stop a service the registry says is
// dangerous (Settings → Updates; on unless the Owner turned it off).
func (m *Manager) StopsOn() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.noStopsLocked()
}

func (m *Manager) noStopsLocked() bool {
	if m.noStops == nil {
		v := m.Lab.Registry.NoStops
		m.noStops = &v
	}
	return *m.noStops
}

// SetStops turns stopping on or off, saving it in lab.yaml, and applies
// the change: off, services Roostack stopped start again.
func (m *Manager) SetStops(on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.UpdateLab(func(l *config.Lab) { l.Registry.NoStops = !on }); err != nil {
		return err
	}
	off := !on
	m.noStops = &off
	m.applyReason = "Changing whether Roostack stops a service the registry says is dangerous"
	m.startApplyLocked()
	return nil
}

// Withdrawals lists the installed services whose version the registry
// has withdrawn, by service name.
func (m *Manager) Withdrawals(ctx context.Context) map[string]*Withdrawal {
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		return nil
	}
	m.mu.Lock()
	noStops := m.noStopsLocked()
	m.mu.Unlock()
	return m.withdrawals(ctx, services, noStops)
}

// withdrawals works out each service's withdrawal from the lab's copy of
// the index, reading when each was stopped but recording nothing.
func (m *Manager) withdrawals(ctx context.Context, services []state.Service, noStops bool) map[string]*Withdrawal {
	if m.RegistryIndex == nil {
		return nil
	}
	ix, err := m.RegistryIndex()
	if err != nil {
		m.Log.Error("reading the registry's index", "err", err)
		return nil
	}
	if ix == nil {
		return nil
	}
	out := map[string]*Withdrawal{}
	for _, s := range services {
		in := s.Instance
		if in.Waiting {
			continue // runs nowhere already; restoring it asks again
		}
		file, err := os.ReadFile(m.Lab.Paths.LabPackageFile(in.Package, in.Version))
		if err != nil {
			continue
		}
		rw := ix.Withdrawn(in.Package, in.Version, file)
		if rw == nil {
			continue
		}
		w := &Withdrawal{Withdrawal: *rw, Version: in.Version, Shown: rw.Level, NoStops: noStops,
			Public: isPublic(m, s)}
		if rw.Level == registry.LevelStop {
			w.StartedAnyway = in.StartedAnyway == in.Version
			if noStops || w.StartedAnyway {
				w.Shown = registry.LevelWarning
			} else {
				w.Stopped = true
				w.Since = m.stoppedSince(ctx, s.Name(), in.Version)
				w.LastBackup = m.lastBackupDue(ctx, s, w.Since)
			}
		}
		out[s.Name()] = w
	}
	return out
}

// isPublic says whether any of a service's addresses or ports is public.
func isPublic(m *Manager, s state.Service) bool {
	_, routes := generate.Values(m.Lab, s.Package, s.Instance)
	for _, rc := range routes {
		if rc.Exposure == pkgspec.ExposurePublic {
			return true
		}
	}
	for _, pc := range generate.ResolvePorts(s.Package, s.Instance) {
		if pc.Enabled && pc.Exposure == pkgspec.ExposurePublic {
			return true
		}
	}
	return false
}

// stoppedSince is when Roostack first stopped a service at this version
// (zero if it hasn't recorded it yet).
func (m *Manager) stoppedSince(ctx context.Context, name, version string) time.Time {
	if m.Settings == nil {
		return time.Time{}
	}
	v, _ := m.Settings.Setting(ctx, stopKey+name)
	ver, unix, ok := strings.Cut(v, " ")
	if !ok || ver != version {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(unix, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// ownImages says whether a service has a database on an image its
// package sets (not Roostack's own), which keeps running after a stop
// only until the last backup.
func ownImages(p *pkgspec.Package) bool {
	return slices.ContainsFunc(slices.Collect(maps.Values(p.Databases)), func(d *pkgspec.Database) bool {
		return d.Type != pkgspec.DBSQLite && d.Image != ""
	})
}

// lastBackupDue says whether a stopped service's databases on the
// package's own images must keep running for a last backup: until a
// backup of its server started after the stop has finished. A server
// with no backups set up has none to wait for; one that can't be asked
// keeps them running until it can.
func (m *Manager) lastBackupDue(ctx context.Context, s state.Service, since time.Time) bool {
	if !ownImages(s.Package) {
		return false
	}
	if since.IsZero() {
		return true // being stopped now
	}
	n := m.node(s.Instance.Host)
	if n == nil {
		return false // its server has left the lab
	}
	b := n.Backups()
	if b == nil {
		return false
	}
	st, err := b.Status(ctx)
	if err != nil {
		return true
	}
	if !st.Configured || st.Fenced != nil {
		return false
	}
	return st.LastOK == nil || st.LastOK.Started.Before(since)
}

// markStops sets Stop on the services Roostack stops, records when each
// was first stopped (and forgets services that aren't any more), and asks
// for the last backups that are due. It returns a key that changes when
// what should run does, so a new index only applies when it matters.
// Callers hold m.mu.
func (m *Manager) markStops(ctx context.Context, services []state.Service) string {
	ws := m.withdrawals(ctx, services, m.noStopsLocked())
	var key []string
	for i := range services {
		s := &services[i]
		name := s.Name()
		w := ws[name]
		if w == nil || !w.Stopped {
			if m.Settings != nil {
				if v, _ := m.Settings.Setting(ctx, stopKey+name); v != "" {
					m.Settings.SetSetting(ctx, stopKey+name, "")
				}
			}
			continue
		}
		if w.Since.IsZero() && m.Settings != nil {
			m.Settings.SetSetting(ctx, stopKey+name, s.Instance.Version+" "+strconv.FormatInt(time.Now().Unix(), 10))
			m.Log.Warn("stopping a service the registry withdrew as dangerous", "service", name,
				"version", s.Instance.Version, "reason", w.Reason, "link", w.Link)
		}
		s.Stop = &state.Stop{LastBackup: w.LastBackup}
		key = append(key, name+"="+strconv.FormatBool(w.LastBackup))
		if w.LastBackup {
			m.awaitLastBackup(s.Instance.Host)
		}
	}
	slices.Sort(key)
	return strings.Join(key, ",")
}

// awaitLastBackup starts a backup of a server now, for a stopped service's
// databases on the package's own images, and applies again once it has
// finished so they stop too. One wait per server.
func (m *Manager) awaitLastBackup(host string) {
	if m.awaiting == nil {
		m.awaiting = map[string]bool{}
	}
	if m.awaiting[host] {
		return
	}
	n := m.node(host)
	if n == nil || n.Backups() == nil {
		return
	}
	m.awaiting[host] = true
	if err := n.Backups().RunNow(); err != nil {
		m.Log.Warn("starting a backup before stopping a service's databases", "server", host, "err", err)
	}
	go func() {
		for {
			time.Sleep(lastBackupCheck)
			m.mu.Lock()
			services, err := state.LoadServices(m.Lab)
			due := false
			if err == nil {
				ws := m.withdrawals(context.Background(), services, m.noStopsLocked())
				for _, s := range services {
					if w := ws[s.Name()]; w != nil && w.LastBackup && s.Instance.Host == host {
						due = true
					}
				}
			}
			if !due || err != nil {
				delete(m.awaiting, host)
				m.applyReason = "A backup finished, so the databases of services Roostack stopped can stop too"
				m.startApplyLocked()
				m.mu.Unlock()
				return
			}
			m.mu.Unlock()
		}
	}()
}

// RegistryChanged is called after each check of the registry: if a
// withdrawal changed what should run, the servers get new assignments.
func (m *Manager) RegistryChanged() {
	// A stopped service whose fix is out may update now (update.go).
	defer func() { go m.UpdateStopped(context.Background()) }()
	m.mu.Lock()
	defer m.mu.Unlock()
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return // the next apply says why
	}
	ctx := context.Background()
	ws := m.withdrawals(ctx, services, m.noStopsLocked())
	var key []string
	for name, w := range ws {
		if w.Stopped {
			key = append(key, name+"="+strconv.FormatBool(w.LastBackup))
		}
	}
	slices.Sort(key)
	if strings.Join(key, ",") == m.stopsKey {
		return
	}
	m.applyReason = "The service registry changed which services Roostack stops"
	m.startApplyLocked()
}

// StartAnyway runs a service Roostack stopped, at the Owner's word: this
// version isn't stopped again. private makes each of its public addresses
// and ports private first (SPEC.md §8.3.4, the Owner 2026-10-14), which
// is offered, and ticked, when any is public.
func (m *Manager) StartAnyway(ctx context.Context, name string, private bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == name })
	if i < 0 {
		return ErrNotFound
	}
	p, in := services[i].Package, services[i].Instance
	in.StartedAnyway = in.Version
	if private {
		for rn := range p.Routes {
			if in.Routes == nil {
				in.Routes = map[string]instance.RouteOverride{}
			}
			o := in.Routes[rn]
			o.Exposure, o.PublicPaths = pkgspec.ExposurePrivate, nil
			in.Routes[rn] = o
		}
		for pn, pc := range generate.ResolvePorts(p, in) {
			if pc.Exposure != pkgspec.ExposurePublic {
				continue
			}
			if in.Ports == nil {
				in.Ports = map[string]instance.PortOverride{}
			}
			o := in.Ports[pn]
			o.Exposure = pkgspec.ExposurePrivate
			in.Ports[pn] = o
		}
	}
	if err := in.Validate(p); err != nil {
		return err
	}
	if err := in.Save(state.InstanceFile(m.Lab, name)); err != nil {
		return err
	}
	m.applyReason = "Starting " + name + " again, at the Owner's word, though the service registry withdrew its version as dangerous"
	m.startApplyLocked()
	return nil
}
