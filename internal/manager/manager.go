// Package manager is the business logic behind managing services from the
// UI: listing the catalog, installing and removing services, and applying
// the result to the host. HTTP handlers stay thin (SPEC.md §6.3) and call
// this package.
package manager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/agent"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/firewall"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/instance"
	"github.com/mylab/mylab/internal/pkgspec"
	"github.com/mylab/mylab/internal/reconcile"
	"github.com/mylab/mylab/internal/state"
)

// ErrNotFound means no such package or installed service.
var ErrNotFound = errors.New("not found")

// Manager installs, removes and applies services.
type Manager struct {
	Lab config.Lab
	// Reconciler applies desired state. Its UserSecret is set by Manager.
	Reconciler *reconcile.Reconciler
	Log        *slog.Logger

	// mu guards state changes (install/remove) and the apply status.
	mu     sync.Mutex
	status ApplyStatus
	// again is set when a change arrives during an apply, so another
	// apply runs afterwards.
	again bool
	// userSecrets holds values typed into install forms until the next
	// apply creates the Podman secrets. Never written to disk.
	userSecrets map[string][]byte
	// done is closed when the current apply finishes (for tests).
	done chan struct{}

	// Hooks (hooks.go). State records install hook progress; without
	// State and Exec, hooks don't run.
	State   KV
	Secrets SecretStore
	Exec    ExecFunc
	// Activity shows applies and install hooks live in the UI (optional).
	Activity *activity.Registry

	// Firewall applies the host firewall through mylab-agent after each
	// apply. Nil leaves the firewall alone.
	Firewall FirewallApplier

	// HookRetry and HookDeadline tune install hook retries (0: defaults).
	HookRetry, HookDeadline time.Duration

	hooksBusy, hooksAgain bool
	hooksRunning          map[string]bool
	hooksDone             chan struct{}
}

// FirewallApplier is mylab-agent's firewall operation.
type FirewallApplier interface {
	ApplyFirewall(ctx context.Context, fw agent.Firewall) error
}

// ApplyStatus is shown on the dashboard.
type ApplyStatus struct {
	Running  bool
	LastRun  time.Time
	LastErr  error
	Finished bool // at least one apply has finished
}

// CatalogItem is one package in the catalog.
type CatalogItem struct {
	Name        string
	Title       string
	Description string
	Installed   bool
}

// Catalog lists every package, marking the installed ones.
func (m *Manager) Catalog() ([]CatalogItem, error) {
	entries, err := os.ReadDir(m.Lab.Paths.CatalogDir)
	if err != nil {
		return nil, err
	}
	installed, err := m.installed()
	if err != nil {
		return nil, err
	}
	var items []CatalogItem
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := pkgspec.Load(m.Lab.Paths.PackageFile(e.Name()))
		if err != nil {
			// One broken package shouldn't hide the rest of the catalog.
			m.Log.Error("loading package", "package", e.Name(), "err", err)
			continue
		}
		items = append(items, CatalogItem{Name: p.Name, Title: p.Title, Description: p.Description, Installed: installed[p.Name]})
	}
	slices.SortFunc(items, func(a, b CatalogItem) int { return strings.Compare(a.Title, b.Title) })
	return items, nil
}

// Package returns a catalog package.
func (m *Manager) Package(name string) (*pkgspec.Package, error) {
	if !validName(name) {
		return nil, ErrNotFound
	}
	p, err := pkgspec.Load(m.Lab.Paths.PackageFile(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return p, err
}

// InstallRequest is what the install form collects. Values are raw form
// strings; Install converts them using the package's setting types.
type InstallRequest struct {
	// Subdomain for the package's main route; empty keeps the default.
	Subdomain string
	// Settings holds non-secret settings. A missing bool means false
	// (an unticked checkbox).
	Settings map[string]string
	// Secrets holds secret settings the user must provide.
	Secrets map[string]string
}

// FormError is a problem the user can fix in the form.
type FormError struct{ Msg string }

func (e *FormError) Error() string { return e.Msg }

func formErrorf(format string, args ...any) error {
	return &FormError{fmt.Sprintf(format, args...)}
}

// Install adds a service to desired state and starts applying it. The
// service is private (LAN/VPN only); public access is not offered yet.
func (m *Manager) Install(ctx context.Context, name string, req InstallRequest) error {
	p, err := m.Package(name)
	if err != nil {
		return err
	}
	in := &instance.Instance{Package: p.Name, Version: p.Version, Updates: instance.UpdatesNotify, Settings: map[string]any{}}

	secrets := map[string][]byte{}
	for _, s := range p.Settings {
		raw, given := req.Settings[s.Key]
		switch s.Type {
		case pkgspec.SettingBool:
			in.Settings[s.Key] = given && (raw == "on" || raw == "true")
		case pkgspec.SettingString:
			v := strings.TrimSpace(raw)
			if s.Required && v == "" {
				return formErrorf("%s is required.", s.Title)
			}
			if s.MinLength > 0 && len([]rune(v)) < s.MinLength {
				return formErrorf("%s must be at least %d characters.", s.Title, s.MinLength)
			}
			in.Settings[s.Key] = v
		case pkgspec.SettingInt:
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				return formErrorf("%s must be a whole number.", s.Title)
			}
			in.Settings[s.Key] = n
		case pkgspec.SettingSecret:
			if s.Generate {
				continue
			}
			v := req.Secrets[s.Key]
			if v == "" {
				return formErrorf("%s is required.", s.Title)
			}
			if s.MinLength > 0 && len([]rune(v)) < s.MinLength {
				return formErrorf("%s must be at least %d characters.", s.Title, s.MinLength)
			}
			if strings.ContainsAny(v, "\n\r\x00") {
				return formErrorf("%s can't contain line breaks.", s.Title)
			}
			secrets[secretName(p.Name, s.Key)] = []byte(v)
		}
	}
	if sub := strings.ToLower(strings.TrimSpace(req.Subdomain)); sub != "" {
		route := MainRoute(p)
		if route == "" {
			return formErrorf("%s has no web address to choose.", p.Title)
		}
		in.Routes = map[string]instance.RouteOverride{route: {Subdomain: sub}}
	}
	if err := in.Validate(p); err != nil {
		return formErrorf("%s", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return err
	}
	for _, s := range services {
		if s.Package.Name == p.Name {
			return formErrorf("%s is already installed.", p.Title)
		}
	}
	services = append(services, state.Service{Instance: in, Package: p})
	if err := m.allocateAndSave(services[:len(services)-1]); err != nil {
		return err
	}
	if _, err := state.AllocatePorts(services); err != nil {
		return err
	}
	// Check the whole lab still generates (e.g. no hostname clash) before
	// saving anything.
	if _, err := state.Desired(m.Lab, services); err != nil {
		if strings.Contains(err.Error(), "already used") {
			return formErrorf("That web address is already used by another service. Choose a different one.")
		}
		return err
	}
	// Install hooks (e.g. creating the admin account) run once the
	// service has started.
	if m.State != nil && len(p.HooksOn(pkgspec.HookInstall)) > 0 {
		if err := m.State.SetSetting(ctx, hookKey(p.Name), hookPending); err != nil {
			return err
		}
	}
	if err := in.Save(state.InstanceFile(m.Lab, p.Name)); err != nil {
		return err
	}
	if m.userSecrets == nil {
		m.userSecrets = map[string][]byte{}
	}
	for k, v := range secrets {
		m.userSecrets[k] = v
	}
	m.startApplyLocked()
	return nil
}

// PortStatus is a package port with its current settings, for the
// service page.
type PortStatus struct {
	Name        string
	Title       string
	Description string
	generate.PortContext
}

// Ports lists an installed service's package ports.
func (m *Manager) Ports(name string) ([]PortStatus, error) {
	p, in, err := m.installedService(name)
	if err != nil {
		return nil, err
	}
	resolved := generate.ResolvePorts(p, in)
	var ports []PortStatus
	for _, pn := range slices.Sorted(maps.Keys(p.Ports)) {
		pp := p.Ports[pn]
		ports = append(ports, PortStatus{Name: pn, Title: pp.Title, Description: pp.Description, PortContext: resolved[pn]})
	}
	return ports, nil
}

// SetPort turns a package port on or off, or moves it to another host
// port, and applies the change. Ports stay private (home network only);
// public ports come with the firewall and CrowdSec (SPEC.md §6.10).
func (m *Manager) SetPort(ctx context.Context, name, port string, enabled bool, hostPort int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(services, func(s state.Service) bool { return s.Package.Name == name })
	if i < 0 {
		return ErrNotFound
	}
	p, in := services[i].Package, services[i].Instance
	pp := p.Ports[port]
	if pp == nil {
		return ErrNotFound
	}
	if hostPort < pkgspec.MinHostPort || hostPort > 65535 {
		return formErrorf("Choose a port number from %d to 65535.", pkgspec.MinHostPort)
	}
	if in.Ports == nil {
		in.Ports = map[string]instance.PortOverride{}
	}
	o := in.Ports[port]
	o.Enabled, o.HostPort = &enabled, hostPort
	if hostPort == pp.HostPort {
		o.HostPort = 0 // keep following the package's default
	}
	in.Ports[port] = o
	if _, err := state.Desired(m.Lab, services); err != nil {
		var pe *state.PortInUseError
		switch {
		case errors.As(err, &pe) && pe.UsedBy == "":
			return formErrorf("MyLab keeps ports %d-%d for its web services. Choose another port.",
				config.DefaultRoutePortMin, config.DefaultRoutePortMax)
		case errors.As(err, &pe):
			return formErrorf("Port %d is already used by %s. Choose another port.", pe.Port, pe.UsedBy)
		}
		return err
	}
	if err := in.Save(state.InstanceFile(m.Lab, name)); err != nil {
		return err
	}
	m.startApplyLocked()
	return nil
}

// installedService loads an installed service's package and instance.
func (m *Manager) installedService(name string) (*pkgspec.Package, *instance.Instance, error) {
	if !validName(name) {
		return nil, nil, ErrNotFound
	}
	p, err := m.Package(name)
	if err != nil {
		return nil, nil, err
	}
	in, err := instance.Load(state.InstanceFile(m.Lab, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrNotFound
	}
	return p, in, err
}

// Remove takes a service out of desired state and applies the change. Its
// data stays on disk (SPEC.md: the reconciler never deletes data).
func (m *Manager) Remove(ctx context.Context, name string) error {
	if !validName(name) {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	err := os.Remove(state.InstanceFile(m.Lab, name))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if m.State != nil {
		m.State.SetSetting(ctx, hookKey(name), "")
	}
	m.startApplyLocked()
	return nil
}

// DataDirs lists where a service keeps data, to tell the user what
// removing it leaves behind.
func (m *Manager) DataDirs(name string) []string {
	return []string{
		filepath.Join(m.Lab.Paths.ServicesDir, name),
		filepath.Join(m.Lab.Paths.MediaDir, name),
	}
}

// Apply starts applying desired state in the background (e.g. at startup).
func (m *Manager) Apply() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startApplyLocked()
}

// Status reports the current or last apply.
func (m *Manager) Status() ApplyStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// startApplyLocked runs an apply in the background. If one is already
// running, another runs when it finishes, so the latest change is applied.
func (m *Manager) startApplyLocked() {
	if m.status.Running {
		m.again = true
		return
	}
	m.status.Running = true
	m.done = make(chan struct{})
	go m.applyLoop(m.done)
}

func (m *Manager) applyLoop(done chan struct{}) {
	defer close(done)
	for {
		err := m.applyOnce(context.Background())
		m.mu.Lock()
		m.status.LastRun, m.status.LastErr, m.status.Finished = time.Now(), err, true
		if err != nil {
			m.Log.Error("applying changes", "err", err)
		} else {
			defer m.startInstallHooks() // after unlocking
		}
		if !m.again {
			m.status.Running = false
			m.mu.Unlock()
			return
		}
		m.again = false
		m.mu.Unlock()
	}
}

func (m *Manager) applyOnce(ctx context.Context) (err error) {
	// A log only for applies that change something (or fail): the one at
	// every startup usually has nothing to do.
	var act *activity.Log
	defer func() {
		if err != nil && act == nil {
			act = m.Activity.Start(activity.Apply, "Applying changes")
		}
		act.Finish(err)
	}()
	// Load and allocate under the lock so an install can't interleave.
	m.mu.Lock()
	services, err := state.LoadServices(m.Lab)
	if err == nil {
		err = m.allocateAndSave(services)
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	desired, err := state.Desired(m.Lab, services)
	if err != nil {
		return err
	}
	r := *m.Reconciler
	r.UserSecret = func(name string) ([]byte, bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		v, ok := m.userSecrets[name]
		return v, ok
	}
	plan, err := r.Plan(ctx, desired)
	if err != nil {
		return err
	}
	if !plan.Empty() {
		act = m.Activity.Start(activity.Apply, "Applying changes")
		r.Log = act
	}
	if err := r.Apply(ctx, plan); err != nil {
		return err
	}
	// Created secrets live in Podman now; forget the typed-in values.
	m.mu.Lock()
	for _, s := range plan.Secrets {
		delete(m.userSecrets, s.Name)
	}
	m.mu.Unlock()

	// The firewall follows the services, e.g. a package port just turned on.
	if fw, ok := firewall.Desired(m.Lab, desired.Services); ok && m.Firewall != nil {
		act.Step("Updating the firewall")
		if err := m.Firewall.ApplyFirewall(ctx, fw); err != nil {
			return fmt.Errorf("applying the firewall: %w", err)
		}
	}
	return nil
}

// allocateAndSave gives routes without a port one, and saves those
// instances so the ports never change. Callers hold m.mu.
func (m *Manager) allocateAndSave(services []state.Service) error {
	changed, err := state.AllocatePorts(services)
	if err != nil {
		return err
	}
	for _, s := range changed {
		if err := s.Instance.Save(state.InstanceFile(m.Lab, s.Package.Name)); err != nil {
			return err
		}
	}
	return nil
}

// Wait blocks until the current apply finishes (for tests and tools).
func (m *Manager) Wait() {
	m.mu.Lock()
	done := m.done
	m.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (m *Manager) installed() (map[string]bool, error) {
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, s := range services {
		out[s.Package.Name] = true
	}
	return out, nil
}

// MainRoute is the route whose subdomain the install form lets the user
// choose: "web" if present, otherwise the first by name.
func MainRoute(p *pkgspec.Package) string {
	if _, ok := p.Routes["web"]; ok {
		return "web"
	}
	var names []string
	for n := range p.Routes {
		names = append(names, n)
	}
	slices.Sort(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// secretName matches generate's naming for secret settings.
func secretName(svc, key string) string { return "mylab-" + svc + "-" + key }

// validName guards against path tricks in names taken from URLs.
func validName(name string) bool {
	if name == "" || len(name) > 31 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
