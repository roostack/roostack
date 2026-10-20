package pkgspec

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/quadlet"
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
//
// It's the facts registry packages must give (missing ones first), then
// Risks.
func (p *Package) Policy() []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	p.aboutPolicy(add)
	for _, name := range slices.Sorted(maps.Keys(p.Containers)) {
		if p.Containers[name].Memory == "" {
			add("containers.%s.memory: say how much memory it typically uses (e.g. 300 MB), for the summary shown before installing.", name)
		}
	}
	return append(problems, p.Risks()...)
}

// Risks is the part of the policy that's about safety: what a package
// asks for that could reach beyond its service, or change without anyone
// knowing (SPEC.md §8.3.4). A package of the Owner's own may leave out
// the facts Policy requires (about:, memory), but these are shown to the
// Owner to confirm before it's installed.
func (p *Package) Risks() []string {
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

// aboutPolicy requires the facts about the project that review checks and
// the catalog and website show (SPEC.md §8.3.6), and the Roostack it needs
// (§8.3.5). Agreed with the Owner on 2026-10-14: the website, support page
// and keywords stay optional, since not every project has them; an app
// whose code isn't published (licence: closed) has a website instead of a
// source.
func (p *Package) aboutPolicy(add func(string, ...any)) {
	a := p.About
	if a.Maker == "" {
		add("about.maker: say who makes the app, in a few words, so the catalog credits them.")
	}
	switch {
	case a.Licence == "":
		add("about.licence: give the app's licence as an SPDX identifier (e.g. AGPL-3.0-only), or %q if its code isn't published.", LicenceClosed)
	case a.Licence == LicenceClosed:
		if a.Website == "" {
			add("about.website: an app whose code isn't published needs its website, so people can see who makes it.")
		}
	default:
		for _, id := range ambiguousLicences(a.Licence) {
			add("about.licence: %s doesn't say whether later versions apply; use %s-only or %s-or-later, as the project's licence notice says.", id, strings.TrimSuffix(id, "+"), strings.TrimSuffix(id, "+"))
		}
		if a.Source == "" {
			add("about.source: give the address of the app's published code.")
		}
	}
	if a.Icon == "" {
		add("about.icon: add the project's logo as a PNG beside the package (at least %d×%d pixels, square).", IconMinSize, IconMinSize)
	}
	if len(a.Categories) == 0 {
		add("about.categories: choose at least one, for the catalog's filters.")
	}
	if p.Requires.Roostack == "" {
		add("requires.roostack: give the oldest Roostack release the package works with (e.g. \"0.6\").")
	}
}

// pinnedRe is an image reference pinned by a sha256 digest.
var pinnedRe = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// quadletRule refuses a raw Quadlet line that could reach beyond the
// service, and says what to use instead (SPEC.md §8.3.4): the deny-list,
// checked before the allow-list (quadletAllowed).
type quadletRule struct {
	section string
	// keys are the keys it refuses, whatever their case: a name, a name
	// ending in * for every key starting with it (Exec*), *name* for
	// every key containing it, or * for every key in the section.
	keys []string
	// denies names the test in valueTests that says whether a value is
	// refused; "" refuses any value.
	denies string
	// instead says why, and what the package should use instead.
	instead string
}

// matches says whether the rule covers key.
func (r quadletRule) matches(key string) bool {
	key = strings.ToLower(key)
	for _, k := range r.keys {
		k = strings.ToLower(k)
		switch {
		case k == "*":
			return true
		case len(k) > 2 && k[0] == '*' && k[len(k)-1] == '*':
			if strings.Contains(key, k[1:len(k)-1]) {
				return true
			}
		case strings.HasSuffix(k, "*"):
			if strings.HasPrefix(key, k[:len(k)-1]) {
				return true
			}
		case key == k:
			return true
		}
	}
	return false
}

// The reasons rules share.
const (
	podmanAnything = "it can pass anything to Podman; something the package format can't say needs a new field"
	serverCommand  = "it runs a command on the server; commands run inside the container (command: or hooks:)"
	serverFiles    = "it reads or writes files on the server, outside the service; use storage:, env: and secrets:"
	roostackWrites = "Roostack writes it itself, from what the package declares"
	runtimeChange  = "it changes how Podman or systemd runs the container, beyond the service; something the package format can't say needs a new field"
)

// Agreed with the Owner on 2026-10-13 (the deny-list), plus on 2026-10-14
// the keys that override what Roostack writes itself (the generator
// appends a package's lines after its own), and on 2026-10-16 those the
// security review found reaching beyond the service: keys that read or
// write host paths, change which image or command runs, or tie the unit
// to others (REVIEW-2026-10-16 H2).
var quadletRules = []quadletRule{
	// [Container]: folders and files on the server.
	{section: "Container", keys: []string{"Volume"}, denies: "volumeHasSource",
		instead: "a folder from the server or a named volume is outside the service and its backups; use storage: and mounts: (an anonymous Volume=/path is allowed)"},
	{section: "Container", keys: []string{"Mount"}, denies: "notTmpfsMount",
		instead: "only type=tmpfs is allowed, one per line, without quotes or backslashes; use storage: and mounts: for the service's folders"},
	{section: "Container", keys: []string{"Rootfs"},
		instead: "it runs the container from a folder on the server; use the container's image:"},
	{section: "Container", keys: []string{"EnvironmentFile", "EnvironmentHost", "ContainersConfModule", "SeccompProfile"},
		instead: serverFiles},
	{section: "Container", keys: []string{"LogOpt"}, denies: "logPath",
		instead: "a log path writes to a file on the server; Roostack keeps the container's log"},
	{section: "Container", keys: []string{"HealthLogDestination"}, denies: "healthLogFolder",
		instead: "a folder for the health log is on the server; use local or events_logger"},

	// [Container]: Podman itself.
	{section: "Container", keys: []string{"PodmanArgs", "GlobalArgs"}, instead: podmanAnything},
	{section: "Container", keys: []string{"AddCapability"},
		instead: "containers keep Podman's default capabilities"},
	{section: "Container", keys: []string{"SecurityLabel*", "Unmask", "Annotation"},
		instead: "Roostack keeps Podman's security settings; annotations can change them too"},
	{section: "Container", keys: []string{"Network", "Pod"},
		instead: "Roostack puts each service on its own network"},
	{section: "Container", keys: []string{"PublishPort"},
		instead: "use routes: for web addresses and ports: for other connections"},
	{section: "Container", keys: []string{"AddDevice"},
		instead: "use devices: (e.g. gpu: intel)"},
	{section: "Container", keys: []string{"GroupAdd"}, denies: "keepGroups",
		instead: "keep-groups gives the container the server's groups; use devices: for a GPU"},

	// [Container]: users and namespaces.
	{section: "Container", keys: []string{"User", "Group"}, denies: "rootUser",
		instead: "run as the image's own user, with UserNS=keep-id:uid=…,gid=… if its files need that owner"},
	{section: "Container", keys: []string{"UIDMap", "GIDMap", "SubUIDMap", "SubGIDMap"},
		instead: "use UserNS=keep-id:uid=…,gid=… if the service's files need a particular owner"},
	{section: "Container", keys: []string{"UserNS"}, denies: "sharedUserNS",
		instead: "only keep-id, auto, nomap or private: the others share a user namespace beyond the service; use UserNS=keep-id:uid=…,gid=… if the service's files need a particular owner"},

	// [Container]: what Roostack writes itself.
	{section: "Container", keys: []string{"Image", "AutoUpdate"},
		instead: "it would replace the pinned image; use image:"},
	{section: "Container", keys: []string{"Secret"},
		instead: "it could read another service's secrets; use secrets:"},
	{section: "Container", keys: []string{"ContainerName", "ServiceName"},
		instead: "Roostack names the containers and their units"},
	{section: "Container", keys: []string{"Label"}, denies: "roostackLabel",
		instead: "Roostack finds a service's containers by its own io.roostack labels (and write labels without backslashes)"},

	// [Service]: the unit that runs Podman, on the server.
	{section: "Service", keys: []string{"Exec*"}, instead: serverCommand},
	{section: "Service", keys: []string{"Environment*", "PassEnvironment", "UnsetEnvironment"},
		instead: "it sets Podman's own environment, which can point it at other files; use env: for the container's"},
	{section: "Service", keys: []string{"Standard*", "TTY*", "PIDFile", "*Path*", "*Director*", "*Credential*", "*Image*", "TemporaryFileSystem", "Root*"},
		instead: serverFiles},
	{section: "Service", keys: []string{"User", "Group", "SupplementaryGroups", "DynamicUser", "PAMName", "KeyringMode"},
		instead: "the roostack user runs every service; use UserNS=keep-id:uid=…,gid=… for the container's user"},
	{section: "Service", keys: []string{"Type", "NotifyAccess", "Delegate*", "Slice", "Sockets", "BusName", "LogNamespace", "SELinuxContext*", "AppArmorProfile", "SmackProcessLabel"},
		instead: runtimeChange},

	// [Unit] and [Install]: how the unit relates to others.
	{section: "Unit", keys: []string{"Exec*"}, instead: serverCommand},
	{section: "Unit", keys: []string{"Requires", "Requisite", "Wants", "BindsTo", "PartOf", "Upholds", "Conflicts", "Before", "After",
		"OnFailure", "OnSuccess", "PropagatesReloadTo", "ReloadPropagatedFrom", "PropagatesStopTo", "StopPropagatedFrom",
		"JoinsNamespaceOf", "RequiresMountsFor", "WantsMountsFor", "SourcePath"},
		instead: "it ties the service to other units; " + roostackWrites + " (depends_on:)"},
	{section: "Unit", keys: []string{"*Action*", "RebootArgument"},
		instead: "it acts beyond the service, such as stopping every service of the roostack user"},
	{section: "Install", keys: []string{"*"},
		instead: "it ties the service to other units, or names it after one; " + roostackWrites},
}

// valueTests are the tests a quadletRule refuses a value by, named so
// the package editor runs the same ones (internal/editor, which a test
// keeps in step).
var valueTests = map[string]func(value string) bool{
	"volumeHasSource": volumeHasSource,
	"notTmpfsMount":   func(v string) bool { return !isTmpfsMount(v) },
	"logPath":         func(v string) bool { return strings.Contains(strings.ToLower(v), "path") || strings.Contains(v, `\`) },
	"healthLogFolder": func(v string) bool { return v != "local" && v != "events_logger" },
	"keepGroups": func(v string) bool {
		return strings.Contains(strings.ToLower(v), "keep-groups") || strings.Contains(v, `\`)
	},
	"rootUser":      isRootUser,
	"sharedUserNS":  func(v string) bool { return !isOwnUserNS(v) },
	"roostackLabel": hasRoostackLabel,
}

// quadletAllowed are the keys a package may use, by section: an
// allow-list, decided by the Owner on 2026-10-16 after the security review
// of that day, replacing "any key but the deny-list" (SPEC.md §8.3.4).
// They're the keys Roostack has looked at and found to stay inside the
// service: for [Container], from the keys Podman 5.4's Quadlet knows
// (Debian 13's; podman-systemd.unit(5)); for [Service] and [Unit], a few
// of systemd's that only change how and when this unit runs. Some are
// allowed only with certain values (quadletRules, which still apply). A
// key that isn't here, including one a newer Podman adds, waits until
// it's been looked at: a package that needs one asks for it.
var quadletAllowed = map[string][]string{
	"Container": {
		// Inside the container.
		"AddHost", "DNS", "DNSOption", "DNSSearch", "DropCapability", "Entrypoint", "Environment", "Exec",
		"ExposeHostPort", "HostName", "IP", "IP6", "Mask", "NetworkAlias", "NoNewPrivileges", "Notify",
		"PidsLimit", "Pull", "ReadOnly", "ReadOnlyTmpfs", "RunInit", "ShmSize", "StopSignal", "StopTimeout",
		"Sysctl", "Timezone", "Tmpfs", "Ulimit", "WorkingDir",
		"HealthCmd", "HealthInterval", "HealthMaxLogCount", "HealthMaxLogSize", "HealthOnFailure",
		"HealthRetries", "HealthStartPeriod", "HealthStartupCmd", "HealthStartupInterval",
		"HealthStartupRetries", "HealthStartupSuccess", "HealthStartupTimeout", "HealthTimeout",
		// With some values only (quadletRules).
		"Volume", "Mount", "User", "Group", "UserNS", "Label", "LogOpt", "GroupAdd", "HealthLogDestination",
	},
	"Service": {
		// When it's restarted or given up on.
		"Restart", "RestartSec", "RestartSteps", "RestartMaxDelaySec", "RestartPreventExitStatus",
		"RestartForceExitStatus", "SuccessExitStatus", "TimeoutSec", "TimeoutStartSec", "TimeoutStopSec",
		"TimeoutAbortSec", "RuntimeMaxSec", "WatchdogSec",
		// How much of the server it may use.
		"CPUQuota", "CPUWeight", "IOWeight", "MemoryHigh", "MemoryMax", "MemorySwapMax", "TasksMax",
		"LimitNOFILE", "LimitNPROC", "Nice", "OOMPolicy", "OOMScoreAdjust",
	},
	"Unit": {"Documentation", "StartLimitIntervalSec", "StartLimitBurst"},
}

// quadletPolicy returns a sentence for each refused line of q: a key or
// value that wouldn't be read back as written, a line the deny-list
// refuses (which says what to use instead), and a key that isn't on the
// allow-list. The deny-list stays as a second check, should a key be
// allowed by mistake.
func quadletPolicy(q Quadlet) []string {
	var problems []string
	for _, section := range slices.Sorted(maps.Keys(q)) {
		for _, key := range slices.Sorted(maps.Keys(q[section])) {
			// Validate refuses these too; the policy doesn't rely on it.
			if err := quadlet.CheckKey(key); err != nil {
				problems = append(problems, fmt.Sprintf("%s.%q isn't allowed: %v.", section, key, err))
				continue
			}
			rule := findQuadletRule(section, key)
			refused := false
			for _, value := range quadletValues(q[section][key]) {
				if err := quadlet.CheckValue(value); err != nil {
					problems = append(problems, fmt.Sprintf("%s.%s=%q isn't allowed: %v.", section, key, value, err))
					refused = true
					continue
				}
				if rule == nil || rule.denies != "" && !valueTests[rule.denies](value) {
					continue
				}
				problems = append(problems, fmt.Sprintf("%s.%s=%s isn't allowed: %s.", section, key, value, rule.instead))
				refused = true
			}
			if !refused && !slices.Contains(quadletAllowed[section], key) {
				problems = append(problems, fmt.Sprintf("%s.%s isn't allowed: %s.", section, key, notAllowed(section, key)))
			}
		}
	}
	return problems
}

// notAllowed says why a key isn't on the allow-list, and what to do.
func notAllowed(section, key string) string {
	for _, k := range quadletAllowed[section] {
		if strings.EqualFold(k, key) {
			return fmt.Sprintf("write it as %s, as systemd and Quadlet only read it that way", k)
		}
	}
	return "it isn't one of the keys Roostack has checked stay inside the service; a package that needs it asks for it to be added to the package policy"
}

func findQuadletRule(section, key string) *quadletRule {
	for i, r := range quadletRules {
		if r.section == section && r.matches(key) {
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
// Quadlet passes the line to Podman as it is (quotes too: checked on a
// dev VM, docs/ASSUMPTIONS.md A12).
func volumeHasSource(v string) bool {
	src, _, found := strings.Cut(strings.TrimSpace(v), ":")
	return found && src != ""
}

// isTmpfsMount says whether a Mount= line is a single tmpfs (type=tmpfs,
// ...), which holds nothing beyond the container. Quadlet splits a Mount=
// line into words, unquoting and unescaping them, and each word is a
// mount of its own (A12), so a line with spaces, quotes or backslashes is
// refused rather than parsed.
func isTmpfsMount(v string) bool {
	if strings.ContainsAny(v, " \t\"'\\") {
		return false
	}
	types := 0
	for opt := range strings.SplitSeq(v, ",") {
		k, val, _ := strings.Cut(opt, "=")
		switch strings.ToLower(k) {
		case "type":
			types++
			if val != "tmpfs" {
				return false
			}
		case "source", "src":
			return false
		}
	}
	return types == 1
}

// hasRoostackLabel says whether a Label= line sets one of Roostack's own
// labels. Quadlet reads a Label= line as several key=value words, unquoting
// them and unescaping \x69 and the like (A12), so each word is checked,
// and a line with a backslash is refused rather than unescaped here.
func hasRoostackLabel(v string) bool {
	if strings.Contains(v, `\`) {
		return true
	}
	for _, word := range strings.Fields(strings.NewReplacer(`"`, " ", "'", " ").Replace(v)) {
		if strings.HasPrefix(strings.ToLower(word), "io.roostack.") {
			return true
		}
	}
	return false
}

// isRootUser says whether a User= or Group= line (name[:group]) is root:
// root, or a number that is 0 however it's written (0, 00, +0).
func isRootUser(v string) bool {
	user, _, _ := strings.Cut(strings.Trim(strings.TrimSpace(v), `"'`), ":")
	user = strings.TrimSpace(user)
	if strings.EqualFold(user, "root") {
		return true
	}
	digits := strings.TrimLeft(user, "+-")
	return digits != "" && strings.Trim(digits, "0") == ""
}

// isOwnUserNS says whether a UserNS= line gives the container a user
// namespace of its own: keep-id (with options), auto, nomap or private.
// The others (host, ns:<path>, container:<name>) share one beyond the
// service.
func isOwnUserNS(v string) bool {
	mode, _, _ := strings.Cut(strings.TrimSpace(v), ":")
	switch mode {
	case "keep-id", "auto", "nomap", "private":
		return true
	}
	return false
}
