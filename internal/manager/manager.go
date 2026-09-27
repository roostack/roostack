// Package manager is the business logic behind managing services from the
// UI: listing the catalog, installing and removing services, and applying
// the result to the host. HTTP handlers stay thin (SPEC.md §6.3) and call
// this package.
package manager

import (
	"cmp"
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

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/b2"
	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/instance"
	"github.com/mylab/mylab/internal/node"
	"github.com/mylab/mylab/internal/pkgspec"
	"github.com/mylab/mylab/internal/state"
)

// Types shared with the node that does the work (internal/node), under
// the names the web layer uses.
type (
	FormError   = node.FormError
	ApplyStatus = node.ApplyStatus
	HookStatus  = node.HookStatus
	ActionError = node.ActionError
)

// ErrNotFound means no such package or installed service.
var ErrNotFound = node.ErrNotFound

// Manager is the controller side of the lab (SPEC.md §10.3): it keeps
// desired state (instance files), decides what each machine runs, and
// hands that to the machine as an Assignment. Machine work (applying,
// install hooks, actions) happens in the Node.
type Manager struct {
	Lab config.Lab
	Log *slog.Logger
	// Node is this machine (the controller).
	Node node.Node
	// Others lists the lab's other machines (SPEC.md §10); nil if none.
	Others func() []RemoteMachine
	// Registry records the other machines (removing, renaming).
	Registry MachineRegistry
	// RenameSelf renames this machine (optional).
	RenameSelf func(name string) error
	// BackupStore records backup providers and targets, and OpenRepo opens
	// a repository with the recovery key (backups.go; optional).
	BackupStore BackupStore
	OpenRepo    func(url string, env []string) *backup.Repo
	// HasRecoveryKey reports whether the lab has its recovery key yet.
	HasRecoveryKey func() bool
	// NewB2 makes a Backblaze client (tests; nil uses the real service).
	NewB2 func(keyID, key string) *b2.Client
	// Activity shows problems building an assignment (optional).
	Activity *activity.Registry

	// mu guards state changes (install/remove) and what is still to hand
	// over to the node.
	mu sync.Mutex
	// secrets holds values typed into install forms, by service, until
	// the next assignment hands them to the service's machine. Never
	// written to disk.
	secrets map[string]map[string][]byte
	// setUp lists newly installed services whose install hooks the node
	// should run.
	setUp []string
	// assignErr is why the last assignment couldn't be built, if it failed.
	assignErr error
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
	// Host is the machine to run it on (a node ID); empty is this one.
	Host string
}

func formErrorf(format string, args ...any) error {
	return &FormError{Msg: fmt.Sprintf(format, args...)}
}

// Install adds a service to desired state and starts applying it. The
// service is private (LAN/VPN only); public access is not offered yet.
func (m *Manager) Install(ctx context.Context, name string, req InstallRequest) error {
	p, err := m.Package(name)
	if err != nil {
		return err
	}
	host := cmp.Or(req.Host, m.Lab.NodeID)
	if m.node(host) == nil {
		return formErrorf("Choose which machine runs %s.", p.Title)
	}
	in := &instance.Instance{Package: p.Name, Host: host, Version: p.Version, Updates: instance.UpdatesNotify,
		Settings: map[string]any{}}

	secrets := map[string][]byte{}
	for _, s := range p.Settings {
		raw, given := req.Settings[s.Key]
		switch s.Type {
		case pkgspec.SettingBool, pkgspec.SettingString, pkgspec.SettingInt:
			v, err := parseSetting(s, raw, given)
			if err != nil {
				return err
			}
			in.Settings[s.Key] = v
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
			secrets[node.SecretName(p.Name, s.Key)] = []byte(v)
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
	onNode := m.onNode(services, in.Host)
	if _, err := state.AllocatePorts(onNode); err != nil {
		return err
	}
	// Check the machine's services still generate, and no hostname is used
	// twice in the lab, before saving anything.
	if _, err := state.Desired(m.Lab, onNode, m.proxied(services, in.Host)...); err != nil {
		if strings.Contains(err.Error(), "already used") {
			return formErrorf("That web address is already used by another service. Choose a different one.")
		}
		return err
	}
	if err := in.Save(state.InstanceFile(m.Lab, p.Name)); err != nil {
		return err
	}
	// Install hooks (e.g. creating the admin account) run once the
	// service has started.
	if len(p.HooksOn(pkgspec.HookInstall)) > 0 {
		m.setUp = append(m.setUp, p.Name)
	}
	if m.secrets == nil {
		m.secrets = map[string]map[string][]byte{}
	}
	m.secrets[p.Name] = secrets
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
	if _, err := state.Desired(m.Lab, state.OnNode(services, in.Host)); err != nil {
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

// parseSetting converts a form value for a bool, string or int setting.
// A missing bool means false (an unticked checkbox).
func parseSetting(s pkgspec.Setting, raw string, given bool) (any, error) {
	title := cmp.Or(s.Title, s.Key)
	switch s.Type {
	case pkgspec.SettingBool:
		return given && (raw == "on" || raw == "true"), nil
	case pkgspec.SettingInt:
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return nil, formErrorf("%s must be a whole number.", title)
		}
		return n, nil
	}
	v := strings.TrimSpace(raw)
	if s.Required && v == "" {
		return nil, formErrorf("%s is required.", title)
	}
	if s.MinLength > 0 && len([]rune(v)) < s.MinLength {
		return nil, formErrorf("%s must be at least %d characters.", title, s.MinLength)
	}
	return v, nil
}

// SettingsRequest is what the service settings form collects.
type SettingsRequest struct {
	// Subdomain for the package's main route; empty keeps the current one.
	Subdomain string
	// Settings holds raw form values for the changeable settings.
	Settings map[string]string
}

// Changeable reports whether a setting can be changed after install from
// MyLab: not secrets, and not install-only settings (e.g. the first
// admin's details).
func Changeable(s pkgspec.Setting) bool {
	return s.Type != pkgspec.SettingSecret && !s.InstallOnly
}

// CurrentSettings returns an installed service's setting values (with
// package defaults filled in) and its main route's subdomain.
func (m *Manager) CurrentSettings(name string) (map[string]any, string, error) {
	p, in, err := m.installedService(name)
	if err != nil {
		return nil, "", err
	}
	values, routes := generate.Values(m.Lab, p, in)
	return values, routes[MainRoute(p)].Subdomain, nil
}

// UpdateSettings changes an installed service's settings and web address,
// then applies the change (which restarts it).
func (m *Manager) UpdateSettings(ctx context.Context, name string, req SettingsRequest) error {
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
	if in.Settings == nil {
		in.Settings = map[string]any{}
	}
	for _, s := range p.Settings {
		if !Changeable(s) {
			continue
		}
		raw, given := req.Settings[s.Key]
		v, err := parseSetting(s, raw, given)
		if err != nil {
			return err
		}
		in.Settings[s.Key] = v
	}
	if sub := strings.ToLower(strings.TrimSpace(req.Subdomain)); sub != "" {
		if route := MainRoute(p); route != "" {
			if in.Routes == nil {
				in.Routes = map[string]instance.RouteOverride{}
			}
			o := in.Routes[route]
			o.Subdomain = sub
			in.Routes[route] = o
		}
	}
	if err := in.Validate(p); err != nil {
		return formErrorf("%s", err)
	}
	if _, err := state.Desired(m.Lab, state.OnNode(services, in.Host)); err != nil {
		if strings.Contains(err.Error(), "already used") {
			return formErrorf("That web address is already used by another service. Choose a different one.")
		}
		return err
	}
	if err := in.Save(state.InstanceFile(m.Lab, name)); err != nil {
		return err
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

// Wait blocks until the current apply finishes (for tests and tools).
func (m *Manager) Wait() {
	if w, ok := m.Node.(interface{ Wait() }); ok {
		w.Wait()
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
