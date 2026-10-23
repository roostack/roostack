package manager

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
)

// serverArchKey + server ID is the processor a server last said it has
// (node.Processor), so it's still known while the server doesn't answer.
const serverArchKey = "server-arch."

// archOf is a server's processor as Go names it (amd64, arm64): what it
// says now, or else what it said last; "" if it's never said.
func (m *Manager) archOf(ctx context.Context, id string) string {
	id = cmp.Or(id, m.Lab.NodeID)
	n := m.node(id)
	if n == nil {
		return ""
	}
	if p := n.Processor(); p != nil {
		if a := p.Arch(); a != "" {
			if id != m.Lab.NodeID && m.Settings != nil && m.setting(ctx, serverArchKey+id) != a {
				if err := m.Settings.SetSetting(ctx, serverArchKey+id, a); err != nil {
					m.Log.Warn("recording a server's processor", "server", id, "err", err)
				}
			}
			return a
		}
	}
	if id == m.Lab.NodeID {
		return ""
	}
	return m.setting(ctx, serverArchKey+id)
}

// LabArches are the processors of the lab's servers, as far as they're
// known: the registry fetcher keeps each package's newest version with a
// build for one of them (SPEC.md §8.3.3).
func (m *Manager) LabArches() []string {
	ctx := context.Background()
	ids := []string{m.Lab.NodeID}
	for _, r := range m.others() {
		ids = append(ids, r.Identity().ID)
	}
	var out []string
	for _, id := range ids {
		if a := m.archOf(ctx, id); a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

// packageArches are the processors a registry package's version has a
// build of every image for, from the index (SPEC.md §8.3.3). Nil when
// that isn't known: one of the Owner's own packages, or an index that
// doesn't say. Then nothing is checked here, and a server whose processor
// an image lacks says so when it pulls it (podman.NoImageForArch).
func (m *Manager) packageArches(name, version string) []string {
	if m.RegistryIndex == nil || m.IsLocal(name) {
		return nil
	}
	ix, err := m.RegistryIndex()
	if err != nil {
		return nil
	}
	return ix.Architectures(name, version)
}

// NoBuild is a server a package can't run on: it has no version for the
// server's processor.
type NoBuild struct {
	ID, Name string
	// Arch is the server's processor (arm64).
	Arch string
}

// NoBuildOn lists the lab's servers a package can't run on, because it
// has no build for their processor, for the install page to say so and
// offer only the others (SPEC.md §8.3.3). A server whose processor isn't
// known isn't listed.
func (m *Manager) NoBuildOn(p *pkgspec.Package) []NoBuild {
	arches := m.packageArches(p.Name, p.Version)
	if len(arches) == 0 {
		return nil
	}
	ctx := context.Background()
	ids := []string{m.Lab.NodeID}
	for _, r := range m.others() {
		ids = append(ids, r.Identity().ID)
	}
	var out []NoBuild
	for _, id := range ids {
		if a := m.archOf(ctx, id); a != "" && !slices.Contains(arches, a) {
			out = append(out, NoBuild{ID: id, Name: m.nameOf(id), Arch: a})
		}
	}
	return out
}

// InstallServer is where a new service runs unless someone chooses
// another: on chosen (a server's ID, "" for this one) if the package has
// a version for its processor, otherwise on the first of the lab's
// servers it has one for (SPEC.md §8.3.3). noBuild is NoBuildOn's list.
func InstallServer(servers []Server, noBuild []NoBuild, chosen string) string {
	can := func(id string) bool {
		return !slices.ContainsFunc(noBuild, func(nb NoBuild) bool { return nb.ID == id })
	}
	id := chosen
	for _, m := range servers {
		if id == "" && m.This {
			id = m.ID
		}
	}
	if can(id) {
		return chosen
	}
	for _, m := range servers {
		if can(m.ID) {
			return m.ID
		}
	}
	return chosen
}

// noBuildFor is the processor of server host when version of a package
// has no build for it ("" when it has, or either isn't known: then it's
// allowed, and the server says so if an image is missing when it pulls
// it), and the processors the version has builds for.
func (m *Manager) noBuildFor(ctx context.Context, name, version, host string) (string, []string) {
	arches := m.packageArches(name, version)
	a := m.archOf(ctx, host)
	if len(arches) == 0 || a == "" || slices.Contains(arches, a) {
		return "", arches
	}
	return a, arches
}

// checkArch refuses to place a package on server host when it has no
// build for that server's processor, saying which servers it can run on
// (SPEC.md §8.3.3).
func (m *Manager) checkArch(ctx context.Context, p *pkgspec.Package, title, host string) error {
	if a, arches := m.noBuildFor(ctx, p.Name, p.Version, host); a != "" {
		return usererr.New("%s", m.noBuildText(ctx, title, host, a, arches))
	}
	return nil
}

// noBuildText says a package can't run on a server, and where it can.
func (m *Manager) noBuildText(ctx context.Context, title, host, arch string, arches []string) string {
	msg := fmt.Sprintf("%s has no version for %s's kind of processor (%s), so it can't run there.",
		title, m.nameOf(cmp.Or(host, m.Lab.NodeID)), arch)
	ids := []string{m.Lab.NodeID}
	for _, r := range m.others() {
		ids = append(ids, r.Identity().ID)
	}
	var can []string
	for _, id := range ids {
		if a := m.archOf(ctx, id); a != "" && slices.Contains(arches, a) {
			can = append(can, m.nameOf(id))
		}
	}
	if len(can) > 0 {
		msg += " It can run on " + words.Or(can) + "."
	}
	return msg
}
