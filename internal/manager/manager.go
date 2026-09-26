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
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mylab/mylab/internal/config"
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
			in.Settings[s.Key] = strings.TrimSpace(raw)
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

func (m *Manager) applyOnce(ctx context.Context) error {
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
	if err := r.Apply(ctx, plan); err != nil {
		return err
	}
	// Created secrets live in Podman now; forget the typed-in values.
	m.mu.Lock()
	for _, s := range plan.Secrets {
		delete(m.userSecrets, s.Name)
	}
	m.mu.Unlock()
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
