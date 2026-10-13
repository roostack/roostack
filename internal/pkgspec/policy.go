package pkgspec

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Policy checks the package against the package policy every registry
// package must follow (SPEC.md §8.3.4): what it may not do, beyond being a
// valid package. It reports every problem found, each a sentence saying
// which field or line is wrong and what to use instead; none means the
// package passes.
//
// It's separate from Validate because a package the Owner adds themselves
// may break it, once they've confirmed the list of what breaks it. The
// package must already be valid (Parse or Load).
func (p *Package) Policy() []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	for _, name := range slices.Sorted(maps.Keys(p.Containers)) {
		c := p.Containers[name]
		at := "containers." + name
		if !pinnedRe.MatchString(c.Image) {
			add("%s.image: pin the image by digest (name:tag@sha256:…), so every lab runs exactly the image that was reviewed.", at)
		}
		for _, msg := range quadletPolicy(c.Quadlet) {
			add("%s.quadlet: %s", at, msg)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.Databases)) {
		d := p.Databases[name]
		at := "databases." + name
		// No image means Roostack's own, which Roostack pins.
		if d.Image != "" && !pinnedRe.MatchString(d.Image) {
			add("%s.image: pin the image by digest (name:tag@sha256:…), so every lab runs exactly the image that was reviewed.", at)
		}
		for _, msg := range quadletPolicy(d.Quadlet) {
			add("%s.quadlet: %s", at, msg)
		}
	}

	// Nothing is public until the Owner makes it so (SPEC.md §6.11), and
	// then the package has said what's safe.
	for _, name := range slices.Sorted(maps.Keys(p.Routes)) {
		r := p.Routes[name]
		at := "routes." + name
		if r.Exposure != ExposurePrivate {
			add("%s.exposure: must be private; only the Owner makes an address public.", at)
		}
		if r.Public.Unsafe == "" && len(r.Public.LoginPaths) == 0 {
			add("%s.public: name the pages where people sign in (login_paths), or say why the address shouldn't be public at all (unsafe).", at)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.Ports)) {
		if p.Ports[name].DefaultExposure() != ExposurePrivate {
			add("ports.%s.exposure: must be private; only the Owner opens a connection to the internet.", name)
		}
	}
	return problems
}

// pinnedRe is an image reference pinned by a sha256 digest.
var pinnedRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// quadletRule refuses a raw Quadlet line that could reach beyond the
// service (SPEC.md §8.3.4). Keys match whatever their case.
type quadletRule struct {
	section, key string
	// prefix matches every key starting with key (ExecStart, ExecStop, ...).
	prefix bool
	// denies says whether this value is refused; nil refuses any value.
	denies func(value string) bool
	// instead says why, and what the package should use instead.
	instead string
}

// Agreed with the Owner on 2026-10-13 (the deny-list), plus on 2026-10-14
// the keys that override what Roostack writes itself: the generator
// appends a package's lines after its own.
var quadletRules = []quadletRule{
	{section: "Container", key: "Volume", denies: volumeHasSource,
		instead: "a folder from the server or a named volume is outside the service and its backups; use storage: and mounts: (an anonymous Volume=/path is allowed)"},
	{section: "Container", key: "Mount", denies: func(v string) bool { return !isTmpfsMount(v) },
		instead: "only type=tmpfs is allowed; use storage: and mounts: for the service's folders"},
	{section: "Container", key: "Rootfs",
		instead: "it runs the container from a folder on the server; use the container's image:"},
	{section: "Container", key: "EnvironmentFile",
		instead: "it reads a file on the server; use env: and secrets:"},
	{section: "Container", key: "PodmanArgs",
		instead: "it can pass anything to Podman; something the package format can't say needs a new field"},
	{section: "Container", key: "GlobalArgs",
		instead: "it can pass anything to Podman; something the package format can't say needs a new field"},
	{section: "Container", key: "AddCapability",
		instead: "containers keep Podman's default capabilities"},
	{section: "Container", key: "SecurityLabelDisable",
		instead: "Roostack keeps Podman's security labels on"},
	{section: "Container", key: "Network",
		instead: "Roostack puts each service on its own network"},
	{section: "Container", key: "Pod",
		instead: "Roostack puts each service on its own network"},
	{section: "Container", key: "PublishPort",
		instead: "use routes: for web addresses and ports: for other connections"},
	{section: "Container", key: "AddDevice",
		instead: "use devices: (e.g. gpu: intel)"},
	{section: "Container", key: "User", denies: isRootUser,
		instead: "run as the image's own user, with UserNS=keep-id:uid=…,gid=… if its files need that owner"},
	{section: "Container", key: "UIDMap",
		instead: "use UserNS=keep-id:uid=…,gid=… if the service's files need a particular owner"},
	{section: "Container", key: "GIDMap",
		instead: "use UserNS=keep-id:uid=…,gid=… if the service's files need a particular owner"},
	{section: "Container", key: "UserNS", denies: func(v string) bool { return strings.EqualFold(strings.TrimSpace(v), "host") },
		instead: "the host's user namespace is the roostack user's own; use UserNS=keep-id:uid=…,gid=… if the service's files need a particular owner"},
	{section: "Container", key: "Image",
		instead: "it would replace the pinned image; use image:"},
	{section: "Container", key: "Secret",
		instead: "it could read another service's secrets; use secrets:"},
	{section: "Container", key: "ContainerName",
		instead: "Roostack names the containers"},
	{section: "Container", key: "Label", denies: func(v string) bool { return strings.HasPrefix(strings.TrimSpace(v), "io.roostack.") },
		instead: "Roostack finds a service's containers by its own io.roostack labels"},
	{section: "Service", key: "Exec", prefix: true,
		instead: "it runs a command on the server; commands run inside the container (command: or hooks:)"},
	{section: "Unit", key: "Exec", prefix: true,
		instead: "it runs a command on the server; commands run inside the container (command: or hooks:)"},
}

// quadletPolicy returns a sentence for each refused line of q.
func quadletPolicy(q Quadlet) []string {
	var problems []string
	for _, section := range slices.Sorted(maps.Keys(q)) {
		for _, key := range slices.Sorted(maps.Keys(q[section])) {
			rule := findQuadletRule(section, key)
			if rule == nil {
				continue
			}
			for _, value := range quadletValues(q[section][key]) {
				if rule.denies != nil && !rule.denies(value) {
					continue
				}
				problems = append(problems, fmt.Sprintf("%s.%s=%s isn't allowed: %s.", section, key, value, rule.instead))
			}
		}
	}
	return problems
}

func findQuadletRule(section, key string) *quadletRule {
	for i, r := range quadletRules {
		if r.section != section {
			continue
		}
		if strings.EqualFold(key, r.key) || r.prefix && len(key) >= len(r.key) && strings.EqualFold(key[:len(r.key)], r.key) {
			return &quadletRules[i]
		}
	}
	return nil
}

// quadletValues lists a passthrough value's lines, as the generator writes
// them.
func quadletValues(val any) []string {
	if list, ok := val.([]any); ok {
		values := make([]string, len(list))
		for i, e := range list {
			values[i] = fmt.Sprint(e)
		}
		return values
	}
	return []string{fmt.Sprint(val)}
}

// volumeHasSource says whether a Volume= line mounts something into the
// container ([SOURCE:]DEST[:OPTIONS]): a host path or a named volume, both
// shared beyond the service. Only an anonymous volume (DEST) has none.
func volumeHasSource(v string) bool {
	src, _, found := strings.Cut(strings.TrimSpace(v), ":")
	return found && src != ""
}

// isTmpfsMount says whether a Mount= line is a tmpfs (type=tmpfs, ...),
// which holds nothing beyond the container.
func isTmpfsMount(v string) bool {
	for opt := range strings.SplitSeq(v, ",") {
		k, val, _ := strings.Cut(strings.TrimSpace(opt), "=")
		if k == "type" {
			return val == "tmpfs"
		}
	}
	return false
}

// isRootUser says whether a User= line (user[:group]) runs as root.
func isRootUser(v string) bool {
	user, _, _ := strings.Cut(strings.TrimSpace(v), ":")
	return user == "0" || user == "root"
}
