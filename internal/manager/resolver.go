package manager

import (
	"cmp"
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
)

// The lab's DNS server (SPEC.md §6.5.2): which one its devices use
// through the lab's VPN, and the servers that choose it for their own
// lookups. By default it's a DNS server among the lab's services (a
// package with dns_server, such as Pi-hole) once there is one.

// DNSService is a service that answers DNS for the home network.
type DNSService struct {
	Name, Title string
	// Host is the server it runs on, by ID.
	Host string
}

// Resolver is the lab's DNS server as it is now.
type Resolver struct {
	// Choice is what the Owner chose (config.Resolver).
	Choice config.Resolver
	// Services are the lab's DNS services, to choose from.
	Services []DNSService
	// Service is the one in use ("" if none).
	Service *DNSService
	// Addr is where devices ask it (port 53): a home address, or the
	// address typed. "" when the lab uses each device's own, or its DNS
	// service's server has no home address.
	Addr string
	// ServerAddr is where the lab's servers ask it (host:port): its
	// server's home address and its port there, or the address typed.
	ServerAddr string
	// Why says why there's none to use, if so.
	Why string
}

// DNSServices are the lab's services that answer DNS, by name. Stopped
// and turned-off ones aren't.
func dnsServices(services []state.Service) []DNSService {
	var out []DNSService
	for _, s := range services {
		if s.Package.DNSServer == "" || s.Stop != nil || s.Instance.Off != nil {
			continue
		}
		out = append(out, DNSService{Name: s.Name(), Title: cmp.Or(s.Instance.Title, s.Package.Title), Host: s.Instance.Host})
	}
	slices.SortFunc(out, func(a, b DNSService) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Resolver says which DNS server the lab uses now.
func (m *Manager) Resolver(ctx context.Context) Resolver {
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		m.Log.Error("listing services for the lab's DNS server", "err", err)
	}
	m.mu.Lock()
	choice := m.labNow().Resolver
	m.mu.Unlock()
	return m.resolverOf(choice, services, m.linkInfos(ctx))
}

func (m *Manager) resolverOf(choice config.Resolver, services []state.Service, infos map[string]linkInfo) Resolver {
	r := Resolver{Choice: choice, Services: dnsServices(services)}
	switch choice.Use {
	case config.ResolverProvider:
		return r
	case config.ResolverAddress:
		r.Addr, r.ServerAddr = choice.Address, net.JoinHostPort(choice.Address, "53")
		return r
	}
	if len(r.Services) == 0 {
		return r
	}
	svc := r.Services[0]
	r.Service = &svc
	i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == svc.Name })
	s := services[i]
	port := generate.ResolvePorts(s.Package, s.Instance)[s.Package.DNSServer]
	if !port.Enabled {
		r.Why = "its DNS connection is turned off on its page"
		return r
	}
	if svc.Host == m.Lab.NodeID {
		r.Addr = m.vpnHomeAddress()
	} else if home := infos[svc.Host].Home; len(home) > 0 {
		r.Addr = home[0]
	}
	if r.Addr == "" {
		r.Why = "its server isn't on the home network"
		return r
	}
	r.ServerAddr = net.JoinHostPort(r.Addr, strconv.Itoa(port.HostPort))
	return r
}

// SetResolver saves which DNS server the lab uses and applies it: the
// servers that chose it change their own lookups, and the lab's VPN
// devices get it in their files when they next download them.
func (m *Manager) SetResolver(ctx context.Context, choice config.Resolver) error {
	if m.UpdateLab == nil {
		return errors.New("manager: UpdateLab is not set")
	}
	switch choice.Use {
	case "", config.ResolverAuto, config.ResolverProvider:
		choice.Address = ""
	case config.ResolverAddress:
		a, err := netip.ParseAddr(strings.TrimSpace(choice.Address))
		if err != nil || !a.Is4() {
			return usererr.New("Enter the DNS server's address, such as 192.168.1.2.")
		}
		choice.Address = a.String()
	default:
		return usererr.New("Choose which DNS server to use.")
	}
	if choice.Use == config.ResolverAuto {
		choice.Use = ""
	}
	known := map[string]bool{m.Lab.NodeID: true}
	for _, r := range m.others() {
		known[r.Identity().ID] = true
	}
	choice.Servers = slices.DeleteFunc(slices.Clone(choice.Servers), func(id string) bool { return !known[id] })
	slices.Sort(choice.Servers)
	choice.Servers = slices.Compact(choice.Servers)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveLab(func(l *config.Lab) { l.Resolver = choice }); err != nil {
		return err
	}
	m.startApplyLocked("Changing the lab's DNS server")
	return nil
}

// resolverFor is the DNS server a server's own lookups use (host:port),
// if it chose the lab's, else "".
func resolverFor(r Resolver, id string) string {
	if !slices.Contains(r.Choice.Servers, id) {
		return ""
	}
	return r.ServerAddr
}

// DeviceDNS is the DNS server a VPN device's file names (SPEC.md
// §6.5.2), "" for none: the lab's, when the device reaches it, which is
// when it's on the internet or inside what the device sends through the
// VPN (allowed), and, for a DNS service, the device's person may use it.
// A server it couldn't reach would leave the device with no DNS at all.
func (m *Manager) DeviceDNS(ctx context.Context, userID int64, allowed []string) string {
	r := m.Resolver(ctx)
	a, err := netip.ParseAddr(r.Addr)
	if err != nil {
		return ""
	}
	reached := a.IsGlobalUnicast() && !a.IsPrivate()
	for _, p := range allowed {
		if pp, err := netip.ParsePrefix(p); err == nil && pp.Contains(a) {
			reached = true
		}
	}
	if !reached {
		return ""
	}
	if r.Service != nil && m.People != nil {
		services, _ := state.LoadServices(m.Lab)
		i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == r.Service.Name })
		if i >= 0 && generate.SSOOn(m.Lab, services[i].Package, services[i].Instance) {
			if ok, err := m.People.CanUse(ctx, r.Service.Name, userID); err != nil || !ok {
				return ""
			}
		}
	}
	return r.Addr
}
