package node

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/systemd"
	"roostack.cloud/roostack/internal/units"
)

// What a server can tell about its services and itself, so nothing about
// a lab looks like magic to someone curious.

// ServiceDetail is one service on a server.
type ServiceDetail struct {
	Status     string      `json:"status"`
	Containers []Container `json:"containers"`
	// Ports are its enabled package ports; Routes its web routes, as
	// "host → address:port" (where Caddy forwards).
	Ports    []generate.HostPort `json:"ports,omitempty"`
	Routes   []string            `json:"routes,omitempty"`
	DataDirs []string            `json:"data_dirs"`
	// DataSize is bytes in its data folders (-1 until counted; counted in
	// the background, at most every 10 minutes).
	DataSize int64 `json:"data_size"`
}

// Container is one of a service's containers.
type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	State string `json:"state"` // e.g. "active (running)"
}

// BuiltIn is part of Roostack itself on a server: its proxy, backup stores,
// the roostack service and agent, and network links.
type BuiltIn struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"` // proxy, store, roostack, network
	Status string `json:"status"`
	Detail string `json:"detail"`
	// LastSeen is a network link's latest handshake (zero: none or n/a).
	LastSeen time.Time `json:"last_seen,omitzero"`
}

// WireGuardStatuser reads WireGuard interfaces (roostack-agent).
type WireGuardStatuser interface {
	WireGuardStatus(ctx context.Context, iface string) (agent.WireGuardStatus, error)
}

// Sizes (SPEC.md §6.7) are counted with du, which reads every file's
// metadata: quick on an SSD, but on a spinning disk a large photo library
// can take a minute or more, and it fills the kernel's metadata cache. So
// they're counted once a day, right after the nightly backup (restic has
// just read that metadata, so it's cached and the count is quick), at the
// lowest CPU and disk priority; or when asked for a size that's missing
// or older than sizeTTL (no backups set up, a new service).
const sizeTTL = 24 * time.Hour

// sizes caches what Roostack's data takes on the disk.
type sizes struct {
	mu       sync.Mutex
	services map[string]int64 // each service's data
	// backups and guests are the backup store's parts: the lab's own
	// backups, and other labs'.
	backups, guests int64
	at              time.Time // when last counted (zero: never)
	counting        bool
}

// dataSize returns a service's counted data size (-1 if not counted yet).
func (n *Local) dataSize(service string) int64 {
	n.sizes.mu.Lock()
	defer n.sizes.mu.Unlock()
	size, ok := n.sizes.services[service]
	if !ok || time.Since(n.sizes.at) > sizeTTL {
		n.countSizesLocked()
	}
	if !ok {
		return -1
	}
	return size
}

// storeSize returns the backup store's counted sizes (-1 if not yet).
func (n *Local) storeSize() (backups, guests int64, at time.Time) {
	n.sizes.mu.Lock()
	defer n.sizes.mu.Unlock()
	if n.sizes.at.IsZero() || time.Since(n.sizes.at) > sizeTTL {
		n.countSizesLocked()
	}
	if n.sizes.at.IsZero() {
		return -1, -1, time.Time{}
	}
	return n.sizes.backups, n.sizes.guests, n.sizes.at
}

// CountSizes counts every size again, in the background (after the
// nightly backup).
func (n *Local) CountSizes() {
	n.sizes.mu.Lock()
	defer n.sizes.mu.Unlock()
	n.countSizesLocked()
}

// countSizesLocked starts a count unless one is running.
func (n *Local) countSizesLocked() {
	if n.sizes.counting {
		return
	}
	n.sizes.counting = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		_, services := n.Assigned()
		counted := map[string]int64{}
		for _, s := range services {
			if size, err := n.DataSize(ctx, s.Package.Name); err == nil {
				counted[s.Package.Name] = size
			} else if n.Log != nil {
				n.Log.Warn("counting a service's data", "service", s.Package.Name, "err", err)
			}
		}
		dir := n.Lab.Paths.BackupStoreDir()
		guests, gerr := n.du(ctx, false, filepath.Join(dir, "guests"))
		all, err := n.du(ctx, false, dir)
		n.sizes.mu.Lock()
		defer n.sizes.mu.Unlock()
		n.sizes.counting, n.sizes.at, n.sizes.services = false, time.Now(), counted
		n.sizes.backups, n.sizes.guests = -1, -1
		if err == nil && gerr == nil {
			n.sizes.backups, n.sizes.guests = all-guests, guests
		}
	}()
}

// ServiceDetails describes each assigned service.
func (n *Local) ServiceDetails(ctx context.Context) (map[string]ServiceDetail, error) {
	n.mu.Lock()
	a := n.assignment
	n.mu.Unlock()
	lab := n.lab(a)
	out := map[string]ServiceDetail{}
	for _, s := range a.Services {
		name := s.Package.Name
		d := ServiceDetail{Status: "unknown", DataDirs: n.DataDirs(name), DataSize: n.dataSize(name)}
		gen, err := generate.Service(lab, s.Package, s.Instance)
		if err != nil {
			d.Status = "failed"
			out[name] = d
			continue
		}
		d.Ports = gen.HostPorts
		states := map[string]string{}
		if n.Units != nil {
			if units, err := n.Units.Status(ctx, gen.Units...); err == nil {
				d.Status = Summarize(units)
				for _, u := range units {
					states[u.Unit] = unitState(u)
				}
			}
		}
		for _, q := range gen.Quadlets {
			if !strings.HasSuffix(q.Name, ".container") {
				continue
			}
			unit := strings.TrimSuffix(q.Name, ".container")
			d.Containers = append(d.Containers, Container{Name: unit, Image: quadletValue(q.Content, "Image"),
				State: states[unit+".service"]})
		}
		for i, host := range gen.Hosts {
			if i < len(gen.Ports) {
				addr := "127.0.0.1"
				if lab.IsNode() {
					addr = lab.OverlayAddr
				}
				d.Routes = append(d.Routes, host+" → "+addr+":"+itoa(gen.Ports[i]))
			}
		}
		out[name] = d
	}
	return out, nil
}

// BuiltIns describes Roostack's own parts on this server.
func (n *Local) BuiltIns(ctx context.Context) ([]BuiltIn, error) {
	n.mu.Lock()
	a := n.assignment
	n.mu.Unlock()
	var out []BuiltIn
	userUnit := func(name, kind, unit, detail string) {
		b := BuiltIn{Name: name, Kind: kind, Status: "unknown", Detail: detail}
		if n.Units != nil {
			if st, err := n.Units.Status(ctx, unit); err == nil && len(st) == 1 {
				b.Status = unitState(st[0])
			}
		}
		out = append(out, b)
	}
	sd := systemd.New()
	sysUnit := func(name, unit, detail string) {
		b := BuiltIn{Name: name, Kind: "roostack", Status: "unknown", Detail: detail}
		if st, err := sd.SystemStatus(ctx, unit); err == nil && len(st) == 1 {
			b.Status = unitState(st[0])
		}
		out = append(out, b)
	}
	sysUnit("Roostack", "roostack.service", "Runs this server's services, backups and updates"+n.versionText())
	sysUnit("Roostack agent", "roostack-agent.service", "Does the few things that need root: firewall, WireGuard, OS updates, restarts")
	if !n.Lab.IsNode() {
		b := BuiltIn{Name: "Caddy", Kind: "proxy", Status: "unknown", Detail: "Web addresses and HTTPS certificates for the lab's services"}
		if n.Units != nil {
			if st, err := n.Units.Status(ctx, generate.CaddyUnit+".service", CaddyBuildUnit); err == nil && len(st) == 2 {
				b.Status = caddyState(st[0], st[1])
			}
		}
		out = append(out, b)
	}
	if a.Storage != nil {
		userUnit("Backup store", "store", generate.StorageUnit+".service",
			plural(len(a.Storage.Users), "server", "servers")+" back up to this server's disk (append-only)")
		userUnit("Backup store (controller access)", "store", generate.StorageAdminUnit+".service",
			"The controller's access, to remove old backups")
	}
	for _, g := range a.Guests {
		userUnit("Store for another lab ("+g.User+")", "store", "roostack-guest-"+g.User+".service",
			"Up to "+gbText(g.Quota)+" of encrypted backups")
	}
	for _, l := range n.links(ctx) {
		out = append(out, l)
	}
	return out, nil
}

// links describes this server's WireGuard interfaces.
func (n *Local) links(ctx context.Context) []BuiltIn {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	var out []BuiltIn
	for _, e := range entries {
		iface := e.Name()
		if !agent.ValidInterface(iface) {
			continue
		}
		b := BuiltIn{Name: iface, Kind: "network", Status: "up"}
		switch {
		case iface == agent.WireGuardInterface:
			b.Detail = "Roostack's network between this lab's servers"
		case iface == agent.GuestsInterface:
			b.Detail = "Other labs storing backups here (UDP " + itoa(agent.GuestsPort) + ")"
		default:
			b.Detail = "A link to another lab's storage"
		}
		if n.WireGuard != nil {
			if st, err := n.WireGuard.WireGuardStatus(ctx, iface); err == nil {
				b.Detail += "; address " + st.Address + ", " + plural(len(st.Handshakes), "peer", "peers")
				var last int64
				for _, t := range st.Handshakes {
					last = max(last, t)
				}
				if last > 0 {
					b.LastSeen = time.Unix(last, 0)
				}
			}
		}
		out = append(out, b)
	}
	slices.SortFunc(out, func(a, b BuiltIn) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (n *Local) versionText() string {
	if n.Version == "" {
		return ""
	}
	return " (version " + n.Version + ")"
}

// caddyState says what Caddy is doing, from its unit and its image's build
// unit: building its image (minutes the first time), or waiting for the
// domain step, whose DNS login its container needs, rather than "inactive".
func caddyState(caddy, build systemd.UnitStatus) string {
	switch {
	case build.ActiveState == "activating":
		return "building its image (the first time takes several minutes)"
	case build.ActiveState == "failed":
		return "its image didn't build"
	case caddy.LoadState == "not-found":
		return "waiting for your domain"
	}
	return unitState(caddy)
}

func unitState(u systemd.UnitStatus) string {
	if u.LoadState == "not-found" {
		return "not installed"
	}
	return u.ActiveState + " (" + u.SubState + ")"
}

// quadletValue reads a key from a Quadlet file (the first one).
func quadletValue(content, key string) string {
	for _, line := range strings.Split(content, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}

func itoa(n int) string { return strconv.Itoa(n) }

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func gbText(b int64) string { return units.Bytes(b) }
