package manager

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
)

// Security advisories (SPEC.md §6.16): the service registry's warnings of
// security problems, matched to the lab's services from the newest index
// each time, like withdrawals (withdrawals.go). Nothing about them is
// saved but the Owner's choices (the policy in lab.yaml, Keep it public
// anyway in an instance), so an advisory the registry changes or removes,
// or a service updated past it, stops pausing it by itself.

// Audit actions for what Roostack does by itself about advisories, which
// the Owners are emailed (attention.securityEvent).
const (
	AuditAdvisory       = "security advisory"
	AuditAdvisoryPaused = "public access paused by Roostack"
	AuditAdvisoryOpened = "public access opened by Roostack"
)

// Settings keys: an advisory seen for a service (when), and the advisory
// Roostack paused a service for.
const (
	advisorySeenKey = "advisory-seen:"
	advisedKey      = "advised:"
)

// ServiceAdvisory is an advisory affecting an installed service, and what
// Roostack does about it.
type ServiceAdvisory struct {
	*registry.Advisory
	// Version is the service's version it affects.
	Version string
	// Fixed is the package version that fixes it, if there's one.
	Fixed string
	// Possibly: the service is from a package of the Owner's own built
	// with what the advisory is about, so it may be affected. Roostack
	// never pauses for it.
	Possibly bool
	// Public: one of the service's addresses or ports is public.
	Public bool
	// Paused: Roostack has paused the service's public access for it.
	Paused bool
	// KeptPublic: the Owner chose to keep this version public anyway.
	KeptPublic bool
	// Since is when Roostack first saw it affecting the service.
	Since time.Time
}

// Pauses says whether an advisory pauses a public service under the lab's
// policy: only a critical one, when it's being used in attacks or the
// Owner chose any critical one, and not one the Owner kept it public
// despite.
func (a *ServiceAdvisory) pauses(policy string) bool {
	if a.Possibly || !a.Public || a.KeptPublic || a.Severity != registry.SeverityCritical {
		return false
	}
	return policy == config.AdvisoriesCritical || policy == config.AdvisoriesExploited && a.Exploited
}

// AdvisoryPolicy is what Roostack does about advisories (lab.yaml).
func (m *Manager) AdvisoryPolicy() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.labNow().Public.AdvisoryPolicy()
}

// SetAdvisoryPolicy saves what Roostack does about advisories and applies
// it at once (a pause it now allows, or one it no longer does).
func (m *Manager) SetAdvisoryPolicy(policy string) error {
	if !slices.Contains([]string{config.AdvisoriesTell, config.AdvisoriesExploited, config.AdvisoriesCritical}, policy) {
		return usererr.New("Choose what Roostack should do when the service registry warns of a security problem.")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveLab(func(l *config.Lab) { l.Public.Advisories = policy }); err != nil {
		return err
	}
	m.startApplyNowLocked("Changing what Roostack does about the service registry's security advisories")
	return nil
}

// Advisories lists the advisories affecting each installed service, by
// service name, newest first.
func (m *Manager) Advisories(ctx context.Context) map[string][]*ServiceAdvisory {
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		return nil
	}
	m.mu.Lock()
	lab := m.labNow()
	m.mu.Unlock()
	return m.advisories(ctx, lab, services)
}

// advisories works out each service's advisories from the lab's copy of
// the index, reading when each was first seen but recording nothing.
func (m *Manager) advisories(ctx context.Context, lab config.Lab, services []state.Service) map[string][]*ServiceAdvisory {
	if m.RegistryIndex == nil {
		return nil
	}
	ix, err := m.RegistryIndex()
	if err != nil {
		m.Log.Error("reading the registry's index", "err", err)
		return nil
	}
	if ix == nil || len(ix.Advisories) == 0 {
		return nil
	}
	policy := lab.Public.AdvisoryPolicy()
	out := map[string][]*ServiceAdvisory{}
	for _, s := range services {
		in := s.Instance
		if in.Waiting {
			continue
		}
		var found []*ServiceAdvisory
		if in.Local {
			for _, a := range ix.Possibly(s.Package.BuiltWithNames()) {
				found = append(found, &ServiceAdvisory{Advisory: a, Version: in.Version, Possibly: true})
			}
		} else if file, err := os.ReadFile(m.Lab.Paths.LabPackageFile(in.Package, in.Version)); err == nil {
			for _, a := range ix.Affecting(in.Package, in.Version, file) {
				found = append(found, &ServiceAdvisory{Advisory: a, Version: in.Version, Fixed: a.Affects[in.Package].Fixed})
			}
		}
		if len(found) == 0 {
			continue
		}
		_, public := PublicServiceOf(lab, s)
		for _, a := range found {
			a.Public, a.KeptPublic = public, in.KeptPublicFor(a.ID)
			a.Paused = a.pauses(policy)
			a.Since = m.advisorySeen(ctx, s.Name(), a.ID)
		}
		out[s.Name()] = found
	}
	return out
}

// advisorySeen is when Roostack first saw an advisory affecting a service
// (zero before markAdvisories records it).
func (m *Manager) advisorySeen(ctx context.Context, name, id string) time.Time {
	if m.Settings == nil {
		return time.Time{}
	}
	v, _ := m.Settings.Setting(ctx, advisorySeenKey+name+":"+id)
	at, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(at, 0)
}

// markAdvisories works out which services Roostack pauses for an advisory
// now (service → advisory ID), for the assignment, and records what
// changed: an advisory seen for the first time (high or critical: the
// Owners are emailed), a pause Roostack placed, and one it lifted (the
// service was updated past it, the registry changed it, or the Owner kept
// it public). It also returns a key of what's paused, to tell a change.
func (m *Manager) markAdvisories(ctx context.Context, lab config.Lab, services []state.Service) (map[string]string, string) {
	all := m.advisories(ctx, lab, services)
	advised := map[string]string{}
	for _, s := range services {
		name := s.Name()
		var paused *ServiceAdvisory
		for _, a := range all[name] {
			if a.Since.IsZero() && m.Settings != nil {
				m.Settings.SetSetting(ctx, advisorySeenKey+name+":"+a.ID, strconv.FormatInt(time.Now().Unix(), 10))
				if a.Severity != registry.SeverityLow && !a.Paused {
					m.audit(ctx, AuditAdvisory, advisoryDetail(s.Title(), a))
				}
			}
			if a.Paused && paused == nil {
				paused = a
			}
		}
		was := ""
		if m.Settings != nil {
			was, _ = m.Settings.Setting(ctx, advisedKey+name)
		}
		switch {
		case paused != nil:
			advised[name] = paused.ID
			if was != paused.ID && m.Settings != nil {
				m.Settings.SetSetting(ctx, advisedKey+name, paused.ID)
				m.Log.Warn("pausing public access for a security advisory", "service", name, "advisory", paused.ID, "title", paused.Title)
				m.audit(ctx, AuditAdvisoryPaused, advisoryDetail(s.Title(), paused))
			}
		case was != "":
			m.Settings.SetSetting(ctx, advisedKey+name, "")
			m.Log.Info("opening public access again after a security advisory", "service", name, "advisory", was)
			m.audit(ctx, AuditAdvisoryOpened, s.Title()+": "+liftReason(s.Instance, was, all[name]))
		}
	}
	var key []string
	for _, name := range slices.Sorted(maps.Keys(advised)) {
		key = append(key, name+"="+advised[name])
	}
	return advised, strings.Join(key, ",")
}

// advisoryDetail is an advisory for the audit log and the Owners' email:
// the service, then what the registry says, its ID and severity.
func advisoryDetail(title string, a *ServiceAdvisory) string {
	s := fmt.Sprintf("%s: %s (%s, %s", title, strings.TrimRight(a.Title, "."), a.ID, a.Severity)
	if a.Exploited {
		s += ", being used in attacks"
	}
	if a.Possibly {
		s += ", may affect it: it's one of your own packages"
	}
	return s + ")"
}

// liftReason says why Roostack stopped pausing a service for an advisory.
func liftReason(in *instance.Instance, id string, now []*ServiceAdvisory) string {
	for _, a := range now {
		if a.ID == id {
			if a.KeptPublic {
				return "the Owner chose to keep it public despite security advisory " + id
			}
			return "Settings → Public access no longer pauses it for security advisory " + id
		}
	}
	return "security advisory " + id + " no longer affects it: it was updated, or the registry changed the advisory"
}

// KeepPublicAnyway keeps a service's current version public despite an
// advisory, at the Owner's word (SPEC.md §6.16): Roostack won't pause it
// for that advisory again until its version changes.
func (m *Manager) KeepPublicAnyway(ctx context.Context, name, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		return err
	}
	i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == name })
	if i < 0 {
		return ErrNotFound
	}
	in := services[i].Instance
	if in.KeptPublicFor(id) {
		return nil
	}
	if in.PublicAnyway == nil || in.PublicAnyway.Version != in.Version {
		in.PublicAnyway = &instance.KeptPublic{Version: in.Version}
	}
	in.PublicAnyway.Advisories = append(in.PublicAnyway.Advisories, id)
	if err := in.Save(state.InstanceFile(m.Lab, name)); err != nil {
		return err
	}
	m.startApplyNowLocked("Keeping " + services[i].Title() + " public despite a security advisory")
	return nil
}

// UpdateAdvised updates a service Roostack paused for an advisory straight
// away, rather than at night, when the registry offers a version the
// advisory doesn't affect and the service's update policy takes it (not
// notify): its public access is down meanwhile (as UpdateStopped does for
// a service the registry stopped). The pause lifts once the update has
// worked; a failed one goes back to the affected version, still paused.
func (m *Manager) UpdateAdvised(ctx context.Context) {
	advs := m.Advisories(ctx)
	for name, u := range m.AvailableUpdates(ctx) {
		var paused *ServiceAdvisory
		for _, a := range advs[name] {
			if a.Paused {
				paused = a
				break
			}
		}
		if paused == nil || !u.Auto || m.Updating(name) != nil {
			continue
		}
		if aff := paused.Affects[u.Package]; aff != nil && aff.Versions[u.To] != "" {
			continue // the version on offer is affected too
		}
		why := fmt.Sprintf("Roostack paused its public access for a security problem (%s) that %s fixes, and its update policy (%s) takes this %s update",
			paused.ID, u.ToApp(), u.Policy, u.Kind)
		if err := m.startUpdate(u, why, true, "", true); err != nil {
			m.Log.Info("update of a service paused for an advisory not started", "service", name, "err", err)
		}
	}
}
