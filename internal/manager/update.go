package manager

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
)

// Updating a service to a newer package version (SPEC.md §7). The
// registry's offered version (the one the fetcher keeps) is the only
// target. An update runs in the background, one at a time in the lab:
//
//  1. its server downloads the new images while the old version runs;
//  2. it backs up (a manual update; an automatic one follows the nightly
//     backup), then holds its maintenance queue (node/update.go);
//  3. the controller keeps the new package file (state.KeepPackage),
//     switches the instance's version and applies;
//  4. it checks the service: every unit active, its container checks
//     passing, and no restarts, for healthStable within healthWait;
//  5. if that fails, it goes back to the previous package and image (the
//     old file is kept meanwhile, and Podman keeps the old image). Data
//     isn't rolled back: the backup from step 2 is the way back.

// Results of an update.
const (
	UpdateDone = "updated"
	// UpdateRolledBack: it didn't work, and the previous version runs.
	UpdateRolledBack = "rolled-back"
	// UpdateFailed: it stopped before switching (nothing changed), or the
	// previous version didn't come back either.
	UpdateFailed = "failed"
)

// updateKey + service name, in the controller's settings: the service's
// last update (UpdateRecord, as JSON).
const updateKey = "service-update:"

// Timings for checking an updated service (variables for tests).
var (
	healthWait   = 5 * time.Minute
	healthStable = time.Minute
	healthEvery  = 5 * time.Second
	statusEvery  = 2 * time.Second
)

// maxRestarts is how many restarts during the check mean it keeps
// failing, without waiting for healthWait; so does a unit systemd gave up
// on (failed) for failedFor.
const maxRestarts = 5

var failedFor = 30 * time.Second

// Update is an installed service's available update.
type Update struct {
	Service, Title string
	// Package is the package it's a copy of.
	Package string
	// From and To are package versions; the app's own are
	// pkgspec.Upstream of them.
	From, To string
	// Kind is pkgspec.UpdateKind.
	Kind string
	// Notes are the upgrade notes to read first (oldest first).
	Notes []pkgspec.UpgradeNote
	// Policy is the service's update policy (instance.Updates*).
	Policy string
	// Auto: Roostack applies it by itself, after the nightly backup.
	Auto bool
	// NoBackups: its server's backups aren't set up, so it never updates
	// by itself, and updating by hand has no backup to go back to.
	NoBackups bool
	// FailedBefore: updating to To didn't work last time, so it isn't
	// tried again by itself.
	FailedBefore bool
	// Blocked says why it can't update at all (a sentence).
	Blocked string
	// Local: the service is from one of the Owner's own packages, and so
	// is the update (SPEC.md §8.3.4).
	Local bool
	// NewRisks are what the Owner's newer package asks for that the
	// installed one didn't: only the Owner can accept them.
	NewRisks []string
}

// FromApp and ToApp are the app's own versions, with the package's
// revision when only that changes ("1.34.3, package revision 2"), so
// the two never read the same.
func (u *Update) FromApp() string { return versionName(u.From, u.Kind) }
func (u *Update) ToApp() string   { return versionName(u.To, u.Kind) }

func versionName(v, kind string) string {
	if kind != pkgspec.UpdateRevision {
		return pkgspec.Upstream(v)
	}
	rev := strings.TrimPrefix(v[len(pkgspec.Upstream(v)):], "-roostack.")
	return pkgspec.Upstream(v) + ", package revision " + rev
}

// Confirm is the question asking to update, for the confirmation shown
// when there are notes to read or no backup to go back to.
func (u *Update) Confirm() string {
	q := fmt.Sprintf("Update %s to %s?", u.Title, u.ToApp())
	if u.NoBackups {
		q += " Its server has no backups set up, so if the update changes its data, there's no copy to go back to."
	} else {
		q += " Roostack backs it up first (only what changed since the last backup), then updates it; it's unavailable for a few minutes."
	}
	if len(u.Notes) > 0 {
		q += " Read the notes first."
	}
	return q
}

// UpdateRecord is a service's last update, kept in the controller's
// settings for its page and for not trying a failed version again.
type UpdateRecord struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`
	Result    string    `json:"result"`
	Error     string    `json:"error,omitempty"`
	Automatic bool      `json:"automatic,omitempty"`
	By        string    `json:"by,omitempty"`
	// Backup is when the backup taken for it finished (zero: none).
	Backup time.Time `json:"backup,omitzero"`
	// Server is where it ran: its backups are that server's.
	Server string `json:"server,omitempty"`
}

// ServiceUpdate is an update queued or in progress, for the service page.
type ServiceUpdate struct {
	From, To string
	// Progress has the steps, as an install's do.
	Progress *node.InstallProgress
	// Waiting says what it waits for before its first step.
	Waiting string
}

// updates are the lab's updates in progress (Manager.upd).
type updates struct {
	// run makes updates one at a time.
	run sync.Mutex
	mu  sync.Mutex
	// active are queued or running updates, by service.
	active map[string]*ServiceUpdate
	// keep are the previous package versions of services updating, kept
	// from PrunePackages until the update is done (for going back).
	keep map[string]string
}

// AvailableUpdates lists the installed services with a newer version in
// the registry, by service name.
func (m *Manager) AvailableUpdates(ctx context.Context) map[string]*Update {
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		return nil
	}
	out := map[string]*Update{}
	for _, s := range services {
		in := s.Instance
		if in.Waiting {
			continue // runs nowhere; restoring it comes first
		}
		p := m.updateSource(in)
		if p == nil || pkgspec.CompareVersions(p.Version, in.Version) <= 0 {
			continue
		}
		u := &Update{Service: s.Name(), Title: s.Title(), Package: in.Package, From: in.Version, To: p.Version,
			Kind: pkgspec.UpdateKind(in.Version, p.Version), Notes: p.NotesSince(in.Version), Policy: in.Updates,
			Local: in.Local}
		if in.Local {
			u.NewRisks = newRisks(s.Package, p)
		}
		if _, _, err := updatedInstance(in, s.Package, p); err != nil {
			u.Blocked = err.Error()
		}
		if rec := m.LastUpdate(ctx, s.Name()); rec != nil && rec.To == p.Version && rec.Result != UpdateDone {
			u.FailedBefore = true
		}
		u.NoBackups = !m.backupsOn(ctx, cmp.Or(in.Host, m.Lab.NodeID))
		u.Auto = in.AllowsUpdate(u.Kind) && len(u.Notes) == 0 && len(u.NewRisks) == 0 && !u.FailedBefore && !u.NoBackups &&
			u.Blocked == ""
		out[s.Name()] = u
	}
	return out
}

// updateSource is the package a service would update to: for a service
// installed from one of the Owner's own packages, that package as the
// Owner last added it; for the registry's, the registry's offered version
// (the catalog), when the installed package is the registry's. Nil if
// there's none. The two never mix: a registry package never updates a
// service of the Owner's, nor the other way round (SPEC.md §7.1).
func (m *Manager) updateSource(in *instance.Instance) *pkgspec.Package {
	if in.Local {
		p, err := state.LoadLocalPackage(m.Lab.Paths, in.Package)
		if err != nil {
			return nil // removed from the list since: nothing newer
		}
		return p
	}
	if m.RegistryIndex == nil || m.IsLocal(in.Package) {
		return nil
	}
	ix, err := m.RegistryIndex()
	if err != nil || ix == nil {
		return nil
	}
	file, err := os.ReadFile(m.Lab.Paths.LabPackageFile(in.Package, in.Version))
	if err != nil || !ix.Updates(in.Package, in.Version, file) {
		return nil
	}
	p, err := state.LoadCatalogPackage(m.Lab.Paths, in.Package)
	if err != nil {
		return nil
	}
	return p
}

// newRisks are what a newer version of one of the Owner's own packages
// asks for that the installed one didn't (pkgspec.Package.Risks): only
// the Owner can accept them, as when installing, and such an update never
// applies by itself.
func newRisks(old, p *pkgspec.Package) []string {
	had := map[string]bool{}
	for _, r := range old.Risks() {
		had[r] = true
	}
	var out []string
	for _, r := range p.Risks() {
		if !had[r] {
			out = append(out, r)
		}
	}
	return out
}

// backupsOn says whether a server's backups are set up through Roostack.
func (m *Manager) backupsOn(ctx context.Context, host string) bool {
	n := m.node(host)
	if n == nil || n.Backups() == nil {
		return false
	}
	t, _, ok := m.BackupTargetOf(ctx, host)
	return ok && t.Provider != "setup"
}

// updatedInstance is a service's instance for a newer package: the same
// choices, without routes, ports and settings the package no longer has.
// It fails, with a sentence, when the new version needs a choice the
// service never made (a new required setting with no default).
func updatedInstance(in *instance.Instance, old, p *pkgspec.Package) (*instance.Instance, []string, error) {
	cp := *in
	cp.Version, cp.StartedAnyway = p.Version, ""
	var dropped []string
	if len(in.Routes) > 0 {
		cp.Routes = map[string]instance.RouteOverride{}
		for k, v := range in.Routes {
			if p.Routes[k] == nil {
				dropped = append(dropped, "web address "+k)
				continue
			}
			cp.Routes[k] = v
		}
	}
	if len(in.Ports) > 0 {
		cp.Ports = map[string]instance.PortOverride{}
		for k, v := range in.Ports {
			if p.Ports[k] == nil {
				dropped = append(dropped, "connection "+k)
				continue
			}
			cp.Ports[k] = v
		}
	}
	cp.Settings = map[string]any{}
	for k, v := range in.Settings {
		if s := p.Setting(k); s == nil || s.Type == pkgspec.SettingSecret {
			dropped = append(dropped, "setting "+k)
			continue
		}
		cp.Settings[k] = v
	}
	var needs []string
	for _, s := range p.Settings {
		if old.Setting(s.Key) != nil || !s.Required || s.InstallOnly {
			continue
		}
		if s.Type == pkgspec.SettingSecret && !s.Generate {
			needs = append(needs, s.Title)
		} else if s.Type != pkgspec.SettingSecret && s.Default == nil {
			needs = append(needs, s.Title)
		}
	}
	if len(needs) > 0 {
		return nil, nil, formErrorf("%s %s needs something set that this version didn't (%s), and Roostack can't ask for it yet.",
			p.Title, pkgspec.Upstream(p.Version), strings.Join(needs, ", "))
	}
	if err := cp.Validate(p); err != nil {
		return nil, nil, formErrorf("%s %s doesn't fit this service's settings: %v", p.Title, pkgspec.Upstream(p.Version), err)
	}
	slices.Sort(dropped)
	return &cp, dropped, nil
}

// LastUpdate is a service's last update (nil if none).
func (m *Manager) LastUpdate(ctx context.Context, name string) *UpdateRecord {
	if m.Settings == nil {
		return nil
	}
	v, err := m.Settings.Setting(ctx, updateKey+name)
	if err != nil || v == "" {
		return nil
	}
	var rec UpdateRecord
	if json.Unmarshal([]byte(v), &rec) != nil {
		return nil
	}
	return &rec
}

func (m *Manager) saveUpdate(ctx context.Context, name string, rec UpdateRecord) {
	if m.Settings == nil {
		return
	}
	data, _ := json.Marshal(rec)
	if err := m.Settings.SetSetting(ctx, updateKey+name, string(data)); err != nil {
		m.Log.Error("recording an update", "service", name, "err", err)
	}
}

// Updating is a service's update queued or in progress (nil if none).
func (m *Manager) Updating(name string) *ServiceUpdate {
	m.upd.mu.Lock()
	defer m.upd.mu.Unlock()
	if u := m.upd.active[name]; u != nil {
		cp := *u
		p := *u.Progress
		p.Steps = slices.Clone(p.Steps)
		cp.Progress = &p
		return &cp
	}
	return nil
}

// keptVersions are package versions kept while services update.
func (m *Manager) keptVersions() map[string]string {
	m.upd.mu.Lock()
	defer m.upd.mu.Unlock()
	out := map[string]string{}
	for k, v := range m.upd.keep {
		out[k] = v
	}
	return out
}

// UpdatePolicy is how a service updates (instance.Updates*; notify if
// it can't be read).
func (m *Manager) UpdatePolicy(name string) string {
	if !validName(name) {
		return instance.UpdatesNotify
	}
	in, err := instance.Load(state.InstanceFile(m.Lab, name))
	if err != nil || in.Updates == "" {
		return instance.UpdatesNotify
	}
	return in.Updates
}

// SetUpdatePolicy changes how a service updates (SPEC.md §7).
func (m *Manager) SetUpdatePolicy(ctx context.Context, name, policy string) error {
	if !instance.ValidUpdates(policy) {
		return formErrorf("Choose how %s updates.", name)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	path := state.InstanceFile(m.Lab, name)
	if !validName(name) {
		return ErrNotFound
	}
	in, err := instance.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	in.Updates = policy
	return in.Save(path)
}

// UpdateService starts updating a service to version to (the offered
// one) in the background. by names who asked, for the log; acceptRisks
// says the Owner accepted what a newer version of their own package asks
// for (Update.NewRisks).
func (m *Manager) UpdateService(ctx context.Context, name, to, by string, acceptRisks bool) error {
	u := m.AvailableUpdates(ctx)[name]
	switch {
	case u == nil:
		if !validName(name) {
			return ErrNotFound
		}
		return formErrorf("There's no update for this service now.")
	case u.To != to:
		return formErrorf("The update on offer has changed to %s: have a look, then update again.", u.ToApp())
	case u.Blocked != "":
		return formErrorf("%s", u.Blocked)
	case len(u.NewRisks) > 0 && !acceptRisks:
		return formErrorf("%s %s asks for things the version it runs didn't. Read them, and tick the box if you want to update anyway.", u.Title, u.ToApp())
	case len(u.NewRisks) > 0:
		m.Log.Warn("updating to a package of your own with new risks, as the Owner confirmed",
			"service", name, "version", to, "risks", u.NewRisks)
	}
	return m.startUpdate(u, "Update now, by "+by, false, by, true)
}

// startUpdate queues an update, unless the service already has one. why
// goes in its log; by is who asked (empty for an automatic one).
func (m *Manager) startUpdate(u *Update, why string, automatic bool, by string, backup bool) error {
	m.upd.mu.Lock()
	if m.upd.active == nil {
		m.upd.active, m.upd.keep = map[string]*ServiceUpdate{}, map[string]string{}
	}
	if m.upd.active[u.Service] != nil {
		m.upd.mu.Unlock()
		return formErrorf("%s is already updating.", u.Title)
	}
	backup = backup && !u.NoBackups
	steps := []node.InstallStep{{Key: "download", Title: "Downloading the new version"}}
	if backup {
		steps = append(steps, node.InstallStep{Key: "backup", Title: "Backing up " + u.Title})
	}
	steps = append(steps, node.InstallStep{Key: "switch", Title: "Switching to " + u.ToApp()},
		node.InstallStep{Key: "check", Title: "Checking it works"})
	su := &ServiceUpdate{From: u.From, To: u.To, Progress: &node.InstallProgress{Steps: steps, Log: activity.ServiceUpdate},
		Waiting: "Waiting for another update to finish"}
	m.upd.active[u.Service] = su
	m.upd.mu.Unlock()
	go func() {
		m.upd.run.Lock()
		defer m.upd.run.Unlock()
		m.progress(u.Service, func(su *ServiceUpdate) { su.Waiting = "" })
		rec := m.runUpdate(u, why, automatic, by, backup)
		m.saveUpdate(context.Background(), u.Service, rec)
		m.upd.mu.Lock()
		delete(m.upd.active, u.Service)
		delete(m.upd.keep, u.Service)
		m.upd.mu.Unlock()
	}()
	return nil
}

// progress changes an update's progress.
func (m *Manager) progress(name string, f func(su *ServiceUpdate)) {
	m.upd.mu.Lock()
	defer m.upd.mu.Unlock()
	if su := m.upd.active[name]; su != nil {
		f(su)
	}
}

// step marks a step started (and the ones before it finished).
func (m *Manager) step(name, key string) {
	m.progress(name, func(su *ServiceUpdate) {
		now := time.Now()
		for i := range su.Progress.Steps {
			s := &su.Progress.Steps[i]
			if s.Key == key {
				if s.Started.IsZero() {
					s.Started = now
				}
				return
			}
			if s.Started.IsZero() {
				s.Started = now
			}
			if s.Finished.IsZero() {
				s.Finished = now
			}
		}
	})
}

// runUpdate does one update, logging it, and returns its record.
func (m *Manager) runUpdate(u *Update, why string, automatic bool, by string, backup bool) (rec UpdateRecord) {
	ctx := context.Background()
	rec = UpdateRecord{From: u.From, To: u.To, Started: time.Now(), Automatic: automatic, By: by}
	act := m.Activity.Start(activity.ServiceUpdate, fmt.Sprintf("Updating %s to %s", u.Title, u.ToApp()))
	var err error
	defer func() {
		rec.Finished = time.Now()
		if err != nil {
			rec.Error = err.Error()
			if rec.Result == "" {
				rec.Result = UpdateFailed
			}
			m.progress(u.Service, func(su *ServiceUpdate) { su.Progress.Failed = err.Error() })
		} else {
			rec.Result = UpdateDone
		}
		if err != nil && rec.Result == UpdateRolledBack {
			act.Finish(fmt.Errorf("it didn't work, so %s is back on %s: %w", u.Title, u.FromApp(), err))
		} else {
			act.Finish(err)
		}
	}()
	act.Printf("Why: %s.", why)
	act.Printf("From %s to %s.", u.From, u.To)

	// The service as it is, and as it will be.
	services, lerr := state.LoadServices(m.Lab)
	i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == u.Service })
	if i < 0 {
		err = cmp.Or(lerr, ErrNotFound)
		return rec
	}
	old := services[i]
	if old.Instance.Version != u.From {
		err = fmt.Errorf("%s changed to %s meanwhile", u.Title, old.Instance.Version)
		return rec
	}
	host := cmp.Or(old.Instance.Host, m.Lab.NodeID)
	rec.Server = host
	n := m.node(host)
	up, ok := n.(node.Updater)
	if !ok {
		err = fmt.Errorf("Roostack can't reach %s's server", u.Title)
		return rec
	}
	p := m.updateSource(old.Instance)
	if p == nil || p.Version != u.To {
		err = fmt.Errorf("%s %s isn't on offer any more", u.Title, u.ToApp())
		return rec
	}
	newIn, dropped, ierr := updatedInstance(old.Instance, old.Package, p)
	if ierr != nil {
		err = ierr
		return rec
	}
	for _, d := range dropped {
		act.Printf("%s %s has no %s any more: it's left out.", p.Title, u.ToApp(), d)
	}
	for _, note := range u.Notes {
		act.Printf("Upgrade note (%s): %s", note.Version, note.Note)
	}

	// 1–2: download, back up, hold the server's maintenance queue.
	act.Step("Downloading the new version while %s keeps running", u.Title)
	m.step(u.Service, "download")
	images := updateImages(m, p, newIn)
	for _, im := range images {
		act.Printf("%s", im)
	}
	id, serr := up.StartUpdate(ctx, node.UpdateRequest{Title: "Updating " + u.Title, Images: images, Backup: backup})
	if serr != nil {
		err = serr
		return rec
	}
	defer func() {
		if ferr := up.FinishUpdate(ctx, id); ferr != nil {
			m.Log.Warn("ending an update's hold", "service", u.Service, "err", ferr)
		}
	}()
	st, werr := m.awaitHold(ctx, act, u, up, id)
	if werr != nil {
		err = werr
		return rec
	}
	rec.Backup = st.Backup
	if backup && !st.Backup.IsZero() {
		act.Printf("Backed up at %s.", st.Backup.Format(time.Kitchen))
	} else if !backup {
		act.Printf("No backup first: %s.", map[bool]string{true: "it follows the nightly backup", false: "backups aren't set up"}[automatic])
	}
	// Keep the hold while switching and checking.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				up.UpdateStatus(ctx, id)
			}
		}
	}()

	// 3: switch.
	act.Step("Switching to %s", u.ToApp())
	m.step(u.Service, "switch")
	if kerr := state.KeepPackage(m.Lab.Paths, p.Name, p.Version); kerr != nil {
		err = kerr
		return rec
	}
	m.upd.mu.Lock()
	m.upd.keep[u.Service] = old.Instance.Version
	m.upd.mu.Unlock()
	if serr := m.switchVersion(old.Instance, newIn, fmt.Sprintf("Updating %s from %s to %s", u.Title, u.FromApp(), u.ToApp())); serr != nil {
		err = serr
		return rec
	}
	switched := time.Now()
	herr := waitApplied(ctx, n, switched.Add(-time.Second))
	if herr == nil {
		// 4: check.
		act.Step("Checking %s works", u.Title)
		m.step(u.Service, "check")
		herr = m.checkHealth(ctx, act, up, u.Service)
	}
	if herr == nil {
		m.step(u.Service, "")
		act.Step("%s is updated to %s", u.Title, u.ToApp())
		return rec
	}

	// 5: go back.
	act.Printf("%v", herr)
	act.Step("Going back to %s", u.FromApp())
	rec.Result, err = UpdateRolledBack, herr
	if serr := m.switchVersion(newIn, old.Instance, fmt.Sprintf("Updating %s to %s didn't work: going back to %s", u.Title, u.ToApp(), u.FromApp())); serr != nil {
		rec.Result, err = UpdateFailed, fmt.Errorf("%w; going back failed too: %v", herr, serr)
		return rec
	}
	back := time.Now()
	if aerr := waitApplied(ctx, n, back.Add(-time.Second)); aerr != nil {
		rec.Result, err = UpdateFailed, fmt.Errorf("%w; going back failed too: %v", herr, aerr)
		return rec
	}
	if w := m.Withdrawals(ctx)[u.Service]; w != nil && w.Stopped {
		act.Printf("%s %s stays stopped: the service registry says it's dangerous.", u.Title, u.FromApp())
		return rec
	}
	if cerr := m.checkHealth(ctx, act, up, u.Service); cerr != nil {
		rec.Result, err = UpdateFailed, fmt.Errorf("%w; %s doesn't work on %s either: %v", herr, u.Title, u.FromApp(), cerr)
		return rec
	}
	act.Printf("%s runs %s again.", u.Title, u.FromApp())
	return rec
}

// updateImages are the images a new version runs, from its generated
// files; if those can't be made yet (a route new in this version has no
// port until it's applied), every image the package names.
func updateImages(m *Manager, p *pkgspec.Package, in *instance.Instance) []string {
	if out, err := generate.Service(m.Lab, p, in); err == nil {
		return out.Images
	}
	var images []string
	for _, c := range p.Containers {
		images = append(images, c.Image)
	}
	for _, d := range p.Databases {
		switch {
		case d.Image != "":
			images = append(images, d.Image)
		case d.Type == pkgspec.DBPostgres:
			images = append(images, generate.DefaultPostgresImage)
		case d.Type == pkgspec.DBRedis:
			images = append(images, generate.DefaultRedisImage)
		}
	}
	slices.Sort(images)
	return slices.Compact(images)
}

// awaitHold follows an update on its server until it holds the queue.
func (m *Manager) awaitHold(ctx context.Context, act *activity.Log, u *Update, up node.Updater, id string) (node.UpdateStatus, error) {
	var errSince time.Time
	last := ""
	for {
		st, err := up.UpdateStatus(ctx, id)
		switch {
		case err != nil && errSince.IsZero():
			errSince = time.Now()
		case err != nil && time.Since(errSince) > time.Minute:
			return st, fmt.Errorf("lost touch with %s's server: %w", u.Title, err)
		case err != nil:
		default:
			errSince = time.Time{}
			if st.Stage != last {
				last = st.Stage
				switch st.Stage {
				case node.UpdateBackingUp:
					act.Step("Backing up %s first (only what changed since the last backup)", u.Title)
					m.step(u.Service, "backup")
				case node.UpdateWaiting:
					act.Step("Waiting for other maintenance on its server to finish")
				}
			}
			switch st.Stage {
			case node.UpdateHeld:
				return st, nil
			case node.UpdateFailed:
				return st, errors.New(st.Error)
			case node.UpdateReleased:
				return st, errors.New("its server let go of the update")
			}
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(statusEvery):
		}
	}
}

// switchVersion saves a service's instance as to (it must still be from)
// and applies.
func (m *Manager) switchVersion(from, to *instance.Instance, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := state.InstanceFile(m.Lab, to.ID())
	cur, err := instance.Load(path)
	if err != nil {
		return err
	}
	if cur.Version != from.Version {
		return fmt.Errorf("%s changed to %s meanwhile", to.ID(), cur.Version)
	}
	if err := to.Save(path); err != nil {
		return err
	}
	m.applyReason = reason
	m.startApplyLocked()
	return nil
}

// checkHealth waits for a service to be healthy, without restarts, for
// healthStable, giving up after healthWait with the last problem.
func (m *Manager) checkHealth(ctx context.Context, act *activity.Log, up node.Updater, name string) error {
	deadline := time.Now().Add(healthWait)
	var stableSince, failedSince time.Time
	restarts, first, problem := -1, -1, "it didn't start"
	for time.Now().Before(deadline) {
		h, err := up.CheckHealth(ctx, name)
		if err == nil && first < 0 {
			first = h.Restarts
		}
		switch {
		case err != nil:
			problem, stableSince = err.Error(), time.Time{}
		case h.Failed && !failedSince.IsZero() && time.Since(failedSince) >= failedFor:
			return fmt.Errorf("It kept stopping, and systemd gave up restarting it: %s", h.Problem)
		case h.Restarts-first >= maxRestarts:
			return fmt.Errorf("It kept stopping: systemd restarted it %d times in %s (%s)", h.Restarts-first,
				minutes(time.Since(deadline.Add(-healthWait))), cmp.Or(h.Problem, problem))
		case !h.Healthy:
			if h.Problem != problem {
				act.Printf("Not yet: %s", h.Problem)
			}
			problem, stableSince = h.Problem, time.Time{}
		case restarts >= 0 && h.Restarts > restarts:
			problem, stableSince = "it keeps restarting", time.Time{}
			act.Printf("It restarted (%d times so far)", h.Restarts)
		case stableSince.IsZero():
			stableSince = time.Now()
			act.Printf("Running; checking it stays up for %s", healthStable)
		case time.Since(stableSince) >= healthStable:
			act.Printf("It works")
			return nil
		}
		if err == nil {
			restarts = h.Restarts
			if !h.Failed {
				failedSince = time.Time{}
			} else if failedSince.IsZero() {
				failedSince = time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthEvery):
		}
	}
	return fmt.Errorf("It still wasn't working after %s: %s", minutes(healthWait), problem)
}

// nightlyWait bounds waiting for a server's nightly backup before its
// automatic updates (a long first upload can take days: then they wait
// for the next night).
var nightlyWait = 12 * time.Hour

// AutoUpdate runs the night's automatic updates (SPEC.md §3.2: backup,
// then automatic service updates): for each server, once its nightly
// backup, started after since, has finished well, each service whose
// policy takes its update. A server whose backup failed updates nothing.
func (m *Manager) AutoUpdate(ctx context.Context, since time.Time) {
	byHost := map[string][]*Update{}
	services, _ := state.LoadServices(m.Lab)
	for name, u := range m.AvailableUpdates(ctx) {
		if !u.Auto {
			continue
		}
		i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == name })
		if i < 0 {
			continue
		}
		host := cmp.Or(services[i].Instance.Host, m.Lab.NodeID)
		byHost[host] = append(byHost[host], u)
	}
	var wg sync.WaitGroup
	for host, us := range byHost {
		wg.Go(func() {
			n := m.node(host)
			if n == nil || n.Backups() == nil {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, nightlyWait)
			defer cancel()
			if _, err := waitBackup(wctx, n.Backups(), since); err != nil {
				m.Log.Warn("automatic updates skipped: no nightly backup", "server", m.nameOf(host), "err", err)
				return
			}
			slices.SortFunc(us, func(a, b *Update) int { return strings.Compare(a.Service, b.Service) })
			for _, u := range us {
				why := fmt.Sprintf("its update policy (%s) takes this %s update, after the nightly backup", u.Policy, u.Kind)
				if err := m.startUpdate(u, why, true, "", false); err != nil {
					m.Log.Info("automatic update not started", "service", u.Service, "err", err)
				}
			}
		})
	}
	wg.Wait()
}

// UpdateStopped updates, straight away, each service Roostack stopped
// because the registry says its version is dangerous, when a newer
// version fits its update policy (the Owner, 2026-10-14: not on notify,
// which only offers it). The service is down anyway, so it doesn't wait
// for the night; it backs up first.
func (m *Manager) UpdateStopped(ctx context.Context) {
	ws := m.Withdrawals(ctx)
	for name, u := range m.AvailableUpdates(ctx) {
		if w := ws[name]; w == nil || !w.Stopped || !u.Auto || m.Updating(name) != nil {
			continue
		}
		why := fmt.Sprintf("Roostack stopped it because the service registry says %s is dangerous, and its update policy (%s) takes this %s update",
			u.FromApp(), u.Policy, u.Kind)
		if err := m.startUpdate(u, why, true, "", true); err != nil {
			m.Log.Info("update of a stopped service not started", "service", name, "err", err)
		}
	}
}

// minutes says roughly how long d is, for people ("3 minutes").
func minutes(d time.Duration) string {
	switch m := int(d.Round(time.Minute) / time.Minute); {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Round(time.Second)/time.Second))
	case m == 1:
		return "a minute"
	default:
		return fmt.Sprintf("%d minutes", m)
	}
}
