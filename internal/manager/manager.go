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
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/b2"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/s3api"
	"roostack.cloud/roostack/internal/state"
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
// desired state (instance files), decides what each server runs, and
// hands that to the server as an Assignment. Server work (applying,
// install hooks, actions) happens in the Node.
type Manager struct {
	Lab config.Lab
	Log *slog.Logger
	// Version is the running Roostack's (main.version), checked against
	// the requires.roostack of a package the Owner adds (SPEC.md §8.3.5);
	// empty is a development build, which runs everything.
	Version string
	// Relay is Roostack Cloud's relay, a way in for public visitors.
	Relay Relay
	// Node is this server (the controller).
	Node node.Node
	// Others lists the lab's other servers (SPEC.md §10); nil if none.
	Others func() []RemoteServer
	// Registry records the other servers (removing, renaming).
	Registry ServerRegistry
	// RenameSelf renames this server (optional).
	RenameSelf func(name string) error
	// BackupStore records backup providers and targets, and OpenRepo opens
	// a repository with the recovery key (backups.go; optional).
	BackupStore BackupStore
	OpenRepo    func(url string, env []string) *backup.Repo
	// CloudBackup is Roostack Cloud backup, when the lab is linked (nil:
	// none): its short-lived keys and the lab's usage (SPEC.md §6.9).
	CloudBackup CloudBackup
	// HasRecoveryKey reports whether the lab has its recovery key yet;
	// EnsureRecoveryKey makes it when the first backup is set up (optional).
	HasRecoveryKey    func() bool
	EnsureRecoveryKey func() error
	// Drives are this server's drives, for backups on them (roostack-agent).
	Drives DriveAgent
	// Storage for other labs (guests.go; optional): the host's hub and
	// records, how much a guest stores here, the agent for links between
	// labs, and a hook when this lab starts storing at another.
	GuestHub     *labnet.GuestHub
	GuestRecords GuestRecords

	LinkAgent   LinkAgent
	RemoteAdded func(RemoteStorage)

	// remoteSeen is each storage at another lab's latest answer (by Ref).
	remoteMu   sync.Mutex
	remoteSeen map[string]labnet.GuestUsage
	// NewB2 makes a Backblaze client (tests; nil uses the real service).
	NewB2 func(keyID, key string) *b2.Client
	// NewS3 makes an S3 client for an online storage account (tests; nil
	// uses the account's endpoint).
	NewS3 func(s3 S3Provider) *s3api.Client
	// Activity shows problems building an assignment (optional).
	Activity *activity.Registry
	// UpdateLab saves a change to lab.yaml (config.Update), for settings
	// changed without a restart (optional).
	UpdateLab func(change func(*config.Lab)) error
	// Submit queues a job with the maintenance coordinator (optional).
	Submit func(maintenance.Job)
	// Settings are the controller's settings (store.Store): servers'
	// locations and direct links (links.go; optional).
	Settings KV
	// RegistryIndex is the newest index the controller has fetched from
	// the service registry (registry.Fetcher.Index; nil before the first),
	// for withdrawals of installed services (withdrawals.go; optional).
	RegistryIndex func() (*registry.Index, error)

	// mu guards state changes (install/remove) and what is still to hand
	// over to the node.
	mu sync.Mutex
	// secrets holds values typed into install forms, by service, until
	// the next assignment hands them to the service's server. Never
	// written to disk.
	secrets map[string]map[string][]byte
	// setUp lists newly installed services whose install hooks the node
	// should run.
	setUp []string
	// assignErr is why the last assignment couldn't be built, if it failed.
	assignErr error
	// applyReason says why the next apply starts, for the servers' logs
	// (ApplyBecause); callers hold mu.
	applyReason string
	// restoring are the services the controller is restoring (restore.go).
	restoring map[string]bool
	// links are direct links between servers (links.go).
	links linkState
	// keyMu makes recovery key changes one at a time (keychange.go).
	keyMu sync.Mutex
	// public is the lab's public access settings as changed at runtime
	// (public.go); nil until first used, then read from Lab. Kept apart
	// from Lab, which is read without the lock.
	public *config.Public
	// noStops is the Owner's opt-out from registry stops as changed at
	// runtime (withdrawals.go); nil until first used, then read from Lab.
	noStops *bool
	// stopsKey is what the last assignment stopped (markStops), and
	// awaiting the servers whose last backup a stop waits for.
	stopsKey string
	awaiting map[string]bool
	// upd are services updating (update.go).
	upd updates
	// relayMu makes updates to Roostack Cloud's relay one at a time, so an
	// older one can't reach Cloud after a newer one (edge.go). relayGen
	// counts updates (under mu), so a background one gives way to any
	// newer; relayWG is the background ones still running.
	relayMu  sync.Mutex
	relayGen uint64
	relayWG  sync.WaitGroup
}

// CatalogItem is one package in the catalog.
type CatalogItem struct {
	Name        string
	Title       string
	Description string
	// Categories and Keywords are its about: filters and search terms
	// (SPEC.md §8.3.6); Icon says its logo is there (PackageIcon).
	Categories []string
	Keywords   []string
	Icon       bool
	Installed  bool
	// Local is a package the Owner added themselves ("Your own", SPEC.md
	// §8.3.4), not one from the service registry.
	Local bool
}

// Catalog lists every package, marking the installed ones: the Owner's
// own, then the registry's. A registry package with the name of one of
// the Owner's own isn't listed: theirs keeps the name (SPEC.md §8.3.4).
func (m *Manager) Catalog() ([]CatalogItem, error) {
	installed, err := m.installed()
	if err != nil {
		return nil, err
	}
	locals, err := state.LocalPackageNames(m.Lab.Paths)
	if err != nil {
		return nil, err
	}
	var items []CatalogItem
	local := map[string]bool{}
	for _, name := range locals {
		local[name] = true
		p, err := state.LoadLocalPackage(m.Lab.Paths, name)
		if err != nil {
			m.Log.Error("loading one of your own packages", "package", name, "err", err)
			continue
		}
		_, err = os.Stat(state.LocalIconFile(m.Lab.Paths, p))
		items = append(items, CatalogItem{Name: p.Name, Title: p.Title, Description: p.Description, Categories: p.About.Categories,
			Keywords: p.About.Keywords, Icon: err == nil, Installed: installed[p.Name], Local: true})
	}

	entries, err := os.ReadDir(m.Lab.Paths.CatalogDir)
	if errors.Is(err, os.ErrNotExist) {
		entries = nil // nothing fetched from the registry yet (SPEC.md §8.3.3)
	} else if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || local[e.Name()] {
			continue
		}
		p, err := state.LoadCatalogPackage(m.Lab.Paths, e.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue // only its logo: it needs a newer Roostack (registry.Listed)
		}
		if err != nil {
			// One broken package shouldn't hide the rest of the catalog.
			m.Log.Error("loading package", "package", e.Name(), "err", err)
			continue
		}
		// The catalog is registry packages, which follow the package
		// policy (SPEC.md §8.3.4: the registry's CI won't publish one that
		// doesn't); one that breaks it anyway is a registry bug, so it's
		// logged, and installing it says why it can't be (Install).
		for _, problem := range p.Policy() {
			m.Log.Error("package breaks the package policy", "package", p.Name, "problem", problem)
		}
		item := CatalogItem{Name: p.Name, Title: p.Title, Description: p.Description, Categories: p.About.Categories,
			Keywords: p.About.Keywords, Installed: installed[p.Name]}
		if p.About.Icon != "" {
			_, err := os.Stat(filepath.Join(m.Lab.Paths.CatalogPackageDir(p.Name), p.About.Icon))
			item.Icon = err == nil
		}
		items = append(items, item)
	}
	slices.SortFunc(items, func(a, b CatalogItem) int { return strings.Compare(a.Title, b.Title) })
	return items, nil
}

// Package returns what installing a package would install: one of the
// Owner's own by that name, or else the newest version in the catalog. An
// installed service uses the lab's own copy (ServicePackage, SPEC.md
// §8.3.1).
func (m *Manager) Package(name string) (*pkgspec.Package, error) {
	if !validName(name) {
		return nil, ErrNotFound
	}
	p, err := state.LoadLocalPackage(m.Lab.Paths, name)
	if !errors.Is(err, os.ErrNotExist) {
		return p, err
	}
	p, err = state.LoadCatalogPackage(m.Lab.Paths, name)
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
	// Host is the server to run it on (a node ID); empty is this one.
	Host string
	// Public makes its addresses public from the start, except any its
	// package says aren't safe to: the default on a lab on the internet,
	// where private means only through an SSH tunnel (SPEC.md §6.12).
	Public bool
	// Name and Title are a second copy's own (SPEC.md §8.2): its ID,
	// which never changes, and what people call it. Empty for the first
	// copy, which is named after its package.
	Name, Title string
	// AcceptRisks says the Owner has read what one of their own packages
	// asks for that registry packages can't (Package.Risks) and wants it
	// installed anyway (SPEC.md §8.3.4). The web layer lets only the
	// Owner set it.
	AcceptRisks bool
}

func formErrorf(format string, args ...any) error {
	return &FormError{Msg: fmt.Sprintf(format, args...)}
}

// Install adds a service to desired state and starts applying it. It's
// private (the home network, or on a lab on the internet only through an
// SSH tunnel) unless req.Public makes it public from the start.
func (m *Manager) Install(ctx context.Context, name string, req InstallRequest) error {
	p, err := m.Package(name)
	if err != nil {
		return err
	}
	if err := m.CheckPolicy(p, req.AcceptRisks); err != nil {
		return err
	}
	in, id, err := m.draft(p, req, false)
	if err != nil {
		return err
	}
	in.Local = m.IsLocal(p.Name)
	secrets := map[string][]byte{}
	for _, s := range p.Settings {
		if s.Type != pkgspec.SettingSecret || s.Generate {
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
		secrets[node.SecretName(id, s.Key)] = []byte(v)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return err
	}
	for _, s := range services {
		switch {
		case s.Name() == id && id == p.Name:
			return formErrorf("%s is already installed. To add another copy, give it a name of its own.", p.Title)
		case s.Name() == id:
			return formErrorf("A service called %s is already installed. Choose another name.", id)
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
	// Check the server's services still generate, and no hostname is used
	// twice in the lab, before saving anything.
	if _, err := state.Desired(m.Lab, onNode, m.proxied(services, in.Host)...); err != nil {
		if strings.Contains(err.Error(), "already used") {
			return formErrorf("That web address is already used by another service. Choose a different one.")
		}
		return err
	}
	// The lab keeps the package file it installs from, before the
	// instance that uses it (SPEC.md §8.3.1).
	if err := state.KeepPackage(m.Lab.Paths, p.Name, p.Version); err != nil {
		return err
	}
	if err := in.Save(state.InstanceFile(m.Lab, id)); err != nil {
		return err
	}
	// Install hooks (e.g. creating the admin account) run once the
	// service has started.
	if len(p.HooksOn(pkgspec.HookInstall)) > 0 {
		m.setUp = append(m.setUp, id)
	}
	if m.secrets == nil {
		m.secrets = map[string]map[string][]byte{}
	}
	m.secrets[id] = secrets
	m.applyReason = "Installing " + id
	m.startApplyLocked()
	return nil
}

// draft is the service an install request describes, not yet saved and
// without its ports: its server, title, name (returned as its ID),
// settings and addresses. Secrets are Install's alone. A lenient draft
// (for the summary, while the form is being filled in) leaves out
// settings that aren't valid yet, so they take the package's defaults.
func (m *Manager) draft(p *pkgspec.Package, req InstallRequest, lenient bool) (*instance.Instance, string, error) {
	host := cmp.Or(req.Host, m.Lab.NodeID)
	if m.node(host) == nil {
		return nil, "", formErrorf("Choose which server runs %s.", p.Title)
	}
	in := &instance.Instance{Package: p.Name, Host: host, Version: p.Version, Updates: instance.UpdatesNotify,
		Settings: map[string]any{}}
	title, err := checkTitle(req.Title, p)
	if err != nil {
		return nil, "", err
	}
	in.Title = title
	id := p.Name
	if n := strings.ToLower(strings.TrimSpace(req.Name)); n != "" && n != p.Name {
		if !strings.HasPrefix(n, p.Name+"-") || !instance.ValidName(n) {
			return nil, "", formErrorf("Its name is %s, or %s- and then lowercase letters, digits and dashes (up to %d in all), e.g. %s-work.",
				p.Name, p.Name, instance.MaxName, p.Name)
		}
		id, in.Name = n, n
	}
	for _, s := range p.Settings {
		if s.Type == pkgspec.SettingSecret {
			continue
		}
		raw, given := req.Settings[s.Key]
		v, err := parseSetting(s, raw, given)
		if err != nil && lenient {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		in.Settings[s.Key] = v
	}
	if sub := strings.ToLower(strings.TrimSpace(req.Subdomain)); sub != "" {
		route := MainRoute(p)
		if route == "" {
			return nil, "", formErrorf("%s has no web address to choose.", p.Title)
		}
		in.Routes = map[string]instance.RouteOverride{route: {Subdomain: sub}}
	}
	if req.Public {
		for name, r := range p.Routes {
			if r.Public.Unsafe != "" {
				continue // stays private: making it public needs its warning
			}
			if in.Routes == nil {
				in.Routes = map[string]instance.RouteOverride{}
			}
			o := in.Routes[name]
			o.Exposure = pkgspec.ExposurePublic
			in.Routes[name] = o
		}
	}
	if err := in.Validate(p); err != nil {
		return nil, "", formErrorf("%s", err)
	}
	return in, id, nil
}

// InstallSummary is what installing a package with these choices would
// set up (generate.Summary), and how that compares with the memory of the
// server it would run on.
type InstallSummary struct {
	*generate.Summary
	Server ServerMemory
}

// ServerMemory is a server's memory, for the install summary (SPEC.md
// §8.3.4): what it has, what's free now, and what the services installed
// on it expect.
type ServerMemory struct {
	// Name is the server's name; This, that it's the one serving the
	// page; Servers, how many the lab has (to suggest another).
	Name    string
	This    bool
	Servers int
	// Total and Available are the server's own figures (node.MemoryInfo);
	// zero when it couldn't say (unreachable, say).
	Total, Available int64
	// Expected adds up what its installed services expect
	// (generate.ExpectedMemory).
	Expected int64
}

// memoryWait is how long the summary waits for a server to report its
// memory: the page asks again whenever the form changes, so a server that
// doesn't answer shouldn't hold it up.
const memoryWait = 3 * time.Second

// InstallSummary is what installing a package with these choices would
// set up, for the install page to show first (SPEC.md §8.3.4). Nothing is
// saved; the route ports are the ones Install would pick now. Secrets
// aren't needed: the generated files never hold them.
func (m *Manager) InstallSummary(name string, req InstallRequest) (*InstallSummary, error) {
	p, err := m.Package(name)
	if err != nil {
		return nil, err
	}
	in, _, err := m.draft(p, req, true)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	services, err := state.LoadServices(m.Lab)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	installed := m.onNode(services, in.Host)
	services = append(services, state.Service{Instance: in, Package: p})
	if _, err := state.AllocatePorts(m.onNode(services, in.Host)); err != nil {
		return nil, err
	}
	sum, err := generate.Summarize(m.Lab, p, in)
	if err != nil {
		return nil, err
	}
	out := &InstallSummary{Summary: sum, Server: ServerMemory{Servers: 1 + len(m.others())}}
	n := m.node(in.Host)
	id := n.Identity()
	out.Server.Name, out.Server.This = id.Name, id.ID == m.Lab.NodeID || in.Host == ""
	for _, s := range installed {
		mem, err := generate.ExpectedMemory(m.Lab, s.Package, s.Instance)
		if err != nil {
			m.Log.Warn("expected memory", "service", s.Name(), "err", err)
			continue
		}
		out.Server.Expected += mem
	}
	if nm, ok := n.(node.Memory); ok {
		ctx, cancel := context.WithTimeout(context.Background(), memoryWait)
		defer cancel()
		if info, err := nm.Memory(ctx); err == nil {
			out.Server.Total, out.Server.Available = info.Total, info.Available
		} else {
			m.Log.Warn("server memory", "server", id.Name, "err", err)
		}
	}
	return out, nil
}

// PackageFile is the file of the package Package returns, as written, for
// people to read before installing (SPEC.md §8.3.4).
func (m *Manager) PackageFile(name string) ([]byte, error) {
	p, err := m.Package(name)
	if err != nil {
		return nil, err
	}
	path := m.Lab.Paths.CatalogPackageFile(name, p.Version)
	if m.IsLocal(name) {
		path = filepath.Join(m.Lab.Paths.LocalPackageDir(name), p.Version+".yaml")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

// PackageIcon is a package's logo (about.icon, a PNG beside its file;
// SPEC.md §8.3.6). It's also there for a package listed but needing a
// newer Roostack, which has only its logo (icon.png) in the catalog.
func (m *Manager) PackageIcon(name string) ([]byte, error) {
	path := filepath.Join(m.Lab.Paths.CatalogPackageDir(name), "icon.png")
	p, err := m.Package(name)
	switch {
	case errors.Is(err, ErrNotFound) && validName(name):
	case err != nil:
		return nil, err
	case m.IsLocal(name):
		path = state.LocalIconFile(m.Lab.Paths, p)
	case p.About.Icon == "":
		return nil, ErrNotFound
	default:
		path = filepath.Join(m.Lab.Paths.CatalogPackageDir(name), p.About.Icon)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
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
	i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == name })
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
			return formErrorf("Roostack keeps ports %d-%d for its web services. Choose another port.",
				config.DefaultRoutePortMin, config.DefaultRoutePortMax)
		case errors.As(err, &pe):
			return formErrorf("Port %d is already used by %s. Choose another port.", pe.Port, pe.UsedBy)
		}
		return err
	}
	if err := in.Save(state.InstanceFile(m.Lab, name)); err != nil {
		return err
	}
	m.applyReason = "Changing " + name + "'s connections"
	m.startApplyLocked()
	return nil
}

// checkTitle tidies a title for a service: empty when it's the package's
// own (so it follows the package's), and short and on one line.
func checkTitle(title string, p *pkgspec.Package) (string, error) {
	title = strings.TrimSpace(title)
	if len([]rune(title)) > 60 || strings.ContainsAny(title, "\n\r\x00") {
		return "", formErrorf("Choose a shorter title, on one line.")
	}
	if title == p.Title {
		return "", nil
	}
	return title, nil
}

// SetTitle changes what people call a service (its ID never changes:
// SPEC.md §8.2). Empty goes back to its package's title. Only the pages
// change: nothing is applied.
func (m *Manager) SetTitle(ctx context.Context, name, title string) error {
	if !validName(name) {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	in, err := instance.Load(state.InstanceFile(m.Lab, name))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	p, err := state.LoadPackage(m.Lab.Paths, in.Package, in.Version)
	if err != nil {
		return err
	}
	if in.Title, err = checkTitle(title, p); err != nil {
		return err
	}
	return in.Save(state.InstanceFile(m.Lab, name))
}

// ServicePackage is an installed service's package, with the service's
// own title if it has one (a second copy, SPEC.md §8.2).
func (m *Manager) ServicePackage(name string) (*pkgspec.Package, error) {
	p, _, err := m.installedService(name)
	return p, err
}

// installedService loads an installed service (by its name, which is its
// package's for a first copy) and its package. The package's Title is
// the service's own, if it has one, for the messages that use it.
func (m *Manager) installedService(name string) (*pkgspec.Package, *instance.Instance, error) {
	if !validName(name) {
		return nil, nil, ErrNotFound
	}
	in, err := instance.Load(state.InstanceFile(m.Lab, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	p, err := state.LoadPackage(m.Lab.Paths, in.Package, in.Version)
	if err != nil {
		return nil, nil, err
	}
	if in.Title != "" {
		named := *p
		named.Title = in.Title
		p = &named
	}
	return p, in, nil
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
// Roostack: not secrets, and not install-only settings (e.g. the first
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
	i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == name })
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
	m.applyReason = "Changing " + name + "'s settings"
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
	m.applyReason = "Removing " + name
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

// Wait blocks until the current apply finishes, and the relay update it
// started (for tests and tools).
func (m *Manager) Wait() {
	if w, ok := m.Node.(interface{ Wait() }); ok {
		w.Wait()
	}
	m.relayWG.Wait()
}

func (m *Manager) installed() (map[string]bool, error) {
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return nil, err
	}
	// By package: the catalog marks a package installed once any copy is.
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
	if name == "" || len(name) > instance.MaxName {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
